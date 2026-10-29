package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/researchrun"
)

// priceAlert is the "price_alert" card: an automation the user sets with one
// tap (POST /api/alerts with source "agent" and the card's proposalId). A
// price trigger fills op through distancePct; a schedule every, at and weekday;
// an odds trigger forecastId, market, op and price (a Yes probability, 0..1);
// a drawdown trigger drawdownPct, sellPct, ath, threshold and fromAthPct, with
// markPrice the current price and distancePct its distance to the threshold.
type priceAlert struct {
	Prompt      *string             `json:"prompt,omitempty"`
	Thesis      *researchrun.Thesis `json:"thesis,omitempty"`
	NotifyWhen  *string             `json:"notifyWhen,omitempty"`
	ProposalID  string              `json:"proposalId"`
	Symbol      string              `json:"symbol"`  // "" for odds
	Trigger     string              `json:"trigger"` // price | schedule | odds | drawdown
	ForecastID  *string             `json:"forecastId,omitempty"`
	Market      *oddsMarket         `json:"market,omitempty"`
	Op          *string             `json:"op"`
	Price       *float64            `json:"price"`
	Currency    *string             `json:"currency,omitempty"` // price: price and markPrice units when not USD
	MarkPrice   *float64            `json:"markPrice"`          // odds: the market's stored Yes price
	DistancePct *float64            `json:"distancePct"`        // (price − mark) / mark × 100
	Every       *string             `json:"every"`
	At          *string             `json:"at"`
	Weekday     *int                `json:"weekday"`
	Action      string              `json:"action"`
	Notional    *float64            `json:"notional"`
	StopLoss    *float64            `json:"stopLoss"`
	TakeProfit  *float64            `json:"takeProfit"`
	Note        string              `json:"note"`
	DrawdownPct *float64            `json:"drawdownPct,omitempty"` // drawdown: fire this many % below the high
	SellPct     *float64            `json:"sellPct,omitempty"`     // drawdown sell: the share of the paper long sold
	ATH         *alerts.ATH         `json:"ath,omitempty"`         // drawdown: the high, its source and dates
	Threshold   *float64            `json:"threshold,omitempty"`   // drawdown: ath × (1 − drawdownPct / 100)
	FromATHPct  *float64            `json:"fromAthPct,omitempty"`  // drawdown: (markPrice / ath − 1) × 100
}

// oddsMarket is the market an odds card watches.
type oddsMarket struct {
	Question string  `json:"question"`
	URL      string  `json:"url"`
	Outcome  *string `json:"outcome"` // categorical: the option it prices
}

type alertInput struct {
	Symbol     string   `json:"symbol"`
	Op         string   `json:"op"`
	Price      float64  `json:"price"`
	Currency   string   `json:"currency"`
	Action     string   `json:"action"`
	Notional   *float64 `json:"notional_usd"`
	StopLoss   *float64 `json:"stop_loss"`
	TakeProfit *float64 `json:"take_profit"`
	Note       string   `json:"note"`
}

// alertTools shows at most one alert card per message, whichever tool draws it.
func (a *agentRun) alertTools() []llm.Tool {
	shown := false
	once := func(run func(context.Context, json.RawMessage) (string, error)) func(context.Context, json.RawMessage) (string, error) {
		return func(ctx context.Context, input json.RawMessage) (string, error) {
			if shown {
				return "", errors.New("one alert card per message; the card is already shown")
			}
			out, err := run(ctx, input)
			shown = err == nil
			return out, err
		}
	}
	return []llm.Tool{{
		Name: "set_price_alert",
		Description: "Draft a price-alert card; only user confirmation sets it. Notify or paper-trade when price crosses a level. " +
			"Require below < live price, above > live price. Buy/sell needs notional_usd ≥ 10; optional stop_loss / take_profit: " +
			"buy stop < trigger < target, sell reverse. Checked every 15 s. Returns distance from live price and USD at risk to an order's stop.",
		Schema: schema(map[string]any{
			"symbol":       map[string]any{"type": "string", "enum": symbols(), "description": "Ticker from the universe, e.g. SOL."},
			"op":           map[string]any{"type": "string", "enum": []string{"above", "below"}, "description": "Fire when the price rises above or falls below the level."},
			"price":        map[string]any{"type": "number", "description": "Trigger price in currency (default USD)."},
			"currency":     map[string]any{"type": "string", "description": "Notify: listing currency; omitted/USD converts to native at live FX. Orders: USD."},
			"action":       map[string]any{"type": "string", "enum": []string{"notify", "buy", "sell"}, "description": "notify (default), or the paper order to place when it fires."},
			"notional_usd": map[string]any{"type": "number", "minimum": 10, "description": "Order size in USD; buy/sell only."},
			"stop_loss":    map[string]any{"type": "number", "description": "Optional stop for the position the order opens; buy/sell only."},
			"take_profit":  map[string]any{"type": "number", "description": "Optional target for the position the order opens; buy/sell only."},
			"note":         map[string]any{"type": "string", "maxLength": 200, "description": "Why this level, shown on the card and in the notification."},
		}, "symbol", "op", "price", "note"),
		Run: once(a.setPriceAlert),
	}, {
		Name: "set_schedule",
		Description: "Show a scheduled-automation card the user sets with one tap: every day, weekday (Monday to Friday) or week at a UTC time, post a brief " +
			"on an asset (price, 24h move, wire sentiment, top headline) to their bell, or place a paper buy/sell of a fixed USD " +
			"amount (dollar-cost averaging). Use for 'every morning at 8 …' or 'buy $100 of BTC every Monday'. Times are UTC: " +
			"convert a stated timezone and say so.",
		Schema: schema(map[string]any{
			"symbol":       map[string]any{"type": "string", "enum": symbols(), "description": "Ticker from the universe, e.g. BTC."},
			"every":        map[string]any{"type": "string", "enum": []string{"day", "weekdays", "week"}, "description": "day (every day), weekdays (Monday to Friday) or week (one weekday)."},
			"at_utc":       map[string]any{"type": "string", "description": "Time of day in UTC, HH:MM."},
			"weekday":      map[string]any{"type": "integer", "minimum": 0, "maximum": 6, "description": "Weekly only: 0 = Sunday … 6 = Saturday."},
			"action":       map[string]any{"type": "string", "enum": []string{"notify", "buy", "sell", "research"}, "description": "research: 2 credits/run, read-only; notify (default): post a brief to the bell; or the paper order to place."},
			"notional_usd": map[string]any{"type": "number", "minimum": 10, "description": "Order size in USD per run; buy/sell only."},
			"note":         map[string]any{"type": "string", "maxLength": 200, "description": "Why, shown on the card."},
			"prompt":       map[string]any{"type": "string", "minLength": 10, "maxLength": 500},
			"thesis":       schema(map[string]any{"statement": map[string]any{"type": "string", "maxLength": 300}, "invalidation": map[string]any{"type": "string", "maxLength": 300}}, "statement", "invalidation"),
			"notifyWhen":   map[string]any{"type": "string", "enum": []string{"always", "changed", "invalidated"}},
		}, "every", "at_utc"),
		Run: once(a.setSchedule),
	}, {
		Name: "track_thesis", Description: "Propose a read-only scheduled thesis review (2 credits/run). Nothing is set until the user confirms. Symbol required; UTC day/weekdays/week cadence.",
		Schema: schema(map[string]any{"symbol": map[string]any{"type": "string", "enum": symbols()}, "thesis": map[string]any{"type": "string", "maxLength": 300}, "invalidation": map[string]any{"type": "string", "maxLength": 300}, "every": map[string]any{"type": "string", "enum": []string{"day", "weekdays", "week"}}, "at_utc": map[string]any{"type": "string"}, "weekday": map[string]any{"type": "integer", "minimum": 0, "maximum": 6}, "notifyWhen": map[string]any{"type": "string", "enum": []string{"always", "changed", "invalidated"}}}, "symbol", "thesis", "invalidation", "every", "at_utc"),
		Run:    once(a.trackThesis),
	}, {
		Name: "set_odds_alert",
		Description: "Show a prediction-market alert card the user sets with one tap: notify them when the Polymarket market " +
			"matched to one of their forecasts rises above or falls below a probability ('tell me if Polymarket goes above 25%'). " +
			"It sets nothing itself. Give the forecast's id (the chat's report, or from get_forecasts) and a level the market " +
			"has not reached yet. Notify only; the market is re-priced about once a minute.",
		Schema: schema(map[string]any{
			"forecast_id": map[string]any{"type": "string", "description": "The forecast id (UUID) whose matched Polymarket market to watch."},
			"op":          map[string]any{"type": "string", "enum": []string{"above", "below"}, "description": "Fire when the market's Yes price rises above or falls below the level."},
			"price_pct":   map[string]any{"type": "number", "minimum": 1, "maximum": 99, "description": "The level as a Yes probability in percent, e.g. 25 for 25%."},
			"note":        map[string]any{"type": "string", "maxLength": 120, "description": "Why this level, shown on the card and in the notification."},
		}, "forecast_id", "op", "price_pct"),
		Run: once(a.setOddsAlert),
	}, {
		Name: "set_drawdown_alert",
		Description: "Show a drawdown-alert card the user sets with one tap: when the price falls drawdown_pct below the " +
			"instrument's all-time high, notify them, or also sell sell_pct (1–100, default 100) of their paper long. The sale " +
			"never opens or adds to a short; with no long it only notifies. It sets nothing itself. The high is Binance daily " +
			"highs since listing for crypto, the listing's Yahoo lifetime high for USD-listed stocks and ETFs, a Yahoo " +
			"quote-feed listing's own lifetime high in its currency, else the perp's own history (its label says so); it ratchets up with new highs and fires once, checked every 15 s. Returns the " +
			"high, its source, date and coverage, the threshold and the current drawdown. A price already at or below the " +
			"threshold fires on the next check once the user sets it: say so.",
		Schema: schema(map[string]any{
			"symbol":       map[string]any{"type": "string", "enum": symbols(), "description": "Ticker from the universe, e.g. NVDA."},
			"drawdown_pct": map[string]any{"type": "number", "minimum": 0.01, "maximum": 99.99, "description": "Fire this many percent below the all-time high, e.g. 20."},
			"action":       map[string]any{"type": "string", "enum": []string{"notify", "sell"}, "description": "notify (default), or notify and sell part of the paper long."},
			"sell_pct":     map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Share of the current paper long to sell, in percent; sell only (default 100)."},
			"note":         map[string]any{"type": "string", "maxLength": 200, "description": "Why this level, shown on the card and in the notification."},
		}, "symbol", "drawdown_pct", "note"),
		Run: once(a.setDrawdownAlert),
	}}
}

// fieldNames maps an alerts.Validate message to the tool's parameter names.
var fieldNames = strings.NewReplacer("notional", "notional_usd", "stopLoss", "stop_loss", "takeProfit", "take_profit", "at must", "at_utc must",
	"drawdownPct", "drawdown_pct", "sellPct", "sell_pct")

// validated runs alerts.Validate for a tool: rule errors go back to the model as is.
func (a *agentRun) validated(ctx context.Context, in alerts.Input) (alerts.Input, float64, error) {
	v, mark, err := alerts.Validate(ctx, a.d.Market, in)
	return v, mark, ruleErr(err)
}

// ruleErr is an alerts rule error as the model reads it (the tool's parameter
// names); any other failure is toolErr's generic sentence.
func ruleErr(err error) error {
	var bad *app.BadRequest
	var invalid *paper.ValidationError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return errors.New(fieldNames.Replace(bad.Msg))
	case errors.As(err, &invalid):
		return errors.New(invalid.Msg)
	}
	return toolErr(err)
}

// setPriceAlert checks the alert as POST /api/alerts does, against the live
// price, and shows a card. It never stores anything: Set alert does.
func (a *agentRun) setPriceAlert(ctx context.Context, input json.RawMessage) (string, error) {
	var in alertInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	if _, err := lookup(in.Symbol); err != nil {
		return "", err
	}
	v, mark, err := a.validated(ctx, alerts.Input{Symbol: in.Symbol, Op: in.Op, Price: in.Price, Currency: in.Currency, Action: in.Action,
		Notional: in.Notional, StopLoss: in.StopLoss, TakeProfit: in.TakeProfit, Note: in.Note, Source: "agent"})
	if err != nil {
		return "", err
	}
	card := priceAlert{ProposalID: uuid.NewV4().String(), Symbol: v.Symbol, Trigger: "price", Op: &v.Op, Price: &v.Price, MarkPrice: &mark,
		DistancePct: new(round2((v.Price - mark) / mark * 100)), Action: v.Action, Notional: v.Notional, StopLoss: v.StopLoss,
		TakeProfit: v.TakeProfit, Note: v.Note}
	if v.Currency != "" {
		card.Currency = &v.Currency
	}
	a.card = &Card{Type: "price_alert", Data: card}
	a.summary = fmt.Sprintf("%s %s %s", v.Symbol, v.Op, alerts.InCurrency(v.Price, v.Currency))
	if v.Action != "notify" {
		a.summary = fmt.Sprintf("%s %s of %s if %s", strings.ToUpper(v.Action), usd(*v.Notional), v.Symbol, a.summary)
	}

	level, live := usd(v.Price), usd(mark)
	if v.Currency != "" {
		level, live = alerts.InCurrency(v.Price, v.Currency), alerts.InCurrency(mark, v.Currency)
	}
	out := fmt.Sprintf("Alert card shown. NOT set: nothing happens until the user taps Set alert (paper only). "+
		"The trigger %s is %+.2f%% from the live price %s.", level, *card.DistancePct, live)
	if v.StopLoss != nil {
		out += fmt.Sprintf(" At risk to the stop: %s (%.2f%% of the order).", usd(*v.Notional*math.Abs(*v.StopLoss/v.Price-1)),
			math.Abs(*v.StopLoss/v.Price-1)*100)
	}
	// Data, like propose_trade's: the prompt says what to write when the user has seen no analysis yet.
	return out + fmt.Sprintf(" analysisShown: %t.", a.wrote >= minAnalysis), nil
}

// setSchedule checks a schedule as POST /api/alerts does and shows a card. It
// never stores anything: Set automation does.
func (a *agentRun) setSchedule(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Prompt     string              `json:"prompt"`
		Thesis     *researchrun.Thesis `json:"thesis"`
		NotifyWhen string              `json:"notifyWhen"`
		Symbol     string              `json:"symbol"`
		Every      string              `json:"every"`
		At         string              `json:"at_utc"`
		Weekday    *int                `json:"weekday"`
		Action     string              `json:"action"`
		Notional   *float64            `json:"notional_usd"`
		Note       string              `json:"note"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	if in.Symbol != "" || in.Action != "research" {
		if _, err := lookup(in.Symbol); err != nil {
			return "", err
		}
	}
	v, _, err := a.validated(ctx, alerts.Input{Symbol: in.Symbol, Trigger: "schedule", Every: in.Every, At: in.At, Weekday: in.Weekday,
		Action: in.Action, Notional: in.Notional, Note: in.Note, Source: "agent", Prompt: in.Prompt, Thesis: in.Thesis, NotifyWhen: in.NotifyWhen})
	if err != nil {
		return "", err
	}
	var prompt, notify *string
	if v.Action == "research" {
		prompt, notify = &v.Prompt, &v.NotifyWhen
	}
	a.card = &Card{Type: "price_alert", Data: priceAlert{Prompt: prompt, Thesis: v.Thesis, NotifyWhen: notify, ProposalID: uuid.NewV4().String(), Symbol: v.Symbol, Trigger: "schedule",
		Every: &v.Every, At: &v.At, Weekday: v.Weekday, Action: v.Action, Notional: v.Notional, Note: v.Note}}
	when := alerts.ScheduleLabel(v.Every, v.At, v.Weekday)
	a.summary = fmt.Sprintf("Brief on %s %s", v.Symbol, when)
	if v.Action == "research" {
		a.summary = "AI research " + when + " · 2 credits/run"
	} else if v.Action != "notify" {
		a.summary = fmt.Sprintf("%s %s of %s %s", strings.ToUpper(v.Action), usd(*v.Notional), v.Symbol, when)
	}
	return toJSON(map[string]any{"status": "Card shown to the user. Nothing is set until they tap Set automation.",
		"nextRunAt": alerts.FirstRun(v).Format(time.RFC3339)})
}

func (a *agentRun) trackThesis(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Symbol       string `json:"symbol"`
		Thesis       string `json:"thesis"`
		Invalidation string `json:"invalidation"`
		Every        string `json:"every"`
		At           string `json:"at_utc"`
		Weekday      *int   `json:"weekday"`
		NotifyWhen   string `json:"notifyWhen"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	if _, err := lookup(in.Symbol); err != nil {
		return "", err
	}
	raw, err := json.Marshal(map[string]any{"symbol": in.Symbol, "every": in.Every, "at_utc": in.At, "weekday": in.Weekday, "action": "research", "prompt": "Review the investment thesis for " + in.Symbol + " against current evidence and its invalidation condition.", "thesis": researchrun.Thesis{Statement: in.Thesis, Invalidation: in.Invalidation}, "notifyWhen": in.NotifyWhen})
	if err != nil {
		return "", err
	}
	return a.setSchedule(ctx, raw)
}

// setOddsAlert checks an odds trigger as POST /api/alerts does, against the
// market's stored price (no Gamma call in chat), and shows a card. It never
// stores anything: Set alert does.
func (a *agentRun) setOddsAlert(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		ForecastID string  `json:"forecast_id"`
		Op         string  `json:"op"`
		PricePct   float64 `json:"price_pct"`
		Note       string  `json:"note"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	v, m, err := alerts.ValidateOdds(ctx, a.d, a.userID, alerts.Input{Trigger: "odds", ForecastID: in.ForecastID, Op: in.Op,
		Price: math.Round(in.PricePct*100) / 10000, Note: in.Note, Source: "agent"}, false)
	if err != nil {
		return "", ruleErr(err)
	}
	card := priceAlert{ProposalID: uuid.NewV4().String(), Trigger: "odds", ForecastID: &v.ForecastID,
		Market: &oddsMarket{Question: m.Question, URL: m.URL}, Op: &v.Op, Price: &v.Price, MarkPrice: &m.YesPrice, Action: "notify", Note: v.Note}
	if m.Outcome != "" {
		card.Market.Outcome = &m.Outcome
	}
	a.card = &Card{Type: "price_alert", Data: card}
	a.summary = fmt.Sprintf("Polymarket %s %s · now %s", v.Op, alerts.Pct(v.Price), alerts.Pct(m.YesPrice))
	return toJSON(map[string]any{"status": "Card shown to the user. Nothing is set until they tap Set alert.",
		"market": research.Untrusted(m.Question), "marketYesNow": m.YesPrice, "level": v.Price, "op": v.Op}) // Gamma's title is third-party text
}

// oddsAlertLabel is the set_odds_alert tool chip: "Drafting market alert: Polymarket above 25%".
func oddsAlertLabel(input json.RawMessage) string {
	var in struct {
		Op       string  `json:"op"`
		PricePct float64 `json:"price_pct"`
	}
	_ = json.Unmarshal(input, &in)
	return fmt.Sprintf("Drafting market alert: Polymarket %s %s", in.Op, alerts.Pct(in.PricePct/100))
}

// oddsBrief extends the forecast handoff brief: where cards render and, with
// a matched market, an odds alert at the desk's number as an ending action.
func (r report) oddsBrief() string {
	s := "\n- Cards appear below your answer: refer to them as below, never above."
	var m struct {
		YesPrice float64 `json:"yesPrice"`
		Outcome  string  `json:"outcome"`
	}
	if raw := r.market(); raw == nil || json.Unmarshal(raw, &m) != nil {
		return s
	}
	i := slices.IndexFunc(r.Result.Probabilities, func(p outcomeP) bool { return p.Outcome == cmp.Or(m.Outcome, "Yes") })
	if i < 0 || math.Abs(r.Result.Probabilities[i].P-m.YesPrice) < 0.01 {
		return s
	}
	desk, op := r.Result.Probabilities[i].P, "below"
	if desk > m.YesPrice {
		op = "above"
	}
	return s + fmt.Sprintf("\n- One of the ending actions you offer can be an odds alert at the desk's number: set_odds_alert with forecast_id %q, "+
		"op %q, price_pct %s (tell me when the market reaches the desk).", r.ID, op, strings.TrimSuffix(alerts.Pct(desk), "%"))
}

// setDrawdownAlert checks a drawdown trigger as POST /api/alerts does,
// resolving its all-time high and the current price, and shows a card. It
// never stores anything: Set alert does.
func (a *agentRun) setDrawdownAlert(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Symbol      string   `json:"symbol"`
		DrawdownPct float64  `json:"drawdown_pct"`
		Action      string   `json:"action"`
		SellPct     *float64 `json:"sell_pct"`
		Note        string   `json:"note"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	if _, err := lookup(in.Symbol); err != nil {
		return "", err
	}
	v, price, err := alerts.ValidateDrawdown(ctx, a.d, alerts.Input{Symbol: in.Symbol, Trigger: "drawdown", DrawdownPct: in.DrawdownPct,
		Action: in.Action, SellPct: in.SellPct, Note: in.Note, Source: "agent"})
	if err != nil {
		return "", ruleErr(err)
	}
	h := *v.ResolvedATH()
	ccy, level, from := h.Currency, alerts.Threshold(h.High, v.DrawdownPct), round2((price/h.High-1)*100)
	card := priceAlert{ProposalID: uuid.NewV4().String(), Symbol: v.Symbol, Trigger: "drawdown", MarkPrice: &price,
		DistancePct: new(round2((level - price) / price * 100)), Action: v.Action, Note: v.Note, DrawdownPct: &v.DrawdownPct,
		SellPct: v.SellPct, ATH: &h, Threshold: &level, FromATHPct: &from}
	a.card = &Card{Type: "price_alert", Data: card}
	ath := alerts.InCurrency(h.High, ccy)
	a.summary = fmt.Sprintf("%s %s below ATH %s", v.Symbol, alerts.PctLabel(v.DrawdownPct), ath)
	if v.Action == "sell" {
		a.summary = fmt.Sprintf("SELL %s of the %s long if %s", alerts.PctLabel(*v.SellPct), v.Symbol, a.summary)
	}

	out := fmt.Sprintf("Drawdown alert card shown. NOT set: nothing happens until the user taps Set alert (paper only). "+
		"All-time high %s on %s (%s). Threshold %s, %s below it. Now %s, %.2f%% from the high.", ath, h.At.Format(time.DateOnly),
		h.Label, alerts.InCurrency(level, ccy), alerts.PctLabel(v.DrawdownPct), alerts.InCurrency(price, ccy), from)
	if price <= level {
		out += " The price is already at or below the threshold, so it fires on the next check (within 15 s) once set: tell the user."
	}
	if h.Basis == "listing" {
		out += fmt.Sprintf(" Prices are the listing's own in %s, not the USD paper mark.", ccy)
	}
	return out + fmt.Sprintf(" analysisShown: %t.", a.wrote >= minAnalysis), nil
}

// drawdownAlertLabel is the set_drawdown_alert tool chip: "Drafting drawdown alert: NVDA 20% below its high".
func drawdownAlertLabel(input json.RawMessage) string {
	var in struct {
		Symbol      string  `json:"symbol"`
		DrawdownPct float64 `json:"drawdown_pct"`
	}
	_ = json.Unmarshal(input, &in)
	return fmt.Sprintf("Drafting drawdown alert: %s %s below its high", strings.ToUpper(in.Symbol), alerts.PctLabel(in.DrawdownPct))
}

// alertLabel is the set_price_alert tool chip: "Drafting alert: SOL below $175.00".
func alertLabel(input json.RawMessage) string {
	var in alertInput
	_ = json.Unmarshal(input, &in)
	level := usd(in.Price)
	if ccy := strings.ToUpper(strings.TrimSpace(in.Currency)); ccy != "" && ccy != "USD" {
		level = alerts.InCurrency(in.Price, ccy)
	}
	return fmt.Sprintf("Drafting alert: %s %s %s", strings.ToUpper(in.Symbol), in.Op, level)
}
