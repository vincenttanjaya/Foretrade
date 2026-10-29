package market

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// Native daily history must come from the listing's bars: reversing today's
// FX rate cannot recover bars converted at different historical rates.
func TestCandlesOriginalCurrency(t *testing.T) {
	c, f := feedClient(t)
	f.Set(yahootest.Daily("D05.SI", "SGD", 20, 40))
	f.Set(yahootest.Daily("SGDUSD=X", "USD", 0.5, 1))
	mux := mount(c)
	read := func(query string) []Candle {
		t.Helper()
		w := get(mux, context.Background(), "/api/market/candles?symbol=DBS&interval=1d&limit=2"+query)
		var body struct{ Candles []Candle }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || len(body.Candles) != 2 {
			t.Fatalf("candles %s: %d %s / %v", query, w.Code, w.Body, err)
		}
		return body.Candles
	}
	usd := read("")
	if usd[0].C != 10 || usd[1].C != 40 {
		t.Fatalf("default historical FX: %+v", usd)
	}
	// The native path needs no FX data, even on a cold native cache.
	f.SetErr("SGDUSD=X", yahootest.NotFound("SGDUSD=X"))
	native := read("&currency=original")
	if native[0].O != 20 || native[0].H != 20.2 || native[0].L != 19.8 || native[0].C != 20 ||
		native[1].C != 40 || native[0].V != 1000 || native[0].T != usd[0].T || native[1].T != usd[1].T {
		t.Fatalf("native bars changed: %+v", native)
	}
	// Native and USD requests must not share a cached series.
	if got := read("&currency=USD"); got[0].C != 10 || got[1].C != 40 {
		t.Fatalf("native cache contaminated USD: %+v", got)
	}
}

func TestCandlesCurrencyValidation(t *testing.T) {
	c, _ := feedClient(t)
	mux := mount(c)
	for _, query := range []string{
		"symbol=BTC&currency=original",
		"symbol=SOFTBANK&currency=original",
		"symbol=DBS&currency=SGD",
		"symbol=DBS&currency=native",
	} {
		w := get(mux, context.Background(), "/api/market/candles?interval=1d&limit=2&"+query)
		if w.Code != 400 {
			t.Errorf("unsupported currency %s: %d %s", query, w.Code, w.Body)
		}
	}
}
