package alerts

// Prediction-market odds triggers on paper: notify when a forecast's matched
// Polymarket market rises above or falls below a Yes-probability level.
// Re-priced from Gamma at most once a minute per forecast; notify only,
// 0 credits.

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/forecast"
)

// OddsMarket is an odds trigger's market as the alert list shows it: the
// forecast's stored result.market (fresh after each re-price).
type OddsMarket struct {
	Question string    `json:"question"`
	URL      string    `json:"url"`
	Outcome  *string   `json:"outcome"` // categorical: the option it prices; null for binary
	YesPrice float64   `json:"yesPrice"`
	AsOf     time.Time `json:"asOf"`
}

func oddsMarket(m *forecast.MarketOdds) *OddsMarket {
	o := &OddsMarket{Question: m.Question, URL: m.URL, YesPrice: m.YesPrice, AsOf: m.AsOf}
	if m.Outcome != "" {
		o.Outcome = &m.Outcome
	}
	return o
}

// Pct is a probability as a percent, one decimal only when needed: "25%", "26.5%".
func Pct(p float64) string { return strconv.FormatFloat(math.Round(p*1000)/10, 'f', -1, 64) + "%" }

// ValidateOdds normalizes and checks an odds trigger as POST /api/alerts does:
// the caller's own completed forecast (a replay has no owner; archived is not
// completed) with a matched market, notify only, a level in 1..99% not crossed
// yet. live re-prices the market first (forecast.LiveOdds; on a Gamma error the
// stored price stands); chat passes false and checks the stored price only.
// It returns the market it checked against. Rule errors are *app.BadRequest.
func ValidateOdds(ctx context.Context, d *app.Deps, userID string, in Input, live bool) (Input, *forecast.MarketOdds, error) {
	in.Trigger, in.Symbol, in.Note = "odds", "", strings.TrimSpace(in.Note)
	in.Op = strings.ToLower(strings.TrimSpace(in.Op))
	in.Action = cmp.Or(strings.ToLower(strings.TrimSpace(in.Action)), "notify")
	in.Source = cmp.Or(in.Source, "manual")
	in.ForecastID = strings.TrimSpace(in.ForecastID)
	in.MarketSlug, in.MarketID = strings.TrimSpace(in.MarketSlug), strings.TrimSpace(in.MarketID)
	if (in.ForecastID != "") == (in.MarketSlug != "") || in.ForecastID != "" && in.MarketID != "" || in.MarketSlug == "" && in.MarketID != "" {
		return in, nil, bad("Choose exactly one forecastId or leaf marketSlug target.")
	}
	if in.MarketSlug != "" {
		return validateDirectOdds(ctx, d, in)
	}
	if !canonical(in.ForecastID) {
		return in, nil, bad("Unknown forecast.")
	}

	var m *forecast.MarketOdds
	var linked *forecast.LinkedMarket
	err := d.DB.QueryRow(ctx, `select result->'market', linked_market from forecasts where id = $1 and user_id = $2 and status = 'completed'`,
		in.ForecastID, userID).Scan(&m, &linked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return in, nil, bad("Unknown forecast.")
	case err != nil:
		return in, nil, err
	case m == nil:
		return in, nil, bad("This forecast has no matched Polymarket market.")
	case in.Action != "notify":
		return in, nil, bad("Odds alerts notify only.")
	case in.Op != "above" && in.Op != "below":
		return in, nil, bad("op must be above or below.")
	case !(in.Price >= 0.01 && in.Price <= 0.99):
		return in, nil, bad("Pick a level between 1%% and 99%%.")
	case in.Notional != nil || in.StopLoss != nil || in.TakeProfit != nil:
		return in, nil, bad("A notify alert takes no notional, stopLoss or takeProfit.")
	}
	if err := validCommon(in, false); err != nil {
		return in, nil, err
	}
	if live {
		if fresh, err := forecast.LiveOdds(ctx, d, in.ForecastID); err == nil {
			m = fresh
		} else {
			if linked != nil {
				return in, nil, err
			}
			slog.Warn("odds alert: live price, using the stored one", "forecast", in.ForecastID, "err", err)
		}
	}
	if in.Op == "above" && m.YesPrice >= in.Price || in.Op == "below" && m.YesPrice <= in.Price {
		return in, m, bad("The market is already at %s. Pick a level %s it.", Pct(m.YesPrice), in.Op)
	}
	return in, m, nil
}

const (
	oddsEvery        = time.Minute // a forecast's market is re-priced at most this often
	maxOddsForecasts = 20          // ponytail: forecasts re-priced per 15 s check; more wait their turn (longest-waiting first), shard if it grows
)

var (
	oddsMu     sync.Mutex
	repriced   = map[string]time.Time{} // forecast id → its last re-price attempt
	warned     = map[string]time.Time{} // forecast id → its last logged Gamma error
	pruned     time.Time                // the last alert_runs prune
	clock      = time.Now               // tests move it
	oddsBudget = 10 * time.Second       // per check; tests shorten it
)

// checkOdds fires every active odds trigger of an enabled account whose
// forecast's market, re-priced live, has reached its level (above at
// yes ≥ price, below at yes ≤ price), claimed once like a price alert.
// ponytail: throttle state is in-process (one API instance); a second instance re-prices on its own.
func checkOdds(ctx context.Context, d *app.Deps) error {
	if d.PolymarketBase == "" {
		return nil
	}
	type armed struct {
		id, forecast, op, marketID string
		linked                     *forecast.LinkedMarket
		price                      float64
	}
	rows, _ := d.DB.Query(ctx, `select a.id::text, coalesce(a.forecast_id::text,''), a.op, a.price, coalesce(a.market_id,''), a.linked_market from alerts a join users u on u.id = a.user_id
		where a.status = 'active' and a.trigger = 'odds' and u.disabled_at is null order by a.forecast_id, a.market_id`)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (armed, error) {
		var a armed
		return a, r.Scan(&a.id, &a.forecast, &a.op, &a.price, &a.marketID, &a.linked)
	})
	if err != nil || len(list) == 0 {
		return err
	}
	byForecast := map[string][]armed{}
	var due []string
	now := clock()
	oddsMu.Lock()
	for _, a := range list {
		if a.marketID != "" {
			a.forecast = "market:" + a.marketID
		}
		if _, seen := byForecast[a.forecast]; !seen && now.Sub(repriced[a.forecast]) >= oddsEvery {
			due = append(due, a.forecast)
		}
		byForecast[a.forecast] = append(byForecast[a.forecast], a)
	}
	// Longest-waiting first (never re-priced before all), so past
	// maxOddsForecasts per check every forecast still gets its turn instead of
	// the lowest ids taking every slot.
	slices.SortStableFunc(due, func(x, y string) int { return repriced[x].Compare(repriced[y]) })
	due = due[:min(len(due), maxOddsForecasts)]
	before := make([]time.Time, len(due))
	for i, id := range due {
		before[i], repriced[id] = repriced[id], now
	}
	oddsMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, oddsBudget) // a hanging Gamma must not hold up the next check
	defer cancel()
	for i, id := range due {
		if ctx.Err() != nil {
			// Out of budget: the rest were never asked, so they keep their
			// place at the head of the queue instead of waiting another round.
			oddsMu.Lock()
			for j := i; j < len(due); j++ {
				if before[j].IsZero() {
					delete(repriced, due[j])
				} else {
					repriced[due[j]] = before[j]
				}
			}
			oddsMu.Unlock()
			return nil
		}
		var m *forecast.MarketOdds
		var link *forecast.LinkedMarket
		var err error
		if strings.HasPrefix(id, "market:") {
			a := byForecast[id][0]
			// Read the stable ID once; each alert validates its own immutable
			// snapshot below. An obsolete leaf must not starve a current alert.
			link, m, err = forecast.ReadLinkedMarket(ctx, d, a.marketID, "")
		} else {
			m, err = forecast.LiveOdds(ctx, d, id)
		}
		if err != nil {
			oddsMu.Lock()
			quiet := now.Sub(warned[id]) < time.Hour
			if !quiet {
				warned[id] = now
			}
			oddsMu.Unlock()
			if !quiet {
				slog.Warn("odds alert: re-price", "forecast", id, "err", err)
			}
			continue
		}
		for _, a := range byForecast[id] {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if a.linked != nil {
				if !forecast.SameLinkedMarket(a.linked, link) {
					continue
				}
				if _, err := d.DB.Exec(ctx, `update alerts set market_odds=$2 where id=$1 and status='active' and linked_market=$3
                    and exists(select 1 from users where users.id=alerts.user_id and disabled_at is null)`, a.id, oddsMarket(m), a.linked); err != nil {
					return err
				}
			}
			if a.op == "above" && m.YesPrice >= a.price || a.op == "below" && m.YesPrice <= a.price {
				if err := fire(context.WithoutCancel(ctx), d, a.id, m.YesPrice); err != nil {
					slog.Error("odds alert fire", "id", a.id, "err", err)
				}
			}
		}
	}
	return nil
}

// prune deletes schedule runs older than 30 days and notification deliveries
// older than 15, at most once an hour, and
// forgets throttle entries an hour old.
func prune(ctx context.Context, d *app.Deps) error {
	oddsMu.Lock()
	now := clock()
	if now.Sub(pruned) < time.Hour {
		oddsMu.Unlock()
		return nil
	}
	pruned = now
	for _, m := range []map[string]time.Time{repriced, warned} {
		for id, at := range m {
			if now.Sub(at) >= time.Hour {
				delete(m, id)
			}
		}
	}
	oddsMu.Unlock()
	_, err := d.DB.Exec(ctx, `delete from alert_runs where status<>'running' and ran_at < now() - interval '30 days'`)
	_, derr := d.DB.Exec(ctx, `delete from notification_deliveries where sent_at < now() - interval '15 days'`) // past the bell's 14 days
	return errors.Join(err, derr)
}

func validateDirectOdds(ctx context.Context, d *app.Deps, in Input) (Input, *forecast.MarketOdds, error) {
	switch {
	case in.Action != "notify":
		return in, nil, bad("Odds alerts notify only.")
	case in.Op != "above" && in.Op != "below":
		return in, nil, bad("op must be above or below.")
	case !(in.Price >= .01 && in.Price <= .99):
		return in, nil, bad("Pick a level between 1%% and 99%%.")
	case in.Notional != nil || in.StopLoss != nil || in.TakeProfit != nil:
		return in, nil, bad("A notify alert takes no notional, stopLoss or takeProfit.")
	case in.Every != "" || in.At != "" || in.Weekday != nil:
		return in, nil, bad("An odds alert takes no schedule.")
	}
	if err := validCommon(in, false); err != nil {
		return in, nil, err
	}
	if in.MarketID != "" && (len(in.MarketID) > 20 || strings.Trim(in.MarketID, "0123456789") != "") {
		return in, nil, bad("marketId must be numeric.")
	}
	l, m, err := forecast.ReadLinkedMarket(ctx, d, "", in.MarketSlug)
	if err != nil {
		return in, nil, err
	}
	if in.MarketID != "" && in.MarketID != l.MarketID {
		return in, nil, bad("Market identity does not match.")
	}
	if in.Op == "above" && m.YesPrice >= in.Price || in.Op == "below" && m.YesPrice <= in.Price {
		return in, m, bad("The market is already at %s. Pick a level %s it.", Pct(m.YesPrice), in.Op)
	}
	in.MarketID, in.MarketSlug, in.linkedMarket = l.MarketID, l.MarketSlug, l
	return in, m, nil
}
