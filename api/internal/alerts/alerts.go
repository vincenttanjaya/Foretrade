// Package alerts is Automations (contract "Automations"): price triggers on
// the universe that notify the user or place a paper order (limit and
// stop-entry orders), daily, weekday or weekly schedules
// that post a brief or place a paper order (dollar-cost averaging), odds
// triggers on a forecast's matched Polymarket market that notify (odds.go) and
// all-time-high drawdown triggers that notify or sell the paper long
// (drawdown.go), checked every 15 s, plus the notifications bell's feed,
// derived on read. No model call, no credits.
package alerts

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/forecast"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/researchrun"
)

// Alert is the contract Alert.
type Alert struct {
	Prompt       *string                `json:"prompt"`
	Thesis       *researchrun.Thesis    `json:"thesis"`
	NotifyWhen   *string                `json:"notifyWhen"`
	QuotaStreak  int                    `json:"quotaStreak"`
	ID           string                 `json:"id"`
	Symbol       string                 `json:"symbol"`  // "" for an odds trigger
	Trigger      string                 `json:"trigger"` // price | schedule | odds | drawdown
	Op           *string                `json:"op"`      // above | below; null for a schedule
	Price        *float64               `json:"price"`   // odds: the Yes-probability level, 0..1
	Every        *string                `json:"every"`   // day | weekdays | week; null for a price trigger
	At           *string                `json:"at"`      // "HH:MM" UTC
	Weekday      *int                   `json:"weekday"` // 0 = Sunday, weekly only
	NextRunAt    *time.Time             `json:"nextRunAt"`
	LastRun      *RunLog                `json:"lastRun"` // the newest run of a schedule; GET /api/alerts only
	Action       string                 `json:"action"`  // notify | buy | sell (| research; a drawdown trigger: notify | sell)
	Notional     *float64               `json:"notional"`
	StopLoss     *float64               `json:"stopLoss"`
	TakeProfit   *float64               `json:"takeProfit"`
	Note         string                 `json:"note"`
	Source       string                 `json:"source"` // manual | ticket | agent
	ProposalID   *string                `json:"proposalId"`
	Status       string                 `json:"status"` // active | fired | failed | cancelled
	TradeID      *string                `json:"tradeId"`
	Error        *string                `json:"error"`
	FiredPrice   *float64               `json:"firedPrice"` // odds: the Yes price that fired it; drawdown: in the high's currency; price: in Currency
	Currency     *string                `json:"currency"`   // price triggers: the level's currency when not USD (a quote-feed stock's local currency); null is USD
	CreatedAt    time.Time              `json:"createdAt"`
	FiredAt      *time.Time             `json:"firedAt"`
	ForecastID   *string                `json:"forecastId"` // odds triggers only
	MarketSlug   *string                `json:"marketSlug"`
	MarketID     *string                `json:"marketId"`
	LinkedMarket *forecast.LinkedMarket `json:"linkedMarket"`
	Market       *OddsMarket            `json:"market"`   // odds: the forecast's stored market, read on list and create
	Drawdown     *Drawdown              `json:"drawdown"` // drawdown triggers only (drawdown.go)
}

// RunLog is one run of a schedule (alert_runs).
type RunLog struct {
	ID           string    `json:"id"`
	ReportChatID *string   `json:"reportChatId"`
	Excerpt      *string   `json:"excerpt"`
	Verdict      *string   `json:"verdict"`
	At           time.Time `json:"at"`
	Status       string    `json:"status"` // ok | failed
	Body         string    `json:"body"`
	Error        *string   `json:"error"`
}

// Input is a new alert, as POST /api/alerts and the agent's set_price_alert
// and set_schedule take it. Op and Price are zero when absent.
type Input struct {
	Prompt       string              `json:"prompt"`
	Thesis       *researchrun.Thesis `json:"thesis"`
	NotifyWhen   string              `json:"notifyWhen"`
	Symbol       string              `json:"symbol"`
	Trigger      string              `json:"trigger"` // price (default) | schedule | odds | drawdown
	Op           string              `json:"op"`
	Price        float64             `json:"price"`
	Every        string              `json:"every"`
	At           string              `json:"at"`
	Weekday      *int                `json:"weekday"`
	Action       string              `json:"action"`
	Notional     *float64            `json:"notional"`
	StopLoss     *float64            `json:"stopLoss"`
	TakeProfit   *float64            `json:"takeProfit"`
	Note         string              `json:"note"`
	Source       string              `json:"source"`
	ProposalID   *string             `json:"proposalId"`
	ForecastID   string              `json:"forecastId"` // odds only
	MarketSlug   string              `json:"marketSlug"`
	MarketID     string              `json:"marketId"`
	DrawdownPct  float64             `json:"drawdownPct"` // drawdown only: fire this many % below the all-time high
	SellPct      *float64            `json:"sellPct"`     // drawdown sell only: the share of the paper long sold, 1..100
	Currency     string              `json:"currency"`    // price notify: ""/USD input is converted to a quote-feed stock's native currency
	linkedMarket *forecast.LinkedMarket
	ath          *ATH // drawdown: the resolved high (ValidateDrawdown)
}

const (
	columns = `id::text, symbol, trigger, op, price, every, at_minute, weekday, next_run_at, action, notional, stop_loss, take_profit,
		note, source, proposal_id::text, status, trade_id::text, error, fired_price, created_at, fired_at, forecast_id::text, market_slug, market_id, linked_market, prompt, thesis, notify_when, quota_streak,
		drawdown_bps, sell_pct, ath, ath_at, ath_from, ath_source, ath_ticker, ath_currency, ath_live, outcome, currency`
	maxActive = 20
)

func scan(row pgx.Row, extra ...any) (Alert, error) {
	var a Alert
	var at *int
	var dd drawdownRow
	err := row.Scan(append([]any{&a.ID, &a.Symbol, &a.Trigger, &a.Op, &a.Price, &a.Every, &at, &a.Weekday, &a.NextRunAt, &a.Action,
		&a.Notional, &a.StopLoss, &a.TakeProfit, &a.Note, &a.Source, &a.ProposalID, &a.Status, &a.TradeID, &a.Error, &a.FiredPrice,
		&a.CreatedAt, &a.FiredAt, &a.ForecastID, &a.MarketSlug, &a.MarketID, &a.LinkedMarket, &a.Prompt, &a.Thesis, &a.NotifyWhen, &a.QuotaStreak,
		&dd.bps, &dd.sellPct, &dd.ath, &dd.at, &dd.from, &dd.source, &dd.ticker, &dd.currency, &dd.live, &dd.outcome, &a.Currency}, extra...)...)
	if at != nil {
		a.At = new(fmt.Sprintf("%02d:%02d", *at/60, *at%60))
	}
	a.Drawdown = dd.view(a.Symbol)
	return a, err
}

// Label is the trade note and ref label of an alert's fill: "Alert: SOL below $175.00".
func Label(symbol, op string, price float64) string {
	return fmt.Sprintf("Alert: %s %s %s", symbol, op, paper.Money(price))
}

// priceCurrency is the currency column of a new alert: a price trigger's
// non-USD level currency, else "" (stored null).
func priceCurrency(in Input) string {
	if in.Trigger != "price" {
		return ""
	}
	return in.Currency
}

func bad(format string, args ...any) error { return &app.BadRequest{Msg: fmt.Sprintf(format, args...)} }

// Validate normalizes in (canonical symbol, defaults) and checks it as POST
// /api/alerts does: a price trigger against the live mark, which it returns
// (a schedule makes no market call and returns 0). Rule errors are
// *app.BadRequest or *paper.ValidationError (both 400 with a user-facing
// message); a market failure is market.ErrUpstream.
func Validate(ctx context.Context, m *market.Client, in Input) (Input, float64, error) {
	info, ok := market.Lookup(in.Symbol)
	symbollessResearch := strings.EqualFold(strings.TrimSpace(in.Action), "research") && strings.EqualFold(strings.TrimSpace(in.Trigger), "schedule") && strings.TrimSpace(in.Symbol) == ""
	if !ok && !symbollessResearch {
		return in, 0, bad("unknown symbol %q", in.Symbol)
	}
	in.Symbol, in.Note = info.Symbol, strings.TrimSpace(in.Note)
	in.Trigger = cmp.Or(strings.ToLower(strings.TrimSpace(in.Trigger)), "price")
	in.Op = strings.ToLower(strings.TrimSpace(in.Op))
	in.Every, in.At = strings.ToLower(strings.TrimSpace(in.Every)), strings.TrimSpace(in.At)
	in.Action = cmp.Or(strings.ToLower(strings.TrimSpace(in.Action)), "notify")
	in.Source = cmp.Or(in.Source, "manual")
	if in.Action == "research" {
		in.Prompt = strings.TrimSpace(in.Prompt)
		in.NotifyWhen = cmp.Or(strings.ToLower(strings.TrimSpace(in.NotifyWhen)), "always")
	}
	if in.Notional != nil { // cents: a computed size such as 99.99999999999999 is stored as 100
		in.Notional = new(math.Round(*in.Notional*100) / 100)
	}
	order := in.Action == "buy" || in.Action == "sell"
	if in.Trigger == "schedule" {
		return in, 0, validSchedule(in, order)
	}
	switch {
	case in.Trigger != "price":
		return in, 0, bad("trigger must be price, schedule, odds or drawdown.")
	case in.Op != "above" && in.Op != "below":
		return in, 0, bad("op must be above or below.")
	case !(in.Price > 0) || math.IsInf(in.Price, 0):
		return in, 0, bad("price must be a positive number.")
	}
	if err := validCommon(in, order); err != nil {
		return in, 0, err
	}
	switch {
	case !order && (in.Notional != nil || in.StopLoss != nil || in.TakeProfit != nil):
		return in, 0, bad("A notify alert takes no notional, stopLoss or takeProfit.")
	case order && !(in.Notional != nil && *in.Notional >= 10 && !math.IsInf(*in.Notional, 0)):
		return in, 0, bad("notional must be at least $10")
	}
	if order { // the levels protect the position the fill opens: checked against the trigger price
		side := 1.0
		if in.Action == "sell" {
			side = -1
		}
		if err := paper.CheckProtection(side, in.Price, in.StopLoss, in.TakeProfit); err != nil {
			return in, 0, err
		}
	}
	if in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency)); in.Currency == "USD" {
		in.Currency = ""
	}
	var mark float64
	var err error
	if in.Currency != "" || !order && info.QuoteFeed() {
		mark, err = localMark(ctx, m, &in, order)
	} else {
		mark, err = m.Price(ctx, in.Symbol)
	}
	if err != nil {
		var br *app.BadRequest
		if !errors.As(err, &br) && !errors.Is(err, market.ErrUpstream) { // a pair missing from the ticker
			err = fmt.Errorf("%w: %v", market.ErrUpstream, err)
		}
		return in, 0, err
	}
	switch {
	case in.Op == "above" && in.Price <= mark:
		return in, mark, bad("%s is already above %s (now %s).", in.Symbol, InCurrency(in.Price, in.Currency), InCurrency(mark, in.Currency))
	case in.Op == "below" && in.Price >= mark:
		return in, mark, bad("%s is already below %s (now %s).", in.Symbol, InCurrency(in.Price, in.Currency), InCurrency(mark, in.Currency))
	}
	return in, mark, nil
}

// localMark normalizes a new quote-feed notification to its native currency
// and returns the price in those units. Legacy callers' USD amounts are
// converted at the quote's FX rate, never reinterpreted as native amounts.
// Paper orders fill in USD; a perp or crypto pair has no local price.
func localMark(ctx context.Context, m *market.Client, in *Input, order bool) (float64, error) {
	q, err := m.Quote(ctx, in.Symbol)
	if err != nil {
		return 0, err
	}
	switch qf := q.QuoteFeed; {
	case qf == nil:
		return 0, bad("%s alerts are set in USD.", in.Symbol)
	case in.Currency != "" && qf.Currency != in.Currency:
		return 0, bad("%s alerts are set in USD or %s.", in.Symbol, qf.Currency)
	case order:
		return 0, bad("A buy or sell alert is set in USD; only a notify alert takes %s.", qf.Currency)
	case !(qf.LocalPrice > 0):
		return 0, fmt.Errorf("%w: no %s price for %s", market.ErrUpstream, qf.Currency, in.Symbol)
	default:
		if in.Currency == "" && qf.Currency != "USD" {
			if !(qf.FX > 0) || math.IsInf(qf.FX, 0) {
				return 0, fmt.Errorf("%w: no %s FX rate for %s", market.ErrUpstream, qf.Currency, in.Symbol)
			}
			in.Price /= qf.FX
			if !(in.Price > 0) || math.IsInf(in.Price, 0) {
				return 0, bad("price must be a positive number.")
			}
			in.Currency = qf.Currency
		}
		return qf.LocalPrice, nil
	}
}

// validCommon is the checks a price trigger and a schedule share.
func validCommon(in Input, order bool) error {
	switch {
	case !order && in.Action != "notify" && !(in.Action == "research" && in.Trigger == "schedule"):
		return bad("action must be notify, buy or sell.")
	case in.Source != "manual" && in.Source != "ticket" && in.Source != "agent":
		return bad("source must be manual, ticket or agent.")
	case utf8.RuneCountInString(in.Note) > 200:
		return bad("note must be at most 200 characters.")
	case in.ProposalID != nil && !canonical(*in.ProposalID):
		return bad("proposalId must be a UUID.")
	case in.Action != "research" && (in.Prompt != "" || in.Thesis != nil || in.NotifyWhen != ""):
		return bad("Research fields require action research.")
	}
	return nil
}

// validSchedule checks a schedule: every day, weekday or week at a UTC time, a brief
// (notify) or an order of a fixed notional, with no levels.
func validSchedule(in Input, order bool) error {
	_, err := time.Parse("15:04", in.At)
	switch {
	case in.Op != "" || in.Price != 0:
		return bad("A scheduled automation takes no op or price.")
	case in.Every != "day" && in.Every != "weekdays" && in.Every != "week":
		return bad("every must be day, weekdays or week.")
	case err != nil || len(in.At) != 5:
		return bad("at must be HH:MM (UTC).")
	case (in.Every == "week") != (in.Weekday != nil) || in.Weekday != nil && (*in.Weekday < 0 || *in.Weekday > 6):
		return bad("weekday (0–6, 0 = Sunday) is required for a weekly schedule and only then.")
	}
	if err := validCommon(in, order); err != nil {
		return err
	}
	if in.Action == "research" {
		if n := utf8.RuneCountInString(in.Prompt); n < 10 || n > 500 {
			return bad("prompt must be 10–500 characters.")
		}
		if in.NotifyWhen != "always" && in.NotifyWhen != "changed" && in.NotifyWhen != "invalidated" {
			return bad("notifyWhen must be always, changed or invalidated.")
		}
		if in.Thesis != nil && (utf8.RuneCountInString(in.Thesis.Statement) > 300 || utf8.RuneCountInString(in.Thesis.Invalidation) > 300) {
			return bad("thesis statement and invalidation must each be at most 300 characters.")
		}
	}
	switch {
	case in.StopLoss != nil || in.TakeProfit != nil:
		return bad("A scheduled order takes no stopLoss or takeProfit.")
	case !order && in.Notional != nil:
		return bad("A notify alert takes no notional, stopLoss or takeProfit.")
	case order && !(in.Notional != nil && *in.Notional >= 10 && !math.IsInf(*in.Notional, 0)):
		return bad("notional must be at least $10")
	}
	return nil
}

// canonical is a UUID in the 8-4-4-4-12 form only.
func canonical(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil && len(s) == 36
}

// Create validates in and stores it as the user's active alert.
// ponytail: buying power is not reserved; the paper engine checks it when the alert fires.
// The owner row serializes every Create and Resume against both active caps.
func Create(ctx context.Context, d *app.Deps, userID string, in Input) (Alert, error) {
	var m *forecast.MarketOdds
	var err error
	var price float64 // drawdown: the current price, shown on the created alert
	switch strings.ToLower(strings.TrimSpace(in.Trigger)) {
	case "odds":
		in, m, err = ValidateOdds(ctx, d, userID, in, true)
	case "drawdown":
		in, price, err = ValidateDrawdown(ctx, d, in)
	default:
		in, _, err = Validate(ctx, d.Market, in)
	}
	if err != nil {
		return Alert{}, err
	}
	var op, every, forecastID, marketSlug, marketID *string // nil: the columns the other triggers leave null
	var directOdds *OddsMarket
	var level *float64
	var minute *int
	var next *time.Time
	var dd drawdownRow
	switch in.Trigger {
	case "schedule":
		every, minute, next = &in.Every, new(atMinute(in.At)), new(FirstRun(in))
	case "odds":
		op, level = &in.Op, &in.Price
		if in.ForecastID != "" {
			forecastID = &in.ForecastID
		} else {
			marketSlug, marketID = &in.MarketSlug, &in.MarketID
			directOdds = oddsMarket(m)
		}
	case "drawdown":
		h := in.ath
		dd = drawdownRow{bps: new(int(math.Round(in.DrawdownPct * 100))), ath: &h.High, at: &h.At, from: &h.From, source: &h.Source,
			ticker: &h.Ticker, currency: &h.Currency, live: &h.Live}
		if in.SellPct != nil {
			dd.sellPct = new(int(*in.SellPct))
		}
	default:
		op, level = &in.Op, &in.Price
	}
	tx, err := d.DB.Begin(ctx)
	if err != nil {
		return Alert{}, err
	}
	defer tx.Rollback(ctx)
	var enabled bool
	if err = tx.QueryRow(ctx, "select disabled_at is null from users where id=$1 for no key update", userID).Scan(&enabled); err != nil {
		return Alert{}, err
	}
	if !enabled {
		return Alert{}, app.ErrNotFound
	}
	var active, research int
	if err = tx.QueryRow(ctx, "select count(*),count(*) filter(where action='research') from alerts where user_id=$1 and status='active'", userID).Scan(&active, &research); err != nil {
		return Alert{}, err
	}
	if active >= maxActive {
		return Alert{}, &app.Conflict{Msg: fmt.Sprintf("You have %d active alerts. Cancel one first.", maxActive)}
	}
	if in.Action == "research" && research >= 3 {
		return Alert{}, &app.Conflict{Msg: "You have 3 active research schedules. Cancel one first."}
	}
	var prompt, notify *string
	if in.Action == "research" {
		prompt, notify = &in.Prompt, &in.NotifyWhen
	}
	a, err := scan(tx.QueryRow(ctx, `
insert into alerts (user_id, symbol, trigger, op, price, every, at_minute, weekday, next_run_at, action, notional, stop_loss,
			take_profit, note, source, proposal_id, forecast_id, market_slug, market_id, linked_market, market_odds, prompt, thesis, notify_when,
			drawdown_bps, sell_pct, ath, ath_at, ath_from, ath_source, ath_ticker, ath_currency, ath_live, currency)
		select $1::uuid, $2::text, $3::text, $4::text, $5::float8, $6::text, $7::int, $8::int, $9::timestamptz, $10::text, $11::float8,
			$12::float8, $13::float8, $14::text, $15::text, $16::uuid, $18::uuid, $19::text, $20::text, $21::jsonb, $22::jsonb, $23::text, $24::jsonb, $25::text,
			$26::int, $27::int, $28::float8, $29::timestamptz, $30::timestamptz, $31::text, $32::text, $33::text, $34::bool, nullif($35::text, '')
		where (select count(*) from alerts where user_id = $1 and status = 'active') < $17
		returning `+columns,
		userID, in.Symbol, in.Trigger, op, level, every, minute, in.Weekday, next, in.Action, in.Notional, in.StopLoss, in.TakeProfit,
		in.Note, in.Source, in.ProposalID, maxActive, forecastID, marketSlug, marketID, in.linkedMarket, directOdds, prompt, in.Thesis, notify,
		dd.bps, dd.sellPct, dd.ath, dd.at, dd.from, dd.source, dd.ticker, dd.currency, dd.live, priceCurrency(in)))
	if m != nil {
		a.Market = oddsMarket(m)
	}
	a.Drawdown.withPrice(price)
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Alert{}, &app.Conflict{Msg: fmt.Sprintf("You have %d active alerts. Cancel one first.", maxActive)}
	case errors.As(err, &pg) && pg.Code == "23505": // alerts_proposal_id_key
		return Alert{}, &app.Conflict{Msg: "This alert was already set."}
	}
	if err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

// Run checks the alerts every 15 s, then sends new bell items by email and
// Lark (Deliver), until ctx ends; main starts it next to the paper watcher.
// ponytail: checks the last price only, so a wick between ticks is missed (as the stop watcher does).
func Run(ctx context.Context, d *app.Deps) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := Check(ctx, d); err != nil && ctx.Err() == nil {
				slog.Error("alerts check", "err", err)
			}
			if err := Deliver(ctx, d); err != nil && ctx.Err() == nil {
				slog.Error("notification delivery", "err", err)
			}
		}
	}
}

// Check fires every active price alert of an enabled account whose trigger
// the cached mark has reached (above at mark ≥ price, below at mark ≤ price),
// runs the due schedules, then the odds triggers (odds.go) and the drawdown
// triggers (drawdown.go) and, hourly, prunes old schedule runs. With none
// active it makes no market call.
func Check(ctx context.Context, d *app.Deps) error {
	return errors.Join(checkPrices(ctx, d), runSchedules(ctx, d), checkOdds(ctx, d), checkDrawdowns(ctx, d), prune(ctx, d))
}

func checkPrices(ctx context.Context, d *app.Deps) error {
	type armed struct {
		id, symbol, op, currency string
		price                    float64
	}
	rows, _ := d.DB.Query(ctx, `select a.id::text, a.symbol, a.op, a.price, coalesce(a.currency, '') from alerts a join users u on u.id = a.user_id
		where a.status = 'active' and a.trigger = 'price' and u.disabled_at is null`)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (armed, error) {
		var a armed
		return a, r.Scan(&a.id, &a.symbol, &a.op, &a.price, &a.currency)
	})
	if err != nil || len(list) == 0 {
		return err
	}
	assets, err := d.Market.Assets(ctx)
	if err != nil {
		return err
	}
	marks := make(map[string]float64, len(assets))
	var local []string // stocks with a level in their own currency
	for _, a := range assets {
		marks[a.Symbol] = a.Price
	}
	for _, a := range list {
		if a.currency != "" && !slices.Contains(local, a.symbol) {
			local = append(local, a.symbol)
		}
	}
	if len(local) > 0 {
		// Their local quote, real-time where the venue has a source, at most a minute old.
		fresh, err := d.Market.FillAssets(ctx, local)
		if err != nil {
			slog.Warn("alerts: local quotes", "err", err) // the quotes that did arrive still count
		}
		for _, a := range fresh {
			if qf := a.QuoteFeed; qf != nil && qf.LocalPrice > 0 {
				marks[a.Symbol+" "+qf.Currency] = qf.LocalPrice
			}
		}
	}
	for _, a := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		key := a.symbol
		if a.currency != "" {
			key += " " + a.currency
		}
		mark, ok := marks[key]
		if !ok || !(a.op == "above" && mark >= a.price || a.op == "below" && mark <= a.price) {
			continue
		}
		if err := fire(context.WithoutCancel(ctx), d, a.id, mark); err != nil {
			slog.Error("alert fire", "id", a.id, "err", err)
		}
	}
	return nil
}

// fire claims the alert once (active → fired in one update, so concurrent
// checks never fire it twice) and, for buy/sell, places its paper order,
// recording the trade or why it failed.
// ponytail: claim and order are two transactions; a crash between them leaves a fired alert without a trade.
func fire(ctx context.Context, d *app.Deps, id string, mark float64) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, err := d.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID string
	// The owner is locked before the alert, in the same order as account
	// disabling/deletion. This rechecks a disable that committed while waiting,
	// instead of reading the old enabled owner from an UPDATE's statement snapshot.
	err = tx.QueryRow(ctx, `select u.id::text from users u join alerts a on a.user_id=u.id
		where a.id=$1 and a.status='active' and u.disabled_at is null for no key update of u`, id).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	a, err := scan(tx.QueryRow(ctx, `update alerts set status = 'fired', fired_at = now(), fired_price = $2
		where id = $1 and status = 'active' and exists (select 1 from users where users.id=alerts.user_id and disabled_at is null) returning `+columns+`, user_id::text`, id, mark), &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if a.Action == "notify" {
		return nil
	}
	// Release both row locks before the existing paper engine transaction.
	t, _, err := d.Paper.Order(ctx, userID, paper.OrderRequest{Symbol: a.Symbol, Side: a.Action, Notional: a.Notional,
		StopLoss: a.StopLoss, TakeProfit: a.TakeProfit, Source: "alert", Note: Label(a.Symbol, *a.Op, *a.Price), RefID: &a.ID})
	if err == nil {
		_, err = d.DB.Exec(ctx, "update alerts set trade_id = $2 where id = $1", a.ID, t.ID)
		return err
	}
	_, err = d.DB.Exec(ctx, "update alerts set status = 'failed', error = $2 where id = $1", a.ID, orderError(a.ID, err))
	return err
}

// orderError is why an alert's or schedule's paper order was refused, for the user.
func orderError(id string, err error) string {
	var ve *paper.ValidationError
	var halted *paper.HaltedError
	switch {
	case errors.Is(err, paper.ErrInsufficient):
		return "Not enough buying power when the alert fired."
	case errors.As(err, &ve):
		return ve.Msg
	case errors.As(err, &halted):
		return halted.Error()
	}
	slog.Error("alert order", "id", id, "err", err)
	return "The paper order could not be placed when the alert fired."
}

// Register mounts /api/alerts* and /api/notifications* behind the session check.
func Register(mux *http.ServeMux, d *app.Deps) {
	h := &handler{d}
	auth := func(f http.HandlerFunc) http.Handler { return d.Auth.Require(f) }
	mux.Handle("GET /api/alerts", auth(h.list))
	mux.Handle("GET /api/alerts/ath", auth(h.ath))
	mux.Handle("POST /api/alerts", auth(h.create))
	mux.Handle("DELETE /api/alerts/{id}", auth(h.cancel))
	mux.Handle("GET /api/alerts/{id}/runs", auth(h.runs))
	mux.Handle("POST /api/alerts/{id}/resume", auth(h.resume))
	mux.Handle("GET /api/notifications", auth(h.notifications))
	mux.Handle("POST /api/notifications/seen", auth(h.seen))
	mux.Handle("GET /api/notifications/channels", auth(h.getChannels))
	mux.Handle("PUT /api/notifications/channels", auth(h.putChannels))
	mux.Handle("POST /api/notifications/channels/test", auth(h.testChannels))
}

type handler struct{ d *app.Deps }

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, status := r.Context(), cmp.Or(r.URL.Query().Get("status"), "all")
	if status != "all" && status != "active" {
		app.WriteError(w, bad("status must be active or all."))
		return
	}
	list, err := List(ctx, h.d, httpx.UserID(ctx), status, 100)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if list == nil {
		list = []Alert{}
	}
	livePrices(ctx, h.d, list)
	httpx.JSON(w, http.StatusOK, map[string]any{"alerts": list})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := httpx.Decode(w, r, &in); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	a, err := Create(r.Context(), h.d, httpx.UserID(r.Context()), in)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"alert": a})
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	if !canonical(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	var status string
	err := h.d.DB.QueryRow(ctx, `with c as (update alerts set status = 'cancelled' where id = $1 and user_id = $2 and status in ('active','paused') returning 1)
		select case when exists (select from c) then 'cancelled_now' else status end from alerts where id = $1 and user_id = $2`,
		id, httpx.UserID(ctx)).Scan(&status)
	switch {
	case err != nil:
		app.WriteError(w, err) // no rows: unknown or another user's → 404
	case status == "cancelled":
		app.WriteError(w, &app.Conflict{Msg: "This alert was already cancelled."})
	case status != "cancelled_now":
		app.WriteError(w, &app.Conflict{Msg: "This alert already fired."})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
