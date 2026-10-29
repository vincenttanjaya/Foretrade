package alerts

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/forecast"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

// Notification is one item of the bell (contract Notification): derived on
// read from fired alerts, schedule runs, stop/target/autopilot fills and
// forecast results; there is no notifications table and no hook in other packages.
type Notification struct {
	ID     string    `json:"id"`   // "<kind>:<row id>"
	Kind   string    `json:"kind"` // alert | alert_order | alert_failed | odds_alert | drawdown_alert | drawdown_order | drawdown_failed | workflow | workflow_order | workflow_failed | stop_loss | take_profit | autopilot | copy_order | copy_stopped | forecast_completed | forecast_failed | forecast_resolved
	Title  string    `json:"title"`
	Body   string    `json:"body"`
	Symbol *string   `json:"symbol"`
	Link   string    `json:"link"`
	At     time.Time `json:"at"`
}

// feed is the user's notifications of the last 14 days, newest first, and how
// many are newer than users.notifications_seen_at (≤ 99).
// ponytail: each source reads its newest 50, so unread undercounts past 50 of one kind in 14 days.
func feed(ctx context.Context, db *pgxpool.Pool, userID string) ([]Notification, int, error) {
	out := []Notification{}
	add := func(kind, id, title, body string, symbol *string, link string, at time.Time) {
		out = append(out, Notification{kind + ":" + id, kind, title, body, symbol, link, at})
	}

	var (
		id, symbol, op, action, note, status, msg, side, source, question, shape string
		price, firedPrice, qty, realized                                         float64
		notional                                                                 *float64
		at                                                                       time.Time
	)
	rows, _ := db.Query(ctx, `
		select a.id::text, a.symbol, a.op, a.price, a.action, a.note, a.status, coalesce(a.error, ''), a.fired_price, a.fired_at,
			coalesce(t.price, a.fired_price), coalesce(t.notional, a.notional), coalesce(a.currency, '')
		from alerts a left join trades t on t.id = a.trade_id
		where a.user_id = $1 and a.trigger = 'price' and a.status in ('fired', 'failed') and a.fired_at > now() - interval '14 days'
		order by a.fired_at desc limit 50`, userID)
	var fill float64
	var levelCcy string // a price alert's level currency, "" for USD
	if _, err := pgx.ForEachRow(rows, []any{&id, &symbol, &op, &price, &action, &note, &status, &msg, &firedPrice, &at, &fill, &notional, &levelCcy}, func() error {
		sym := symbol
		switch {
		case status == "failed":
			add("alert_failed", id, "Alert order not filled · "+symbol, msg, &sym, "/automations", at)
		case action == "notify":
			title := fmt.Sprintf("%s rose above %s", symbol, InCurrency(price, levelCcy))
			if op == "below" {
				title = fmt.Sprintf("%s fell below %s", symbol, InCurrency(price, levelCcy))
			}
			body := "Now " + InCurrency(firedPrice, levelCcy)
			if note != "" {
				body += " · " + note
			}
			add("alert", id, title, body, &sym, "/automations", at)
		default:
			verb := map[string]string{"buy": "Bought", "sell": "Sold"}[action]
			add("alert_order", id, "Alert order filled · "+symbol,
				fmt.Sprintf("%s %s at %s (trigger %s)", verb, paper.Money(*notional), paper.Money(fill), paper.Money(price)), &sym, "/account", at)
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}

	// Fired odds triggers, with the desk's P of the option the market prices (binary: Yes).
	var forecastID, directAlert string
	var desk *float64
	rows, _ = db.Query(ctx, `select a.id::text, a.op, a.price, a.note, a.fired_price, a.fired_at, coalesce(f.id::text,''), coalesce(a.market_id,''),
			coalesce(a.linked_market->>'question',f.result->'market'->>'question', f.question),
			(select (p->>'p')::float8 from jsonb_array_elements(coalesce(f.result->'probabilities', '[]')) p
			  where p->>'outcome' = coalesce(f.result->'market'->>'outcome', 'Yes') limit 1)
		from alerts a left join forecasts f on f.id = a.forecast_id
		where a.user_id = $1 and a.trigger = 'odds' and a.status = 'fired' and a.fired_at > now() - interval '14 days'
		order by a.fired_at desc limit 50`, userID)
	if _, err := pgx.ForEachRow(rows, []any{&id, &op, &price, &note, &firedPrice, &at, &forecastID, &directAlert, &question, &desk}, func() error {
		verb := map[string]string{"above": "rose above", "below": "fell below"}[op]
		body := "Now " + Pct(firedPrice)
		if desk != nil {
			body += " · desk " + Pct(*desk)
		}
		if note != "" {
			body += " · " + note
		}
		link := "/forecasts/" + forecastID
		if directAlert != "" {
			link = "/automations?tab=history&alert=" + id
		}
		add("odds_alert", id, fmt.Sprintf(`Polymarket moved: "%s" %s %s`, clip(question, 80), verb, Pct(price)), body, nil, link, at)
		return nil
	}); err != nil {
		return nil, 0, err
	}

	// Fired and failed drawdown triggers (drawdown.go). A sale's trade may be gone after a reset.
	var bps int
	var ath float64
	var ccy, fired string // fired: the outcome
	var sellPct *int
	var soldQty, soldAt, soldPnl *float64
	rows, _ = db.Query(ctx, `select a.id::text, a.symbol, a.drawdown_bps, a.ath, a.ath_currency, a.status, coalesce(a.outcome, ''), coalesce(a.error, ''),
			a.fired_price, a.fired_at, a.note, a.sell_pct, t.qty, t.price, t.realized_pnl
		from alerts a left join trades t on t.id = a.trade_id
		where a.user_id = $1 and a.trigger = 'drawdown' and a.status in ('fired', 'failed') and a.fired_at > now() - interval '14 days'
		order by a.fired_at desc limit 50`, userID)
	if _, err := pgx.ForEachRow(rows, []any{&id, &symbol, &bps, &ath, &ccy, &status, &fired, &msg, &firedPrice, &at, &note, &sellPct,
		&soldQty, &soldAt, &soldPnl}, func() error {
		sym, pct, link := symbol, PctLabel(float64(bps)/100), "/automations?tab=history&alert="+id
		level := fmt.Sprintf("%s below ATH %s", pct, InCurrency(ath, ccy))
		switch {
		case status == "failed":
			add("drawdown_failed", id, "Drawdown sell not filled · "+symbol, msg, &sym, link, at)
		case fired == "sold":
			body := fmt.Sprintf("%s · sold %d%% of the paper long", level, orZero(sellPct))
			if soldQty != nil {
				body = fmt.Sprintf("%s · sold %s %s (%d%% of the long) at %s · realized %s", level, fmtQty(*soldQty), symbol, orZero(sellPct),
					paper.Money(*soldAt), signed(*soldPnl))
			}
			add("drawdown_order", id, "Drawdown sell filled · "+symbol, body, &sym, "/account", at)
		default:
			body := "Now " + InCurrency(firedPrice, ccy) + " · ATH " + InCurrency(ath, ccy)
			if fired == "nothing_to_sell" {
				body += " · Nothing to sell: no paper long"
			}
			if note != "" {
				body += " · " + note
			}
			add("drawdown_alert", id, fmt.Sprintf("%s fell %s below its all-time high", symbol, pct), body, &sym, link, at)
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}

	var tradeNotional float64
	rows, _ = db.Query(ctx, `select id::text, symbol, side, qty, price, notional, realized_pnl, source, note, created_at from trades
		where user_id = $1 and source in ('stop_loss', 'take_profit', 'autopilot', 'copy') and created_at > now() - interval '14 days'
		order by created_at desc limit 50`, userID)
	if _, err := pgx.ForEachRow(rows, []any{&id, &symbol, &side, &qty, &price, &tradeNotional, &realized, &source, &note, &at}, func() error {
		sym, verb := symbol, map[string]string{"buy": "Bought", "sell": "Sold"}[side]
		switch source {
		case "autopilot":
			add(source, id, fmt.Sprintf("Autopilot %s %s", strings.ToLower(verb), symbol), note, &sym, "/account", at)
		case "copy": // the note is "Copy · <agent>" (round 7)
			add("copy_order", id, fmt.Sprintf("Copied %s · %s %s", strings.TrimPrefix(note, "Copy · "), strings.ToLower(verb), symbol),
				fmt.Sprintf("%s at %s", paper.Money(tradeNotional), paper.Money(price)), &sym, "/account", at)
		default:
			title := "Stop loss hit · " + symbol
			if source == "take_profit" {
				title = "Target hit · " + symbol
			}
			add(source, id, title, fmt.Sprintf("%s %s %s at %s · realized %s", verb, fmtQty(qty), symbol, paper.Money(price), signed(realized)),
				&sym, "/account", at)
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}

	rows, _ = db.Query(ctx, `select p.id::text,a.name,coalesce(p.stop_reason,''),p.stopped_at from copy_plans p join live_agents a on a.id=p.agent_id
  where p.user_id=$1 and p.status='stopped' and p.stopped_at>now()-interval '14 days' order by p.stopped_at desc limit 50`, userID)
	if _, err := pgx.ForEachRow(rows, []any{&id, &note, &msg, &at}, func() error {
		body := "Stopped by you; positions kept or closed."
		if msg == "account_disabled" {
			body = "Account disabled by an admin; positions kept."
		}
		add("copy_stopped", id, "Stopped copying "+note, body, nil, "/account", at)
		return nil
	}); err != nil {
		return nil, 0, err
	}

	var every, body string
	rows, _ = db.Query(ctx, `select r.id::text, a.symbol, a.action, a.every, r.status, r.body, coalesce(r.error, ''), r.ran_at
		from alert_runs r join alerts a on a.id = r.alert_id
		where r.user_id = $1 and a.user_id=$1 and a.action<>'research' and r.ran_at > now() - interval '14 days' order by r.ran_at desc limit 50`, userID)
	if _, err := pgx.ForEachRow(rows, []any{&id, &symbol, &action, &every, &status, &body, &msg, &at}, func() error {
		sym := symbol
		switch {
		case status == "failed":
			add("workflow_failed", id, "Scheduled order not filled · "+symbol, msg, &sym, "/automations", at)
		case action == "notify":
			add("workflow", id, map[string]string{"day": "Daily", "weekdays": "Weekday", "week": "Weekly"}[every]+" brief · "+symbol, body, &sym, "/automations", at)
		default:
			add("workflow_order", id, fmt.Sprintf("Scheduled %s filled · %s", action, symbol), body, &sym, "/automations", at)
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}

	var chatID string
	rows, _ = db.Query(ctx, `select r.id::text,a.symbol,coalesce(nullif(a.note,''),a.prompt,''),coalesce(r.excerpt,''),c.id::text,r.ran_at
 from alert_runs r join alerts a on a.id=r.alert_id and a.user_id=r.user_id
 join chats c on c.id=r.report_chat_id and c.user_id=r.user_id
 where r.user_id=$1 and a.action='research' and r.status='ok' and r.notification_eligible
 and r.ran_at>clock_timestamp()-interval '14 days' order by r.ran_at desc,r.id desc limit 50`, userID)
	if _, err := pgx.ForEachRow(rows, []any{&id, &symbol, &note, &body, &chatID, &at}, func() error {
		var sym *string
		if symbol != "" {
			sym = &symbol
		}
		add("workflow_research", id, "Scheduled research · "+clip(note, 80), clip(body, 160), sym, "/chat/"+chatID, at)
		return nil
	}); err != nil {
		return nil, 0, err
	}

	var probs []struct {
		Outcome string  `json:"outcome"`
		P       float64 `json:"p"`
	}
	var completed, resolved *time.Time
	var outcome *string
	var created time.Time
	var view *forecast.DeskView
	rows, _ = db.Query(ctx, `select id::text, question, shape, status, coalesce(result->'probabilities', '[]'), completed_at, created_at, outcome, resolved_at,
		case when shape = 'ordered' then result->'view' end
		from forecasts where user_id = $1 and status in ('completed', 'failed') and seeded_from is null
		  and greatest(coalesce(completed_at, created_at), resolved_at) > now() - interval '14 days'
		order by greatest(coalesce(completed_at, created_at), resolved_at) desc limit 50`, userID)
	since := time.Now().Add(-14 * 24 * time.Hour)
	if _, err := pgx.ForEachRow(rows, []any{&id, &question, &shape, &status, &probs, &completed, &created, &outcome, &resolved, &view}, func() error {
		link, body := "/forecasts/"+id, clip(question, 120)
		switch {
		case status == "failed" && created.After(since): // a failed run has no completed_at: it ends a minute or so in
			add("forecast_failed", id, "Forecast failed", body, nil, link, created)
		case status == "completed" && completed != nil && completed.After(since):
			title := "Forecast ready"
			if read, ok := forecast.ViewTitle(view); ok { // an ordered report whose view leads: its median or range, as the page states it
				title += " · " + read
			} else if len(probs) > 0 { // the headline: Yes for binary, else the likeliest outcome
				top := probs[0]
				for _, p := range probs[1:] {
					if shape != "binary" && p.P > top.P {
						top = p
					}
				}
				title = fmt.Sprintf("Forecast ready · %s %d%%", top.Outcome, int(math.Round(top.P*100)))
			}
			add("forecast_completed", id, title, body, nil, link, *completed)
		}
		if resolved != nil && outcome != nil && resolved.After(since) {
			add("forecast_resolved", id, "Forecast resolved · "+*outcome, body, nil, link, *resolved)
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}

	var seen time.Time
	if err := db.QueryRow(ctx, "select notifications_seen_at from users where id = $1", userID).Scan(&seen); err != nil {
		return nil, 0, err
	}
	slices.SortStableFunc(out, func(a, b Notification) int { return b.At.Compare(a.At) })
	unread := 0
	for _, n := range out {
		if n.At.After(seen) {
			unread++
		}
	}
	return out, min(unread, 99), nil
}

func orZero[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// fmtQty is "12.4", "0.004215": up to 4 decimals from 1 up, 6 below.
func fmtQty(q float64) string {
	d := 6
	if math.Abs(q) >= 1 {
		d = 4
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(q, 'f', d, 64), "0"), ".")
}

// signed is P&L to the cent: "+$12.30", "−$0.32", "−$1,084.10".
func signed(x float64) string {
	sign := "+"
	if x < 0 {
		sign, x = "−", -x
	}
	s := strconv.FormatFloat(x, 'f', 2, 64)
	for i := len(s) - 6; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + "$" + s
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (h *handler) notifications(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 50 {
			app.WriteError(w, bad("limit must be 1..50."))
			return
		}
		limit = n
	}
	items, unread, err := feed(r.Context(), h.d.DB, httpx.UserID(r.Context()))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items[:min(limit, len(items))], "unread": unread})
}

func (h *handler) seen(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := h.d.DB.Exec(ctx, "update users set notifications_seen_at = now() where id = $1", httpx.UserID(ctx)); err != nil {
		app.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
