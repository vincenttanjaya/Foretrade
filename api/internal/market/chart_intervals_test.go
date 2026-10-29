package market

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

func TestChartCalendarIntervalsSpotHTTP(t *testing.T) {
	for _, interval := range []string{"1w", "1M"} {
		t.Run(interval, func(t *testing.T) {
			starts := []int64{1704067200, 1706745600, 1709251200} // Jan, leap-Feb, March 2024
			if interval == "1w" {
				starts = []int64{1704067200, 1704672000, 1705276800} // Mondays
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("interval"); got != interval {
					t.Errorf("upstream interval %q", got)
				}
				end, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
				rows := [][]any{}
				for _, at := range starts {
					if end == 0 || at*1000 <= end {
						rows = append(rows, []any{at * 1000, "10", "12", "9", "11", "100"})
					}
				}
				json.NewEncoder(w).Encode(rows)
			}))
			t.Cleanup(srv.Close)
			c := testMarketClient(srv.URL)
			mux := mount(c)
			path := "/api/market/candles?symbol=BTC&interval=" + interval + "&limit=2"
			bars := readHistoryCandles(t, mux, path)
			if len(bars) != 2 || bars[0].T != starts[1] || bars[1].T != starts[2] {
				t.Fatalf("latest calendar candles: %+v", bars)
			}
			older := readHistoryCandles(t, mux, fmt.Sprintf("%s&before=%d", path, bars[0].T))
			if len(older) != 1 || older[0].T != starts[0] {
				t.Fatalf("exclusive calendar history: %+v", older)
			}
			if ValidInterval(interval) {
				t.Fatal("chart interval became a strategy/analysis interval")
			}
			if _, err := c.Candles(context.Background(), "BTC", interval, 2); err == nil {
				t.Fatal("financial history accepted a chart-only interval")
			}
			if _, err := c.CandlesBefore(context.Background(), "BTC", interval, time.Now(), 2); err == nil {
				t.Fatal("financial past history accepted a chart-only interval")
			}
			if got := get(mux, context.Background(), "/api/market/analysis?symbol=BTC&interval="+interval); got.Code != http.StatusBadRequest {
				t.Fatalf("analysis accepted chart interval: %d %s", got.Code, got.Body)
			}
		})
	}
}

func TestChartCalendarBoundaries(t *testing.T) {
	for _, tc := range []struct{ at, interval, start, end string }{
		{"2024-02-29T17:00:00Z", "1M", "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z"},
		{"2025-02-28T17:00:00Z", "1M", "2025-02-01T00:00:00Z", "2025-03-01T00:00:00Z"},
		{"2026-01-31T17:00:00Z", "1M", "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z"},
		{"2026-04-30T17:00:00Z", "1M", "2026-04-01T00:00:00Z", "2026-05-01T00:00:00Z"},
		{"2026-10-08T17:00:00Z", "1w", "2026-10-05T00:00:00Z", "2026-10-12T00:00:00Z"},
		{"2026-10-11T23:59:59Z", "1w", "2026-10-05T00:00:00Z", "2026-10-12T00:00:00Z"},
		{"2026-10-12T00:00:00Z", "1w", "2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if got := chartPeriodStart(at, tc.interval).Format(time.RFC3339); got != tc.start {
			t.Errorf("%s %s start %s", tc.interval, tc.at, got)
		}
		if got := nextChartBoundary(at, tc.interval).Format(time.RFC3339); got != tc.end {
			t.Errorf("%s %s end %s", tc.interval, tc.at, got)
		}
	}
	for _, interval := range []string{"1m", "1W", "1mo", "1wk"} {
		if validChartInterval(interval) {
			t.Errorf("accepted unsupported/case-confused interval %s", interval)
		}
	}
}

func TestChartCalendarCacheExpiresAtClose(t *testing.T) {
	for _, interval := range []string{"1w", "1M"} {
		t.Run(interval, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				boundary := nextChartBoundary(time.Now(), interval)
				time.Sleep(time.Until(boundary) - 5*time.Second)
				hits := 0
				cache := cached[int]{calendar: interval}
				read := func() int {
					v, err := cache.get(context.Background(), 30*time.Second, func() (int, error) { hits++; return hits, nil })
					if err != nil {
						t.Fatal(err)
					}
					return v
				}
				if read() != 1 {
					t.Fatal("initial fetch")
				}
				time.Sleep(4 * time.Second)
				if read() != 1 {
					t.Fatal("cache expired before calendar close")
				}
				time.Sleep(time.Second)
				if read() != 2 || hits != 2 {
					t.Fatal("open candle reused after calendar close")
				}
			})
		})
	}
}

func TestChartCalendarQuoteCurrencies(t *testing.T) {
	c, f := feedClient(t)
	local := yahootest.Daily("D05.SI", "SGD", 20, 40, 60, 80)
	fx := yahootest.Daily("SGDUSD=X", "USD", .5, .25, 2, 1)
	for i, date := range []string{"2024-01-31", "2024-02-01", "2024-02-29", "2024-03-01"} {
		at, _ := time.Parse("2006-01-02", date)
		local.Bars[i].T, fx.Bars[i].T = at.Add(9*time.Hour), at
	}
	f.Set(local)
	f.Set(fx)
	mux := mount(c)
	for _, interval := range []string{"1w", "1M"} {
		path := "/api/market/candles?symbol=DBS&limit=10&interval=" + interval
		usd := readHistoryCandles(t, mux, path)
		f.SetErr("SGDUSD=X", yahootest.NotFound("SGDUSD=X"))
		native := readHistoryCandles(t, mux, path+"&currency=original")
		f.SetErr("SGDUSD=X", nil)
		want := 3
		if interval == "1w" {
			want = 2
		}
		if len(usd) != want || len(native) != want {
			t.Fatalf("%s USD/native length %d/%d", interval, len(usd), len(native))
		}
		if interval == "1M" {
			feb := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).Unix()
			if usd[1].T != feb || usd[1].O != 10 || usd[1].C != 120 || !approxEq(usd[1].H, 121.2) || usd[1].V != 2000 {
				t.Fatalf("monthly daily-FX aggregation %+v", usd)
			}
			if native[1].T != feb || native[1].O != 40 || native[1].C != 60 || !approxEq(native[1].H, 60.6) || native[1].V != 2000 {
				t.Fatalf("monthly original aggregation %+v", native)
			}
		} else {
			if usd[1].T != time.Date(2024, 2, 26, 0, 0, 0, 0, time.UTC).Unix() || usd[1].O != 120 || usd[1].C != 80 || native[1].O != 60 || native[1].C != 80 {
				t.Fatalf("weekly calendar aggregation USD%+v original%+v", usd, native)
			}
		}
		older := readHistoryCandles(t, mux, fmt.Sprintf("%s&before=%d&currency=original", path, native[len(native)-1].T))
		if len(older) != want-1 || older[len(older)-1].T >= native[len(native)-1].T {
			t.Fatalf("calendar original history %+v", older)
		}
		if rng, yiv := feedRange(interval, 10, true); rng != "max" || (interval == "1w" && yiv != "1wk") || (interval == "1M" && yiv != "1mo") {
			t.Fatal(rng, yiv)
		}
	}
}

func TestChartCalendarPerpRestatesSplitsBeforeGrouping(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	ctx := context.Background()
	daily, err := c.Candles(ctx, "KIOXIA", "1d", 90)
	if err != nil {
		t.Fatal(err)
	}
	mux := mount(c)
	for _, interval := range []string{"1w", "1M"} {
		path := "/api/market/candles?symbol=KIOXIA&limit=3&interval=" + interval
		bars := readHistoryCandles(t, mux, path)
		if len(bars) != 3 {
			t.Fatalf("%s %+v", interval, bars)
		}
		for _, bar := range bars {
			end := nextChartBoundary(time.Unix(bar.T, 0), interval).Unix()
			parts := []Candle{}
			for _, d := range daily {
				if d.T >= bar.T && d.T < end {
					parts = append(parts, d)
				}
			}
			if len(parts) == 0 {
				t.Fatal("missing independent daily fixture")
			}
			expect := Candle{T: bar.T, O: parts[0].O, C: parts[len(parts)-1].C, H: parts[0].H, L: parts[0].L}
			for _, d := range parts {
				expect.H = max(expect.H, d.H)
				expect.L = min(expect.L, d.L)
				expect.V += d.V
			}
			if bar != expect {
				t.Fatalf("%s must aggregate adjusted daily history: got%+v want%+v", interval, bar, expect)
			}
		}
		older := readHistoryCandles(t, mux, fmt.Sprintf("%s&before=%d", path, bars[0].T))
		if len(older) != 3 || older[len(older)-1].T >= bars[0].T {
			t.Fatalf("%s perp cursor %+v", interval, older)
		}
		// A cursor inside a historical period selects the full candle that
		// opened before it; it does not truncate that candle's OHLC mid-period.
		inside := readHistoryCandles(t, mux, fmt.Sprintf("/api/market/candles?symbol=KIOXIA&limit=1&interval=%s&before=%d", interval, bars[0].T+3*86400))
		if len(inside) != 1 || inside[0] != bars[0] {
			t.Fatalf("%s cursor clipped an already-open historical candle: got%+v want%+v", interval, inside, bars[0])
		}
	}
}

func TestChartCalendarPerpHistoryDoesNotRestoreTruncatedRetentionBucket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(markettest.End.Sub(time.Now()))
		b := markettest.New(t)
		b.SetRetention("xyz:NVDA", 5000)
		c := testMarketClient(b.URL)
		c.http = &http.Client{}
		mux := mount(c)
		path := "/api/market/candles?symbol=NVDA&interval=1M&limit=1000"
		latest := readHistoryCandles(t, mux, path)
		if len(latest) < 2 {
			t.Fatal("missing retained monthly history")
		}
		before := readHistoryCandles(t, mux, fmt.Sprintf("%s&before=%d", path, latest[0].T))
		if len(before) != 0 {
			t.Fatalf("history paging restored a retention-truncated first calendar candle: %+v", before)
		}
	})
}

func TestChartCalendarQuoteOmitsFXTruncatedFirstBucket(t *testing.T) {
	for _, interval := range []string{"1w", "1M"} {
		t.Run(interval, func(t *testing.T) {
			c, f := feedClient(t)
			last := "2024-02-01"
			if interval == "1w" {
				last = "2024-01-08"
			}
			local := yahootest.Daily("D05.SI", "SGD", 100, 20, 30)
			fx := yahootest.Daily("SGDUSD=X", "USD", .5, 1)
			for i, date := range []string{"2024-01-01", "2024-01-03", last} {
				at, _ := time.Parse("2006-01-02", date)
				local.Bars[i].T = at.Add(9 * time.Hour)
				if i > 0 {
					fx.Bars[i-1].T = at
				}
			}
			f.Set(local)
			f.Set(fx)
			mux := mount(c)
			path := "/api/market/candles?symbol=DBS&limit=10&interval=" + interval
			native := readHistoryCandles(t, mux, path+"&currency=original")
			usd := readHistoryCandles(t, mux, path)
			at, _ := time.Parse("2006-01-02", last)
			if len(native) != 2 || native[0].O != 100 || native[0].V != 2000 {
				t.Fatalf("native first bucket %+v", native)
			}
			if len(usd) != 1 || usd[0].T != at.Unix() || usd[0].O != 30 || usd[0].C != 30 || usd[0].V != 1000 {
				t.Fatalf("FX prefix fabricated an incomplete calendar candle: %+v", usd)
			}
			daily := readHistoryCandles(t, mux, "/api/market/candles?symbol=DBS&limit=10&interval=1d")
			if len(daily) != 2 || daily[0].C != 10 {
				t.Fatalf("daily FX prefix contract changed: %+v", daily)
			}
		})
	}
}
