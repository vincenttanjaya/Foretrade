package market

import (
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// tsmChart is a synthetic NYSE TSM listing: years of weekday closes from a
// seeded GBM at σ (252 sessions a year) with a 10%/yr log drift, ending at
// about $487.
func tsmChart(years int, sigma float64, seed uint64) yahoo.Chart {
	r := rand.New(rand.NewPCG(seed, 2))
	n := years * 261
	logs := make([]float64, n)
	for i := 1; i < n; i++ {
		logs[i] = logs[i-1] + 0.10/252 + sigma/math.Sqrt(252)*r.NormFloat64()
	}
	closes := make([]float64, n)
	for i, x := range logs {
		closes[i] = math.Round(487.10*math.Exp(x-logs[n-1])*100) / 100
	}
	return yahootest.Daily("TSM", "USD", closes...)
}

func TestDailyHistoryRouting(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	f := yahootest.New()
	c.Yahoo = f
	ctx := t.Context()

	tsm := tsmChart(29, 0.38, 1)
	f.Set(tsm)
	s, err := c.DailyHistory(ctx, "tsm")
	if err != nil || s.Symbol != "TSM" || s.Ticker != "TSM" || s.Source != "Yahoo Finance daily closes (NYSE: TSM)" || s.PerYear != 252 ||
		len(s.Closes) != len(tsm.Bars) || s.Currency != "USD" || s.Years() < 28.9 || b.Requests("candle:xyz:TSM") != 0 {
		t.Fatalf("TSM: %+v %v (perp candles read %d times)", s.Source, err, b.Requests("candle:xyz:TSM"))
	}
	if last := s.Closes[len(s.Closes)-1]; last.C != 487.10 || last.T.Hour() != 0 || last.T.Format("2006-01-02") != "2026-10-05" {
		t.Errorf("last close %+v", last)
	}

	// A quote-feed stock reads its Pair; DailyHistoryUSD converts a non-USD listing.
	dbs := yahootest.Daily("D05.SI", "SGD", 40, 41, 42)
	dbs.Meta.Timezone = "Asia/Singapore"
	f.Set(dbs)
	f.SetFX("SGD", 0.78)
	if s, err := c.DailyHistory(ctx, "DBS"); err != nil || s.Ticker != "D05.SI" || s.Currency != "SGD" || s.Closes[2].C != 42 {
		t.Errorf("DBS: %+v %v", s, err)
	}
	if s, err := c.DailyHistoryUSD(ctx, "DBS"); err != nil || s.Currency != "USD" || math.Abs(s.Closes[len(s.Closes)-1].C-42*0.78) > 1e-9 {
		t.Errorf("DBS in USD: %+v %v", s, err)
	}
	smsn := yahootest.Daily("005930.KS", "KRW", 80000, 81000)
	f.Set(smsn)
	f.SetFX("KRW", 0.00072)
	if s, err := c.DailyHistoryUSD(ctx, "SMSN"); err != nil || s.Ticker != "005930.KS" || math.Abs(s.Closes[1].C-81000*0.00072) > 1e-9 {
		t.Errorf("SMSN in USD: %+v %v", s, err)
	}

	// Crypto reads Binance's daily bars; a perp with no listing its own venue bars.
	if s, err := c.DailyHistory(ctx, "BTC"); err != nil || s.Ticker != "BTCUSDT" || s.PerYear != 365 || len(s.Closes) != maxCandles ||
		s.Source != "Binance daily closes (BTC/USDT)" || s.Closes[len(s.Closes)-1].C != markettest.Price("BTCUSDT") {
		t.Errorf("BTC: %+v %v", s.Source, err)
	}
	if s, err := c.DailyHistory(ctx, "XYZ100"); err != nil || s.PerYear != 365 || !strings.HasPrefix(s.Source, "Hyperliquid perp daily closes (xyz:XYZ100), venue history since ") {
		t.Errorf("XYZ100: %q %v", s.Source, err)
	}

	// Benchmarks: the allowlist only; UST10Y is a level.
	f.Set(yahootest.Daily("^SOX", "USD", 5000, 5100))
	f.Set(yahootest.Daily("^TNX", "USD", 4.1, 4.2))
	if s, err := c.TickerHistory(ctx, "sox"); err != nil || s.Symbol != "SOX" || s.Ticker != "^SOX" || s.Kind != "price" {
		t.Errorf("SOX: %+v %v", s, err)
	}
	if s, err := c.TickerHistory(ctx, "UST10Y"); err != nil || s.Kind != "level" {
		t.Errorf("UST10Y: %+v %v", s, err)
	}
	if _, err := c.TickerHistory(ctx, "GME"); err == nil {
		t.Error("a ticker off the allowlist must fail")
	}

	// Yahoo failures pass through; nothing is fabricated.
	f.SetErr("TSM", yahootest.Down)
	if _, err := c.DailyHistory(ctx, "TSM"); !errors.Is(err, yahoo.ErrUnavailable) {
		t.Errorf("Yahoo 503: %v", err)
	}
	c.Yahoo = nil
	if _, err := c.DailyHistory(ctx, "NVDA"); !errors.Is(err, yahoo.ErrUnavailable) {
		t.Errorf("no Yahoo: %v", err)
	}
	if _, err := c.DailyHistory(ctx, "PEPE"); err == nil {
		t.Error("unknown symbol")
	}
}

// Past a year a listed stock perp's outlook takes σ and the drift case from
// its listing's decades of Yahoo closes; spot stays the perp's mark.
func TestOutlookLongRunFromListing(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	f := yahootest.New()
	c.Yahoo = f
	ctx := t.Context()
	f.Set(tsmChart(29, 0.38, 1))
	series, _ := c.DailyHistory(ctx, "TSM")

	o, err := c.LongOutlook(ctx, "TSM", 1825)
	vb := o.VolBasis
	if err != nil || vb.Window != "long-run" || vb.Days != len(series.Closes) || vb.Since != day(series.Closes[0].T) ||
		vb.Source != "Yahoo Finance daily closes (NYSE: TSM)" || math.Abs(o.VolAnnPct-series.VolAnn()*100) > 1e-9 || o.Spot != 485 || o.P50 != 485 {
		t.Fatalf("TSM 5y: %+v vol %v spot %v err %v", vb, o.VolAnnPct, o.Spot, err)
	}
	if math.Abs(o.VolAnnPct-38) > 2 {
		t.Errorf("listing vol %v, want about 38", o.VolAnnPct)
	}
	mu, _ := HalvedDrift(series)
	if d := o.LongHorizon.DriftCase; d == nil || math.Abs(d.MuAnnPct-mu*100) > 1e-9 {
		t.Errorf("drift case %+v, want μ %v", d, mu)
	}
	if got := len(o.LongHorizon.TenX); got != 3 { // 1, 3 and 5 years within the horizon
		t.Errorf("tenX %+v", o.LongHorizon.TenX)
	}
	if o, _ := c.LongOutlook(ctx, "TSM", 7300); len(o.LongHorizon.TenX) != 6 || o.HorizonDays != 7300 {
		t.Errorf("20 years: tenX %+v", o.LongHorizon.TenX)
	}

	// Up to 365 days nothing changes: 90 venue closes.
	if o, err := c.LongOutlook(ctx, "TSM", 365); err != nil || o.VolBasis.Window != "90d" || o.VolBasis.Source != "" || o.LongHorizon != nil {
		t.Errorf("TSM 1y: %+v %v", o.VolBasis, err)
	}
	// The listing unavailable: the venue's own bars, as before.
	f.SetErr("TSM", yahootest.Down)
	if o, err := c.LongOutlook(ctx, "TSM", 1825); err != nil || o.VolBasis.Days != 320 || o.VolBasis.Source != "" || o.LongHorizon.DriftCase != nil {
		t.Errorf("TSM without Yahoo: %+v %v", o.VolBasis, err)
	}
}

// A non-USD listing's long outlook reads its USD series (the spot is USD); an
// FX failure falls back to the listing's own currency, never the venue's bars.
func TestLongOutlookUSDListing(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	f := yahootest.New()
	c.Yahoo = f
	ctx := t.Context()
	krw := tsmChart(12, 0.3, 3)
	krw.Meta.Symbol, krw.Meta.Currency = "005930.KS", "KRW"
	f.Set(krw)
	fx := tsmChart(12, 0.1, 4)
	fx.Meta.Symbol = "KRWUSD=X"
	f.Set(fx)
	usd, err := c.DailyHistoryUSD(ctx, "SMSN")
	if err != nil || !usd.Converted() || usd.Source != "Yahoo Finance daily closes (NYSE: 005930.KS), in USD at Yahoo daily FX" {
		t.Fatalf("SMSN in USD: %q %v", usd.Source, err)
	}
	o, err := c.LongOutlook(ctx, "SMSN", 1825)
	if err != nil || o.VolBasis.Source != usd.Source || math.Abs(o.VolAnnPct-usd.VolAnn()*100) > 1e-9 {
		t.Errorf("SMSN 5y: %+v %v %v", o.VolBasis, o.VolAnnPct, err)
	}
	mu, _ := HalvedDrift(usd)
	if d := o.LongHorizon.DriftCase; d == nil || math.Abs(d.MuAnnPct-mu*100) > 1e-9 {
		t.Errorf("drift case %+v, want μ %v", d, mu)
	}
	// TickerHistoryUSD converts a benchmark the same way; TickerHistory is unchanged.
	ks := tsmChart(12, 0.2, 5)
	ks.Meta.Symbol, ks.Meta.Currency = "^KS11", "KRW"
	f.Set(ks)
	if s, err := c.TickerHistoryUSD(ctx, "KOSPI"); err != nil || s.Currency != "USD" || !s.Converted() {
		t.Errorf("KOSPI in USD: %+v %v", s.Source, err)
	}
	if s, err := c.TickerHistory(ctx, "KOSPI"); err != nil || s.Currency != "KRW" || s.Converted() {
		t.Errorf("KOSPI: %+v %v", s.Source, err)
	}

	f.SetErr("KRWUSD=X", yahootest.Down)
	if _, err := c.DailyHistoryUSD(ctx, "SMSN"); !errors.Is(err, ErrFXHistory) || !errors.Is(err, yahoo.ErrUnavailable) {
		t.Errorf("FX down: %v", err)
	}
	local, _ := c.DailyHistory(ctx, "SMSN")
	if o, err := c.LongOutlook(ctx, "SMSN", 1825); err != nil || o.VolBasis.Source != local.Source || o.VolBasis.Days != len(local.Closes) {
		t.Errorf("SMSN 5y without FX: %+v %v", o.VolBasis, err)
	}
	if _, err := c.TickerHistoryUSD(ctx, "KOSPI"); !errors.Is(err, ErrFXHistory) {
		t.Errorf("KOSPI without FX: %v", err)
	}
}
