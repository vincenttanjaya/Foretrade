package market

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"
)

// dailyBars is fetched for every analysis and outlook up to 365 days so they
// share one cache key.
const dailyBars = 300

type Analysis struct {
	Symbol            string   `json:"symbol"`
	Interval          string   `json:"interval"`
	Price             float64  `json:"price"`
	Return1dPct       float64  `json:"return1dPct"`
	Return7dPct       float64  `json:"return7dPct"`
	Return30dPct      float64  `json:"return30dPct"`
	RealizedVolAnnPct float64  `json:"realizedVolAnnPct"`
	Rsi14             float64  `json:"rsi14"`
	Sma20             float64  `json:"sma20"`
	Sma50             float64  `json:"sma50"`
	Sma200            *float64 `json:"sma200"`
	Ema20             float64  `json:"ema20"`
	Atr14             float64  `json:"atr14"`
	Support           float64  `json:"support"`
	Resistance        float64  `json:"resistance"`
	Trend             string   `json:"trend"`
}

// RealizedVolAnn is the sample stdev of daily log returns, annualized with
// √365 (crypto trades every day). Returned as a fraction: 0.55 = 55%.
func RealizedVolAnn(dailyCloses []float64) float64 {
	if len(dailyCloses) < 3 {
		return 0
	}
	r := make([]float64, 0, len(dailyCloses)-1)
	for i := 1; i < len(dailyCloses); i++ {
		r = append(r, math.Log(dailyCloses[i]/dailyCloses[i-1]))
	}
	mean := 0.0
	for _, x := range r {
		mean += x
	}
	mean /= float64(len(r))
	ss := 0.0
	for _, x := range r {
		ss += (x - mean) * (x - mean)
	}
	return math.Sqrt(ss/float64(len(r)-1)) * math.Sqrt(365)
}

// Analyze computes the contract Analysis. Returns and volatility come from
// daily closes (vol from the last 90); everything else from the interval.
func (c *Client) Analyze(ctx context.Context, symbol, interval string) (Analysis, error) {
	info, ok := Lookup(symbol)
	if !ok || !ValidInterval(interval) {
		return Analysis{}, fmt.Errorf("unknown symbol or interval %q/%q", symbol, interval)
	}
	bars, err := c.Candles(ctx, info.Symbol, interval, dailyBars)
	if err != nil {
		return Analysis{}, err
	}
	daily := bars
	if interval != "1d" {
		if daily, err = c.Candles(ctx, info.Symbol, "1d", dailyBars); err != nil {
			return Analysis{}, err
		}
	}
	closes := Closes(daily)
	if info.QuoteFeed() {
		closes = CalendarCloses(daily) // 7d and 30d are calendar days, as for the 24/7 rows
	}
	return AnalyzeCandles(info.Symbol, interval, bars, closes), nil
}

// AnalyzeCandles is Analyze over given data, pure: bars are the interval's
// candles (oldest first, at least one; the last close is the price) and daily
// the daily closes that returns and volatility come from. Pass only bars that
// have closed by the decision time to analyze the past without look-ahead.
func AnalyzeCandles(symbol, interval string, bars []Candle, daily []float64) Analysis {
	closes := Closes(bars)
	price := closes[len(closes)-1]
	a := Analysis{
		Symbol: symbol, Interval: interval, Price: price,
		Return1dPct:       pctBack(daily, 1),
		Return7dPct:       pctBack(daily, 7),
		Return30dPct:      pctBack(daily, 30),
		RealizedVolAnnPct: RealizedVolAnn(tail(daily, 90)) * 100,
		Rsi14:             last(RSI(closes, 14)),
		Sma20:             last(SMA(closes, 20)),
		Sma50:             last(SMA(closes, 50)),
		Ema20:             last(EMA(closes, 20)),
		Atr14:             last(ATR(bars, 14)),
		Support:           math.Inf(1),
		Resistance:        math.Inf(-1),
	}
	if sma200 := SMA(closes, 200)[len(closes)-1]; !math.IsNaN(sma200) {
		a.Sma200 = &sma200
	}
	for _, k := range tail(bars, 50) {
		a.Support, a.Resistance = min(a.Support, k.L), max(a.Resistance, k.H)
	}
	a.Trend = trend(price, a.Sma20, a.Sma50, a.Sma200)
	return a
}

// trend: price above a rising stack of averages (price > Sma50 > Sma200) is an
// uptrend, the mirror image a downtrend, anything mixed a range. Without 200
// bars of history the stack is price / Sma20 / Sma50 instead.
func trend(price, sma20, sma50 float64, sma200 *float64) string {
	fast, slow := sma50, 0.0
	if sma200 != nil {
		slow = *sma200
	} else {
		fast, slow = sma20, sma50
	}
	switch {
	case price > fast && fast > slow:
		return "uptrend"
	case price < fast && fast < slow:
		return "downtrend"
	}
	return "range"
}

// pctBack is the percent change from n values ago to the last value.
func pctBack(v []float64, n int) float64 {
	if len(v) <= n || v[len(v)-1-n] == 0 {
		return 0
	}
	return (v[len(v)-1]/v[len(v)-1-n] - 1) * 100
}

func tail[T any](v []T, n int) []T { return v[max(len(v)-n, 0):] }

// last is the final value of a series, 0 if it is still warming up (JSON has no NaN).
func last(v []float64) float64 {
	if len(v) == 0 || math.IsNaN(v[len(v)-1]) {
		return 0
	}
	return v[len(v)-1]
}

type Bucket struct {
	Label string   `json:"label"`
	Lo    *float64 `json:"lo"`
	Hi    *float64 `json:"hi"`
	P     float64  `json:"p"`
}

type PriceOutlook struct {
	Symbol      string   `json:"symbol"`
	Spot        float64  `json:"spot"`
	HorizonDays int      `json:"horizonDays"`
	VolAnnPct   float64  `json:"volAnnPct"`
	Buckets     []Bucket `json:"buckets"`
	P5          float64  `json:"p5"`
	P50         float64  `json:"p50"`
	P95         float64  `json:"p95"`
	// VolBasis is the daily closes σ came from; nil from OutlookFrom alone.
	VolBasis *VolBasis `json:"volBasis,omitempty"`
	// LongHorizon is the multi-year view, only past 365 days.
	LongHorizon *LongHorizon `json:"longHorizon,omitempty"`

	// The fields above are the zero-drift, volatility-only baseline. The chat's
	// price_outlook card adds the view it leads with (none of these come from
	// LongOutlook itself; older cards lack them all).
	Horizon      *OutlookHorizon   `json:"horizon,omitempty"`
	History      *OutlookHistory   `json:"history,omitempty"`
	View         *OutlookView      `json:"view,omitempty"`
	Scenarios    []OutlookScenario `json:"scenarios,omitempty"`
	TimeToTarget *TimeToTarget     `json:"timeToTarget,omitempty"`
	Warnings     []string          `json:"warnings,omitempty"`
}

// OutlookHorizon is where the card's horizon came from: the user's own words
// ("explicit"), a by-when question ("by_when"; Days nil when the card has no
// at-horizon view of its own) or the model's choice with its reason ("chosen").
type OutlookHorizon struct {
	Days   *int   `json:"days"`
	Basis  string `json:"basis"` // explicit | by_when | chosen
	Reason string `json:"reason"`
}

// OutlookHistory is the card's history line, from ComputeHistoryStats.
type OutlookHistory struct {
	Source            string      `json:"source"`
	From              string      `json:"from"`
	To                string      `json:"to"`
	Years             float64     `json:"years"`
	VolAnnPct         *float64    `json:"volAnnPct"` // full history
	CAGRPct           HistoryCAGR `json:"cagrPct"`
	MaxDrawdownPct    *float64    `json:"maxDrawdownPct"`
	MaxDrawdownPeak   *string     `json:"maxDrawdownPeak"`
	MaxDrawdownTrough *string     `json:"maxDrawdownTrough"`
	Recovered         *string     `json:"recovered"`
	DriftHalvedPct    *float64    `json:"driftHalvedPct"` // HalvedDrift ×100 (annual log), uncapped; nil under 2 years
}

type HistoryCAGR struct {
	Y5  *float64 `json:"y5"`
	Y10 *float64 `json:"y10"`
	All *float64 `json:"all"`
}

// CompactHistory is h as the card's history line.
func CompactHistory(h HistoryStats) *OutlookHistory {
	o := &OutlookHistory{Source: h.Source, From: h.From, To: h.To, Years: math.Round(h.Years*10) / 10, VolAnnPct: h.VolAnnPct["all"],
		CAGRPct: HistoryCAGR{Y5: h.CAGRPct["y5"], Y10: h.CAGRPct["y10"], All: h.CAGRPct["all"]}, DriftHalvedPct: h.DriftHalvedPct}
	if d := h.MaxDrawdown; d != nil {
		o.MaxDrawdownPct, o.MaxDrawdownPeak, o.MaxDrawdownTrough, o.Recovered = &d.Pct, &d.Peak, &d.Trough, d.Recovered
	}
	return o
}

// OutlookView is the distribution the card leads with: Kind "scenarios" (the
// model's, subjective weights), "historical_drift" (the asset's own drift,
// halved: an assumption) or "none" (no direction: the baseline again).
type OutlookView struct {
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	MuAnnPct *float64 `json:"muAnnPct"` // historical_drift only: the drift the view uses (capped), annual log ×100
	Median   float64  `json:"median"`
	P5       float64  `json:"p5"`
	P25      float64  `json:"p25"`
	P75      float64  `json:"p75"`
	P95      float64  `json:"p95"`
	PAbove   float64  `json:"pAbove"` // P(price at the horizon ≥ today): the view's chance of a gain, spreads included
	// PAboveByWeights is a scenarios view's share of its weights on
	// scenarios at or above today: the sum the model would
	// otherwise write as its "P(gain)"; nil for other kinds.
	PAboveByWeights *float64 `json:"pAboveByWeights,omitempty"`
	ChangePct       float64  `json:"changePct"` // median vs spot
	AnnualPct       *float64 `json:"annualPct"` // horizons of a year or more
	// Buckets are cut at ViewLadderEdges (multiples of spot), with the baseline's
	// probability of the same range. Buckets and Multiples are at-horizon:
	// absent on a by-when card with no horizon.
	Buckets     []ViewBucket   `json:"buckets,omitempty"`
	Multiples   []ViewMultiple `json:"multiples,omitempty"` // 0.5, 2, 5, 10 (and the target)
	ListingSpot *float64       `json:"listingSpot"`
	VolAnnPct   float64        `json:"volAnnPct"` // the σ the view is spread by
	VolAssumed  bool           `json:"volAssumed"`
	// DriftCap is how a historical_drift view's return was capped (nil for
	// other kinds): at its market's long-run return + 2 points, at most 10%/yr.
	DriftCap *DriftCap `json:"driftCap,omitempty"`
}

type ViewBucket struct {
	Label     string   `json:"label"`
	Lo        *float64 `json:"lo"`
	Hi        *float64 `json:"hi"`
	P         float64  `json:"p"`
	PBaseline float64  `json:"pBaseline"`
}

// ViewMultiple is Multiple under the view and the baseline.
type ViewMultiple struct {
	X              float64 `json:"x"`
	PEnd           float64 `json:"pEnd"`
	PTouch         float64 `json:"pTouch"`
	BaselinePEnd   float64 `json:"baselinePEnd"`
	BaselinePTouch float64 `json:"baselinePTouch"`
}

// OutlookScenario is one validated scenario on the card.
type OutlookScenario struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	P         float64  `json:"p"`
	Level     float64  `json:"level"`
	ChangePct float64  `json:"changePct"`
	AnnualPct *float64 `json:"annualPct"`
	Inputs    []Input  `json:"inputs"`
}

// VolBasis: Window "90d" (the last 90 daily closes, horizons up to 365 days)
// or "long-run" (the longest daily history: a stock or index perp's
// underlying listing on Yahoo, else every daily close the client serves, up
// to maxCandles); Days is how many closes, Since the UTC date of the first,
// Source the series when it is not the venue's own candles.
type VolBasis struct {
	Window string `json:"window"`
	Days   int    `json:"days"`
	Since  string `json:"since"` // YYYY-MM-DD
	Source string `json:"source,omitempty"`
}

// LongHorizon is the odds of price multiples over a multi-year horizon. See
// longHorizon for the method.
type LongHorizon struct {
	Multiples []Multiple `json:"multiples"`
	TenX      []TenX     `json:"tenX"`
	DriftCase *DriftCase `json:"driftCase"` // null under 2 years of history
}

// Multiple is the odds of x·spot at the horizon. PEnd is P(S_T ≥ x·spot) for
// x > 1 and P(S_T ≤ x·spot) for x < 1; PTouch the chance the price trades at
// x·spot at any time before the horizon (first passage).
type Multiple struct {
	X      float64 `json:"x"`
	PEnd   float64 `json:"pEnd"`
	PTouch float64 `json:"pTouch"`
}

// TenX is the chance of touching 10x spot within Years, zero drift.
type TenX struct {
	Years  int     `json:"years"`
	PTouch float64 `json:"pTouch"`
}

// DriftCase is the long horizon again with a labelled drift assumption:
// MuAnnPct is the annual log drift in percent and P50 the median it implies.
type DriftCase struct {
	Label     string     `json:"label"`
	MuAnnPct  float64    `json:"muAnnPct"`
	P50       float64    `json:"p50"`
	Multiples []Multiple `json:"multiples"`
}

// MaxOutlookDays is LongOutlook's longest horizon: 20 years, the longest
// at-horizon and time-to-target view.
const MaxOutlookDays = 7300

// Outlook is the lognormal price distribution at the horizon (clamped to
// 1..365 days), with σ from the last 90 daily closes.
func (c *Client) Outlook(ctx context.Context, symbol string, horizonDays int) (PriceOutlook, error) {
	return c.outlook(ctx, symbol, min(max(horizonDays, 1), 365))
}

// LongOutlook is Outlook with horizons up to MaxOutlookDays: past 365 days σ
// is the long-run volatility of the longest daily history and the outlook
// adds LongHorizon. For a stock or index with a Yahoo listing that is the
// listing's full Yahoo daily history in USD (DailyHistoryUSD: decades for TSM,
// not the perp's year of venue bars; the listing's own currency when only the
// FX read fails), when it spans at least 2 years; otherwise every
// daily close the client serves (up to maxCandles, about 8.2 years: Binance
// pages back 1,000 bars a call; a Hyperliquid perp returns its whole listing).
// Spot stays the venue's last close either way: what the user trades.
func (c *Client) LongOutlook(ctx context.Context, symbol string, horizonDays int) (PriceOutlook, error) {
	return c.outlook(ctx, symbol, min(max(horizonDays, 1), MaxOutlookDays))
}

func (c *Client) outlook(ctx context.Context, symbol string, horizonDays int) (PriceOutlook, error) {
	info, ok := Lookup(symbol)
	if !ok {
		return PriceOutlook{}, fmt.Errorf("unknown symbol %q", symbol)
	}
	bars := dailyBars
	if horizonDays > 365 {
		bars = maxCandles
	}
	daily, err := c.Candles(ctx, info.Symbol, "1d", bars)
	if err != nil {
		return PriceOutlook{}, err
	}
	if horizonDays > 365 && info.Perp() && info.Listing != nil && (info.Listing.Yahoo != "" || info.QuoteFeed()) {
		s, err := c.DailyHistoryUSD(ctx, info.Symbol) // the spot is USD: so are σ and the drift case
		if errors.Is(err, ErrFXHistory) {
			s, err = c.DailyHistory(ctx, info.Symbol) // the listing's own currency, never the venue's year of bars
		}
		if err == nil && s.Years() >= 2 && s.VolAnn() > 0 {
			return OutlookSeries(info.Symbol, daily[len(daily)-1].C, s, horizonDays), nil
		} else if err != nil {
			slog.Warn("market: long outlook on venue history; listing history unavailable", "symbol", info.Symbol, "err", err)
		}
	}
	return outlookCandles(info.Symbol, daily, horizonDays, info.QuoteFeed()), nil
}

// OutlookSeries is the long-horizon outlook at spot with σ and the drift case
// from a daily series (a listing's Yahoo history), pure.
func OutlookSeries(symbol string, spot float64, s DailySeries, horizonDays int) PriceOutlook {
	vol := s.VolAnn()
	o := OutlookFrom(symbol, spot, vol, horizonDays)
	o.VolBasis = &VolBasis{Window: "long-run", Days: len(s.Closes), Since: day(s.Closes[0].T), Source: s.Source}
	mu, ok := HalvedDrift(s)
	o.LongHorizon = longHorizon(spot, vol, horizonDays, mu, ok)
	return o
}

// OutlookCandles is OutlookFrom over given daily bars (oldest first, at least
// one; the last close is spot), pure. Up to 365 days σ comes from the last 90
// closes; past that from all of them, and LongHorizon is filled.
func OutlookCandles(symbol string, daily []Candle, horizonDays int) PriceOutlook {
	return outlookCandles(symbol, daily, horizonDays, false)
}

// outlookCandles is OutlookCandles; calendar (a quote feed's trading-day
// bars) takes σ over calendar days instead, as Analyze does: the last 90
// calendar days of CalendarCloses (all of them past 365 days), each weekend
// and holiday carrying the last close, so √365 annualizes the same days
// Analyze's realized volatility does. VolBasis describes that window.
func outlookCandles(symbol string, daily []Candle, horizonDays int, calendar bool) PriceOutlook {
	window, basis := tail(daily, 90), "90d"
	if horizonDays > 365 {
		window, basis = daily, "long-run"
	}
	closes, since := Closes(window), window[0].T
	if calendar {
		days := calendarDays(daily)
		if horizonDays <= 365 {
			days = tail(days, 90)
		}
		closes, since = make([]float64, len(days)), days[0].T.Unix()
		for i, d := range days {
			closes[i] = d.C
		}
	}
	spot, vol := daily[len(daily)-1].C, RealizedVolAnn(closes)
	o := OutlookFrom(symbol, spot, vol, horizonDays)
	o.VolBasis = &VolBasis{Window: basis, Days: len(closes), Since: time.Unix(since, 0).UTC().Format(time.DateOnly)}
	if horizonDays > 365 {
		mu, ok := candleDrift(daily)
		o.LongHorizon = longHorizon(spot, vol, horizonDays, mu, ok)
	}
	return o
}

// candleDrift is HalvedDrift over daily bars.
func candleDrift(daily []Candle) (float64, bool) {
	first, last := daily[0], daily[len(daily)-1]
	if span := float64(last.T-first.T) / (365 * 86400); span >= 2 && first.C > 0 && last.C > 0 {
		return halved(math.Log(last.C/first.C) / span), true
	}
	return 0, false
}

var (
	outlookMultiples = []float64{0.5, 2, 5, 10}
	tenXYears        = []int{1, 3, 5, 10, 15, 20}
)

// longHorizon is the multi-year view under OutlookFrom's model: ln(S_t/spot)
// is a Brownian motion with volatility σ (vol, floored at 1%) and drift μ per
// year, μ = 0 for the baseline, so the baseline median stays spot and this is
// a volatility model, not a forecast of direction. For each multiple x the
// barrier is b = |ln x| and ν = ±μ the drift toward it; with s = σ√T,
//
//	pEnd   = Φ((νT − b)/s)
//	pTouch = pEnd + e^(2νb/σ²)·Φ((−b − νT)/s)
//
// the reflection principle (first passage of a drifted Brownian motion), so
// at μ = 0 pTouch = 2·pEnd, as ProbTouch. tenX is the baseline pTouch of 10x
// at each of 1, 3, 5 and 10 years within the horizon.
//
// driftCase is an assumption, not a measurement: μ (HalvedDrift) is the
// log-return CAGR ln(last/first)/years over the history σ came from, halved
// (shrunk toward zero) and capped at ±25%/yr; nil (drift false) under 2 years
// of history. Continuous monitoring, as ProbTouch: fat tails and regime
// changes are outside the model.
func longHorizon(spot, vol float64, horizonDays int, mu float64, drift bool) *LongHorizon {
	years := float64(horizonDays) / 365
	lh := &LongHorizon{Multiples: multiples(vol, 0, years)}
	for _, y := range tenXYears {
		if y*365 <= horizonDays {
			_, p := passage(10, 0, vol, float64(y))
			lh.TenX = append(lh.TenX, TenX{Years: y, PTouch: p})
		}
	}
	if drift {
		lh.DriftCase = &DriftCase{Label: "historical drift, halved (assumption)", MuAnnPct: mu * 100,
			P50: spot * math.Exp(mu*years), Multiples: multiples(vol, mu, years)}
	}
	return lh
}

func multiples(vol, mu, years float64) []Multiple {
	out := make([]Multiple, len(outlookMultiples))
	for i, x := range outlookMultiples {
		pEnd, pTouch := passage(x, mu, vol, years)
		out[i] = Multiple{X: x, PEnd: pEnd, PTouch: pTouch}
	}
	return out
}

// passage is longHorizon's pEnd and pTouch for x·spot after years, with log
// drift mu and volatility vol (floored at 1% as in OutlookFrom).
func passage(x, mu, vol, years float64) (pEnd, pTouch float64) {
	sig := max(vol, 0.01)
	b, nu, s := math.Log(x), mu, sig*math.Sqrt(years)
	if x < 1 {
		b, nu = -b, -mu
	}
	pEnd = normCDF((nu*years - b) / s)
	return pEnd, min(pEnd+expNormCDF(2*nu*b/(sig*sig), (-b-nu*years)/s), 1)
}

func normCDF(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }

// expNormCDF is e^k·Φ(z) without overflow. It is called only with
// z² ≥ 2k (k = 2νb/σ², z = −(b + νT)/s), so when e^k would overflow or Φ(z)
// underflow z < −37 and the Mills-ratio series Φ(z) ≈ φ(z)/|z|·(1 − z⁻² + 3z⁻⁴)
// is good to about 1e-8 relative.
func expNormCDF(k, z float64) float64 {
	if p := normCDF(z); k < 700 && p > 1e-300 {
		return math.Exp(k) * p
	}
	z2 := z * z
	return math.Exp(k-z2/2) / (-z * math.Sqrt(2*math.Pi)) * (1 - 1/z2 + 3/(z2*z2))
}

var outlookZ = []float64{-1.5, -0.75, -0.25, 0.25, 0.75, 1.5}

// OutlookFrom models ln(S_T) ~ N(ln spot, σ²T) — zero drift, so the median is
// spot. Six edges at spot·exp(σ√T·z) make seven buckets: two open-ended tails
// and five closed ranges. Bucket probabilities come from the rounded edges,
// so labels and p agree and p sums to 1.
func OutlookFrom(symbol string, spot, volAnn float64, horizonDays int) PriceOutlook {
	// ponytail: floor σ at 1% so a flat series still yields a valid distribution.
	s := max(volAnn, 0.01) * math.Sqrt(float64(horizonDays)/365)
	edges, figs := roundedEdges(spot, s)
	cdf := func(x float64) float64 { return 0.5 * math.Erfc(-math.Log(x/spot)/s/math.Sqrt2) }

	o := PriceOutlook{Symbol: symbol, Spot: spot, HorizonDays: horizonDays, VolAnnPct: volAnn * 100,
		P5: spot * math.Exp(-1.6448536269514722*s), P50: spot, P95: spot * math.Exp(1.6448536269514722*s)}
	prev := 0.0
	for i := 0; i <= len(edges); i++ {
		b := Bucket{}
		cum := 1.0
		switch {
		case i == 0:
			b.Hi, b.Label = &edges[0], "< "+usd(edges[0], figs)
			cum = cdf(edges[0])
		case i == len(edges):
			b.Lo, b.Label = &edges[i-1], "≥ "+usd(edges[i-1], figs)
		default:
			b.Lo, b.Hi = &edges[i-1], &edges[i]
			b.Label = usd(edges[i-1], figs) + " – " + usd(edges[i], figs)
			cum = cdf(edges[i])
		}
		b.P, prev = cum-prev, cum
		o.Buckets = append(o.Buckets, b)
	}
	return o
}

// ProbAbove is P(S_T > strike) under OutlookFrom's model (ln S_T ~ N(ln spot,
// σ²T), zero drift), T in years of 365 days: 1 − Φ(ln(strike/spot)/(σ√T)).
func ProbAbove(spot, strike, volAnn, years float64) float64 {
	return 0.5 * math.Erfc(volZ(spot, strike, volAnn, years)/math.Sqrt2)
}

// ProbTouch is the chance the price trades at strike before T, from above
// (up false) or below (up true): 1 when it is already there, else
// 2·(1 − Φ(|z|)), the reflection principle for the same driftless log price.
// ponytail: continuous monitoring; a venue that settles on 1-minute highs and
// lows touches a little less often than this.
func ProbTouch(spot, strike, volAnn, years float64, up bool) float64 {
	if up && spot >= strike || !up && spot <= strike {
		return 1
	}
	return math.Erfc(math.Abs(volZ(spot, strike, volAnn, years)) / math.Sqrt2)
}

// volZ is ln(strike/spot)/(σ√T), σ floored at 1% as in OutlookFrom.
func volZ(spot, strike, volAnn, years float64) float64 {
	return math.Log(strike/spot) / (max(volAnn, 0.01) * math.Sqrt(years))
}

// roundedEdges rounds the edges to 3 significant figures, adding figures only
// when a tight distribution would otherwise collapse two edges into one.
func roundedEdges(spot, s float64) ([]float64, int) {
	edges := make([]float64, len(outlookZ))
	for figs := 3; ; figs++ {
		distinct := true
		for i, z := range outlookZ {
			edges[i] = roundSig(spot*math.Exp(s*z), figs)
			distinct = distinct && (i == 0 || edges[i] > edges[i-1])
		}
		if distinct || figs >= 8 {
			return edges, figs
		}
	}
}

// roundSig rounds x > 0 to figs significant figures, exactly: a step of 10^e
// with e ≥ 0 divides and multiplies by that power (a whole number), so 1e6
// stays 1000000; multiplying by the fractional 10^−e, which floats cannot
// hold, gave 999999.9999999999 and 1599999.9999999998 from a million up.
// Finer steps multiply by the exact 10^−e as before. Non-positive x and +Inf
// are returned as they are.
func roundSig(x float64, figs int) float64 {
	if !(x > 0) || math.IsInf(x, 1) {
		return x
	}
	e := int(math.Floor(math.Log10(x))) - figs + 1
	if e >= 0 {
		p := math.Pow(10, float64(e))
		return math.Round(x/p) * p
	}
	p := math.Pow(10, float64(-e))
	return math.Round(x*p) / p
}

// usd formats a price with figs significant figures and a k/M suffix:
// 84200 → "$84.2k", 88000 → "$88.0k", 0.1234 → "$0.123".
func usd(x float64, figs int) string {
	unit, suffix := 1.0, ""
	switch {
	case x >= 1e6:
		unit, suffix = 1e6, "M"
	case x >= 1e3:
		unit, suffix = 1e3, "k"
	}
	v := x / unit
	decimals := 0
	if v > 0 {
		decimals = max(figs-1-int(math.Floor(math.Log10(v))), 0)
	}
	return "$" + strconv.FormatFloat(v, 'f', decimals, 64) + suffix
}
