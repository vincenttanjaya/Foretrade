package paper

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// thirtyDays are crafted daily returns: ETH is 1.2 × BTC, SUI equals SOL (a
// noisy half-BTC), PAXG never moves and TAO only lists on day 15.
func thirtyDays() dailyReturns {
	r := dailyReturns{bySymbol: map[string][]float64{}}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for t := range 30 {
		r.dates = append(r.dates, start.AddDate(0, 0, t))
		btc := 0.01 * float64((t*7)%11-5)
		sol := 0.5*btc + 0.03*float64((t*5)%7-3)
		tao := math.NaN()
		if t >= 15 {
			tao = btc
		}
		for sym, v := range map[string]float64{"BTC": btc, "ETH": 1.2 * btc, "SOL": sol, "SUI": sol, "PAXG": 0, "TAO": tao} {
			r.bySymbol[sym] = append(r.bySymbol[sym], v)
		}
	}
	return r
}

func pos(symbol string, qty, mark float64, stop *float64) Position {
	return Position{Symbol: symbol, Qty: qty, MarkPrice: mark, MarketValue: qty * mark, StopLoss: stop}
}

func TestReview(t *testing.T) {
	r := thirtyDays()
	for i := range r.bySymbol["ETH"] {
		r.bySymbol["ETH"][i] = 2 * r.bySymbol["BTC"][i]
	}
	p := Portfolio{Revision: 7, Equity: 100000, GrossExposure: 16000, Positions: []Position{pos("BTC", .1, 100000, ptr(90000)), pos("ETH", -2, 3000, ptr(3300))}}
	got := review(p, nil, r, nil, 3.14159, time.Now())
	if got.Concentration == nil || got.Concentration.HHI != .531 || got.Concentration.Top1Pct != 62.5 || got.NetExposure != 4000 {
		t.Fatalf("concentration/exposure: %+v", got)
	}
	if got.BetaBtc == nil || *got.BetaBtc != -.02 || len(got.Stress) != 3 || got.Stress[0].EstPnl != 200 || got.Stress[0].EstPnlPct != .2 {
		t.Fatalf("signed/equity beta: %+v", got)
	}
	if got.Stops.Covered != 2 || got.Stops.AtRiskUsd != 1600 || got.Drawdown30dPct == nil || *got.Drawdown30dPct != 3.1 {
		t.Fatalf("stop/replay: %+v", got)
	}
	p.Equity = 200000
	twice := review(p, nil, r, nil, 0, time.Now())
	if twice.Stress[0].EstPnl != 200 || twice.Stress[0].EstPnlPct != .1 || twice.VaR.Usd != got.VaR.Usd {
		t.Fatal("cash changed dollar risk")
	}
}

func TestReviewEdges(t *testing.T) {
	now := time.Now()
	t.Run("no positions", func(t *testing.T) {
		got := review(Portfolio{Equity: 100_000, PredictionStakes: 250}, nil, dailyReturns{}, nil, 0, now)
		if got.Concentration != nil || got.BetaBtc != nil || got.VolAnnPct != nil || got.VaR != nil || got.WorstDay != nil ||
			got.PredictionStakesPct == nil || *got.PredictionStakesPct != 0.3 || len(got.Notes) != 2 {
			t.Errorf("empty: %+v", got)
		}
		b, _ := json.Marshal(got)
		for _, want := range []string{`"holdings":[]`, `"stress":[]`, `"correlations":[]`, `"clusters":[]`, `"uncovered":[]`, `"concentration":null`, `"var":null`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("JSON lacks %s: %s", want, b)
			}
		}
	})
	t.Run("one short position", func(t *testing.T) {
		r := thirtyDays()
		got := review(Portfolio{Equity: 100_000, GrossExposure: 1000, Positions: []Position{pos("ETH", -0.5, 2000, nil)}}, nil, r, nil, 0, now)
		if c := got.Concentration; c.Level != "high" || c.Top1Pct != 100 || c.Top3Pct != 100 || c.HHI != 1 || c.Effective != 1 {
			t.Errorf("concentration: %+v", c)
		}
		// Short 1.2β × $1,000: a 10% BTC drop gains $120.
		if *got.BetaBtc != -0.01 || got.Stress[0].EstPnl != 120 || got.Stress[0].EstPnlPct != 0.1 || got.Holdings[0].Side != "short" ||
			len(got.Correlations) != 0 || len(got.Clusters) != 0 || got.NetExposure != -1000 {
			t.Errorf("short: beta %v stress %+v holdings %+v", *got.BetaBtc, got.Stress, got.Holdings)
		}
		want := []string{"ETH is 100.0% of your exposure.", "A 10% BTC drop would gain about $120.00 (0.1% of equity) at the book's beta of -0.01.",
			"Your only position has no valid current stop.", "Only 30 daily closes were available, so the statistics are rough."}
		if !slices.Equal(got.Notes, want) {
			t.Errorf("notes\n got %q\nwant %q", got.Notes, want)
		}
	})
	t.Run("too little history and a pair without bars", func(t *testing.T) {
		r := thirtyDays()
		r.dates = r.dates[:5]
		for sym := range r.bySymbol {
			r.bySymbol[sym] = r.bySymbol[sym][:5]
		}
		delete(r.bySymbol, "SOL")
		p := Portfolio{Equity: 100_000, GrossExposure: 3000, Positions: []Position{pos("BTC", 0.02, 100_000, ptr(95_000)), pos("SOL", 5, 200, ptr(150))}}
		got := review(p, nil, r, []string{"SOL"}, 0, now)
		if got.VaR != nil || got.VolAnnPct != nil || got.WorstDay != nil || got.Holdings[1].Beta != nil || got.Holdings[0].Beta != nil ||
			got.BetaBtc != nil || got.Correlations[0].Rho != nil || got.Stops.Covered != 2 || got.Stops.AtRiskUsd != 100+250 {
			t.Errorf("short window: %+v", got)
		}

	})
}

// The endpoint on the test DB and the fake Binance, whose pairs all share one
// price shape: every pair is perfectly correlated with beta 1, so two longs
// cluster and a short against them is a hedge.
func TestReviewHandler(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	e := NewEngine(pool, testMarketClient(bin.URL))
	get := serveHistory(e)
	ctx := context.Background()

	if w := get("/api/portfolio/review", ""); w.Code != 401 {
		t.Fatalf("without a session: %d", w.Code)
	}
	var rev Review
	decode := func(path string) {
		t.Helper()
		w := get(path, user)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		rev = Review{}
		if err := json.Unmarshal(w.Body.Bytes(), &rev); err != nil {
			t.Fatal(err)
		}
	}
	decode("/api/portfolio/review")
	if rev.Positions != 0 || rev.Equity != StartingBalance || rev.Concentration != nil || rev.Window.Bars != 0 || len(rev.Holdings) != 0 ||
		rev.Drawdown30dPct == nil || *rev.Drawdown30dPct != 0 || !slices.Equal(rev.Notes, []string{"No open positions: nothing to review."}) || time.Since(rev.AsOf) > time.Minute {
		t.Fatalf("fresh account: %+v", rev)
	}

	for _, o := range []OrderRequest{
		{Symbol: "ETH", Side: "buy", Notional: ptr(1000)},
		{Symbol: "SOL", Side: "buy", Notional: ptr(500), StopLoss: ptr(180)},
		{Symbol: "BTC", Side: "sell", Notional: ptr(300)},
	} {
		if _, _, err := e.Order(ctx, user, o); err != nil {
			t.Fatal(err)
		}
	}
	p, err := e.Portfolio(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	decode("/api/portfolio/review")
	symbols := func(hs []Holding) (out []string) {
		for _, h := range hs {
			out = append(out, h.Symbol)
		}
		return out
	}
	if rev.Positions != 3 || rev.Revision != p.Revision || rev.Equity != round(p.Equity, 2) || rev.Window.Bars != reviewDays ||
		!slices.Equal(symbols(rev.Holdings), []string{"ETH", "SOL", "BTC"}) || rev.Holdings[2].Side != "short" ||
		rev.Concentration.Level != "high" || math.Abs(rev.Concentration.Top1Pct-55.6) > 0.1 {
		t.Fatalf("review: %+v", rev)
	}
	for _, h := range rev.Holdings {
		if h.Beta == nil || *h.Beta != 1 || h.Halted {
			t.Errorf("%s: beta %v halted %v, want 1 and live", h.Symbol, h.Beta, h.Halted)
		}
	}
	for _, c := range rev.Correlations {
		if c.Rho == nil || *c.Rho != 1 {
			t.Errorf("%s-%s: ρ %v, want 1", c.A, c.B, c.Rho)
		}
	}
	// ETH and SOL long together; the BTC short hedges each. Beta on equity ≈ (1000 + 500 − 300) / 100,000.
	if len(rev.Clusters) != 1 || !slices.Equal(rev.Clusters[0].Symbols, []string{"ETH", "SOL"}) || rev.Clusters[0].Rho != 1 ||
		math.Abs(rev.Clusters[0].Pct-83.3) > 0.1 || *rev.BetaBtc != 0.01 || rev.VaR == nil || rev.VaR.Usd <= 0 || rev.WorstDay == nil ||
		*rev.VolAnnPct <= 0 || rev.Stops.Covered != 1 || !slices.Equal(rev.Stops.Uncovered, []string{"ETH", "BTC"}) {
		t.Fatalf("clusters %+v beta %v var %+v stops %+v", rev.Clusters, *rev.BetaBtc, rev.VaR, rev.Stops)
	}
	hedges := 0
	for _, n := range rev.Notes {
		if strings.Contains(n, "a hedge, not two bets") {
			hedges++
		}
	}
	if hedges != 2 || !slices.Contains(rev.Notes, "2 of 3 positions have no valid current stop: 72.2% of your exposure is unprotected.") {
		t.Errorf("notes: %q", rev.Notes)
	}

	// A halted held pair stays in the review, flagged and noted.
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "TON", Side: "buy", Notional: ptr(200)}); err != nil {
		t.Fatal(err)
	}
	bin.Halt("TONUSDT", true)
	e = NewEngine(pool, testMarketClient(bin.URL)) // no cached ticker: the halt reaches the marks
	get = serveHistory(e)
	decode("/api/portfolio/review")
	ton := slices.IndexFunc(rev.Holdings, func(h Holding) bool { return h.Symbol == "TON" })
	if rev.Positions != 4 || ton < 0 || !rev.Holdings[ton].Halted ||
		!slices.Contains(rev.Notes, "TON is halted on Binance: it is valued at its last trade and only orders that shrink the position fill.") {
		t.Fatalf("halted: %+v", rev)
	}

	// A Binance outage: 502 (a fresh client, so no cached ticker).
	bin.Down("", true)
	if w := serveHistory(NewEngine(pool, testMarketClient(bin.URL)))("/api/portfolio/review", user); w.Code != 502 || code(t, w) != "upstream" {
		t.Errorf("during an outage: %d %s", w.Code, w.Body)
	}
}
