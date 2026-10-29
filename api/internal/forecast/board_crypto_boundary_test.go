package forecast

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

func TestBoardParserCryptoOnly(t *testing.T) {
	for _, a := range market.Universe {
		for _, name := range []string{a.Symbol, polyName(a)} {
			for _, question := range []string{"Will " + name + " be above $100 on October 20?", "Will " + name + " reach $100 in October?"} {
				t.Run(question, func(t *testing.T) {
					got := parseRule(question)
					if a.Perp() || a.Symbol == "PAXG" {
						if got != nil {
							t.Errorf("non-crypto or external gold price accepted: %+v", got)
						}
					} else if got == nil || got.Symbol != a.Symbol {
						t.Errorf("configured crypto price question rejected: %+v", got)
					}
				})
			}
		}
	}
}

func TestBoardEstimatorRejectsForgedPerpRuleBeforeHistory(t *testing.T) {
	fixture := markettest.New(t)
	client := market.NewClient(fixture.URL)
	client.HyperliquidBase = fixture.URL
	// Fix the observation clock relative to the deterministic Binance history;
	// the HIP3 rows are equally recent and usable, so refusal cannot pass merely
	// because provider data are absent, stale, or malformed.
	now := markettest.End.Add(36 * time.Hour)
	for _, sym := range []string{"AAPL", "SP500", "XYZ100"} {
		rows := make([]map[string]any, 90)
		for i := range rows {
			open := markettest.End.Add(-time.Duration(89-i) * 24 * time.Hour)
			close := 300 + float64(i%7) + float64(i)/10
			rows[i] = map[string]any{"s": "xyz:" + sym, "i": "1d", "t": open.UnixMilli(), "T": open.Add(24*time.Hour).UnixMilli() - 1,
				"o": fmt.Sprint(close), "h": fmt.Sprint(close + 1), "l": fmt.Sprint(close - 1), "c": fmt.Sprint(close), "v": "1000"}
		}
		fixture.SetCandleRows("xyz:"+sym, rows)
	}
	if _, err := client.Assets(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	events := []BoardEvent{{Markets: []BoardMarket{}}}
	for _, sym := range []string{"AAPL", "SP500", "XYZ100", "BTC", "BCH", "ETC"} {
		before[sym] = fixture.Requests("candle:xyz:" + sym)
		// Rules are constructed independently: no parser invocation protects this boundary.
		events[0].Markets = append(events[0].Markets, BoardMarket{ID: sym, Rule: &PriceQuestion{Symbol: sym, Op: "above", Strike: 100, Kind: "close"}, YesPrice: .3, end: now.Add(15 * 24 * time.Hour)})
	}
	b := board{d: &app.Deps{Market: client}}
	b.estimate(context.Background(), events, now)
	for _, m := range events[0].Markets {
		if _, ok := market.LookupCrypto(m.ID); !ok {
			if m.Estimate != nil || m.EstimateUnavailable == nil {
				t.Errorf("forged %s must have no proxy: %+v", m.ID, m)
			}
			if calls := fixture.Requests("candle:xyz:" + m.ID); calls != before[m.ID] {
				t.Errorf("forged %s selected HIP3 history: before=%d after=%d", m.ID, before[m.ID], calls)
			}
		} else if m.Estimate == nil || m.Estimate.Source != "Binance completed daily candles" || m.Estimate.Closes != 90 {
			t.Errorf("valid %s proxy lost: %+v", m.ID, m)
		}
	}
}

// A noncrypto-only batch must return before consulting even the market client.
// This supplements the mixed-batch history counters without relying on parser
// behavior or an upstream outage to make a forged rule unavailable.
func TestBoardEstimatorRejectsNonCryptoBeforeMarketSelection(t *testing.T) {
	for _, a := range market.Universe {
		if !a.Perp() {
			continue
		}
		for _, kind := range []string{"close", "touch"} {
			t.Run(a.Symbol+"/"+kind, func(t *testing.T) {
				events := []BoardEvent{{Markets: []BoardMarket{{Rule: &PriceQuestion{Symbol: a.Symbol, Kind: kind, Op: "above", Strike: 100}, Estimate: &Estimate{P: .8}}}}}
				b := board{d: &app.Deps{}}
				b.estimate(context.Background(), events, time.Now())
				if m := events[0].Markets[0]; m.Estimate != nil || m.EstimateUnavailable == nil {
					t.Errorf("forged existing proxy survived: %+v", m)
				}
			})
		}
	}
}

func TestCachedBoardRejectsPerpProxyWithHealthyAndStaleAssets(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%t", stale), func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{})
			b := newBoard(e.Deps)
			now := b.now()
			l := b.lists["macro"]
			l.at = now
			if stale {
				l.at, l.failed = now.Add(-2*boardTTL), now
			}
			l.events = []BoardEvent{{Slug: "generic-macro", Category: "macro", EndDate: now.Add(48 * time.Hour).Format(time.RFC3339)}}
			for _, sym := range []string{"AAPL", "SP500", "XYZ100", "BTC"} {
				l.events[0].Markets = append(l.events[0].Markets, BoardMarket{ID: sym, Question: "Will " + sym + " be above $100?", YesPrice: .3, end: now.Add(48 * time.Hour), Rule: &PriceQuestion{Symbol: sym, Op: "above", Strike: 100, Kind: "close"}, Estimate: &Estimate{P: .8, Source: "Binance completed daily candles", Lean: Lean{Signal: "yes", EVPer100: ptr(100.0)}}})
			}
			req := httptest.NewRequest("GET", "/api/predictions/markets?category=macro", nil)
			req = req.WithContext(httpx.WithUser(req.Context(), e.User))
			w := httptest.NewRecorder()
			b.handleList(w, req)
			if w.Code != 200 {
				t.Fatalf("board: %d %s", w.Code, w.Body)
			}
			body := apptest.JSON[boardBody](t, w)
			if body.Stale != stale || len(body.Events) != 1 || body.Events[0].Category != "macro" || len(body.Events[0].Markets) != 4 {
				t.Fatalf("generic macro rows lost: %+v", body)
			}
			for _, sym := range []string{"AAPL", "SP500", "XYZ100", "BTC"} {
				m := boardRow(t, body.Events, sym)
				if m.YesPrice != .3 {
					t.Errorf("crowd price changed: %+v", m)
				}
				if sym != "BTC" || stale {
					if m.Estimate != nil || m.EstimateUnavailable == nil {
						t.Errorf("cached forbidden/stale proxy survived: %+v", m)
					}
				} else if m.Estimate == nil || m.Estimate.Signal != "yes" || m.Estimate.EVPer100 == nil {
					t.Errorf("healthy BTC cached estimate lost: %+v", m)
				}
			}
			// Response filtering must not mutate the shared listing.
			if l.events[0].Markets[0].Estimate == nil {
				t.Error("response filtering mutated cached provider data")
			}
		})
	}
}
