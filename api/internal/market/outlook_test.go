package market

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

// dailySeries is n daily bars from 2016-01-01 whose log price grows g a year
// under a ±5% three-day wobble, so σ is non-trivial and the drift is known.
func dailySeries(n int, g float64) []Candle {
	start := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	out := make([]Candle, n)
	for i := range out {
		c := 100 * math.Exp(g*float64(i)/365+0.05*math.Sin(float64(i)/3))
		out[i] = Candle{T: start + int64(i)*86400, O: c, H: c, L: c, C: c}
	}
	return out
}

// Closed forms (Φ from tables): zero drift, σ 100%, 1 year, x = e is z = 1, so
// pEnd = 1 − Φ(1) and pTouch twice that. With μ 50%/yr the up barrier is
// Φ(−0.5) + e·Φ(−1.5) and the down one (x = 1/e) Φ(−1.5) + e⁻¹·Φ(−0.5).
// A 10x over 10 years at σ 90% is z = ln 10/(0.9·√10) ≈ 0.809.
func TestPassageClosedForm(t *testing.T) {
	for _, c := range []struct {
		name               string
		x, mu, vol, years  float64
		wantEnd, wantTouch float64
	}{
		{"e, zero drift", math.E, 0, 1, 1, 0.1586553, 0.3173105},
		{"e, drift 50%", math.E, 0.5, 1, 1, 0.3085375, 0.4901383},
		{"1/e, drift 50%", 1 / math.E, 0.5, 1, 1, 0.0668072, 0.1803118},
		{"1/e, drift -50%", 1 / math.E, -0.5, 1, 1, 0.3085375, 0.4901383}, // the mirror of "e, drift 50%"
		{"10x in 10y at 90%", 10, 0, 0.9, 10, 0.2092444, 0.4184887},
	} {
		pEnd, pTouch := passage(c.x, c.mu, c.vol, c.years)
		if math.Abs(pEnd-c.wantEnd) > 1e-6 || math.Abs(pTouch-c.wantTouch) > 1e-6 {
			t.Errorf("%s: pEnd %.7f pTouch %.7f, want %.7f %.7f", c.name, pEnd, pTouch, c.wantEnd, c.wantTouch)
		}
	}
	// Zero drift is exactly the forecast desk's ProbAbove/ProbTouch model.
	const T = 30.0 / 365
	if pEnd, pTouch := passage(1.1, 0, 0.5, T); !near(pEnd, ProbAbove(100, 110, 0.5, T)) || !near(pTouch, ProbTouch(100, 110, 0.5, T, true)) {
		t.Errorf("up: %v %v vs ProbAbove/ProbTouch", pEnd, pTouch)
	}
	if pEnd, pTouch := passage(0.9, 0, 0.5, T); !near(pEnd, 1-ProbAbove(100, 90, 0.5, T)) || !near(pTouch, ProbTouch(100, 90, 0.5, T, false)) {
		t.Errorf("down: %v %v vs ProbAbove/ProbTouch", pEnd, pTouch)
	}
	// A near-flat series with a strong drift overflows e^(2νb/σ²): the Mills
	// series takes over, matching the direct form where both still fit a float.
	if got, want := expNormCDF(700, -37.42), math.Exp(700)*normCDF(-37.42); math.Abs(got/want-1) > 1e-7 {
		t.Errorf("expNormCDF Mills branch %v, direct %v", got, want)
	}
	for _, vol := range []float64{0, 0.005, 0.02, 0.03} {
		for _, years := range []float64{1, 9.2, 10} {
			pEnd, pTouch := passage(10, 0.25, vol, years)
			if !finite(pTouch) || pTouch < pEnd || pTouch > 1 || pEnd < 0 {
				t.Errorf("vol %v, %v years: pEnd %v pTouch %v", vol, years, pEnd, pTouch)
			}
		}
	}
}

func TestLongHorizon(t *testing.T) {
	bars := dailySeries(4*365+1, 0.4) // 4 years
	spot := bars[len(bars)-1].C

	// Up to 365 days nothing changes: σ from the last 90 closes, no long view.
	o := OutlookCandles("BTC", bars, 365)
	if o.LongHorizon != nil || *o.VolBasis != (VolBasis{Window: "90d", Days: 90, Since: time.Unix(bars[len(bars)-90].T, 0).UTC().Format(time.DateOnly)}) {
		t.Fatalf("365 days: %+v %+v", o.VolBasis, o.LongHorizon)
	}
	if want := OutlookFrom("BTC", spot, RealizedVolAnn(Closes(bars[len(bars)-90:])), 365); o.VolAnnPct != want.VolAnnPct || o.P95 != want.P95 {
		t.Fatalf("365 days must be OutlookFrom on 90 closes: %v vs %v", o.VolAnnPct, want.VolAnnPct)
	}
	if b, _ := json.Marshal(o); strings.Contains(string(b), "longHorizon") || !strings.Contains(string(b), `"volBasis":{"window":"90d","days":90,"since":"`) {
		t.Fatalf("365-day JSON %s", b)
	}

	// Past 365 days σ is the long-run vol of every bar.
	o = OutlookCandles("BTC", bars, 366)
	vol := RealizedVolAnn(Closes(bars))
	if lr := o.VolBasis; lr.Window != "long-run" || lr.Days != len(bars) || lr.Since != "2016-01-01" || o.VolAnnPct != vol*100 || o.P50 != spot {
		t.Fatalf("366 days: %+v vol %v want %v", lr, o.VolAnnPct, vol*100)
	}
	if lh := o.LongHorizon; lh == nil || len(lh.TenX) != 1 || lh.TenX[0].Years != 1 {
		t.Fatalf("366 days: tenX must hold only the 1-year horizon: %+v", lh)
	}

	o = OutlookCandles("BTC", bars, 3650)
	lh, years := o.LongHorizon, 10.0
	if xs := []float64{lh.Multiples[0].X, lh.Multiples[1].X, lh.Multiples[2].X, lh.Multiples[3].X}; len(lh.Multiples) != 4 || !slices.Equal(xs, []float64{0.5, 2, 5, 10}) {
		t.Fatalf("multiples %+v", lh.Multiples)
	}
	for i, m := range lh.Multiples {
		if !(m.PEnd > 0 && m.PTouch >= m.PEnd && m.PTouch <= 1) {
			t.Errorf("multiple %v: pEnd %v pTouch %v", m.X, m.PEnd, m.PTouch)
		}
		if i > 1 && !(m.PEnd < lh.Multiples[i-1].PEnd && m.PTouch < lh.Multiples[i-1].PTouch) {
			t.Errorf("odds must fall as the multiple rises: %+v", lh.Multiples)
		}
	}
	// 0.5x mirrors 2x at zero drift and agrees with the bucket model.
	half, two := lh.Multiples[0], lh.Multiples[1]
	if math.Abs(half.PEnd-two.PEnd) > 1e-12 || math.Abs(half.PTouch-two.PTouch) > 1e-12 ||
		!near(half.PEnd, 1-ProbAbove(spot, spot/2, vol, years)) || !near(half.PTouch, ProbTouch(spot, spot/2, vol, years, false)) {
		t.Errorf("0.5x %+v vs 2x %+v", half, two)
	}
	if ten := lh.Multiples[3]; !near(ten.PEnd, ProbAbove(spot, 10*spot, vol, years)) || !near(ten.PTouch, 2*ten.PEnd) {
		t.Errorf("10x %+v", ten)
	}
	if got := []int{lh.TenX[0].Years, lh.TenX[1].Years, lh.TenX[2].Years, lh.TenX[3].Years}; len(lh.TenX) != 4 || !slices.Equal(got, []int{1, 3, 5, 10}) {
		t.Fatalf("tenX %+v", lh.TenX)
	}
	for i := 1; i < 4; i++ {
		if lh.TenX[i].PTouch <= lh.TenX[i-1].PTouch {
			t.Errorf("a 10x gets likelier with time: %+v", lh.TenX)
		}
	}
	if lh.TenX[3].PTouch != lh.Multiples[3].PTouch {
		t.Errorf("tenX at 10 years %v vs the 10-year 10x %v", lh.TenX[3].PTouch, lh.Multiples[3].PTouch)
	}

	// The drift case: the 4-year log CAGR (≈ 40%/yr), halved.
	d := lh.DriftCase
	mu := math.Log(spot/bars[0].C) / 4 / 2
	if d == nil || d.Label != "historical drift, halved (assumption)" || !near(d.MuAnnPct, mu*100) || math.Abs(mu-0.2) > 0.01 ||
		!near(d.P50, spot*math.Exp(mu*years)) || len(d.Multiples) != 4 {
		t.Fatalf("drift case %+v, want μ %v", d, mu)
	}
	for i, m := range d.Multiples {
		if base := lh.Multiples[i]; m.PTouch < m.PEnd || (m.X > 1) != (m.PEnd > base.PEnd) {
			t.Errorf("a positive drift lifts the upside and cuts the downside: %+v vs %+v", m, base)
		}
	}
	b, _ := json.Marshal(o)
	for _, want := range []string{`"volBasis":{"window":"long-run","days":1461,"since":"2016-01-01"}`, `"longHorizon":{"multiples":[{"x":0.5,"pEnd":`,
		`"tenX":[{"years":1,"pTouch":`, `"driftCase":{"label":"historical drift, halved (assumption)","muAnnPct":`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("3650-day JSON lacks %s:\n%s", want, b)
		}
	}

	// Capped at ±25%/yr, and null under 2 years of history.
	for g, want := range map[float64]float64{1.2: 25, -1.2: -25} {
		if d := OutlookCandles("SOL", dailySeries(3*365, g), 3650).LongHorizon.DriftCase; d == nil || d.MuAnnPct != want {
			t.Errorf("drift %v: %+v, want μ %v%%", g, d, want)
		}
	}
	short := OutlookCandles("NVDA", dailySeries(2*365-1, 0.4), 3650)
	if short.LongHorizon.DriftCase != nil || short.VolBasis.Days != 2*365-1 {
		t.Errorf("under 2 years: %+v", short.LongHorizon.DriftCase)
	}
	if b, _ := json.Marshal(short); !strings.Contains(string(b), `"driftCase":null`) {
		t.Errorf("driftCase must be null under 2 years: %s", b)
	}
}

// A 10-year 10x on a high-vol asset is a real possibility under the model, not
// a rounding error; on a gold-like 15% it is not.
func TestLongHorizonHighVol(t *testing.T) {
	ten := func(vol float64) Multiple { return longHorizon(1, vol, 3650, 0, false).Multiples[3] }
	if m := ten(0.9); m.PEnd < 0.2 || m.PTouch < 0.4 {
		t.Errorf("σ 90%%: 10x in 10 years %+v", m)
	}
	if m := ten(0.15); m.PTouch > 0.001 {
		t.Errorf("σ 15%%: 10x in 10 years %+v", m)
	}
}

func TestLongOutlookClient(t *testing.T) {
	b := markettest.New(t)
	c := testMarketClient(b.URL)
	ctx := context.Background()
	for days, want := range map[int]int{0: 1, 90: 90, 365: 365, 366: 366, 3650: 3650, 7300: 7300, 9000: 7300} {
		o, err := c.LongOutlook(ctx, "btc", days)
		if err != nil || o.HorizonDays != want || o.Symbol != "BTC" || o.Spot != 100_000 {
			t.Fatalf("long outlook %d days: %+v %v", days, o, err)
		}
		if (o.LongHorizon != nil) != (want > 365) || (o.VolBasis.Window == "long-run") != (want > 365) {
			t.Errorf("%d days: window %q, long view %v", days, o.VolBasis.Window, o.LongHorizon != nil)
		}
	}
	// Up to 365 days it is Outlook, which keeps its 365-day clamp for the forecast desk.
	short, _ := c.LongOutlook(ctx, "BTC", 30)
	if o, err := c.Outlook(ctx, "BTC", 30); err != nil || o.VolAnnPct != short.VolAnnPct || o.P95 != short.P95 || *o.VolBasis != *short.VolBasis {
		t.Errorf("outlook %+v vs long outlook %+v", o.VolBasis, short.VolBasis)
	}
	if o, _ := c.Outlook(ctx, "BTC", 3650); o.HorizonDays != 365 || o.LongHorizon != nil {
		t.Errorf("Outlook must stay clamped to 365 days: %d", o.HorizonDays)
	}
	// Binance pages back to maxCandles daily bars; a perp serves its listing.
	o, _ := c.LongOutlook(ctx, "BTC", 3650)
	if want := markettest.End.AddDate(0, 0, -(maxCandles - 1)).Format(time.DateOnly); o.VolBasis.Days != maxCandles || o.VolBasis.Since != want || o.LongHorizon.DriftCase == nil {
		t.Errorf("BTC long run %+v, want since %s", o.VolBasis, want)
	}
	o, err := c.LongOutlook(ctx, "NVDA", 3650)
	if err != nil || o.VolBasis.Days != 320 || o.LongHorizon == nil || o.LongHorizon.DriftCase != nil {
		t.Errorf("NVDA long run %+v %v", o.VolBasis, err)
	}
	if _, err := c.LongOutlook(ctx, "PEPE", 3650); err == nil {
		t.Error("unknown symbol")
	}
}
