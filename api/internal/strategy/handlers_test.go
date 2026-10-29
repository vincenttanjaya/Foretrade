package strategy

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func creditsUsed(t *testing.T, e *apptest.Env) int {
	t.Helper()
	c, err := e.Usage.Get(context.Background(), e.User)
	if err != nil {
		t.Fatal(err)
	}
	return c.Used
}

func TestBacktestEndpoint(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	for _, r := range [][2]string{{"POST", "/api/strategies/generate"}, {"POST", "/api/strategies/backtest"}, {"GET", "/api/strategies"},
		{"GET", "/api/strategies/x"}, {"DELETE", "/api/strategies/x"}} {
		if w := e.DoAs(nil, r[0], r[1], "{}"); w.Code != 401 {
			t.Errorf("%s %s without a session: %d", r[0], r[1], w.Code)
		}
	}
	tooLong := `{"spec":{"symbol":"BTC","interval":"1d","bars":200,"positionPct":100,
		"longEntry":{"all":[{"left":{"kind":"price"},"op":">","right":{"kind":"sma","period":400}}]}}}`
	for body, msg := range map[string]string{
		`{"spec":`: "not valid JSON",
		`{}`:       "spec is required.",
		`{"spec":{"symbol":"SHIB","interval":"4h","positionPct":100}}`: `is not in the universe`,
		tooLong: "not enough history",
	} {
		if w := e.Do("POST", "/api/strategies/backtest", body); w.Code != 400 || !strings.Contains(w.Body.String(), msg) {
			t.Errorf("%.40s: %d %s", body, w.Code, w.Body)
		}
	}

	var g generated
	json.Unmarshal([]byte(designExamples[0].reply), &g)
	w := e.Do("POST", "/api/strategies/backtest", map[string]any{"spec": g.Spec})
	res := apptest.JSON[Result](t, w)
	if w.Code != 200 || res.Metrics.Bars == 0 || len(res.Equity) == 0 || len(res.Equity) > maxPoints || res.Equity[0].V != 100 ||
		res.Metrics.Trades == 0 || len(res.Benchmark) != len(res.Equity) {
		t.Fatalf("backtest: %d %+v", w.Code, res.Metrics)
	}
	if creditsUsed(t, e) != 0 {
		t.Fatal("a backtest is free")
	}

	e.Binance.Down("", true)
	g.Spec.Symbol = "ETH" // not cached yet
	if w := e.Do("POST", "/api/strategies/backtest", map[string]any{"spec": g.Spec}); w.Code != 502 || apptest.ErrorCode(t, w) != "upstream" {
		t.Fatalf("binance down: %d %s", w.Code, w.Body)
	}
}

func TestGenerate(t *testing.T) {
	valid := designExamples[0].reply
	var bad generated
	json.Unmarshal([]byte(valid), &bad)
	bad.Spec.Symbol = "SHIB"
	invalid, _ := json.Marshal(bad)
	noMarket := `{"name":"","description":" Buys strength. ","spec":{"name":"RSI Pop","longEntry":{"all":[{"left":{"kind":"rsi","period":14},"op":">","right":{"kind":"const","value":60}}]},"positionPct":50}}`
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Text: valid}, llmtest.Reply{Text: "  It rides 4h trends.  "},
		llmtest.Reply{Text: string(invalid)}, llmtest.Reply{Text: valid}, llmtest.Reply{Status: 400},
		llmtest.Reply{Text: string(invalid)}, llmtest.Reply{Text: string(invalid)},
		llmtest.Reply{Text: noMarket}, llmtest.Reply{Text: "Buys RSI pops."},
		llmtest.Reply{Status: 400},
	)})

	for _, body := range []string{`{"prompt":`, `{"prompt":" hey "}`, `{"prompt":"` + strings.Repeat("x", 1001) + `"}`,
		`{"prompt":"buy the dip","symbol":"SHIB"}`, `{"prompt":"buy the dip","interval":"5m"}`} {
		if w := e.Do("POST", "/api/strategies/generate", body); w.Code != 400 {
			t.Errorf("%.40s: %d", body, w.Code)
		}
	}

	w := e.Do("POST", "/api/strategies/generate", `{"prompt":"EMA cross on BTC"}`)
	got := apptest.JSON[struct {
		Strategy Strategy
		Credits  struct{ Used int }
	}](t, w)
	s := got.Strategy
	if w.Code != 201 || s.Name != "BTC EMA Cross" || s.Author != "Test" || s.Explanation != "It rides 4h trends." || s.Prompt != "EMA cross on BTC" ||
		s.Spec.Symbol != "BTC" || !isUUID(s.ID) || got.Credits.Used != 2 || len(s.Equity) == 0 {
		t.Fatalf("generate: %d %s", w.Code, w.Body)
	}
	reqs := e.Bedrock.Requests()
	if reqs[0].Effort != "medium" || !strings.HasPrefix(reqs[0].Prompt, "Selected market: BTC 4h") || reqs[1].Effort != "low" ||
		!strings.Contains(reqs[1].Prompt, `"totalReturnPct"`) || !strings.HasPrefix(reqs[1].System, "You explain a strategy backtest") {
		t.Fatalf("requests: %q / %q", reqs[0].Prompt, reqs[1].Prompt)
	}

	// An invalid spec gets one repair round; the explanation falls back to Go's summary.
	w = e.Do("POST", "/api/strategies/generate", `{"prompt":"EMA cross","symbol":"eth","interval":"1h"}`)
	s = apptest.JSON[struct{ Strategy Strategy }](t, w).Strategy
	if w.Code != 201 || !strings.HasPrefix(s.Explanation, "Over the last ") || !strings.Contains(s.Explanation, "BTC 4h bars") {
		t.Fatalf("repaired: %d %s", w.Code, w.Body)
	}
	if p := e.Bedrock.Requests()[3].Prompt; !strings.HasPrefix(p, "Selected market: ETH 1h") || !strings.Contains(p, `It was rejected: symbol "SHIB" is not in the universe`) {
		t.Fatalf("repair prompt: %q", p)
	}

	// Still invalid after the repair round: 502, credits kept (the model was called).
	if w := e.Do("POST", "/api/strategies/generate", `{"prompt":"EMA cross"}`); w.Code != 502 || !strings.Contains(w.Body.String(), "upstream") {
		t.Fatalf("invalid twice: %d %s", w.Code, w.Body)
	}

	// The model left out symbol and interval: the selected market fills them.
	w = e.Do("POST", "/api/strategies/generate", `{"prompt":"RSI pops","symbol":"SOL","interval":"1d"}`)
	s = apptest.JSON[struct{ Strategy Strategy }](t, w).Strategy
	if w.Code != 201 || s.Spec.Symbol != "SOL" || s.Spec.Interval != "1d" || s.Name != "RSI Pop" || s.Description != "Buys strength." {
		t.Fatalf("filled market: %d %s", w.Code, w.Body)
	}
	before := creditsUsed(t, e)
	if w := e.Do("POST", "/api/strategies/generate", `{"prompt":"anything at all"}`); w.Code != 502 || creditsUsed(t, e) != before {
		t.Fatalf("bedrock down: %d, credits %d → %d (must be refunded)", w.Code, before, creditsUsed(t, e))
	}

	poor := apptest.New(t, Register, apptest.Options{Credits: 1})
	if w := poor.Do("POST", "/api/strategies/generate", `{"prompt":"EMA cross on BTC"}`); w.Code != 429 {
		t.Fatalf("no credits: %d", w.Code)
	}
}

func TestListGetDelete(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: designExamples[1].reply}, llmtest.Reply{Text: "x"})})
	w := e.Do("POST", "/api/strategies/generate", `{"prompt":"SOL overbought fade"}`)
	mine := apptest.JSON[struct{ Strategy Strategy }](t, w).Strategy
	if w.Code != 201 {
		t.Fatalf("generate: %d %s", w.Code, w.Body)
	}

	type list struct{ Strategies []Summary }
	if got := apptest.JSON[list](t, e.Do("GET", "/api/strategies?scope=mine", nil)); len(got.Strategies) != 1 || got.Strategies[0].ID != mine.ID ||
		got.Strategies[0].Symbol != "SOL" || len(got.Strategies[0].Sparkline) == 0 || len(got.Strategies[0].Sparkline) > sparkPoints {
		t.Fatalf("mine: %+v", got)
	}
	if w := e.Do("GET", "/api/strategies?scope=all", nil); w.Code != 400 {
		t.Fatalf("bad scope: %d", w.Code)
	}
	w = e.Do("GET", "/api/strategies?scope=top", nil)
	top := apptest.JSON[list](t, w).Strategies
	if w.Code != 200 || len(top) != len(labs) || top[0].Author != labsAuthor ||
		!slices.IsSortedFunc(top, func(a, b Summary) int {
			return cmp.Or(boolCmp(a.ShortSample, b.ShortSample), cmp.Compare(b.Metrics.Sharpe, a.Metrics.Sharpe))
		}) {
		t.Fatalf("top: %d %s", w.Code, w.Body)
	}
	// Full samples (180+ days and 10+ trades) first; the 83-day hourly ETH RSI Dip Buyer is a short sample.
	for _, s := range top {
		if s.ShortSample != shortSample(s.Metrics) || (s.Name == "ETH RSI Dip Buyer" && !s.ShortSample) {
			t.Errorf("%s: shortSample %v, %d trades over %d days", s.Name, s.ShortSample, s.Metrics.Trades, (s.Metrics.EndT-s.Metrics.StartT)/86400)
		}
	}
	if top[0].ShortSample || !top[len(top)-1].ShortSample {
		t.Errorf("top: %v first, %v last", top[0].ShortSample, top[len(top)-1].ShortSample)
	}

	labID := nameID("BTC EMA Trend Rider")
	if got := apptest.JSON[struct{ Strategy Strategy }](t, e.Do("GET", "/api/strategies/"+labID, nil)); got.Strategy.Name != "BTC EMA Trend Rider" ||
		got.Strategy.Author != labsAuthor || got.Strategy.Explanation == "" {
		t.Fatalf("lab strategy: %+v", got.Strategy)
	}
	if got := apptest.JSON[struct{ Strategy Strategy }](t, e.Do("GET", "/api/strategies/"+mine.ID, nil)); got.Strategy.ID != mine.ID ||
		got.Strategy.Spec.ShortEntry == nil || got.Strategy.Explanation != "x" {
		t.Fatalf("own strategy: %+v", got.Strategy)
	}
	other := e.Session(storetest.User(t, e.DB))
	for _, r := range []struct {
		c            bool // as the other user
		method, path string
	}{{false, "GET", "/api/strategies/nope"}, {false, "DELETE", "/api/strategies/nope"}, {true, "GET", "/api/strategies/" + mine.ID},
		{true, "DELETE", "/api/strategies/" + mine.ID}, {false, "GET", "/api/strategies/" + nameID("unknown")}} {
		c := e.Cookie
		if r.c {
			c = other
		}
		if w := e.DoAs(c, r.method, r.path, nil); w.Code != 404 {
			t.Errorf("%s %s (other=%v): %d", r.method, r.path, r.c, w.Code)
		}
	}
	if w := e.Do("DELETE", "/api/strategies/"+mine.ID, nil); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := e.Do("DELETE", "/api/strategies/"+mine.ID, nil); w.Code != 404 {
		t.Fatalf("delete twice: %d", w.Code)
	}

	down := apptest.New(t, Register, apptest.Options{})
	down.Binance.Down("", true)
	for _, path := range []string{"/api/strategies?scope=top", "/api/strategies/" + labID} {
		if w := down.Do("GET", path, nil); w.Code != 502 {
			t.Errorf("%s with Binance down: %d %s", path, w.Code, w.Body)
		}
	}
}

// Rows saved before sortino, volatilityAnnPct, calmar and feesPct existed
// still read: the new fields come back 0 / null, a stale annualized return
// under 90 days is nulled, and a win rate stored as 0 without trades reads null.
func TestLegacyMetricsRead(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	var spec Spec
	json.Unmarshal([]byte(designExamples[0].reply), &struct{ Spec *Spec }{&spec})
	// 1000 bars: about 10 days of 15m bars, 42 of 1h ones, about 3 years of daily ones.
	for iv, wantAnn := range map[string]string{"15m": `"annualizedReturnPct":null`, "1h": `"annualizedReturnPct":null`,
		"1d": `"annualizedReturnPct":182.4`} {
		spec.Interval = iv
		var id string
		err := e.DB.QueryRow(context.Background(), `
			insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades)
			values ($1, 'old', '', 'p', $2, '{"totalReturnPct":3,"annualizedReturnPct":182.4,"sharpe":1.2,"maxDrawdownPct":4,"winRatePct":0,"trades":0,"bars":1000}',
			        '[{"t":1,"v":100}]', '[]', '[]') returning id`, e.User, spec).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		w := e.Do("GET", "/api/strategies/"+id, nil)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, wantAnn) || !strings.Contains(body, `"winRatePct":null`) ||
			!strings.Contains(body, `"shortSample":true`) { // no trades: a short sample, computed on read
			t.Errorf("%s: %d %s", iv, w.Code, body)
		}
		// Metrics they never stored stay absent, not a made-up 0.
		if !strings.Contains(body, `"calmar":null`) || strings.Contains(body, `"sortino"`) || strings.Contains(body, `"volatilityAnnPct"`) ||
			strings.Contains(body, `"feesPct"`) {
			t.Errorf("%s: %s", iv, body)
		}
	}
	if w := e.Do("GET", "/api/strategies?scope=mine", nil); w.Code != 200 || strings.Count(w.Body.String(), `"calmar":null`) != 3 ||
		strings.Count(w.Body.String(), `"shortSample":true`) != 3 ||
		strings.Contains(w.Body.String(), `"sortino"`) {
		t.Fatalf("mine: %d %s", w.Code, w.Body)
	}
}

// A generated spec gets at least 90 days of history (2500 bars on 1h), so its
// annualized return and Calmar are numbers.
func TestGenerateRaisesBarsToTheFloor(t *testing.T) {
	var g generated
	json.Unmarshal([]byte(designExamples[0].reply), &g)
	g.Spec.Interval, g.Spec.Bars = "1h", 1000
	reply, _ := json.Marshal(g)
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: string(reply)}, llmtest.Reply{Text: "x"})})
	w := e.Do("POST", "/api/strategies/generate", `{"prompt":"EMA cross on BTC hourly"}`)
	s := apptest.JSON[struct{ Strategy Strategy }](t, w).Strategy
	if w.Code != 201 || s.Spec.Bars != 2500 || s.Metrics.Bars < 2400 || s.Metrics.AnnualizedReturnPct == nil || s.Metrics.Calmar == nil {
		t.Fatalf("1h strategy: %d spec bars %d, metrics %+v", w.Code, s.Spec.Bars, s.Metrics)
	}
	if !strings.Contains(e.Bedrock.Requests()[0].System, "2500 on 1h") {
		t.Fatal("the design prompt does not ask for the floor")
	}
}

func TestOptimizeEndpoint(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	if w := e.DoAs(nil, "POST", "/api/strategies/optimize", "{}"); w.Code != 401 {
		t.Fatalf("no session: %d", w.Code)
	}
	for body, msg := range map[string]string{`{}`: "spec is required.", `{"spec":{"symbol":"SHIB","interval":"4h","positionPct":100}}`: "is not in the universe"} {
		if w := e.Do("POST", "/api/strategies/optimize", body); w.Code != 400 || !strings.Contains(w.Body.String(), msg) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}

	var g generated
	json.Unmarshal([]byte(designExamples[0].reply), &g) // BTC 4h EMA cross, 4% stop, no target
	w := e.Do("POST", "/api/strategies/optimize", map[string]any{"spec": g.Spec})
	got := apptest.JSON[struct {
		Grid, Best []OptimizeCell
		Current    OptimizeCell
	}](t, w)
	if w.Code != 200 || len(got.Grid) != 25 || creditsUsed(t, e) != 0 {
		t.Fatalf("optimize: %d %s", w.Code, w.Body)
	}
	for i, c := range got.Grid {
		if *c.StopLossPct != gridStops[i/5] || *c.TakeProfitPct != gridTargets[i%5] {
			t.Errorf("cell %d: stop %v target %v", i, *c.StopLossPct, *c.TakeProfitPct)
		}
	}
	bt := apptest.JSON[Result](t, e.Do("POST", "/api/strategies/backtest", map[string]any{"spec": g.Spec}))
	if c := got.Current; *c.StopLossPct != 4 || c.TakeProfitPct != nil || c.Sharpe != bt.Metrics.Sharpe || c.Trades != bt.Metrics.Trades ||
		c.TotalReturnPct != bt.Metrics.TotalReturnPct || c.MaxDrawdownPct != bt.Metrics.MaxDrawdownPct {
		t.Fatalf("current %+v, backtest %+v", c, bt.Metrics)
	}
	eligible := slices.DeleteFunc(slices.Clone(got.Grid), func(c OptimizeCell) bool { return c.Trades < 5 })
	slices.SortStableFunc(eligible, func(a, b OptimizeCell) int { return cmp.Compare(b.Sharpe, a.Sharpe) })
	if len(got.Best) != min(3, len(eligible)) || len(got.Best) == 0 {
		t.Fatalf("best %+v of %d eligible", got.Best, len(eligible))
	}
	for i, c := range got.Best {
		if c.Trades < 5 || c.Sharpe != eligible[i].Sharpe || i > 0 && c.Sharpe > got.Best[i-1].Sharpe {
			t.Errorf("best %d: %+v", i, c)
		}
	}
	// A spec with too little history: 400 like /backtest.
	g.Spec.Bars, g.Spec.LongEntry.All[0].Right.Period = 200, 400
	if w := e.Do("POST", "/api/strategies/optimize", map[string]any{"spec": g.Spec}); w.Code != 400 || !strings.Contains(w.Body.String(), "not enough history") {
		t.Fatalf("short history: %d %s", w.Code, w.Body)
	}
}
