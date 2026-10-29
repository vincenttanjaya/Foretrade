package aipf

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// basket over-weights BTC (capped to 0.6) and adds an unknown and a
// duplicate symbol (dropped): ETH 0.25, SOL 0.15, BTC 0.6 after cleaning.
const basket = `{"name":" Blue Chips ","thesis":"Large caps.","holdings":[{"symbol":"eth","weight":0.5,"rationale":"Smart contracts."},
	{"symbol":"SOL","weight":0.3,"rationale":"Speed."},{"symbol":"BTC","weight":2.0,"rationale":"Store of value."},
	{"symbol":"PEPE","weight":0.2},{"symbol":"ETH","weight":0.1}]}`

func used(t *testing.T, e *apptest.Env) int {
	t.Helper()
	c, err := e.Usage.Get(context.Background(), e.User)
	if err != nil {
		t.Fatal(err)
	}
	return c.Used
}

func TestBuildListGet(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Text: basket},
		llmtest.Reply{Text: `{"name":"Too small","holdings":[{"symbol":"BTC","weight":1},{"symbol":"ETH","weight":1}]}`},
		llmtest.Reply{Status: 400},
	)})
	ctx := context.Background()
	if _, err := e.DB.Exec(ctx, `insert into market_briefs (brief) values
		('{"sentiment":[{"symbol":"SOL","score":0.5,"label":"bullish","reason":"ETF filing"},{"symbol":"TON","score":-0.3,"label":"bearish","reason":"no data"}]}')`); err != nil {
		t.Fatal(err)
	}
	researchtest.AcceptBriefs(t, e.DB)
	e.Binance.Down("TONUSDT", true) // left out of the basket's universe

	for _, r := range [][2]string{{"POST", "/api/ai-portfolios/build"}, {"GET", "/api/ai-portfolios"}, {"GET", "/api/ai-portfolios/x"},
		{"POST", "/api/ai-portfolios/x/invest"}} {
		if w := e.DoAs(nil, r[0], r[1], "{}"); w.Code != 401 {
			t.Errorf("%s %s without a session: %d", r[0], r[1], w.Code)
		}
	}
	for _, body := range []string{`{"prompt":`, `{"prompt":" ab "}`, `{"prompt":"` + strings.Repeat("x", 501) + `"}`} {
		if w := e.Do("POST", "/api/ai-portfolios/build", body); w.Code != 400 {
			t.Errorf("%.30s: %d", body, w.Code)
		}
	}

	w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"steady large caps"}`)
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, w).Portfolio
	var weights []float64
	for _, h := range p.Holdings {
		weights = append(weights, h.Weight)
	}
	if w.Code != 201 || p.Name != "Blue Chips" || len(p.Holdings) != 3 || p.Holdings[0].Symbol != "ETH" || p.Holdings[2].Symbol != "BTC" ||
		!near(weights[0], 0.25) || !near(weights[1], 0.15) || !near(weights[2], 0.6) || used(t, e) != 2 {
		t.Fatalf("build: %d %s", w.Code, w.Body)
	}
	if bt := p.Backtest; len(bt.Series) != days || len(bt.Benchmark) != days || bt.Series[0].V != 100 || bt.Benchmark[0].V != 100 || bt.VolatilityAnnPct == nil || *bt.VolatilityAnnPct <= 0 {
		t.Fatalf("backtest: %d points, %+v", len(bt.Series), bt.Series[:1])
	}
	req := e.Bedrock.Requests()[0]
	if req.Effort != "medium" || !strings.Contains(req.Prompt, "SOL | bullish | +0.50 | ETF filing") || strings.Contains(req.Prompt, "TON") ||
		!strings.HasSuffix(req.Prompt, "<idea>\nsteady large caps\n</idea>") {
		t.Fatalf("prompt (TON is down, so it is out of the table and the sentiment):\n%s", req.Prompt)
	}

	// A brief older than briefMaxAge is ignored: no sentiment table in the prompt.
	if _, err := e.DB.Exec(ctx, "update market_briefs set created_at = now() - interval '3 days'"); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"two coins"}`); w.Code != 502 || used(t, e) != 4 {
		t.Fatalf("unusable basket: %d %s, used %d", w.Code, w.Body, used(t, e))
	}
	if p := e.Bedrock.Requests()[1].Prompt; strings.Contains(p, "News sentiment") || strings.Contains(p, "ETF filing") {
		t.Fatalf("stale brief in the prompt:\n%s", p)
	}
	if w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"anything"}`); w.Code != 502 || used(t, e) != 4 {
		t.Fatalf("bedrock down: %d, used %d (refunded)", w.Code, used(t, e))
	}

	list := apptest.JSON[struct{ Portfolios []Portfolio }](t, e.Do("GET", "/api/ai-portfolios", nil))
	if len(list.Portfolios) != 1 || list.Portfolios[0].ID != p.ID || len(list.Portfolios[0].Holdings) != 3 {
		t.Fatalf("list: %+v", list)
	}
	if got := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("GET", "/api/ai-portfolios/"+p.ID, nil)); got.Portfolio.Thesis != "Large caps." {
		t.Fatalf("get: %+v", got)
	}
	other := e.Session(storetest.User(t, e.DB))
	if w := e.DoAs(other, "GET", "/api/ai-portfolios/"+p.ID, nil); w.Code != 404 {
		t.Fatalf("another user's portfolio: %d", w.Code)
	}
	if w := e.Do("GET", "/api/ai-portfolios/not-a-uuid", nil); w.Code != 404 {
		t.Fatalf("bad id: %d", w.Code)
	}

	poor := apptest.New(t, Register, apptest.Options{Credits: 1}) // a build costs 2
	if w := poor.Do("POST", "/api/ai-portfolios/build", `{"prompt":"steady large caps"}`); w.Code != 429 || apptest.ErrorCode(t, w) != "ai_quota" {
		t.Fatalf("no credits: %d %s", w.Code, w.Body)
	}

	down := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket})})
	down.Binance.Down("BTCUSDT", true) // the benchmark is required
	if w := down.Do("POST", "/api/ai-portfolios/build", `{"prompt":"steady large caps"}`); w.Code != 502 || used(t, down) != 0 {
		t.Fatalf("no BTC: %d %s, used %d", w.Code, w.Body, used(t, down))
	}
}

func TestInvest(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket})})
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"steady large caps"}`)).Portfolio
	path := "/api/ai-portfolios/" + p.ID + "/invest"

	for body, want := range map[string]string{`{"amount":`: "bad_request", `{"amount":49.99}`: "bad_request", `{"amount":300000}`: "insufficient_buying_power"} {
		if w := e.Do("POST", path, body); w.Code != 400 || apptest.ErrorCode(t, w) != want {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := e.Do("POST", "/api/ai-portfolios/6f1c2c1e-0000-4000-8000-000000000000/invest", `{"amount":100}`); w.Code != 404 {
		t.Fatalf("unknown portfolio: %d", w.Code)
	}

	w := e.Do("POST", path, `{"amount":60}`) // SOL's $9 share is under the $10 minimum
	got := apptest.JSON[struct {
		Trades    []paper.Trade
		Portfolio paper.Portfolio
		Skipped   []struct{ Symbol, Reason string }
	}](t, w)
	if w.Code != 200 || len(got.Trades) != 2 || got.Trades[0].Symbol != "ETH" || got.Trades[0].Notional != 15 || got.Trades[1].Notional != 36 ||
		got.Trades[0].Source != "portfolio" || got.Trades[0].Note != "Blue Chips" || len(got.Portfolio.Positions) != 2 {
		t.Fatalf("invest: %d %s", w.Code, w.Body)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].Symbol != "SOL" || got.Skipped[0].Reason != "$9.00 is below the $10 minimum order" {
		t.Fatalf("skipped: %+v", got.Skipped)
	}

	// Market data down after the build (fresh clients: no cached prices): the
	// account cannot be priced, so nothing is bought.
	fresh := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket})})
	q := apptest.JSON[struct{ Portfolio Portfolio }](t, fresh.Do("POST", "/api/ai-portfolios/build", `{"prompt":"steady"}`)).Portfolio
	fresh.Binance.Down("", true)
	fresh.Market = testMarketClient(fresh.Binance.URL) // past the ticker the build cached
	fresh.Paper = paper.NewEngine(fresh.DB, fresh.Market)
	if w := fresh.Do("POST", "/api/ai-portfolios/"+q.ID+"/invest", `{"amount":100}`); w.Code != 502 || apptest.ErrorCode(t, w) != "upstream" {
		t.Fatalf("binance down: %d %s", w.Code, w.Body)
	}
}

func TestInvestRollsBackEarlierHoldingsOnFailure(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket})})
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"steady large caps"}`)).Portfolio
	ctx := context.Background()
	// The third holding fails after the first two have reached the real ledger.
	if _, err := e.DB.Exec(ctx, "alter table trades add constraint reject_btc check (symbol <> 'BTC')"); err != nil {
		t.Fatal(err)
	}
	path := "/api/ai-portfolios/" + p.ID + "/invest"
	if w := e.Do("POST", path, `{"amount":1000}`); w.Code < 400 {
		t.Fatalf("expected rejected basket, got %d", w.Code)
	}
	account, err := e.Paper.Portfolio(ctx, e.User)
	if err != nil {
		t.Fatal(err)
	}
	trades, err := e.Paper.Trades(ctx, e.User, 50)
	if err != nil {
		t.Fatal(err)
	}
	if account.Cash != 100000 || len(account.Positions) != 0 || len(trades) != 0 {
		t.Fatalf("failed basket left partial fills: cash %v, positions %d, trades %d", account.Cash, len(account.Positions), len(trades))
	}
	if _, err := e.DB.Exec(ctx, "alter table trades drop constraint reject_btc"); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("POST", path, `{"amount":1000}`); w.Code != 200 {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	trades, err = e.Paper.Trades(ctx, e.User, 50)
	if err != nil || len(trades) != 3 {
		t.Fatalf("retry should buy each holding once: %d trades, %v", len(trades), err)
	}
}

// A portfolio reads invested null until the caller buys it; then list and
// detail report its legs and their FIFO P&L at live marks, every leg carries
// the portfolio id, and a market outage only turns invested null again.
func TestInvested(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket}, llmtest.Reply{Text: basket})})
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"steady large caps"}`)).Portfolio
	other := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"another"}`)).Portfolio
	ctx := context.Background()
	detail := func() *Invested {
		t.Helper()
		w := e.Do("GET", "/api/ai-portfolios/"+p.ID, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"invested":`) {
			t.Fatalf("detail: %d %s", w.Code, w.Body)
		}
		return apptest.JSON[struct{ Portfolio Portfolio }](t, w).Portfolio.Invested
	}
	if p.Invested != nil || detail() != nil {
		t.Fatal("invested before any invest")
	}

	w := e.Do("POST", "/api/ai-portfolios/"+p.ID+"/invest", `{"amount":1000}`)
	fills := apptest.JSON[struct{ Trades []paper.Trade }](t, w).Trades
	if w.Code != 200 || len(fills) != 3 {
		t.Fatalf("invest: %d %s", w.Code, w.Body)
	}
	var stored int
	if err := e.DB.QueryRow(ctx, "select count(*) from trades where user_id = $1 and ref_id = $2", e.User, p.ID).Scan(&stored); err != nil || stored != 3 {
		t.Fatalf("legs with the portfolio's ref_id: %d %v", stored, err)
	}
	// Marks are the fake's flat prices, so each leg is down its slippage and fee.
	var total float64
	for _, f := range fills {
		total += (f.Price/1.0002-f.Price)*f.Qty - f.Fee
	}
	inv := detail()
	cents := func(a, b float64) bool { return math.Abs(a-b) < 0.011 } // each field is rounded to cents
	if inv == nil || inv.Amount != 1000 || inv.Trades != 3 || !inv.FirstAt.Equal(fills[0].CreatedAt) || !cents(inv.TotalPnl, total) ||
		!cents(inv.TotalPnl, inv.RealizedPnl+inv.UnrealizedPnl) || !cents(inv.ReturnPct, total/1000*100) || inv.ReturnPct >= 0 {
		t.Fatalf("invested: %+v, want total %v", inv, total)
	}
	if inv.BtcReturnPct != nil { // the fake's candles end before now
		t.Fatalf("btcReturnPct past the candles: %v", *inv.BtcReturnPct)
	}
	list := apptest.JSON[struct{ Portfolios []Portfolio }](t, e.Do("GET", "/api/ai-portfolios", nil)).Portfolios
	if len(list) != 2 || list[0].ID != other.ID || list[0].Invested != nil || list[1].Invested == nil || list[1].Invested.Amount != 1000 {
		t.Fatalf("list: %+v", list)
	}

	// No marks: the list still answers, without invested.
	e.Binance.Down("", true)
	fresh := &handler{d: &app.Deps{DB: e.DB, Market: testMarketClient(e.Binance.URL), Paper: paper.NewEngine(e.DB, testMarketClient(e.Binance.URL))}}
	ps := []Portfolio{{ID: p.ID}}
	fresh.invested(ctx, e.User, ps)
	if ps[0].Invested != nil {
		t.Fatalf("invested without marks: %+v", ps[0].Invested)
	}
	e.Binance.Down("", false)
	if _, err := e.Paper.Reset(ctx, e.User); err != nil {
		t.Fatal(err)
	}
	if detail() != nil {
		t.Fatal("invested after a reset")
	}
}

// A halted pair is never offered for a new basket; investing in a basket
// that holds one skips that leg, with the reason, and buys the rest. A halted
// BTC (the benchmark) fails the build before any credit is spent.
func TestHaltedPair(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"name":"Majors","thesis":"Big ones.","holdings":[` +
		`{"symbol":"TON","weight":0.4},{"symbol":"ETH","weight":0.2},{"symbol":"SOL","weight":0.2},{"symbol":"BTC","weight":0.2}]}`})})
	ctx := context.Background()
	e.Binance.Halt("TONUSDT", true)
	w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"the majors"}`)
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, w).Portfolio
	if w.Code != 201 || len(p.Holdings) != 3 || slices.ContainsFunc(p.Holdings, func(h Holding) bool { return h.Symbol == "TON" }) {
		t.Fatalf("build: %d %s", w.Code, w.Body)
	}
	if prompt := e.Bedrock.Requests()[0].Prompt; strings.Contains(prompt, "TON |") || !strings.Contains(prompt, "\nETH | Ethereum |") {
		t.Fatalf("TON offered to the model:\n%s", prompt)
	}

	var id string // built before the halt
	if err := e.DB.QueryRow(ctx, `insert into ai_portfolios (user_id, name, thesis, prompt, holdings, backtest) values ($1, 'Pre-halt', '', 'p',
		'[{"symbol":"TON","weight":0.5,"rationale":"Old."},{"symbol":"ETH","weight":0.5,"rationale":"Old."}]', '{}') returning id::text`, e.User).Scan(&id); err != nil {
		t.Fatal(err)
	}
	w = e.Do("POST", "/api/ai-portfolios/"+id+"/invest", `{"amount":100}`)
	got := apptest.JSON[struct {
		Trades  []paper.Trade
		Skipped []struct{ Symbol, Reason string }
	}](t, w)
	if w.Code != 200 || len(got.Trades) != 1 || got.Trades[0].Symbol != "ETH" || got.Trades[0].Notional != 50 || len(got.Skipped) != 1 ||
		got.Skipped[0].Symbol != "TON" || got.Skipped[0].Reason != "TON is halted on Binance, so new orders are paused" {
		t.Fatalf("invest: %d %s", w.Code, w.Body)
	}

	btc := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket})})
	btc.Binance.Halt("BTCUSDT", true)
	if w := btc.Do("POST", "/api/ai-portfolios/build", `{"prompt":"anything"}`); w.Code != 502 || used(t, btc) != 0 {
		t.Fatalf("BTC halted: %d %s, used %d", w.Code, w.Body, used(t, btc))
	}
}

func TestBtcSince(t *testing.T) {
	bars := []market.Candle{{T: 0, O: 100}, {T: 3600, O: 110}}
	for at, want := range map[int64]float64{0: 20, 1800: 20, 3600: 9.09, 7199: 9.09} {
		if got := btcSince(bars, 120, time.Unix(at, 0)); got == nil || *got != want {
			t.Errorf("at %d: %v, want %v", at, got, want)
		}
	}
	for _, at := range []int64{-1, 7200} { // before the bars, after the last one
		if got := btcSince(bars, 120, time.Unix(at, 0)); got != nil {
			t.Errorf("at %d: %v, want nil", at, *got)
		}
	}
	if btcSince(bars, 0, time.Unix(10, 0)) != nil || btcSince(nil, 120, time.Unix(10, 0)) != nil {
		t.Error("no price or no bars: want nil")
	}
}

// The model cites the caller's desk: F<n> drivers map onto live forecasts and are
// stored with the basket; a superseded, resolved, expired or foreign forecast is
// never offered, an unknown id falls back to stats, and a holding stored before
// drivers reads driver null.
func TestBuildDrivers(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"name":"Desk picks","thesis":"What the desk likes.",
		"holdings":[{"symbol":"SOL","weight":0.2,"rationale":"Underweight: the desk leans No.","driver":"F1"},
		{"symbol":"LINK","weight":0.3,"rationale":"ETF odds.","driver":"F2"},{"symbol":"ETH","weight":0.3,"rationale":"Upbeat wire.","driver":"sentiment"},
		{"symbol":"BTC","weight":0.2,"rationale":"Anchor.","driver":"F9"}]}`})})
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.DB.QueryRow(ctx, sql+" returning id::text", args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// forecast is a completed binary run at P(Yes) 0.39, resolving in days, completed hoursAgo.
	forecast := func(user, q, rule string, days, hoursAgo int, parent, outcome *string) string {
		return exec(`insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, price_rule, completed_at, parent_id, outcome)
			values ($1, 'binary', $2, '["Yes","No"]', (now() at time zone 'utc')::date + $3::int, 'completed',
				'{"probabilities":[{"outcome":"Yes","p":0.39},{"outcome":"No","p":0.61}]}', $4::jsonb, now() - make_interval(hours => $5), $6, $7)`,
			user, q, days, rule, hoursAgo, parent, outcome)
	}
	solRule, yes := `{"symbol":"SOL","op":"above","threshold":130,"mode":"on_date"}`, "Yes"
	root := forecast(e.User, "Will SOL close above $130 (first run)?", solRule, 14, 30, nil, nil) // superseded by its update
	sol := forecast(e.User, "Will SOL close above $130 on 2026-10-17?", solRule, 14, 1, &root, nil)
	etf := exec(`insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, completed_at)
		values ($1, 'binary', 'Will a spot LINK ETF be approved by 2026-12-31?', '["Yes","No"]', '2030-01-01', 'completed',
			'{"probabilities":[{"outcome":"Yes","p":0.7}]}', now() - interval '2 hours')`, e.User)
	forecast(e.User, "Will BTC close above $1 (resolved)?", "null", 14, 3, nil, &yes)
	forecast(e.User, "Will BTC close above $2 (expired)?", "null", -1, 0, nil, nil)
	forecast(storetest.User(t, e.DB), "Someone else's forecast?", "null", 14, 0, nil, nil)
	if _, err := e.DB.Exec(ctx, `insert into market_briefs (brief) values ('{"sentiment":[{"symbol":"ETH","score":0.4,"label":"bullish","reason":"ETF flows"}]}')`); err != nil {
		t.Fatal(err)
	}
	researchtest.AcceptBriefs(t, e.DB)

	w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"what my desk likes"}`)
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, w).Portfolio
	want := map[string]Driver{
		"SOL":  {"forecast", &sol, "Forecast · Yes 39% (leans No)"},
		"LINK": {"forecast", &etf, "Forecast · Yes 70%"},
		"ETH":  {"sentiment", nil, "News sentiment · bullish"},
		"BTC":  {"stats", nil, "Daily history stats"},
	}
	check := func(what string, hs []Holding) {
		t.Helper()
		if len(hs) != 4 {
			t.Fatalf("%s: %+v", what, hs)
		}
		for _, h := range hs {
			d, ok := want[h.Symbol]
			if h.Driver == nil || !ok || h.Driver.Kind != d.Kind || h.Driver.Label != d.Label || (h.Driver.ID == nil) != (d.ID == nil) ||
				(d.ID != nil && *h.Driver.ID != *d.ID) {
				t.Errorf("%s %s driver: %+v", what, h.Symbol, h.Driver)
			}
		}
	}
	if w.Code != 201 {
		t.Fatalf("build: %d %s", w.Code, w.Body)
	}
	check("build", p.Holdings)
	check("detail", apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("GET", "/api/ai-portfolios/"+p.ID, nil)).Portfolio.Holdings)

	prompt := e.Bedrock.Requests()[0].Prompt
	if !strings.Contains(prompt, "F1 · Will SOL close above $130 on 2026-10-17? · Yes 39% (leans No) · bullish probability for SOL: 39%\n"+
		"F2 · Will a spot LINK ETF be approved by 2026-12-31? · Yes 70%\n\n<idea>") {
		t.Fatalf("desk in the prompt:\n%s", prompt)
	}
	for _, q := range []string{"first run", "resolved", "expired", "Someone else"} {
		if strings.Contains(prompt, q) {
			t.Errorf("offered a %s forecast", q)
		}
	}

	exec(`insert into ai_portfolios (user_id, name, thesis, prompt, holdings, backtest)
		values ($1, 'Legacy', '', 'p', '[{"symbol":"BTC","weight":1,"rationale":"Old."}]', '{}')`, e.User)
	if w := e.Do("GET", "/api/ai-portfolios", nil); !strings.Contains(w.Body.String(), `"rationale":"Old.","driver":null`) {
		t.Fatalf("legacy holding: %s", w.Body)
	}
}

// Detail charts the paper fills since investing against BTC: both from the
// first bar at or after the first fill, BTC at 100 there; the newest bar's
// close is the fake's live price, so the last point is the fills marked to
// market, fees and slippage included. The list leaves both null; so does a
// market outage.
func TestInvestedSeries(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket})})
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"steady large caps"}`)).Portfolio
	if w := e.Do("POST", "/api/ai-portfolios/"+p.ID+"/invest", `{"amount":1000}`); w.Code != 200 {
		t.Fatalf("invest: %d %s", w.Code, w.Body)
	}
	// The fake's candles end at markettest.End: date the fills 3 days before it.
	first := markettest.End.Add(-72 * time.Hour)
	if _, err := e.DB.Exec(context.Background(), "update trades set created_at = $2 where user_id = $1", e.User, first); err != nil {
		t.Fatal(err)
	}
	inv := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("GET", "/api/ai-portfolios/"+p.ID, nil)).Portfolio.Invested
	if inv == nil || len(inv.Series) < 2 || len(inv.Series) != len(inv.BtcSeries) || inv.BtcSeries[0].V != 100 || inv.Series[0].T != first.Unix() ||
		inv.Series[len(inv.Series)-1].T != markettest.End.Unix() {
		t.Fatalf("invested: %+v", inv)
	}
	if last := inv.Series[len(inv.Series)-1].V; !(last < 100 && last > 99.5) {
		t.Fatalf("last point %v: the fills at the live price, down fees and slippage", last)
	}
	if w := e.Do("GET", "/api/ai-portfolios", nil); !strings.Contains(w.Body.String(), `"series":null,"btcSeries":null`) {
		t.Fatalf("list: %s", w.Body)
	}
	e.Binance.Down("SOLUSDT", true) // one holding's candles fail: no chart, the rest of the detail stays
	h := &handler{d: &app.Deps{DB: e.DB, Market: testMarketClient(e.Binance.URL)}}
	if s, b := h.actual(context.Background(), e.User, p.ID, first, "BTC"); s != nil || b != nil {
		t.Fatalf("candles down: %v %v", s, b)
	}
}

func TestBasketSuppressesLegacyBriefProse(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket})})
	if _, err := e.DB.Exec(context.Background(), `insert into market_briefs (brief) values ('{"headline":"GAMBLING_DERIVED","summary":"GAMBLING_DERIVED","sentiment":[{"symbol":"BTC","score":1,"reason":"GAMBLING_DERIVED"}]}')`); err != nil {
		t.Fatal(err)
	}
	w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"Large cap crypto basket"}`)
	if w.Code != 201 {
		t.Fatalf("basket: %d %s", w.Code, w.Body)
	}
	reqs := e.Bedrock.Requests()
	if len(reqs) != 1 || strings.Contains(reqs[0].Prompt, "GAMBLING_DERIVED") || strings.Contains(reqs[0].Prompt, "News sentiment") {
		t.Fatalf("model input: %+v", reqs)
	}
}
