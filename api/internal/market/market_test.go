package market

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// same compares series, treating NaN == NaN (warm-up).
func same(t *testing.T, name string, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len %d, want %d", name, len(got), len(want))
	}
	for i := range want {
		if math.IsNaN(want[i]) != math.IsNaN(got[i]) || (!math.IsNaN(want[i]) && !near(got[i], want[i])) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
}

func TestIndicators(t *testing.T) {
	N := math.NaN()
	same(t, "SMA", SMA([]float64{1, 2, 3, 4, 5}, 3), []float64{N, N, 2, 3, 4})
	same(t, "EMA", EMA([]float64{1, 2, 3, 4, 5}, 3), []float64{N, N, 2, 3, 4})
	same(t, "EMA leading NaN", EMA([]float64{N, 1, 2, 3}, 2), []float64{N, N, 1.5, 2.5})
	same(t, "RSI", RSI([]float64{1, 2, 3, 2, 3}, 2), []float64{N, N, 100, 50, 75})
	same(t, "RSI flat", RSI([]float64{5, 5, 5}, 2), []float64{N, N, 50})
	same(t, "ROC", ROC([]float64{100, 110, 121}, 1), []float64{N, 10, 10})
	same(t, "Highest", Highest([]float64{1, 5, 3, 4, 2}, 2), []float64{N, N, 5, 5, 4})
	same(t, "Lowest", Lowest([]float64{1, 5, 3, 4, 2}, 2), []float64{N, N, 1, 3, 3})

	candles := []Candle{{H: 10, L: 8, C: 9}, {H: 11, L: 9, C: 10}, {H: 14, L: 10, C: 12}, {H: 13, L: 11, C: 12}}
	same(t, "ATR", ATR(candles, 2), []float64{N, N, 3, 2.5}) // TR = 2, 4, 2; Wilder from index n

	up, mid, lo := Bollinger([]float64{1, 2, 3}, 3, 2)
	sd := math.Sqrt(2.0 / 3)
	same(t, "BB", append(append(up, mid...), lo...), []float64{N, N, 2 + 2*sd, N, N, 2, N, N, 2 - 2*sd})

	flat := []float64{7, 7, 7, 7, 7, 7, 7}
	macd, sig := MACD(flat, 2, 3, 2)
	same(t, "MACD", macd, []float64{N, N, 0, 0, 0, 0, 0})
	same(t, "MACD signal", sig, []float64{N, N, N, 0, 0, 0, 0})
}

func TestRealizedVolAnn(t *testing.T) {
	// Two returns: their sample stdev is |r1 − r2| / √2.
	want := math.Abs(math.Log(1.1)-math.Log(0.9)) / math.Sqrt2 * math.Sqrt(365)
	if got := RealizedVolAnn([]float64{100, 110, 99}); !near(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if RealizedVolAnn([]float64{1, 2}) != 0 {
		t.Fatal("too few closes must give 0")
	}
}

func TestAnalyzeTrend(t *testing.T) {
	bars := make([]Candle, 250)
	daily := make([]float64, 40)
	for i := range bars {
		p := 100 + float64(i)
		bars[i] = Candle{T: int64(i), O: p, H: p + 1, L: p - 1, C: p}
	}
	for i := range daily {
		daily[i] = 100 + float64(i)
	}
	a := AnalyzeCandles("BTC", "4h", bars, daily)
	if a.Trend != "uptrend" || a.Sma200 == nil || a.Price != 349 {
		t.Fatalf("rising series: %+v", a)
	}
	if !near(a.Support, 299) || !near(a.Resistance, 350) { // last 50 bars: lows 299.., highs ..350
		t.Fatalf("support/resistance = %v/%v", a.Support, a.Resistance)
	}
	if !near(a.Return7dPct, (139.0/132-1)*100) || a.Rsi14 != 100 || !near(a.Atr14, 2) {
		t.Fatalf("returns/rsi/atr: %+v", a)
	}
	if a := AnalyzeCandles("BTC", "1h", bars[:120], daily); a.Sma200 != nil || a.Trend != "uptrend" {
		t.Fatalf("short history must fall back to the Sma20/Sma50 stack: %+v", a)
	}
	for i := range bars {
		bars[i].C = 400 - float64(i)
	}
	if a := AnalyzeCandles("BTC", "4h", bars, daily); a.Trend != "downtrend" {
		t.Fatalf("falling series: %s", a.Trend)
	}
}

func TestOutlookFrom(t *testing.T) {
	o := OutlookFrom("BTC", 100_000, 0.5, 30)
	if len(o.Buckets) != 7 || o.Buckets[0].Lo != nil || o.Buckets[6].Hi != nil {
		t.Fatalf("want 7 buckets with open tails: %+v", o.Buckets)
	}
	sum := 0.0
	for i, b := range o.Buckets {
		sum += b.P
		if b.P <= 0 {
			t.Fatalf("bucket %d has p %v", i, b.P)
		}
		if i > 0 && i < 6 && *b.Lo >= *b.Hi {
			t.Fatalf("bucket %d is not increasing", i)
		}
	}
	if !near(sum, 1) || o.P50 != 100_000 || !(o.P5 < o.P50 && o.P50 < o.P95) {
		t.Fatalf("sum %v, quantiles %v %v %v", sum, o.P5, o.P50, o.P95)
	}
	// σ√T = 0.5·√(30/365) ≈ 0.14335: edges 100k·e^(±1.5·0.14335) ≈ 80.65k / 124.0k.
	if o.Buckets[0].Label != "< $80.7k" || o.Buckets[6].Label != "≥ $124k" || o.Buckets[1].Label != "$80.7k – $89.8k" {
		t.Fatalf("labels: %q … %q", o.Buckets[0].Label, o.Buckets[6].Label)
	}
	if !near(o.Buckets[3].P, 0.5*math.Erfc(-math.Log(*o.Buckets[3].Hi/1e5)/0.5/math.Sqrt(30.0/365)/math.Sqrt2)-
		0.5*math.Erfc(-math.Log(*o.Buckets[3].Lo/1e5)/0.5/math.Sqrt(30.0/365)/math.Sqrt2)) {
		t.Fatalf("middle bucket p = %v", o.Buckets[3].P)
	}

	// A tight distribution needs a 4th figure to keep edges distinct.
	tight := OutlookFrom("PAXG", 4050, 0.001, 1)
	for i := 1; i < 6; i++ {
		if *tight.Buckets[i].Lo >= *tight.Buckets[i].Hi {
			t.Fatalf("tight bucket %d collapsed: %s", i, tight.Buckets[i].Label)
		}
	}
}

// Hand values for spot 100, σ 50%, 30 days (σ√T ≈ 0.143346): strike 110 is
// z ≈ 0.664898, so P(above) ≈ 0.253058 and P(touch) twice that; strike 90 is
// z ≈ −0.735011, P(below) ≈ 0.231166, P(touch from above) ≈ 0.462333. A
// barrier already reached is certain; P(above) agrees with OutlookFrom.
func TestProbAboveAndTouch(t *testing.T) {
	const T = 30.0 / 365
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"above 110", ProbAbove(100, 110, 0.5, T), 0.2530578},
		{"below 90", 1 - ProbAbove(100, 90, 0.5, T), 0.2311665},
		{"at the money", ProbAbove(100, 100, 0.5, T), 0.5},
		{"touch 110", ProbTouch(100, 110, 0.5, T, true), 0.5061157},
		{"touch 90", ProbTouch(100, 90, 0.5, T, false), 0.4623330},
		{"reached 110", ProbTouch(112, 110, 0.5, T, true), 1},
		{"dipped to 90", ProbTouch(90, 90, 0.5, T, false), 1},
		{"flat floor", ProbAbove(100, 101, 0, T), 0.5 * math.Erfc(math.Log(1.01)/(0.01*math.Sqrt(T))/math.Sqrt2)},
	} {
		if math.Abs(c.got-c.want) > 1e-6 {
			t.Errorf("%s: %.7f, want %.7f", c.name, c.got, c.want)
		}
	}
	o := OutlookFrom("BTC", 100, 0.5, 30)
	if top := o.Buckets[6]; math.Abs(top.P-ProbAbove(100, *top.Lo, 0.5, T)) > 1e-9 {
		t.Fatalf("outlook top bucket %v vs ProbAbove %v", top.P, ProbAbove(100, *top.Lo, 0.5, T))
	}
}

func TestUSD(t *testing.T) {
	for _, tc := range []struct {
		x    float64
		want string
	}{{84200, "$84.2k"}, {88000, "$88.0k"}, {105000, "$105k"}, {1_230_000, "$1.23M"}, {0.123, "$0.123"}, {3.45, "$3.45"}, {1000, "$1.00k"}} {
		if got := usd(tc.x, 3); got != tc.want {
			t.Errorf("usd(%v) = %s, want %s", tc.x, got, tc.want)
		}
	}
}

// TestCandlesDepth: any limit inside one 1000-bar page shares a single cached
// fetch (callers cannot fan out cache keys) and still gets the newest bars.
func TestCandlesDepth(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows := make([][]any, n)
		for i := range rows {
			p := strconv.Itoa(i)
			rows[i] = []any{i * 1000, p, p, p, p, "1"}
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	c := testMarketClient(srv.URL)
	for _, limit := range []int{300, 301, 1, 1000} {
		got, err := c.Candles(context.Background(), "BTC", "1h", limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != limit || got[len(got)-1].C != 999 {
			t.Fatalf("limit %d: %d bars ending %+v", limit, len(got), got[len(got)-1])
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d klines calls, want 1", n)
	}
}

// TestCandlesBefore pages back from end over endTime: the newest bar opens
// before end (one opening exactly at end is excluded), and nothing is cached.
func TestCandlesBefore(t *testing.T) {
	b := markettest.New(t)
	c, ctx := testMarketClient(b.URL), context.Background()
	end := markettest.End.AddDate(0, 0, -30) // a 4h bar opens exactly here
	bars, err := c.CandlesBefore(ctx, "eth", "4h", end, 2500)
	if err != nil || len(bars) != 2500 {
		t.Fatalf("%v, %d bars", err, len(bars))
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].T-bars[i-1].T != 4*3600 {
			t.Fatalf("bars %d/%d not contiguous", i-1, i)
		}
	}
	// k bars before the fake's newest: 30 days back is k = 180, so the last bar is k = 181.
	if last := bars[len(bars)-1]; last.T != end.Unix()-4*3600 || last.C != markettest.Close("ETHUSDT", 181) {
		t.Fatalf("last bar %+v, want the one opening 4h before end", last)
	}

	if bars, err := c.CandlesBefore(ctx, "BTC", "1d", end.Add(time.Hour), 0); err != nil || len(bars) != 1 || bars[0].T != end.Unix() {
		t.Fatalf("limit 0 → 1 bar, the one open at end+1h: %v %+v", err, bars)
	}
	// The fake's 4h history starts 5,000 bars back; Binance answers [] before a pair listed.
	if bars, err := c.CandlesBefore(ctx, "ETH", "4h", markettest.End.AddDate(-3, 0, 0), 10); err != nil || len(bars) != 0 {
		t.Fatalf("before the pair's history: %v %+v", err, bars)
	}
	b.Down("ETHUSDT", true)
	if _, err := c.CandlesBefore(ctx, "ETH", "4h", end, 10); !errors.Is(err, ErrUpstream) {
		t.Fatalf("history must not be served from a cache: %v", err)
	}
	for _, bad := range []struct {
		sym, iv string
		end     time.Time
	}{{"SHIB", "1d", end}, {"BTC", "5m", end}, {"BTC", "1d", time.Time{}}} {
		if _, err := c.CandlesBefore(ctx, bad.sym, bad.iv, bad.end, 10); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestUniverse(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Universe {
		if seen[a.Symbol] || a.Pair != a.Symbol+"USDT" && a.Pair != "xyz:"+a.Symbol && !(a.QuoteFeed() && a.Pair == a.Listing.Yahoo) || a.Name == "" {
			t.Errorf("bad or duplicate asset %+v", a)
		}
		seen[a.Symbol] = true
	}
	var core []string
	for _, a := range Core {
		core = append(core, a.Symbol)
	}
	if len(Universe) != 107+len(QuoteFeeds) || !slices.Equal(core, []string{"BTC", "ETH", "SOL", "BNB", "XRP", "DOGE", "ADA", "AVAX", "LINK", "SUI", "TON", "PAXG"}) {
		t.Errorf("universe %d, core %v", len(Universe), core)
	}
	if l := SymbolList(); !strings.HasPrefix(l, "BTC, ETH, SOL,") || !strings.Contains(l, ", PAXG, TRX, DOT,") || !strings.Contains(l, ", META, SP500, XYZ100, TSM, BABA, SMSN, SKHX, HYUNDAI, SOFTBANK, MINIMAX, ZHIPU, EWJ, EWY, PLTR, COIN, HOOD, MSTR, INTC, AMD, ORCL, NFLX, CRCL, MU, SNDK, CRWV, SKHY, CXMT, UNITREE, GIGADEV, KIOXIA, EWT, KSTR, HSDT, ") ||
		!strings.Contains(l, ", MEDIATEK, HONHAI, DELTA, KEPPEL, ") || !strings.HasSuffix(l, ", KEP, SKM, KT, LPL") {
		t.Errorf("symbol list %q", l)
	}
}

// A non-core pair Binance stops quoting drops out of Assets (its price is a
// market outage, not an internal error); a core one still fails it.
func TestAssetsDropsDownPair(t *testing.T) {
	ctx := context.Background()
	b := markettest.New(t)
	b.Down("AAVEUSDT", true)
	c := testMarketClient(b.URL)
	assets, err := c.Assets(ctx)
	if err != nil || len(assets) != 106 || slices.ContainsFunc(assets, func(a Asset) bool { return a.Symbol == "AAVE" }) || assets[11].Symbol != "PAXG" {
		t.Fatalf("down non-core pair: %d assets, %v", len(assets), err)
	}
	if _, err := c.Price(ctx, "aave"); !errors.Is(err, ErrUpstream) {
		t.Fatalf("price of a dropped pair: %v", err)
	}
	if _, err := c.Quote(ctx, "PEPE"); err == nil || errors.Is(err, ErrUpstream) {
		t.Fatalf("an unknown symbol is the caller's mistake: %v", err)
	}
	b.SetPrice("ARBUSDT", 0)
	if assets, err = testMarketClient(b.URL).Assets(ctx); err != nil || len(assets) != 105 {
		t.Fatalf("unpriced non-core pair: %d assets, %v", len(assets), err)
	}
	b.Down("SOLUSDT", true)
	if _, err := testMarketClient(b.URL).Assets(ctx); !errors.Is(err, ErrUpstream) {
		t.Fatalf("down core pair: %v", err)
	}
}
