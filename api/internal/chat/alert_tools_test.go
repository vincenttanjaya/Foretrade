package chat

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// set_price_alert checks the alert against the live price (fake SOL $200),
// shows a price_alert card with a fresh proposalId and stores nothing.
func TestSetPriceAlert(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	a := &agentRun{d: e.Deps, userID: e.User}
	run := func(input string) (string, error) {
		a.summary, a.card = "", nil
		return a.setPriceAlert(ctx, json.RawMessage(input))
	}
	for input, want := range map[string]string{
		`{"symbol":"SOL","op":"below","price":205,"note":"x"}`:                                                   "SOL is already below $205.00 (now $200.00).",
		`{"symbol":"SOL","op":"above","price":199,"note":"x"}`:                                                   "SOL is already above $199.00 (now $200.00).",
		`{"symbol":"SOL","op":"below","price":190,"action":"buy","note":"x"}`:                                    "notional_usd must be at least $10",
		`{"symbol":"SOL","op":"below","price":190,"stop_loss":180,"note":"x"}`:                                   "A notify alert takes no notional_usd, stop_loss or take_profit.",
		`{"symbol":"SOL","op":"below","price":190,"action":"buy","notional_usd":500,"stop_loss":195,"note":"x"}`: "For a long, the stop must be below the price ($190.00).",
		`{"symbol":"SPY","op":"below","price":1,"note":"x"}`:                                                     "no live data",
	} {
		if _, err := run(input); err == nil || !strings.Contains(err.Error(), want) || a.card != nil {
			t.Errorf("%.60s: %v, want %q", input, err, want)
		}
	}

	// "Buy $500 of SOL if it drops 3% from here, stop 4% below that."
	out, err := run(`{"symbol":"sol","op":"below","price":194,"action":"buy","notional_usd":500,"stop_loss":186.24,"take_profit":215,"note":" Buy the dip into support. "}`)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := a.card.Data.(priceAlert)
	if _, err := uuid.Parse(c.ProposalID); err != nil || a.card.Type != "price_alert" || c.Symbol != "SOL" || c.Trigger != "price" || *c.Op != "below" ||
		*c.Price != 194 || *c.MarkPrice != 200 || *c.DistancePct != -3 || c.Action != "buy" || *c.Notional != 500 || *c.StopLoss != 186.24 ||
		*c.TakeProfit != 215 || c.Note != "Buy the dip into support." || c.Every != nil || c.At != nil || c.Weekday != nil {
		t.Fatalf("card %+v", c)
	}
	if !strings.Contains(out, "NOT set") || !strings.Contains(out, "-3.00% from the live price $200.00") ||
		!strings.Contains(out, "At risk to the stop: $20.00 (4.00% of the order)") || !strings.HasSuffix(out, " analysisShown: false.") {
		t.Fatalf("result %q", out)
	}
	if a.summary != "BUY $500.00 of SOL if SOL below $194.00" {
		t.Fatalf("summary %q", a.summary)
	}
	first := c.ProposalID
	if _, err := run(`{"symbol":"BTC","op":"above","price":105000,"note":"Breakout"}`); err != nil || a.card.Data.(priceAlert).ProposalID == first ||
		a.card.Data.(priceAlert).Notional != nil || a.summary != "BTC above $105,000" {
		t.Fatalf("notify card: %v %+v %q", err, a.card, a.summary)
	}
	var n int
	if e.DB.QueryRow(ctx, "select count(*) from alerts").Scan(&n); n != 0 {
		t.Fatalf("the tool stored %d alerts", n)
	}
	if got := label("set_price_alert", json.RawMessage(`{"symbol":"sol","op":"below","price":175}`)); got != "Drafting alert: SOL below $175.00" {
		t.Fatalf("label %q", got)
	}
}

// A foreign notification proposal carries its normalized native units through
// confirmation, and the model can also supply an explicit native threshold.
func TestSetPriceAlertNativeCurrency(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	e.Yahoo.Set(yahootest.Daily("D05.SI", "SGD", 36, 36))
	e.Yahoo.SetFX("SGD", 0.8)
	ctx := context.Background()
	a := &agentRun{d: e.Deps, userID: e.User}
	for _, input := range []string{
		`{"symbol":"DBS","op":"below","price":20,"note":"Support"}`,
		`{"symbol":"DBS","op":"below","price":25,"currency":"SGD","note":"Support"}`,
	} {
		out, err := a.setPriceAlert(ctx, json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(a.card.Data)
		var card map[string]any
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatal(err)
		}
		if card["currency"] != "SGD" || card["price"] != float64(25) || card["markPrice"] != float64(36) || card["distancePct"] != -30.56 {
			t.Fatalf("native proposal: %s", raw)
		}
		if a.summary != "DBS below SGD 25.00" || !strings.Contains(out, "trigger SGD 25.00") || !strings.Contains(out, "live price SGD 36.00") {
			t.Fatalf("native prose: %q / %q", a.summary, out)
		}
		var confirm alerts.Input
		if err := json.Unmarshal(raw, &confirm); err != nil {
			t.Fatal(err)
		}
		v, mark, err := alerts.Validate(ctx, e.Market, confirm)
		if err != nil || v.Currency != "SGD" || v.Price != 25 || mark != 36 {
			t.Fatalf("proposal confirmation converted twice: %+v / %v / %v", v, mark, err)
		}
	}
	if got := alertLabel(json.RawMessage(`{"symbol":"dbs","op":"below","price":25,"currency":"SGD"}`)); got != "Drafting alert: DBS below SGD 25.00" {
		t.Fatalf("native tool label: %s", got)
	}
}

// "Buy $500 of SOL if it drops 3%": the agent answers with a price_alert card,
// stored with the message; Set alert (POST /api/alerts) is the user's.
func TestAlertCardInChat(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{tool("a", "set_price_alert", `{"symbol":"SOL","op":"below","price":194,"action":"buy","notional_usd":500,"stop_loss":186,"note":"Dip into support."}`)}},
		llmtest.Reply{Text: "Set it on the card. **View:** neutral until $194."},
	)})
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Buy $500 of SOL if it drops 3%"}`).Body.String())
	cards := cardsOf(evs)
	if len(cards) != 1 || cards[0].Type != "price_alert" || cards[0].Data["op"] != "below" || cards[0].Data["notional"] != 500.0 || cards[0].Data["proposalId"] == "" {
		t.Fatalf("cards %+v", cards)
	}
	if c := find(evs, "tool_call"); len(c) != 1 || !strings.Contains(string(c[0].Data), `"label":"Drafting alert: SOL below $194.00"`) {
		t.Fatalf("tool call %+v", c)
	}
	if tools := e.Bedrock.Requests()[0].Tools; !slices.Equal(tools[len(tools)-5:], []string{"set_price_alert", "set_schedule", "track_thesis", "set_odds_alert", "set_drawdown_alert"}) {
		t.Fatalf("tools %v", tools)
	}
	var c struct{ ChatID string }
	json.Unmarshal(evs[0].Data, &c)
	if body := e.Do("GET", "/api/chats/"+c.ChatID, nil).Body.String(); !strings.Contains(body, `"type":"price_alert"`) {
		t.Fatalf("stored chat: %s", body)
	}
	var n int
	if e.DB.QueryRow(context.Background(), "select count(*) from alerts").Scan(&n); n != 0 {
		t.Fatal("the agent set the alert")
	}
}

// set_schedule checks the schedule as POST /api/alerts does (no market call),
// shows a price_alert card with trigger "schedule" and no price fields, and
// stores nothing.
func TestSetSchedule(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	a := &agentRun{d: e.Deps, userID: e.User}
	run := func(input string) (string, error) {
		a.summary, a.card = "", nil
		return a.setSchedule(ctx, json.RawMessage(input))
	}
	for input, want := range map[string]string{
		`{"symbol":"BTC","every":"month","at_utc":"08:00"}`:                                         "every must be day, weekdays or week.",
		`{"symbol":"BTC","every":"day","at_utc":"8am"}`:                                             "at_utc must be HH:MM (UTC).",
		`{"symbol":"BTC","every":"week","at_utc":"09:00"}`:                                          "weekday (0–6, 0 = Sunday) is required for a weekly schedule and only then.",
		`{"symbol":"BTC","every":"day","at_utc":"09:00","action":"buy"}`:                            "notional_usd must be at least $10",
		`{"symbol":"BTC","every":"day","at_utc":"09:00","notional_usd":100}`:                        "A notify alert takes no notional_usd, stop_loss or take_profit.",
		`{"symbol":"BTC","every":"day","at_utc":"09:00","note":"` + strings.Repeat("x", 201) + `"}`: "note must be at most 200 characters.",
		`{"symbol":"SPY","every":"day","at_utc":"09:00"}`:                                           "no live data",
	} {
		if _, err := run(input); err == nil || !strings.Contains(err.Error(), want) || a.card != nil {
			t.Errorf("%.60s: %v, want %q", input, err, want)
		}
	}

	// "Buy $100 of BTC every Monday at 9 UTC", with the ticker down: no market call.
	e.Binance.Down("", true)
	out, err := run(`{"symbol":"btc","every":"week","at_utc":"09:00","weekday":1,"action":"buy","notional_usd":100,"note":"DCA"}`)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := a.card.Data.(priceAlert)
	if _, err := uuid.Parse(c.ProposalID); err != nil || a.card.Type != "price_alert" || c.Trigger != "schedule" || c.Symbol != "BTC" ||
		*c.Every != "week" || *c.At != "09:00" || *c.Weekday != 1 || c.Action != "buy" || *c.Notional != 100 || c.Note != "DCA" ||
		c.Op != nil || c.Price != nil || c.MarkPrice != nil || c.DistancePct != nil || c.StopLoss != nil || c.TakeProfit != nil {
		t.Fatalf("card %+v", c)
	}
	var res struct{ Status, NextRunAt string }
	if json.Unmarshal([]byte(out), &res); res.Status != "Card shown to the user. Nothing is set until they tap Set automation." {
		t.Fatalf("result %s", out)
	}
	if next, err := time.Parse(time.RFC3339, res.NextRunAt); err != nil || next.UTC().Weekday() != time.Monday || next.UTC().Format("15:04") != "09:00" {
		t.Fatalf("nextRunAt %q", res.NextRunAt)
	}
	if a.summary != "BUY $100.00 of BTC Mondays 09:00 UTC" {
		t.Fatalf("summary %q", a.summary)
	}
	// The card's fields are what POST /api/alerts takes.
	if _, _, err := alerts.Validate(ctx, e.Market, alerts.Input{Symbol: c.Symbol, Trigger: c.Trigger, Every: *c.Every, At: *c.At, Weekday: c.Weekday,
		Action: c.Action, Notional: c.Notional, Source: "agent", ProposalID: &c.ProposalID}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(`{"symbol":"SOL","every":"day","at_utc":"08:00"}`); err != nil || a.card.Data.(priceAlert).Action != "notify" ||
		a.summary != "Brief on SOL daily 08:00 UTC" {
		t.Fatalf("brief card: %v %q", err, a.summary)
	}
	// Monday to Friday; a weekday is refused for it.
	if _, err := run(`{"symbol":"SOL","every":"weekdays","at_utc":"08:00","action":"buy","notional_usd":99.999}`); err != nil ||
		*a.card.Data.(priceAlert).Every != "weekdays" || *a.card.Data.(priceAlert).Notional != 100 || a.summary != "BUY $100.00 of SOL weekdays 08:00 UTC" {
		t.Fatalf("weekdays card: %v %q", err, a.summary)
	}
	if _, err := run(`{"symbol":"SOL","every":"weekdays","at_utc":"08:00","weekday":1}`); err == nil || !strings.Contains(err.Error(), "weekday (0–6") {
		t.Fatalf("weekdays with a weekday: %v", err)
	}
	var n int
	if e.DB.QueryRow(ctx, "select count(*) from alerts").Scan(&n); n != 0 {
		t.Fatalf("the tool stored %d alerts", n)
	}
	if got := label("set_schedule", json.RawMessage(`{"symbol":"btc","every":"week"}`)); got != "Drafting a schedule for BTC" {
		t.Fatalf("label %q", got)
	}
}

// set_odds_alert checks an odds trigger as POST /api/alerts does against the
// market's stored price (no Gamma call in chat), shows a price_alert card with
// trigger "odds", hands rule errors back as data and stores nothing; one alert
// card per message across the three alert tools.
func TestSetOddsAlert(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	e.PolymarketBase = "http://127.0.0.1:1" // a Gamma call would fail the crossed checks below
	market := `{"source":"Polymarket","question":"Will NVIDIA be the largest company on December 31?","url":"https://polymarket.com/event/largest-company",
		"yesPrice":0.231,"outcome":"Nvidia","asOf":"2026-10-01T00:00:00Z"}`
	var fc, bare string
	for _, row := range []struct {
		id     *string
		market string
	}{{&fc, market}, {&bare, "null"}} {
		if err := e.DB.QueryRow(ctx, `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result)
			values ($1, 'categorical', 'Largest company on 2026-12-31?', '["Nvidia","Apple"]', '2026-12-31', 'completed', $2::jsonb) returning id`,
			e.User, `{"probabilities":[{"outcome":"Nvidia","p":0.19},{"outcome":"Apple","p":0.81}],"market":`+row.market+`}`).Scan(row.id); err != nil {
			t.Fatal(err)
		}
	}
	a := &agentRun{d: e.Deps, userID: e.User}
	run := func(input string) (string, error) {
		a.summary, a.card = "", nil
		return a.setOddsAlert(ctx, json.RawMessage(input))
	}
	for input, want := range map[string]string{
		`{"forecast_id":"nope","op":"above","price_pct":30}`:                                                 "Unknown forecast.",
		`{"forecast_id":"` + bare + `","op":"above","price_pct":30}`:                                         "This forecast has no matched Polymarket market.",
		`{"forecast_id":"` + fc + `","op":"above","price_pct":0.5}`:                                          "Pick a level between 1% and 99%.",
		`{"forecast_id":"` + fc + `","op":"above","price_pct":20}`:                                           "The market is already at 23.1%. Pick a level above it.",
		`{"forecast_id":"` + fc + `","op":"below","price_pct":25}`:                                           "The market is already at 23.1%. Pick a level below it.",
		`{"forecast_id":"` + fc + `","op":"sideways","price_pct":30}`:                                        "op must be above or below.",
		`{"forecast_id":"` + fc + `","op":"above","price_pct":30,"note":"` + strings.Repeat("x", 201) + `"}`: "note must be at most 200 characters.",
	} {
		if _, err := run(input); err == nil || err.Error() != want || a.card != nil {
			t.Errorf("%.60s: %v, want %q", input, err, want)
		}
	}

	out, err := run(`{"forecast_id":"` + fc + `","op":"below","price_pct":19,"note":"Desk's number"}`)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := a.card.Data.(priceAlert)
	if _, err := uuid.Parse(c.ProposalID); err != nil || a.card.Type != "price_alert" || c.Trigger != "odds" || c.Symbol != "" || *c.ForecastID != fc ||
		c.Market.Question != "Will NVIDIA be the largest company on December 31?" || c.Market.URL != "https://polymarket.com/event/largest-company" ||
		*c.Market.Outcome != "Nvidia" || *c.Op != "below" || *c.Price != 0.19 || *c.MarkPrice != 0.231 || c.DistancePct != nil || c.Action != "notify" ||
		c.Note != "Desk's number" || c.Notional != nil || c.Every != nil {
		t.Fatalf("card %+v", c)
	}
	if b, _ := json.Marshal(a.card.Data); !strings.Contains(string(b), `"trigger":"odds","forecastId":"`+fc+`","market":{"question":`) {
		t.Fatalf("card JSON %s", b)
	}
	var res struct {
		Status       string
		MarketYesNow float64
		Level        float64
	}
	if json.Unmarshal([]byte(out), &res); res.Status != "Card shown to the user. Nothing is set until they tap Set alert." || res.MarketYesNow != 0.231 || res.Level != 0.19 ||
		!strings.Contains(out, `"market":"\u003cuntrusted_web_content\u003e`) {
		t.Fatalf("result %s", out)
	}
	if a.summary != "Polymarket below 19% · now 23.1%" {
		t.Fatalf("summary %q", a.summary)
	}
	// The card's fields are what POST /api/alerts takes (here against the stored price: Gamma is down).
	if _, _, err := alerts.ValidateOdds(ctx, e.Deps, e.User, alerts.Input{Trigger: c.Trigger, ForecastID: *c.ForecastID, Op: *c.Op, Price: *c.Price,
		Action: c.Action, Note: c.Note, Source: "agent", ProposalID: &c.ProposalID}, true); err != nil {
		t.Fatal(err)
	}
	if got := label("set_odds_alert", json.RawMessage(`{"forecast_id":"x","op":"above","price_pct":25.5}`)); got != "Drafting market alert: Polymarket above 25.5%" {
		t.Fatalf("label %q", got)
	}

	// One alert card per message: a refused card doesn't count, a shown one does, for every alert tool.
	tools := map[string]llm.Tool{}
	for _, tl := range (&agentRun{d: e.Deps, userID: e.User}).alertTools() {
		tools[tl.Name] = tl
	}
	if _, err := tools["set_odds_alert"].Run(ctx, json.RawMessage(`{"forecast_id":"`+fc+`","op":"above","price_pct":20}`)); err == nil {
		t.Fatal("a crossed level must be refused")
	}
	if _, err := tools["set_odds_alert"].Run(ctx, json.RawMessage(`{"forecast_id":"`+fc+`","op":"above","price_pct":30}`)); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"set_price_alert": `{"symbol":"SOL","op":"below","price":190,"note":"x"}`,
		"set_schedule":    `{"symbol":"BTC","every":"day","at_utc":"08:00"}`,
		"set_odds_alert":  `{"forecast_id":"` + fc + `","op":"above","price_pct":40}`,
	} {
		if _, err := tools[name].Run(ctx, json.RawMessage(input)); err == nil || err.Error() != "one alert card per message; the card is already shown" {
			t.Errorf("%s after a card: %v", name, err)
		}
	}
	var n int
	if e.DB.QueryRow(ctx, "select count(*) from alerts").Scan(&n); n != 0 {
		t.Fatalf("the tool stored %d alerts", n)
	}
}

// The handoff brief says cards render below the answer and, with a matched
// market, offers an odds alert at the desk's number on the right side.
func TestOddsBrief(t *testing.T) {
	r := report{ID: "f1", Shape: "binary"}
	json.Unmarshal([]byte(`{"probabilities":[{"outcome":"Yes","p":0.19},{"outcome":"No","p":0.81}],"market":{"yesPrice":0.231}}`), &r.Result)
	b := r.oddsBrief()
	for _, want := range []string{"Cards appear below your answer: refer to them as below, never above.",
		`set_odds_alert with forecast_id "f1", op "below", price_pct 19`} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q:\n%s", want, b)
		}
	}
	json.Unmarshal([]byte(`{"probabilities":[{"outcome":"Nvidia","p":0.815},{"outcome":"Apple","p":0.185}],"market":{"yesPrice":0.7,"outcome":"Nvidia"}}`), &r.Result)
	if b := r.oddsBrief(); !strings.Contains(b, `op "above", price_pct 81.5`) {
		t.Errorf("categorical: %s", b)
	}
	for _, m := range []string{`null`, `{"yesPrice":0.815,"outcome":"Nvidia"}`} { // none, or the market already at the desk
		r.Result.Market = json.RawMessage(m)
		if b := r.oddsBrief(); strings.Contains(b, "set_odds_alert") || !strings.Contains(b, "never above") {
			t.Errorf("%s: %s", m, b)
		}
	}
}
