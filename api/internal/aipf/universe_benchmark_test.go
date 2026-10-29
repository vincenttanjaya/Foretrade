package aipf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

func dailyAt(first int64, prices ...float64) []market.Candle {
	out := make([]market.Candle, len(prices))
	for i, p := range prices {
		out[i] = market.Candle{T: first + int64(i)*86400, O: p, C: p}
	}
	return out
}

func TestUniverseBacktestCommonDatesAndMetadata(t *testing.T) {
	closes := map[string][]market.Candle{"BTC": dailyAt(0, 100, 110, 120, 130), "ETH": dailyAt(86400, 10, 20, 30, 40), "SOL": dailyAt(86400, 50, 50, 50, 50)}
	bt, err := backtest([]Holding{{Symbol: "ETH", Weight: .5}, {Symbol: "SOL", Weight: .5}}, closes, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(bt.Series) != 3 || bt.Series[0].T != 86400 || bt.Series[2].T != 259200 || bt.Series[2].V != 200 {
		t.Errorf("unequal actual dates must intersect: %+v", bt.Series)
	}
	raw, _ := json.Marshal(bt)
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	if fields["benchmarkSymbol"] != "BTC" || fields["windowStart"] != float64(86400) || fields["windowEnd"] != float64(259200) || fields["bars"] != float64(3) || fields["volatilityAnnPct"] != nil {
		t.Errorf("actual identity/window/warmup metadata missing: %s", raw)
	}
}

func TestUniverseBacktestExcludesOpenInvalidAndGaps(t *testing.T) {
	now := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	closes := map[string][]market.Candle{"BTC": dailyAt(now-3*86400, 100, 110, 120, 130), "ETH": dailyAt(now-3*86400, 10, 20, 30, 40)}
	bt, err := backtest([]Holding{{Symbol: "ETH", Weight: 1}}, closes, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(bt.Series) != 3 || bt.Series[len(bt.Series)-1].T != now-86400 {
		t.Errorf("open daily candle became a measured close: %+v", bt.Series)
	}
	closes = map[string][]market.Candle{"BTC": dailyAt(0, 100, 110, 120, 130, 140), "ETH": dailyAt(0, 10, 0, 30, 40, 50)}
	bt, err = backtest([]Holding{{Symbol: "ETH", Weight: 1}}, closes, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(bt.Series) != 3 || bt.Series[0].T != 2*86400 || bt.Series[2].T != 4*86400 {
		t.Errorf("invalid close/gap must clip to newest consecutive window: %+v", bt.Series)
	}
	closes["ETH"][2].C = math.Inf(1)
	bt, err = backtest([]Holding{{Symbol: "ETH", Weight: 1}}, closes, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(bt.Series) != 2 || math.IsNaN(bt.ReturnPct) || math.IsInf(bt.ReturnPct, 0) {
		t.Errorf("nonfinite close used: %+v", bt)
	}
}

func TestUniverseBenchmarkSelectionPersistence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stock, index float64
		want         string
	}{
		{"tie", .5, 0, "BTC"}, {"stock majority", .5001, 0, "SP500"}, {"stock and index", .25, .2501, "SP500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holdings := fmt.Sprintf(`[{"symbol":"AAPL","weight":%g},{"symbol":"SP500","weight":%g},{"symbol":"BTC","weight":0.25},{"symbol":"ETH","weight":%g}]`, tc.stock, tc.index, .75-tc.stock-tc.index)
			// JSON numeric syntax is decimal; stock+index are fractions before normalization.
			e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"name":"Mixed basket","thesis":"Paper perps.","holdings":` + holdings + `}`})})
			w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"mixed paper universe"}`)
			if w.Code != 201 {
				t.Fatalf("build %d %s", w.Code, w.Body)
			}
			var raw map[string]any
			json.Unmarshal(w.Body.Bytes(), &raw)
			p := raw["portfolio"].(map[string]any)
			bt := p["backtest"].(map[string]any)
			stockWeight := 0.0
			for _, item := range p["holdings"].([]any) {
				h := item.(map[string]any)
				a, _ := market.Lookup(h["symbol"].(string))
				if a.Perp() {
					stockWeight += h["weight"].(float64)
				}
			}
			if !near(stockWeight, tc.stock+tc.index) {
				t.Fatalf("selection must use final normalized weights: %.10f want %.10f", stockWeight, tc.stock+tc.index)
			}
			if bt["benchmarkSymbol"] != tc.want || bt["bars"] == nil {
				t.Fatalf("selected identity/window %+v want %s", bt, tc.want)
			}
			id := p["id"].(string)
			// Selection is persisted, not re-derived from subsequently changed stored allocations.
			if _, err := e.DB.Exec(context.Background(), `update ai_portfolios set holdings='[{"symbol":"BTC","weight":1}]' where id=$1`, id); err != nil {
				t.Fatal(err)
			}
			get := e.Do("GET", "/api/ai-portfolios/"+id, nil)
			json.Unmarshal(get.Body.Bytes(), &raw)
			if got := raw["portfolio"].(map[string]any)["backtest"].(map[string]any)["benchmarkSymbol"]; got != tc.want {
				t.Fatalf("saved selection changed %v", got)
			}
		})
	}
}

const stockBasket = `{"name":"Stock majority","thesis":"Paper mixed venues.","holdings":[{"symbol":"AAPL","weight":0.4},{"symbol":"META","weight":0.2},{"symbol":"BTC","weight":0.4}]}`

func TestUniverseChosenBenchmarkFailureRefund(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: stockBasket})})
	e.Binance.Down("xyz:SP500", true)
	w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"stock majority"}`)
	if w.Code != 502 || used(t, e) != 0 {
		t.Fatalf("unusable selected SP500 must fail/refund: %d %s credits=%d", w.Code, w.Body, used(t, e))
	}
	var count int
	e.DB.QueryRow(context.Background(), "select count(*) from ai_portfolios where user_id=$1", e.User).Scan(&count)
	if count != 0 {
		t.Fatalf("failed comparison saved %d baskets", count)
	}
	crypto := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: basket})})
	crypto.Binance.Down("xyz:SP500", true)
	if w := crypto.Do("POST", "/api/ai-portfolios/build", `{"prompt":"crypto majority"}`); w.Code != 201 || used(t, crypto) != 2 {
		t.Fatalf("crypto should not require SP500: %d %s", w.Code, w.Body)
	}
}

func TestUniverseDailiesKeepActuallyClosedNewestBar(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	closes, err := (&handler{d: e.Deps}).dailies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, sym := range []string{"BTC", "AAPL", "SP500"} {
		c := closes[sym]
		if len(c) == 0 || len(c) > days || c[len(c)-1].T != markettest.End.Unix() {
			t.Errorf("%s dropped an actually closed newest candle: n=%d", sym, len(c))
		}
	}
}

// A quote feed is never offered, even with Yahoo history: its daily bars skip
// weekends, so that history is not read at all.
func TestUniverseDailiesLeaveOutQuoteFeeds(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	closes := make([]float64, 60)
	for i := range closes {
		closes[i] = 2 + float64(i)/100
	}
	e.Yahoo.Set(yahootest.Daily("HSDT", "USD", closes...))
	got, err := (&handler{d: e.Deps}).dailies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range market.QuoteFeeds {
		if _, ok := got[a.Symbol]; ok {
			t.Fatalf("%s offered", a.Symbol)
		}
	}
	if n := e.Yahoo.Calls("HSDT"); n != 1 { // the assets read's quote only, never a daily history
		t.Fatalf("%d Yahoo calls for HSDT", n)
	}
}

func TestUniverseInvestedSavedChoiceTopupLegacyAndRetention(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: stockBasket})})
	w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"stock majority paper basket"}`)
	if w.Code != 201 {
		t.Fatalf("build %d %s", w.Code, w.Body)
	}
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, w).Portfolio
	path := "/api/ai-portfolios/" + p.ID
	for i := 0; i < 2; i++ {
		if w := e.Do("POST", path+"/invest", `{"amount":1000}`); w.Code != 200 {
			t.Fatalf("invest/topup%d %d %s", i, w.Code, w.Body)
		}
		at := markettest.End.Add(time.Duration(-72+i*36) * time.Hour)
		if _, err := e.DB.Exec(context.Background(), "update trades set created_at=$2 where user_id=$1 and created_at>$3", e.User, at, markettest.End); err != nil {
			t.Fatal(err)
		}
		detail := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("GET", path, nil)).Portfolio
		inv := detail.Invested
		if inv == nil || inv.BenchmarkSymbol != "SP500" || inv.BenchmarkLabel != "S&P 500 perp" || inv.BenchmarkReturnPct == nil || inv.BtcReturnPct != nil || inv.BtcSeries != nil || len(inv.BenchmarkSeries) < 2 || len(inv.Series) != len(inv.BenchmarkSeries) || inv.Amount != float64(1000*(i+1)) || inv.Trades != 3*(i+1) {
			t.Fatalf("saved SP500 actual/topup%d: %+v", i, inv)
		}
		if inv.WindowStart == nil || inv.WindowEnd == nil || *inv.WindowStart != inv.Series[0].T || *inv.WindowEnd != inv.Series[len(inv.Series)-1].T || inv.Bars != len(inv.Series) || inv.BenchmarkSeries[0].V != 100 {
			t.Fatalf("actual window/series %+v", inv)
		}
		list := apptest.JSON[struct{ Portfolios []Portfolio }](t, e.Do("GET", "/api/ai-portfolios", nil)).Portfolios
		if len(list) != 1 || list[0].Backtest.BenchmarkSymbol != "SP500" || list[0].Invested.BenchmarkSymbol != "SP500" || list[0].Invested.Series != nil || list[0].Invested.BenchmarkSeries != nil || list[0].Invested.BtcSeries != nil {
			t.Fatalf("list persisted generic fields %+v", list)
		}
	}
	// A shortened retained benchmark does not turn a since-investment comparison into a later one.
	e.Binance.SetRetention("xyz:SP500", 2)
	e.Market = testMarketClient(e.Binance.URL)
	e.Paper = paper.NewEngine(e.DB, e.Market)
	clipped := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("GET", path, nil)).Portfolio.Invested
	if clipped == nil || clipped.BenchmarkReturnPct != nil || clipped.BenchmarkSeries != nil || clipped.BtcReturnPct != nil || len(clipped.Series) < 2 {
		t.Fatalf("uncovered first investment comparison %+v", clipped)
	}
	// JSONB records predating identity stay BTC, even when their holdings happen to be stock-majority.
	if _, err := e.DB.Exec(context.Background(), `update ai_portfolios set backtest=backtest-'benchmarkSymbol'-'benchmarkLabel' where id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	legacy := apptest.JSON[struct{ Portfolio Portfolio }](t, e.Do("GET", path, nil)).Portfolio
	if legacy.Backtest.BenchmarkSymbol != "BTC" || legacy.Backtest.BenchmarkLabel != "BTC" || legacy.Invested.BenchmarkSymbol != "BTC" {
		t.Fatalf("legacy must default BTC without reselecting %+v", legacy)
	}
}

func TestUniverseSinceSeriesFirstOutsideBenchmarkHistory(t *testing.T) {
	closes := map[string][]market.Candle{"BTC": hourly(100, 110, 120), "ETH": hourly(10, 20, 30)}
	s, b := sinceSeries([]fill{{"ETH", 1, 10, time.Unix(-7200, 0)}}, closes, time.Unix(-7200, 0), "BTC")
	if len(s) != 3 || b != nil {
		t.Fatalf("retained portfolio may show, since-investment benchmark unavailable: %v %v", s, b)
	}
}

func TestUniverseBacktestUnavailableComparison(t *testing.T) {
	for _, cs := range []map[string][]market.Candle{
		{"BTC": dailyAt(0, 100), "ETH": dailyAt(0, 10)},
		{"BTC": dailyAt(0, 100, 110), "ETH": dailyAt(2*86400, 10, 20)},
		{"BTC": dailyAt(0, 100, 110), "ETH": dailyAt(0, 10, 20)},
	} {
		symbol := "BTC"
		if len(cs["BTC"]) == 2 && cs["ETH"][0].T == 0 {
			symbol = "SP500"
		}
		if _, err := backtest([]Holding{{Symbol: "ETH", Weight: 1}}, cs, symbol); !errors.Is(err, market.ErrUpstream) {
			t.Errorf("insufficient/missing chosen comparison must be unavailable: %v", err)
		}
	}
}

func TestUniverseShortHistoryWindowAndWarmup(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: stockBasket})})
	e.Binance.SetRetention("xyz:AAPL", 40)
	w := e.Do("POST", "/api/ai-portfolios/build", `{"prompt":"stock majority clipped window"}`)
	if w.Code != 201 {
		t.Fatalf("build %d %s", w.Code, w.Body)
	}
	p := apptest.JSON[struct{ Portfolio Portfolio }](t, w).Portfolio
	if p.Backtest.Bars != 40 || p.Backtest.WindowEnd-p.Backtest.WindowStart != 39*86400 || p.Backtest.VolatilityAnnPct == nil {
		t.Fatalf("retained common window %+v", p.Backtest)
	}
	prompt := e.Bedrock.Requests()[0].Prompt
	if strings.Contains(prompt, "last 180 daily closes") || !strings.Contains(prompt, "stock | hyperliquid") || !strings.Contains(prompt, " | 40\n") {
		t.Fatalf("untruthful prompt window %s", prompt)
	}
	short := apptest.New(t, Register, apptest.Options{})
	short.Binance.SetRetention("xyz:AAPL", 29)
	closes, err := (&handler{d: short.Deps}).dailies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if closes["AAPL"] != nil {
		t.Fatal("insufficient statistical warmup was offered")
	}
}
