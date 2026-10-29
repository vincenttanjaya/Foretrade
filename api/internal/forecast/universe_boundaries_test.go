package forecast

import (
	"context"
	"encoding/json"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestForecastStockRuleAndReplayRejected(t *testing.T) {
	for _, a := range market.StockPerps {
		if r := cleanRule(&PriceRule{a.Symbol, "above", 100, "on_date"}, "binary"); r != nil {
			t.Errorf("stock rule admitted: %+v", r)
		}
		hs := &history{asOf: day("2026-10-01"), bars: map[string][]market.Candle{a.Symbol: {{T: day("2026-09-30").Unix(), C: 200}}}}
		if _, _, err := hs.get(context.Background(), a.Symbol); err == nil {
			t.Errorf("forged cached replay admitted%s", a.Symbol)
		}
		if text := hs.spotLines(context.Background(), []string{a.Symbol}); text != "" {
			t.Errorf("stock underBinance replaylabel%s", text)
		}
	}
	if text := spotLines([]string{"AAPL"}, []market.Asset{{Symbol: "AAPL", Price: 332}}, time.Now()); text != "" {
		t.Errorf("forged stock liveBinance spot:%s", text)
	}
	if symbols := spotSymbols(Draft{PriceRule: &PriceRule{"AAPL", "above", 100, "on_date"}}); len(symbols) != 0 {
		t.Errorf("forged stock spot symbol:%v", symbols)
	}
}

func TestForecastForgedStockToolsAndOverview(t *testing.T) {
	b := markettest.New(t)
	h := &handler{d: &app.Deps{Market: testMarketClient(b.URL)}}
	for _, tool := range h.quantTools(nil) {
		schema, _ := json.Marshal(tool.Schema)
		if strings.Contains(string(schema), `"AAPL"`) || strings.Contains(string(schema), `"SP500"`) {
			t.Errorf("stock in %s enum", tool.Name)
		}
		if tool.Name == "get_market_overview" {
			out, err := tool.Run(context.Background(), json.RawMessage(`{}`))
			if err != nil || strings.Contains(out, `"AAPL"`) || strings.Contains(out, `"SP500"`) {
				t.Errorf("forecast mixedoverview %v %s", err, out)
			}
			continue
		}
		for _, symbol := range []string{"AAPL", "sp500", " xyz:AAPL "} {
			raw, _ := json.Marshal(map[string]any{"symbol": symbol, "interval": "1d", "horizon_days": 10})
			if _, err := tool.Run(context.Background(), raw); err == nil {
				t.Errorf("forged %s(%s) executed", tool.Name, symbol)
			}
		}
	}
	for _, tool := range h.quantTools(nil) {
		if tool.Name == "get_market_overview" {
			continue
		}
		before := b.Requests("candle:xyz:AAPL")
		tool.Run(context.Background(), json.RawMessage(`{"symbol":"AAPL","interval":"1d","horizon_days":10}`))
		if b.Requests("candle:xyz:AAPL") != before {
			t.Errorf("forged %s reachedHIP3", tool.Name)
		}
	}
}

func TestForecastStockMatcherAndLadderDoNotFetch(t *testing.T) {
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer s.Close()
	h := &handler{d: &app.Deps{PolymarketBase: s.URL}}
	d := Draft{Question: "Will AAPL close above $100?", Shape: "binary", ResolutionDate: "2026-10-20", PriceRule: &PriceRule{"AAPL", "above", 100, "on_date"}}
	if m, err := h.findMarket(context.Background(), d); err != nil || m != nil || hits.Load() != 0 {
		t.Errorf("stock rule matcher fetchedprovider: %+v %v hits%d", m, err, hits.Load())
	}
	d.PriceRule = nil
	d.Shape = "ordered"
	d.Outcomes = []string{"Below $100", "$100 to $200", "Above $200"}
	hits.Store(0)
	if m, err := h.findLadder(context.Background(), d); err != nil || m != nil || hits.Load() != 0 {
		t.Errorf("stock ladder fetchedprovider: %+v %v hits%d", m, err, hits.Load())
	}
}

func TestForecastStoredStockRuleCannotResolve(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	id := completed(t, e, e.User, Draft{Shape: "binary", Question: "AAPL above$1", Outcomes: []string{"Yes", "No"}, ResolutionDate: "2026-09-30", PriceRule: &PriceRule{"AAPL", "above", 1, "on_date"}}, .5)
	h := &handler{d: e.Deps}
	n, err := h.resolveDue(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var outcome *string
	if err := e.DB.QueryRow(context.Background(), "select outcome from forecasts where id=$1", id).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if n != 0 || outcome != nil || e.Binance.Requests("candle:xyz:AAPL") != 0 {
		t.Fatalf("forged persistedstock resolved: n%d outcome%v requests%d", n, outcome, e.Binance.Requests("candle:xyz:AAPL"))
	}
}
