package paper

import (
	"context"
	"encoding/json"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReviewMissingMaterialExposure(t *testing.T) {
	for _, qty := range []float64{1, -1} {
		r := thirtyDays()
		delete(r.bySymbol, "SOL")
		p := Portfolio{Equity: 100000, GrossExposure: 20000, Positions: []Position{pos("BTC", .1, 100000, nil), pos("SOL", qty, 10000, nil)}}
		got := review(p, nil, r, []string{"SOL"}, 0, time.Now())
		if got.BetaBtc != nil || got.VaR != nil || got.VolAnnPct != nil || got.WorstDay != nil || len(got.Stress) != 0 {
			t.Errorf("material missing exposure reported whole-book risk: %+v", got)
		}
		if got.Concentration == nil || len(got.Holdings) != 2 {
			t.Fatal("lost known facts")
		}
	}
}
func TestReviewActualSharedDays(t *testing.T) {
	r := thirtyDays()
	for i := 0; i < 12; i++ {
		r.bySymbol["ETH"][i] = math.NaN()
	}
	got := review(Portfolio{Equity: 100000, GrossExposure: 10000, Positions: []Position{pos("ETH", 5, 2000, nil)}}, nil, r, nil, 0, time.Now())
	if got.BetaBtc != nil || got.VaR != nil || len(got.Stress) != 0 {
		t.Fatalf("18 observations claimed risk: %+v", got)
	}
}
func TestReviewCrossedStops(t *testing.T) {
	got := review(Portfolio{Equity: 100000, GrossExposure: 200, Positions: []Position{pos("BTC", 1, 100, ptr(110)), pos("ETH", -1, 100, ptr(90))}}, nil, thirtyDays(), nil, 0, time.Now())
	if got.Stops.Covered != 0 || len(got.Stops.Uncovered) != 2 || got.Stops.AtRiskUsd != 0 {
		t.Fatalf("crossed stops counted: %+v", got.Stops)
	}
}
func TestReviewPredictionOnlyFacts(t *testing.T) {
	got := review(Portfolio{Equity: 100000, PredictionStakes: 250}, nil, dailyReturns{}, nil, 0, time.Now())
	notes := strings.Join(got.Notes, " ")
	if got.PredictionStakes != 250 || got.PredictionStakesPct == nil || *got.PredictionStakesPct != .3 || !strings.Contains(notes, "Prediction stakes") || !strings.Contains(notes, "tail risk is excluded") {
		t.Fatalf("prediction-only facts disappeared: %+v", got)
	}
	if strings.Contains(notes, "nothing to review") || !strings.Contains(notes, "No open asset positions") {
		t.Fatalf("outcome stakes were dismissed instead of separating asset statistics: %q", notes)
	}
	if got.Concentration != nil || got.BetaBtc != nil || got.VaR != nil || got.VolAnnPct != nil || got.WorstDay != nil || len(got.Stress) != 0 || len(got.Correlations) != 0 || len(got.Clusters) != 0 {
		t.Fatalf("outcome stakes reported as asset-history risk: %+v", got)
	}
}

func TestReviewClustersRequireEveryPair(t *testing.T) {
	r := dailyReturns{bySymbol: map[string][]float64{}}
	for i := range 40 {
		x := math.Sin(float64(i) * 2 * math.Pi / 40)
		y := math.Cos(float64(i) * 2 * math.Pi / 40)
		r.dates = append(r.dates, time.Unix(int64(i)*86400, 0))
		r.bySymbol["BTC"] = append(r.bySymbol["BTC"], x)
		r.bySymbol["ETH"] = append(r.bySymbol["ETH"], .9*x+math.Sqrt(1-.9*.9)*y)
		r.bySymbol["SOL"] = append(r.bySymbol["SOL"], .62*x+math.Sqrt(1-.62*.62)*y)
	}
	got := review(Portfolio{Equity: 100000, GrossExposure: 300, Positions: []Position{pos("BTC", 1, 100, nil), pos("ETH", 1, 100, nil), pos("SOL", 1, 100, nil)}}, nil, r, nil, 0, time.Now())
	for _, c := range got.Clusters {
		b, _ := json.Marshal(c)
		if !strings.Contains(string(b), `"days":40`) {
			t.Errorf("cluster lacks measured sample: %s", b)
		}
		if len(c.Symbols) > 2 {
			t.Fatalf("transitive cluster: %+v", c)
		}
	}
}

func TestReviewCapturedRevisionDuringMutation(t *testing.T) {
	for _, mutation := range []string{"reset", "trade"} {
		for _, interval := range []string{"1d", "4h"} {
			t.Run(mutation+interval, func(t *testing.T) { reviewCapturedMutation(t, mutation, interval) })
		}
	}
}
func reviewCapturedMutation(t *testing.T, mutation, interval string) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("interval") == interval {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		resp, err := http.Get(bin.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	e := NewEngine(pool, testMarketClient(proxy.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(1000)}); err != nil {
		t.Fatal(err)
	}
	bin.SetPrice("BTCUSDT", 50000)
	e = NewEngine(pool, testMarketClient(proxy.URL))
	prior, err := e.Portfolio(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		r   Review
		err error
	}
	done := make(chan result, 1)
	go func() { r, err := e.Review(ctx, user, time.Now()); done <- result{r, err} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("daily barrier timeout")
	}
	if mutation == "reset" {
		if _, err := e.Reset(ctx, user); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "sell", Qty: ptr(prior.Positions[0].Qty)}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.r.Revision != prior.Revision || got.r.Drawdown30dPct == nil || *got.r.Drawdown30dPct == 0 {
			t.Fatalf("mixed reset history: %+v", got.r)
		}
	case <-ctx.Done():
		t.Fatal("review timeout")
	}
}

func TestDailyReturnsNoInventedGapsOrDiscardedClosedBar(t *testing.T) {
	bin := markettest.New(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(bin.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, "down", 502)
			return
		}
		defer resp.Body.Close()
		if r.URL.Path != "/api/v3/klines" {
			io.Copy(w, resp.Body)
			return
		}
		var rows [][]any
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Query().Get("symbol") == "ETHUSDT" {
			rows = append(rows[:len(rows)-3], rows[len(rows)-2:]...)
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer proxy.Close()
	e := NewEngine(nil, testMarketClient(proxy.URL))
	r, _, err := e.dailyReturns(context.Background(), []string{"ETH"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.dates[len(r.dates)-1].Equal(markettest.End) {
		t.Fatalf("already closed final candle discarded: %v", r.dates[len(r.dates)-1])
	}
	gap := 0
	for _, v := range r.bySymbol["ETH"] {
		if math.IsNaN(v) {
			gap++
		}
	}
	if gap != 2 {
		t.Fatalf("missing close invented returns: %d unknown, want2", gap)
	}
}

func TestReviewLossQuantilesHandCalculated(t *testing.T) {
	r := dailyReturns{bySymbol: map[string][]float64{"BTC": {}}}
	for i := range 40 {
		v := .01
		if i == 0 {
			v = -.1
		}
		if i == 1 {
			v = -.06
		}
		r.bySymbol["BTC"] = append(r.bySymbol["BTC"], v)
		r.dates = append(r.dates, time.Date(2026, 8, 1+i, 0, 0, 0, 0, time.UTC))
	}
	got := review(Portfolio{Equity: 100000, GrossExposure: 10000, Positions: []Position{pos("BTC", .1, 100000, nil)}}, nil, r, nil, 0, time.Now())
	if got.VaR == nil || got.VaR.Usd != 600 || got.VaR.CvarUsd != 800 || got.VaR.Pct != .6 || got.WorstDay.Pnl != -1000 || got.WorstDay.Date != "2026-08-01" {
		t.Fatalf("hand-calculated tail: %+v %+v", got.VaR, got.WorstDay)
	}
}
func TestReviewNonpositiveEquityNullPercentages(t *testing.T) {
	for _, equity := range []float64{0, -100} {
		got := review(Portfolio{Equity: equity, GrossExposure: 100, PredictionStakes: 5, Positions: []Position{pos("BTC", 1, 100, nil)}}, nil, thirtyDays(), nil, 0, time.Now())
		data, _ := json.Marshal(got)
		if !strings.Contains(string(data), `"exposurePct":null`) || !strings.Contains(string(data), `"predictionStakesPct":null`) {
			t.Fatalf("unavailable normalization claimed zero: %s", data)
		}
	}
}

func TestReviewHistoryOutageKeepsFacts(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/klines" {
			http.Error(w, "history unavailable", 500)
			return
		}
		resp, err := http.Get(bin.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, "down", 502)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	e := NewEngine(pool, testMarketClient(proxy.URL))
	if _, _, err := e.Order(context.Background(), user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(1000)}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Review(context.Background(), user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Positions != 1 || got.Concentration == nil || got.GrossExposure <= 0 || got.Drawdown30dPct != nil || got.BetaBtc != nil || got.Coverage.Reason == nil {
		t.Fatalf("lost known facts during outage: %+v", got)
	}
}
func TestDailyReturnsBenchmarkOutageRetainsOtherPairs(t *testing.T) {
	bin := markettest.New(t)
	bin.Down("BTCUSDT", true)
	e := NewEngine(nil, testMarketClient(bin.URL))
	r, _, _ := e.dailyReturns(context.Background(), []string{"ETH", "SOL"})
	if len(r.bySymbol["ETH"]) < 20 || sharedDays(r.bySymbol["ETH"], r.bySymbol["SOL"]) < 20 {
		t.Fatal("benchmark outage erased independent pair measurements")
	}
}
func TestReviewMissingMarksSuppressesRisk(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	e := NewEngine(pool, testMarketClient(bin.URL))
	if _, _, err := e.Order(context.Background(), user, OrderRequest{Symbol: "APT", Side: "buy", Notional: ptr(1000)}); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(bin.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, "down", 502)
			return
		}
		defer resp.Body.Close()
		if r.URL.Path != "/api/v3/ticker/24hr" {
			io.Copy(w, resp.Body)
			return
		}
		var rows []map[string]any
		json.NewDecoder(resp.Body).Decode(&rows)
		out := []map[string]any{}
		for _, row := range rows {
			if row["symbol"] != "APTUSDT" {
				out = append(out, row)
			}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer proxy.Close()
	got, err := NewEngine(pool, testMarketClient(proxy.URL)).Review(context.Background(), user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.BetaBtc != nil || got.VaR != nil || len(got.Stress) != 0 || !slices.Contains(got.Coverage.MissingSymbols, "APT") {
		t.Fatalf("unmarked holding reported whole-book risk: %+v", got)
	}
}
func TestReviewStopStatusesAndStakeAmount(t *testing.T) {
	got := review(Portfolio{Equity: 100000, PredictionStakes: 250, GrossExposure: 300, Positions: []Position{pos("BTC", 1, 100, ptr(110)), pos("ETH", 1, 100, ptr(90)), pos("SOL", 1, 100, nil)}}, map[string]bool{"ETH": true}, thirtyDays(), nil, 0, time.Now())
	b, _ := json.Marshal(got)
	for _, want := range []string{`"stopStatus":"crossed"`, `"stopStatus":"dormant"`, `"stopStatus":"missing"`, `"predictionStakes":250`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s: %s", want, b)
		}
	}
}
func TestReviewRequiresVaryingCommonBenchmark(t *testing.T) {
	r := dailyReturns{bySymbol: map[string][]float64{}}
	for i := range 40 {
		b := .01
		if i < 10 || i >= 30 {
			b = float64(i%3) * .01
		}
		eth, sol := b, b
		if i < 10 {
			eth = math.NaN()
		}
		if i >= 30 {
			sol = math.NaN()
		}
		r.bySymbol["BTC"] = append(r.bySymbol["BTC"], b)
		r.bySymbol["ETH"] = append(r.bySymbol["ETH"], eth)
		r.bySymbol["SOL"] = append(r.bySymbol["SOL"], sol)
		r.dates = append(r.dates, time.Unix(int64(i)*86400, 0))
	}
	got := review(Portfolio{Equity: 100000, GrossExposure: 200, Positions: []Position{pos("ETH", 1, 100, nil), pos("SOL", 1, 100, nil)}}, nil, r, nil, 0, time.Now())
	if got.BetaBtc != nil || len(got.Stress) != 0 {
		t.Fatalf("whole-book sample has flat BTC but claims beta: %+v", got)
	}
}
