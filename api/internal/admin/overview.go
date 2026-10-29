package admin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

type DayValue struct {
	Day   string `json:"day"` // YYYY-MM-DD, UTC
	Value int    `json:"value"`
}

type Windowed struct {
	Today int `json:"today"`
	D7    int `json:"d7"`
	D30   int `json:"d30"`
}

type Feature struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Windowed
	Daily []DayValue `json:"daily"`
}

type TopUser struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Credits int    `json:"credits"`
}

type System struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	UptimeSec int64     `json:"uptimeSec"`
	DB        struct {
		OK        bool    `json:"ok"`
		LatencyMs float64 `json:"latencyMs"`
	} `json:"db"`
	Provider        string `json:"provider"` // the AI provider preset id; "" = AI off
	Model           string `json:"model"`
	KnowledgeCutoff string `json:"knowledgeCutoff"`
	News            struct {
		Enabled      bool       `json:"enabled"`
		LastIngestAt *time.Time `json:"lastIngestAt"`
		Items        int        `json:"items"`
		BriefAt      *time.Time `json:"briefAt"`
		BriefAgeMin  *int       `json:"briefAgeMin"`
	} `json:"news"`
	Signals struct {
		BoardAt     *time.Time `json:"boardAt"`     // the newest stored signals board
		Failures    int        `json:"failures"`    // failed refreshes in a row (signals_refresh)
		LastErrorAt *time.Time `json:"lastErrorAt"` // the last failed refresh
	} `json:"signals"`
	Brave struct {
		Used  int `json:"used"`
		Limit int `json:"limit"`
	} `json:"brave"`
	Crawler research.CrawlerStatus `json:"crawler"`
	Jobs    struct {
		ForecastsRunning int       `json:"forecastsRunning"`
		ArenaRunsRunning int       `json:"arenaRunsRunning"`
		LiveArena        LiveArena `json:"liveArena"`
	} `json:"jobs"`
}

// LiveArena is the shared live desk's health (contract SystemStatus.jobs.liveArena).
type LiveArena struct {
	Enabled             bool       `json:"enabled"`
	StartedAt           *time.Time `json:"startedAt"`
	LastStepAt          *time.Time `json:"lastStepAt"`
	NextStepAt          *time.Time `json:"nextStepAt"`
	Agents              int        `json:"agents"`
	Benched             int        `json:"benched"`
	CopyPlans           int        `json:"copyPlans"`
	Status              string     `json:"status"`
	Stale               bool       `json:"stale"`
	LastError           *string    `json:"lastError"`
	AttemptsToday       int        `json:"attemptsToday"`
	RequestCeilingToday int        `json:"requestCeilingToday"`
	MaxDecisionsPerDay  int        `json:"maxDecisionsPerDay"`
	MaxRequestsPerDay   int        `json:"maxRequestsPerDay"`
	MaxTokensPerRequest int        `json:"maxTokensPerRequest"`
	DecisionTimeoutSec  int        `json:"decisionTimeoutSec"`
}

type Overview struct {
	AsOf  time.Time `json:"asOf"`
	Users struct {
		Total    int      `json:"total"`
		Admins   int      `json:"admins"`
		Disabled int      `json:"disabled"`
		New30d   int      `json:"new30d"`
		Active   Windowed `json:"active"`
	} `json:"users"`
	Credits struct {
		Windowed
		Daily        []DayValue `json:"daily"`
		DefaultLimit int        `json:"defaultLimit"`
	} `json:"credits"`
	Features []Feature `json:"features"`
	TopUsers []TopUser `json:"topUsers"`
	Paper    struct {
		Accounts      int      `json:"accounts"`
		OpenPositions int      `json:"openPositions"`
		Equity        *float64 `json:"equity"`
		TotalPnl      *float64 `json:"totalPnl"`
		Notional30d   float64  `json:"notional30d"`
	} `json:"paper"`
	System System `json:"system"`
}

// features in contract order; from is the table and the start of its where clause.
var features = []struct{ key, label, from string }{
	{"chat_messages", "Chat messages", "chat_messages where role = 'user' and"},
	{"forecasts", "Forecasts", "forecasts where user_id is not null and"}, // not the shared replay rows
	{"strategies", "Strategies", "strategies where"},
	{"ai_portfolios", "AI portfolios", "ai_portfolios where"},
	{"arena_runs", "Arena runs", "arena_runs where"},
	{"orders", "Paper orders", "trades where"},
	{"copy_plans", "Copy plans", "copy_plans where"},
}

// seriesSQL is every daily series of the last 30 UTC days: (key, day, value).
var seriesSQL = func() string {
	q := "select 'credits', day, sum(credits) from ai_usage where day >= @sinceDay group by day"
	for _, f := range features {
		q += fmt.Sprintf(" union all select '%s', (created_at at time zone 'utc')::date, count(*) from %s created_at >= @since group by 2",
			f.key, f.from)
	}
	return q
}()

const totalsSQL = `select u.total, u.admins, u.disabled, u.new30d, u.active_today, u.active_d7, u.active_d30,
	a.accounts, a.cash, a.starting,
	(select count(*) from positions),
	(select coalesce(sum(p.qty * coalesce(m.mark, p.avg_price)), 0) from positions p left join unnest(@syms::text[], @marks::float8[]) m(symbol, mark) using (symbol)), -- no live price: average price
	(select coalesce(sum(notional), 0) from trades where created_at >= @since),
	(select max(fetched_at) from news_items), (select count(*) from news_items), (select max(created_at) from market_briefs),
	(select count(*) from forecasts where status = 'running'), (select count(*) from arena_runs where status = 'running'),
	(select started_at from live_desk where id = 1), (select last_step_at from live_desk where id = 1),
	(select count(*) from live_agents), (select count(*) from live_agents where benched_until > now()),
	(select count(*) from copy_plans where status = 'active'),
 (select last_error from live_desk where id=1),
 (select count(*) from live_attempts where claimed_at >= @todayStart and claimed_at < @todayStart + interval '1 day'),
	(select max(created_at) from signal_boards), coalesce((select failures from signals_refresh), 0), (select last_error_at from signals_refresh)
	from (select count(*) total, count(*) filter (where role = 'admin') admins, count(*) filter (where disabled_at is not null) disabled,
		count(*) filter (where created_at >= @since) new30d, count(*) filter (where last_active_at >= @todayStart) active_today,
		count(*) filter (where last_active_at >= @since7) active_d7, count(*) filter (where last_active_at >= @since) active_d30
		from users) u,
	(select count(*) accounts, coalesce(sum(cash), 0) + (select coalesce(sum(cost), 0) from market_positions where status = 'open') cash, -- open prediction stakes count at cost
		coalesce(sum(starting_balance), 0) starting from accounts) a`

const topSQL = `select u.id, u.email, u.name, sum(a.credits) from ai_usage a join users u on u.id = a.user_id
	where a.day >= @since7Day group by u.id having sum(a.credits) > 0 order by 4 desc, u.email limit 10`

func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	crawler := make(chan research.CrawlerStatus, 1) // the probe can take 3 s: overlap it with the queries
	probeDone := make(chan struct{})
	defer func() { <-probeDone }() // early query error or panic must still join the actual probe
	go func() { defer close(probeDone); crawler <- h.d.Research.Crawler(ctx) }()

	var o Overview
	o.AsOf = time.Now().UTC()
	s := &o.System
	start := time.Now()
	_, err := h.d.DB.Exec(ctx, "select 1")
	s.DB.OK, s.DB.LatencyMs = err == nil, float64(time.Since(start).Microseconds())/1000

	a := h.args(ctx)
	var cash, starting, positionsValue float64
	series := map[string]map[string]int{}
	b := &pgx.Batch{}
	b.Queue(totalsSQL, a).QueryRow(func(row pgx.Row) error {
		return row.Scan(&o.Users.Total, &o.Users.Admins, &o.Users.Disabled, &o.Users.New30d,
			&o.Users.Active.Today, &o.Users.Active.D7, &o.Users.Active.D30,
			&o.Paper.Accounts, &cash, &starting, &o.Paper.OpenPositions, &positionsValue, &o.Paper.Notional30d,
			&s.News.LastIngestAt, &s.News.Items, &s.News.BriefAt, &s.Jobs.ForecastsRunning, &s.Jobs.ArenaRunsRunning,
			&s.Jobs.LiveArena.StartedAt, &s.Jobs.LiveArena.LastStepAt, &s.Jobs.LiveArena.Agents, &s.Jobs.LiveArena.Benched, &s.Jobs.LiveArena.CopyPlans, &s.Jobs.LiveArena.LastError, &s.Jobs.LiveArena.AttemptsToday,
			&s.Signals.BoardAt, &s.Signals.Failures, &s.Signals.LastErrorAt)
	})
	b.Queue(seriesSQL, a).Query(func(rows pgx.Rows) error {
		var key string
		var day time.Time
		var n int
		_, err := pgx.ForEachRow(rows, []any{&key, &day, &n}, func() error {
			if series[key] == nil {
				series[key] = map[string]int{}
			}
			series[key][day.Format(time.DateOnly)] = n
			return nil
		})
		return err
	})
	b.Queue(topSQL, a).Query(func(rows pgx.Rows) error {
		var err error
		o.TopUsers, err = pgx.CollectRows(rows, pgx.RowToStructByPos[TopUser])
		return err
	})
	if err := h.d.DB.SendBatch(ctx, b).Close(); err != nil {
		app.WriteError(w, err)
		return
	}

	today := a["today"].(time.Time)
	o.Credits.Daily, o.Credits.Windowed = daily(today, series["credits"])
	o.Credits.DefaultLimit = h.d.Usage.DefaultLimit()
	for _, f := range features {
		ft := Feature{Key: f.key, Label: f.label}
		ft.Daily, ft.Windowed = daily(today, series[f.key])
		o.Features = append(o.Features, ft)
	}
	if a["marks"].([]float64) != nil {
		equity := cash + positionsValue
		pnl := equity - starting
		o.Paper.Equity, o.Paper.TotalPnl = &equity, &pnl
	}

	s.Version, s.StartedAt, s.UptimeSec = h.d.Version, h.d.StartedAt, int64(time.Since(h.d.StartedAt).Seconds())
	s.Provider, s.Model, s.KnowledgeCutoff = h.d.LLM.Settings().Provider, h.d.LLM.Model(), h.d.LLM.KnowledgeCutoff().Format(time.DateOnly)
	s.News.Enabled = h.d.Research.Enabled()
	if s.News.BriefAt != nil {
		age := int(time.Since(*s.News.BriefAt).Minutes())
		s.News.BriefAgeMin = &age
	}
	s.Brave.Used, s.Brave.Limit = h.d.Research.BraveUsage()
	live := &s.Jobs.LiveArena
	live.Status = "paused"
	live.MaxDecisionsPerDay, live.MaxRequestsPerDay, live.MaxTokensPerRequest, live.DecisionTimeoutSec = 18, 72, 4096, 90
	live.RequestCeilingToday = live.AttemptsToday * 4
	live.Stale = live.LastStepAt != nil && o.AsOf.Sub(*live.LastStepAt) > 4*time.Hour+2*time.Minute
	if h.d.LiveArena {
		live.Status = "starting"
		if live.LastStepAt != nil {
			live.Status = "live"
		}
		if live.Stale {
			live.Status = "stale"
		}
		if live.LastError != nil {
			live.Status = "outage"
		}
	}
	if s.Jobs.LiveArena.Enabled = h.d.LiveArena; s.Jobs.LiveArena.Enabled {
		next := o.AsOf.Truncate(4 * time.Hour).Add(4 * time.Hour) // the close of the current 4h bar (UTC)
		s.Jobs.LiveArena.NextStepAt = &next
	}
	s.Crawler = <-crawler
	httpx.JSON(w, http.StatusOK, map[string]any{"overview": o})
}

// daily is the contract's 30 zero-filled points (oldest first, ending today)
// and their windows: today = the last point, d7 = the last 7, d30 = all.
func daily(today time.Time, byDay map[string]int) ([]DayValue, Windowed) {
	out := make([]DayValue, 30)
	var w Windowed
	for i := range out {
		day := today.AddDate(0, 0, i-29).Format(time.DateOnly)
		out[i] = DayValue{Day: day, Value: byDay[day]}
		w.D30 += out[i].Value
		if i >= 23 {
			w.D7 += out[i].Value
		}
	}
	w.Today = out[29].Value
	return out, w
}

type ActivityItem struct {
	ID     string    `json:"id"`
	Title  string    `json:"title"`
	Status *string   `json:"status"`
	At     time.Time `json:"at"`
}

type Detail struct {
	User               User                      `json:"user"`
	DefaultCreditLimit int                       `json:"defaultCreditLimit"`
	Usage              []DayValue                `json:"usage"`
	Counts             Counts                    `json:"counts"`
	Portfolio          *paper.Portfolio          `json:"portfolio"`
	Trades             []paper.Trade             `json:"trades"`
	Activity           map[string][]ActivityItem `json:"activity"`
	Audit              []AuditEntry              `json:"audit"`
}

type Counts struct {
	Chats        int `json:"chats"`
	ChatMessages int `json:"chatMessages"`
	Forecasts    int `json:"forecasts"`
	Strategies   int `json:"strategies"`
	AIPortfolios int `json:"aiPortfolios"`
	ArenaRuns    int `json:"arenaRuns"`
	Trades       int `json:"trades"`
}

const countsSQL = `select (select count(*) from chats where user_id = @id),
	(select count(*) from chat_messages m join chats c on c.id = m.chat_id where c.user_id = @id and m.role = 'user'),
	(select count(*) from forecasts where user_id = @id), (select count(*) from strategies where user_id = @id),
	(select count(*) from ai_portfolios where user_id = @id), (select count(*) from arena_runs where user_id = @id),
	(select count(*) from trades where user_id = @id)`

const activitySQL = `select * from (
	(select 'chats', id, title, null::text, updated_at from chats where user_id = @id order by updated_at desc limit 10)
	union all (select 'forecasts', id, question, status, created_at from forecasts where user_id = @id order by created_at desc limit 10)
	union all (select 'strategies', id, name, null, created_at from strategies where user_id = @id order by created_at desc limit 10)
	union all (select 'aiPortfolios', id, name, null, created_at from ai_portfolios where user_id = @id order by created_at desc limit 10)
	union all (select 'arenaRuns', id, name, status, created_at from arena_runs where user_id = @id order by created_at desc limit 10)
	) x order by 1, 5 desc, 2`

func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	uid, err := id(r)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	ctx := r.Context()
	a := h.args(ctx)
	a["id"] = uid
	d := Detail{DefaultCreditLimit: h.d.Usage.DefaultLimit(), Activity: map[string][]ActivityItem{
		"chats": {}, "forecasts": {}, "strategies": {}, "aiPortfolios": {}, "arenaRuns": {}}}
	usage := map[string]int{}
	b := &pgx.Batch{}
	b.Queue(userSQL+" where u.id = @id", a).QueryRow(func(row pgx.Row) (err error) {
		d.User, err = h.scanUser(row)
		return err
	})
	b.Queue("select day, credits from ai_usage where user_id = @id and day >= @sinceDay", a).Query(func(rows pgx.Rows) error {
		var day time.Time
		var n int
		_, err := pgx.ForEachRow(rows, []any{&day, &n}, func() error {
			usage[day.Format(time.DateOnly)] = n
			return nil
		})
		return err
	})
	c := &d.Counts
	b.Queue(countsSQL, a).QueryRow(func(row pgx.Row) error {
		return row.Scan(&c.Chats, &c.ChatMessages, &c.Forecasts, &c.Strategies, &c.AIPortfolios, &c.ArenaRuns, &c.Trades)
	})
	b.Queue(activitySQL, a).Query(func(rows pgx.Rows) error {
		var kind string
		var it ActivityItem
		_, err := pgx.ForEachRow(rows, []any{&kind, &it.ID, &it.Title, &it.Status, &it.At}, func() error {
			d.Activity[kind] = append(d.Activity[kind], it)
			it = ActivityItem{} // a fresh Status pointer per row: pgx scans into a non-nil one in place
			return nil
		})
		return err
	})
	b.Queue(auditSQL+" where target_user_id = @id order by created_at desc, id desc limit 20", a).Query(func(rows pgx.Rows) (err error) {
		d.Audit, err = pgx.CollectRows(rows, scanAudit)
		return err
	})
	if err := h.d.DB.SendBatch(ctx, b).Close(); err != nil {
		app.WriteError(w, err)
		return
	}
	d.Usage, _ = daily(a["today"].(time.Time), usage)
	if d.Trades, err = h.d.Paper.Trades(ctx, uid, 20); err != nil {
		app.WriteError(w, err)
		return
	}
	d.Portfolio = h.portfolio(ctx, uid)
	httpx.JSON(w, http.StatusOK, d)
}
