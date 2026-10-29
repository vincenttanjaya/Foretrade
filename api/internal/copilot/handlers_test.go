package copilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

type planResponse struct {
	Plan      Plan
	Analysis  market.Analysis
	Outlook   market.PriceOutlook
	Headlines []research.NewsItem
	Credits   usage.Credits
}

func used(t *testing.T, e *apptest.Env) int {
	t.Helper()
	c, err := e.Usage.Get(context.Background(), e.User)
	if err != nil {
		t.Fatal(err)
	}
	return c.Used
}

func TestPlanEndpoint(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Text: `{"direction":"long","entry":200,"stopLoss":190,"takeProfit":230,"confidence":64,"horizon":"1-3 days",
			"rationale":["RSI(14) 58: momentum positive"],"invalidation":"A close below 190.","catalysts":["ETF filing"," ","Network upgrade"]}`},
		llmtest.Reply{Text: `{"direction":"long","entry":4000,"stopLoss":3900,"takeProfit":4300,"confidence":50,"catalysts":["Made up"]}`},
		llmtest.Reply{Text: `Here you go: {"direction":"long","confidence":70}`},
	)})
	ctx := context.Background()
	if _, err := e.DB.Exec(ctx, `insert into news_items (url, title, source, published_at, symbols) values
		('https://n.example/1', 'Solana ETF filed', 'Desk', now() - interval '1 hour', '{SOL}'),
		('https://n.example/2', 'Solana upgrade ships', 'Wire', now() - interval '2 hours', '{SOL}'),
		('https://n.example/3', 'SOL volume jumps', 'Desk', now() - interval '3 hours', '{SOL}'),
		('https://n.example/4', 'Solana fees fall', 'Desk', now() - interval '4 hours', '{SOL}'),
		('https://n.example/old', 'Old SOL story', 'Desk', now() - interval '3 days', '{SOL}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `insert into market_briefs (brief, created_at) values
		('{"sentiment":[{"symbol":"ETH","score":0.4,"label":"bullish","reason":"stale"}]}', now() - interval '3 days'),
		('{"sentiment":[{"symbol":"SOL","score":0.42,"label":"bullish","reason":"ETF demand"}]}', now())`); err != nil {
		t.Fatal(err)
	}
	researchtest.AcceptBriefs(t, e.DB)

	if w := e.DoAs(nil, "POST", "/api/copilot/plan", `{"symbol":"SOL","style":"day"}`); w.Code != 401 {
		t.Fatalf("no session: %d", w.Code)
	}
	for _, body := range []string{`{"symbol":`, `{"symbol":"PEPE","style":"day"}`, `{"symbol":"SOL","style":"hodl"}`,
		`{"symbol":"SOL","style":"day","bias":"up"}`} {
		if w := e.Do("POST", "/api/copilot/plan", body); w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}

	w := e.Do("POST", "/api/copilot/plan", `{"symbol":"sol","style":"day"}`)
	got := apptest.JSON[planResponse](t, w)
	p := got.Plan
	if w.Code != 200 || p.Symbol != "SOL" || p.Style != "day" || p.Direction != "long" || p.Entry != 200 || p.StopLoss != 190 ||
		p.TakeProfit != 230 || p.RiskReward != 3 || p.Confidence != 64 || p.Horizon != "1-3 days" || p.SuggestedNotional != 5000 {
		t.Fatalf("plan: %d %s", w.Code, w.Body)
	}
	if strings.Join(p.Catalysts, "|") != "ETF filing|Network upgrade" || len(got.Headlines) != 3 || got.Headlines[0].Title != "Solana ETF filed" ||
		got.Analysis.Interval != "1h" || got.Outlook.HorizonDays != 3 || got.Credits.Used != 1 || p.GeneratedAt.IsZero() ||
		p.Interval != "1h" || p.HigherTrend == nil || p.HigherTrend.Interval != "1d" || p.HigherTrend.Trend == "" {
		t.Fatalf("plan extras: %s", w.Body)
	}
	req := e.Bedrock.Requests()[0]
	for _, want := range []string{"Plan a day trade on SOL (Solana). Chart interval 1h", "Solana fees fall", "<untrusted_web_content>",
		"News-wire sentiment for SOL: bullish (+0.42): ETF demand", `"interval":"1h"`, "Higher timeframe (1d): "} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, req.Prompt)
		}
	}
	if req.Effort != "low" || !strings.HasPrefix(req.System, "You are the Copilot") || strings.Contains(req.Prompt, "Old SOL story") {
		t.Fatalf("request: effort %q", req.Effort)
	}

	// No fresh news: the 3-day-old brief with ETH sentiment becomes the latest and is too old to use,
	// so catalysts are dropped whatever the model says.
	if _, err := e.DB.Exec(ctx, "delete from market_briefs where created_at > now() - interval '1 day'"); err != nil {
		t.Fatal(err)
	}
	w = e.Do("POST", "/api/copilot/plan", `{"symbol":"ETH","style":"swing","bias":"short"}`)
	got = apptest.JSON[planResponse](t, w)
	if w.Code != 200 || got.Plan.Direction != "short" || len(got.Plan.Catalysts) != 0 || !strings.Contains(w.Body.String(), `"headlines":[]`) ||
		!(got.Plan.StopLoss > got.Plan.Entry && got.Plan.Entry > got.Plan.TakeProfit) || got.Analysis.Interval != "4h" {
		t.Fatalf("forced short without news: %d %s", w.Code, w.Body)
	}
	if p := e.Bedrock.Requests()[1].Prompt; !strings.Contains(p, "No stored news for ETH in the last 48 h") || !strings.Contains(p, `direction must be "short"`) ||
		strings.Contains(p, "News-wire sentiment") {
		t.Fatalf("no-news prompt:\n%s", p)
	}

	// The model's levels are missing: ATR-based levels around the live price.
	w = e.Do("POST", "/api/copilot/plan", `{"symbol":"BTC","style":"scalp","bias":"auto"}`)
	got = apptest.JSON[planResponse](t, w)
	if w.Code != 200 || got.Plan.Entry != 100_000 || !(got.Plan.StopLoss < 100_000 && got.Plan.TakeProfit > 100_000) || got.Analysis.Interval != "15m" {
		t.Fatalf("fallback levels: %d %s", w.Code, w.Body)
	}
	if used(t, e) != 3 {
		t.Fatalf("three plans cost 3 credits, used %d", used(t, e))
	}
}

func TestPlanFailures(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Credits: 1, Reply: llmtest.Queue(t, llmtest.Reply{Status: 400})})
	// Bedrock fails: 502 and the credit comes back.
	if w := e.Do("POST", "/api/copilot/plan", `{"symbol":"BTC","style":"day"}`); w.Code != 502 || apptest.ErrorCode(t, w) != "upstream" || used(t, e) != 0 {
		t.Fatalf("bedrock down: %d %s, used %d", w.Code, w.Body, used(t, e))
	}
	if _, err := e.Usage.Spend(context.Background(), e.User, 1); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("POST", "/api/copilot/plan", `{"symbol":"BTC","style":"day"}`); w.Code != 429 || apptest.ErrorCode(t, w) != "ai_quota" {
		t.Fatalf("no credits left: %d %s", w.Code, w.Body)
	}

	// Market data down: 502 before anything is spent.
	down := apptest.New(t, Register, apptest.Options{})
	down.Binance.Down("", true)
	if w := down.Do("POST", "/api/copilot/plan", `{"symbol":"BTC","style":"swing"}`); w.Code != 502 || used(t, down) != 0 {
		t.Fatalf("binance down: %d %s, used %d", w.Code, w.Body, used(t, down))
	}
}

// deskRow is a forecast row for the desk tests: completed, unresolved, resolving in a week, owned by the test user.
type deskRow struct {
	user, question, rule, status, outcome, ago string
	p                                          float64
	days                                       int
}

func addForecast(t *testing.T, e *apptest.Env, f deskRow) string {
	t.Helper()
	if f.user == "" {
		f.user = e.User
	}
	if f.status == "" {
		f.status = "completed"
	}
	if f.days == 0 {
		f.days = 7
	}
	var id string
	err := e.DB.QueryRow(context.Background(), `
		insert into forecasts (user_id, shape, question, outcomes, resolution_criteria, resolution_date, status, result, price_rule, outcome, created_at)
		values ($1, 'binary', $2, '["Yes","No"]', 'Per Binance.', (now() at time zone 'utc')::date + $3::int, $4,
			jsonb_build_object('probabilities', jsonb_build_array(jsonb_build_object('outcome', 'Yes', 'p', $5::float8),
				jsonb_build_object('outcome', 'No', 'p', 1 - $5::float8))),
			nullif($6, '')::jsonb, nullif($7, ''), now() - coalesce(nullif($8, ''), '0 seconds')::interval)
		returning id`, f.user, f.question, f.days, f.status, f.p, f.rule, f.outcome, f.ago).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func getDesk(t *testing.T, e *apptest.Env, symbol string) *DeskRead {
	t.Helper()
	w := e.Do("GET", "/api/copilot/desk?symbol="+symbol, nil)
	if w.Code != 200 {
		t.Fatalf("desk %s: %d %s", symbol, w.Code, w.Body)
	}
	return apptest.JSON[struct{ Desk *DeskRead }](t, w).Desk
}

func TestDeskEndpoint(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	if w := e.DoAs(nil, "GET", "/api/copilot/desk?symbol=SOL", nil); w.Code != 401 {
		t.Fatalf("no session: %d", w.Code)
	}
	for _, q := range []string{"", "?symbol=PEPE"} {
		if w := e.Do("GET", "/api/copilot/desk"+q, nil); w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
			t.Errorf("%q: %d %s", q, w.Code, w.Body)
		}
	}
	if w := e.Do("GET", "/api/copilot/desk?symbol=SOL", nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"desk":null}` {
		t.Fatalf("nothing on the desk: %d %s", w.Code, w.Body)
	}

	const solRule = `{"symbol":"SOL","op":"above","threshold":210,"mode":"on_date"}`
	other := storetest.User(t, e.DB)
	for _, f := range []deskRow{
		{user: other, question: "Will SOL close above $150?", rule: solRule, p: 0.9},       // another user's
		{question: "Will SOL close above $180?", rule: solRule, p: 0.9, outcome: "Yes"},    // resolved
		{question: "Will SOL close above $190?", rule: solRule, p: 0.9, days: -1},          // expired
		{question: "Will SOL close above $195?", rule: solRule, p: 0.9, status: "running"}, // not finished
		{question: "Will Solar stocks rally?", p: 0.9},                                     // not a word match
	} {
		addForecast(t, e, f)
	}
	if d := getDesk(t, e, "SOL"); d != nil {
		t.Fatalf("excluded rows matched: %+v", d)
	}

	// A price rule on the symbol beats a newer name match; a 28% Yes on "above" opposes a long.
	ruled := addForecast(t, e, deskRow{question: "Will SOL close above $210 on Oct 10?", rule: solRule, p: 0.28, ago: "1 hour"})
	addForecast(t, e, deskRow{question: "Will Solana flip BNB by market cap?", p: 0.6})
	d := getDesk(t, e, "sol")
	if d == nil || d.ForecastID != ruled || d.Outcome != "Yes" || d.P != 0.28 || d.Lean != "No 72%" || d.PriceRule == nil ||
		d.PriceRule.Threshold != 210 || d.Stance == nil || *d.Stance != "opposes" || d.ResolutionDate != time.Now().UTC().AddDate(0, 0, 7).Format(time.DateOnly) {
		t.Fatalf("price-rule match: %+v", d)
	}

	// Name matches only: the newest that names the asset as a word wins, with no rule and no stance.
	ethereum := addForecast(t, e, deskRow{question: "Will Ethereum ETFs see inflows this week?", p: 0.64, ago: "2 hours"})
	addForecast(t, e, deskRow{question: "Will Ethereal Labs list?", p: 0.5})
	if d := getDesk(t, e, "ETH"); d == nil || d.ForecastID != ethereum || d.Lean != "Yes 64%" || d.PriceRule != nil || d.Stance != nil {
		t.Fatalf("name match: %+v", d)
	}
}

func TestStance(t *testing.T) {
	above := &priceRule{Symbol: "SOL", Op: "above", Threshold: 210}
	below := &priceRule{Symbol: "SOL", Op: "below", Threshold: 150}
	for _, c := range []struct {
		rule      *priceRule
		symbol    string
		p         float64
		direction string
		want      string // "" = nil
	}{
		{above, "SOL", 0.64, "long", "supports"},
		{above, "SOL", 0.64, "short", "opposes"},
		{above, "SOL", 0.28, "short", "supports"},
		{above, "SOL", 0.51, "long", "split"},
		{above, "SOL", 0.47, "long", "opposes"},
		{above, "SOL", 0.53, "long", "supports"},
		{below, "SOL", 0.70, "long", "opposes"},
		{below, "SOL", 0.70, "short", "supports"},
		{above, "BTC", 0.64, "long", ""},
		{nil, "SOL", 0.64, "long", ""},
		{above, "SOL", 0.64, "neutral", ""},
	} {
		got := stance(c.rule, c.symbol, c.p, c.direction)
		if (got == nil) != (c.want == "") || (got != nil && *got != c.want) {
			t.Errorf("%+v %s %.2f %s: %v, want %q", c.rule, c.symbol, c.p, c.direction, got, c.want)
		}
	}
}

func TestPlanReadsTheDesk(t *testing.T) {
	long := `{"direction":"long","entry":200,"stopLoss":190,"takeProfit":230,"confidence":45,"rationale":["The desk leans No above $210."]}`
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Text: long},
		llmtest.Reply{Text: `{"direction":"short","entry":200,"stopLoss":210,"takeProfit":180,"confidence":60}`},
		llmtest.Reply{Text: `{"direction":"neutral","entry":200,"stopLoss":190,"takeProfit":230,"confidence":30}`},
		llmtest.Reply{Text: long},
	)})
	id := addForecast(t, e, deskRow{question: "Will SOL close above $210 on Oct 10?", rule: `{"symbol":"SOL","op":"above","threshold":210,"mode":"on_date"}`, p: 0.28})
	for i, c := range []struct{ bias, want string }{{"long", "opposes"}, {"short", "supports"}, {"auto", ""}} {
		w := e.Do("POST", "/api/copilot/plan", `{"symbol":"SOL","style":"day","bias":"`+c.bias+`"}`)
		d := apptest.JSON[planResponse](t, w).Plan.Desk
		if w.Code != 200 || d == nil || d.ForecastID != id || d.Lean != "No 72%" || (d.Stance == nil) != (c.want == "") || (d.Stance != nil && *d.Stance != c.want) {
			t.Fatalf("%s plan: %d %s", c.bias, w.Code, w.Body)
		}
		want := "Forecast desk (the user's own AI forecasting panel; may be stale): 'Will SOL close above $210 on Oct 10?' → No 72%, resolves " +
			time.Now().UTC().AddDate(0, 0, 7).Format(time.DateOnly) + ". If your target sits beyond the threshold the desk leans against"
		if p := e.Bedrock.Requests()[i].Prompt; !strings.Contains(p, want) {
			t.Fatalf("prompt lacks the desk line:\n%s", p)
		}
	}
	// Another asset has no desk read: null on the plan, no line in the prompt.
	w := e.Do("POST", "/api/copilot/plan", `{"symbol":"BTC","style":"day","bias":"long"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"desk":null`) || strings.Contains(e.Bedrock.Requests()[3].Prompt, "Forecast desk") {
		t.Fatalf("no desk: %d %s", w.Code, w.Body)
	}
}

func TestPlanSuppressesLegacyBriefProse(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"direction":"long","confidence":50,"rationale":["Momentum"],"catalysts":["GAMBLING_DERIVED"]}`})})
	if _, err := e.DB.Exec(context.Background(), `insert into market_briefs (brief) values ('{"headline":"GAMBLING_DERIVED","summary":"GAMBLING_DERIVED","sentiment":[{"symbol":"BTC","score":1,"reason":"GAMBLING_DERIVED"}]}')`); err != nil {
		t.Fatal(err)
	}
	w := e.Do("POST", "/api/copilot/plan", `{"symbol":"BTC","style":"day"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "GAMBLING_DERIVED") {
		t.Fatalf("plan: %d %s", w.Code, w.Body)
	}
	reqs := e.Bedrock.Requests()
	if len(reqs) != 1 || strings.Contains(reqs[0].Prompt, "GAMBLING_DERIVED") || strings.Contains(reqs[0].Prompt, "News sentiment") {
		t.Fatalf("model input: %+v", reqs)
	}
}
