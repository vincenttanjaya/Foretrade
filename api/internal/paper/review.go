package paper

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Portfolio health (contract "Paper trading" → "Portfolio health"): is the
// book diversified? Concentration, what moves together, beta to BTC, a
// historical VaR / stress and stop coverage, from the open positions at live
// marks and the last 90 daily closes. Pure Go, 0 credits. Review is exported
// so the agent's review tool and the Account page read the
// same numbers.
// ponytail: no per-user cache; the market client's 30 s candle cache bounds Binance calls and the math is microseconds.

const (
	reviewDays    = 90   // daily returns in the window
	reviewMinDays = 20   // a pair statistic needs this many shared days
	clusterRho    = 0.8  // position-direction ρ from which two positions are one bet (≤ −clusterRho: a hedge)
	varTail       = 0.05 // the 95% VaR reads the worst 5% of days
)

type Review struct {
	AsOf                time.Time      `json:"asOf"`
	Revision            int64          `json:"revision"`
	Coverage            ReviewCoverage `json:"coverage"`
	Window              ReviewWindow   `json:"window"`
	Positions           int            `json:"positions"`
	Equity              float64        `json:"equity"`
	GrossExposure       float64        `json:"grossExposure"`
	NetExposure         float64        `json:"netExposure"`
	ExposurePct         *float64       `json:"exposurePct"`
	PredictionStakes    float64        `json:"predictionStakes"`
	PredictionStakesPct *float64       `json:"predictionStakesPct"`
	Holdings            []Holding      `json:"holdings"`
	Concentration       *Concentration `json:"concentration"`
	BetaBtc             *float64       `json:"betaBtc"`
	VolAnnPct           *float64       `json:"volAnnPct"`
	VaR                 *VaR           `json:"var"`
	WorstDay            *WorstDay      `json:"worstDay"`
	Drawdown30dPct      *float64       `json:"drawdown30dPct"`
	Stress              []Stress       `json:"stress"`
	Correlations        []Correlation  `json:"correlations"`
	Clusters            []Cluster      `json:"clusters"`
	Stops               StopCoverage   `json:"stops"`
	Notes               []string       `json:"notes"`
}

type ReviewCoverage struct {
	ModeledGrossExposure float64  `json:"modeledGrossExposure"`
	MissingSymbols       []string `json:"missingSymbols"`
	CommonDays           int      `json:"commonDays"`
	Reason               *string  `json:"reason"`
}

type ReviewWindow struct {
	Days int `json:"days"`
	Bars int `json:"bars"` // daily returns actually used
}

type Holding struct {
	Symbol        string   `json:"symbol"`
	Side          string   `json:"side"` // long | short
	Value         float64  `json:"value"`
	Pct           float64  `json:"pct"`
	Beta          *float64 `json:"beta"`
	StopLoss      *float64 `json:"stopLoss"`
	Halted        bool     `json:"halted"`
	StopStatus    string   `json:"stopStatus"`
	MarkAvailable bool     `json:"markAvailable"`
}

type Concentration struct {
	Level     string  `json:"level"` // low | medium | high
	Top1Pct   float64 `json:"top1Pct"`
	Top3Pct   float64 `json:"top3Pct"`
	HHI       float64 `json:"hhi"`
	Effective float64 `json:"effective"` // equal-weight positions the book is equivalent to
}

type VaR struct {
	Pct     float64 `json:"pct"`
	Usd     float64 `json:"usd"`
	CvarUsd float64 `json:"cvarUsd"`
}

type WorstDay struct {
	Date   string  `json:"date"`
	Pnl    float64 `json:"pnl"`
	PnlPct float64 `json:"pnlPct"`
}

type Stress struct {
	Scenario  string  `json:"scenario"`
	ShockPct  float64 `json:"shockPct"`
	EstPnl    float64 `json:"estPnl"`
	EstPnlPct float64 `json:"estPnlPct"`
}

type Correlation struct {
	A    string   `json:"a"`
	B    string   `json:"b"`
	Rho  *float64 `json:"rho"`
	Days int      `json:"days"`
}

type Cluster struct {
	Days    int      `json:"days"`
	Symbols []string `json:"symbols"`
	Rho     float64  `json:"rho"`
	Pct     float64  `json:"pct"`
}

type StopCoverage struct {
	Covered      int      `json:"covered"`
	Uncovered    []string `json:"uncovered"`
	UncoveredPct float64  `json:"uncoveredPct"`
	AtRiskUsd    float64  `json:"atRiskUsd"`
}

// dailyReturns are simple daily returns per symbol on one grid of days
// (dates[i] is the UTC day of return i); NaN marks a day a pair had no
// history yet.
type dailyReturns struct {
	dates    []time.Time
	bySymbol map[string][]float64
}

// Review is the portfolio health of userID's book as of now. Market data
// failure is market.ErrUpstream.
func (e *Engine) Review(ctx context.Context, userID string, now time.Time) (Review, error) {
	p, trades, stakes, err := e.history(ctx, userID)
	halted := p.halted
	if err != nil {
		return Review{}, err
	}
	var r dailyReturns
	var missing []string
	if len(p.Positions) > 0 {
		symbols := make([]string, len(p.Positions))
		for i, pos := range p.Positions {
			symbols[i] = pos.Symbol
		}
		if r, missing, err = e.dailyReturns(ctx, symbols); err != nil {
			missing = slices.Clone(symbols)
			r = dailyReturns{}
		}
	}
	curve, err := e.equitySnapshot(ctx, p, trades, stakes, "30d", now)
	if err != nil {
		out := review(p, halted, r, missing, 0, now)
		out.Drawdown30dPct = nil
		out.Notes = append(out.Notes, "Account replay unavailable: missing historical prices.")
		return out, nil
	}
	equity := make([]float64, len(curve.Points))
	for i, pt := range curve.Points {
		equity[i] = pt.V
	}
	return review(p, halted, r, missing, market.Stats(equity, "4h").MaxDrawdownPct, now), nil
}

// dailyReturns fetches the last reviewDays+1 closed daily bars of symbols and
// BTC (the benchmark, and the grid every series is aligned on), concurrently.
// A pair whose bars cannot be read (Binance dropped it) is listed in missing
// and reads NaN. Missing candle observations are never forward filled.
func (e *Engine) dailyReturns(ctx context.Context, symbols []string) (dailyReturns, []string, error) {
	cutoff := time.Now().Unix() // anchor finality before upstream waits, never promote an old open row
	want := slices.Clone(symbols)
	if !slices.Contains(want, "BTC") {
		want = append(want, "BTC")
	}
	bars := make([][]market.Candle, len(want))
	errs := make([]error, len(want))
	var wg sync.WaitGroup
	for i, sym := range want {
		wg.Go(func() { bars[i], errs[i] = e.market.Candles(ctx, sym, "1d", reviewDays+2) })
	}
	wg.Wait()
	closes := map[string]map[int64]float64{}
	var missing []string
	for i, sym := range want {
		if errs[i] != nil {
			slog.Warn("portfolio review: no daily bars", "symbol", sym, "err", errs[i])
			missing = append(missing, sym)
			continue
		}
		byT := make(map[int64]float64, len(bars[i]))
		for _, c := range bars[i] {
			if c.T+86400 > cutoff {
				continue
			}
			byT[c.T] = c.C
		}
		closes[sym] = byT
	}
	// An absolute daily grid preserves gaps, including gaps in BTC itself.
	latest := int64(0)
	for _, byT := range closes {
		for t := range byT {
			latest = max(latest, t)
		}
	}
	grid := []market.Candle{}
	if latest > 0 {
		for t := latest - reviewDays*86400; t <= latest; t += 86400 {
			grid = append(grid, market.Candle{T: t})
		}
	}
	r := dailyReturns{bySymbol: map[string][]float64{}}
	for _, bar := range grid[min(1, len(grid)):] {
		r.dates = append(r.dates, time.Unix(bar.T, 0).UTC())
	}
	for sym, byT := range closes {
		series := make([]float64, len(grid))
		for k, bar := range grid {
			series[k] = math.NaN()
			if c, ok := byT[bar.T]; ok {
				series[k] = c
			}
		}
		r.bySymbol[sym] = market.Returns(series)
	}
	return r, missing, nil
}

// review is the pure computation over the portfolio and the aligned daily
// returns of its symbols (and BTC); missing names held pairs without bars.
func review(p Portfolio, halted map[string]bool, r dailyReturns, missing []string, drawdown float64, now time.Time) Review {
	out := Review{AsOf: now.UTC(), Revision: p.Revision, Window: ReviewWindow{Days: reviewDays, Bars: len(r.dates)},
		PredictionStakes: round(p.PredictionStakes, 2), Positions: len(p.Positions), Equity: round(p.Equity, 2), GrossExposure: round(p.GrossExposure, 2),
		Drawdown30dPct: ptrTo(round(drawdown, 1)), Coverage: ReviewCoverage{MissingSymbols: []string{}}, Holdings: []Holding{}, Stress: []Stress{}, Correlations: []Correlation{},
		Clusters: []Cluster{}, Stops: StopCoverage{Uncovered: []string{}}, Notes: []string{}}
	onEquity := func(x float64) float64 { // percent of equity; 0 when the account is wiped out
		if p.Equity > 0 {
			return x / p.Equity * 100
		}
		return 0
	}
	if p.Equity > 0 {
		out.PredictionStakesPct = ptrTo(round(onEquity(p.PredictionStakes), 1))
		out.ExposurePct = ptrTo(round(onEquity(p.GrossExposure), 1))
	}
	if p.PredictionStakes > 0 && p.Equity <= 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("Prediction stakes are %s at cost; equity percentage unavailable. Outcome-contract tail risk is excluded.", Money(p.PredictionStakes)))
	}
	if p.PredictionStakes > 0 && p.Equity > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("Prediction stakes are %s of equity, held at cost; outcome-contract tail risk is excluded from daily asset statistics.", pct(onEquity(p.PredictionStakes))))
	}
	if len(p.Positions) == 0 {
		if p.PredictionStakes > 0 {
			out.Notes = append(out.Notes, "No open asset positions: daily asset statistics are unavailable; prediction stakes remain separate review facts.")
		} else {
			out.Notes = append(out.Notes, "No open positions: nothing to review.")
		}
		return out
	}

	// Positions by weight, largest first.
	pos := slices.Clone(p.Positions)
	slices.SortFunc(pos, func(a, b Position) int {
		return cmp.Or(cmp.Compare(math.Abs(b.MarketValue), math.Abs(a.MarketValue)), cmp.Compare(a.Symbol, b.Symbol))
	})
	n := len(pos)
	gross := p.GrossExposure
	weight := make([]float64, n)
	net, hhi := 0.0, 0.0
	for i, q := range pos {
		net += q.MarketValue
		if gross > 0 {
			weight[i] = math.Abs(q.MarketValue) / gross
		}
		hhi += weight[i] * weight[i]
	}
	out.NetExposure = round(net, 2)
	if p.Equity > 0 {
		out.ExposurePct = ptrTo(round(onEquity(gross), 1))
	}
	top3 := 0.0
	for _, w := range weight[:min(3, n)] {
		top3 += w
	}
	c := &Concentration{Level: "low", Top1Pct: round(weight[0]*100, 1), Top3Pct: round(top3*100, 1), HHI: round(hhi, 3)}
	if hhi > 0 {
		c.Effective = round(1/hhi, 1)
	}
	switch {
	case hhi >= 0.40:
		c.Level = "high"
	case hhi >= 0.25:
		c.Level = "medium"
	}
	out.Concentration = c

	// Betas to BTC and the book's daily P&L replayed on the window.
	btc := r.bySymbol["BTC"]
	beta := make([]float64, n) // 0 where unknown
	known := make([]bool, n)
	bars := len(r.dates)
	pnl := make([]float64, bars)
	dollarBeta := 0.0
	for i, q := range pos {
		series := r.bySymbol[q.Symbol]
		if b, ok := market.Beta(series, btc, reviewMinDays); ok {
			beta[i], known[i] = b, true
		}
		dollarBeta += q.MarketValue * beta[i]
		for t := 0; t < bars && t < len(series); t++ {
			if !math.IsNaN(series[t]) {
				pnl[t] += q.MarketValue * series[t]
			}
		}
		h := Holding{Symbol: q.Symbol, Side: "long", Value: round(math.Abs(q.MarketValue), 2), Pct: round(weight[i]*100, 1),
			StopLoss: q.StopLoss, Halted: halted[q.Symbol], MarkAvailable: !p.missingMarks[q.Symbol]}
		if q.Qty < 0 {
			h.Side = "short"
		}
		switch {
		case p.missingMarks[q.Symbol]:
			h.StopStatus = "unknown"
		case q.StopLoss == nil:
			h.StopStatus = "missing"
		case q.Qty > 0 && *q.StopLoss >= q.MarkPrice || q.Qty < 0 && *q.StopLoss <= q.MarkPrice:
			h.StopStatus = "crossed"
		case halted[q.Symbol]:
			h.StopStatus = "dormant"
		default:
			h.StopStatus = "valid"
		}
		if known[i] {
			h.Beta = ptrTo(round(beta[i], 2))
		}
		out.Holdings = append(out.Holdings, h)
	}
	common := make([]int, 0, bars)
	for day := range bars {
		valid := day < len(btc) && !math.IsNaN(btc[day]) && !math.IsInf(btc[day], 0)
		for _, q := range pos {
			series := r.bySymbol[q.Symbol]
			if day >= len(series) || math.IsNaN(series[day]) || math.IsInf(series[day], 0) {
				valid = false
				break
			}
		}
		if valid {
			common = append(common, day)
		}
	}
	allKnown := true
	for i, q := range pos {
		if known[i] && !p.missingMarks[q.Symbol] {
			out.Coverage.ModeledGrossExposure += math.Abs(q.MarketValue)
		} else {
			allKnown = false
			out.Coverage.MissingSymbols = append(out.Coverage.MissingSymbols, q.Symbol)
		}
	}
	out.Coverage.ModeledGrossExposure = round(out.Coverage.ModeledGrossExposure, 2)
	out.Coverage.CommonDays = len(common)
	// Aggregate sensitivity uses the same common observations as current-book P&L.
	commonBTC := make([]float64, 0, len(common))
	for _, day := range common {
		commonBTC = append(commonBTC, btc[day])
	}
	dollarBeta = 0
	commonBenchmark := true
	for _, q := range pos {
		series := make([]float64, 0, len(common))
		for _, day := range common {
			series = append(series, r.bySymbol[q.Symbol][day])
		}
		b, ok := market.Beta(series, commonBTC, reviewMinDays)
		if !ok {
			commonBenchmark = false
		}
		dollarBeta += q.MarketValue * b
	}
	supported := commonBenchmark && allKnown && len(common) >= reviewMinDays && p.Equity > 0
	if !supported {
		reason := "Whole-book risk unavailable: incomplete material holding history."
		switch {
		case len(p.missingMarks) > 0:
			reason = "Whole-book risk unavailable: missing material holding marks; exposure facts use entry-price estimates."
		case p.Equity <= 0:
			reason = "Whole-book risk unavailable: nonpositive account equity."
		case len(common) < reviewMinDays:
			reason = "Whole-book risk unavailable: fewer than 20 actual common daily returns."
		case !commonBenchmark:
			reason = "Whole-book risk unavailable: BTC benchmark unavailable or flat on common dates."
		}
		out.Coverage.Reason = ptrTo(reason)
	}
	compact := make([]float64, 0, len(common))
	dates := make([]time.Time, 0, len(common))
	for _, day := range common {
		compact = append(compact, pnl[day])
		dates = append(dates, r.dates[day])
	}
	pnl = compact
	bars = len(pnl)
	if supported {
		out.BetaBtc = ptrTo(round(dollarBeta/p.Equity, 2))
	}
	if supported {
		for _, shock := range []float64{-10, -20, 10} {
			est := dollarBeta * shock / 100
			label := fmt.Sprintf("BTC −%d%%", int(-shock))
			if shock > 0 {
				label = fmt.Sprintf("BTC +%d%%", int(shock))
			}
			out.Stress = append(out.Stress, Stress{Scenario: label, ShockPct: shock, EstPnl: round(est, 2), EstPnlPct: round(onEquity(est), 1)})
		}
	}
	if supported {
		mean, ss := 0.0, 0.0
		for _, x := range pnl {
			mean += x / p.Equity
		}
		mean /= float64(bars)
		for _, x := range pnl {
			ss += (x/p.Equity - mean) * (x/p.Equity - mean)
		}
		out.VolAnnPct = ptrTo(round(math.Sqrt(ss/float64(bars-1))*math.Sqrt(365)*100, 1))
		sorted := slices.Clone(pnl)
		slices.Sort(sorted)
		k := int(math.Ceil(varTail * float64(bars)))
		tail := 0.0
		for _, x := range sorted[:k] {
			tail += x
		}
		usd := max(0, -sorted[k-1])
		out.VaR = &VaR{Pct: round(onEquity(usd), 1), Usd: round(usd, 2), CvarUsd: round(max(0, -tail/float64(k)), 2)}
		worst := slices.Index(pnl, sorted[0])
		out.WorstDay = &WorstDay{Date: dates[worst].Format("2006-01-02"), Pnl: round(pnl[worst], 2), PnlPct: round(onEquity(pnl[worst]), 1)}
	}

	// Pairs: ρ of the assets, and in position direction for clusters and hedges.
	signed := map[[2]int]float64{}
	type hedge struct {
		a, b int
		rho  float64
	}
	var hedges []hedge
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			rho, ok := market.Correlation(r.bySymbol[pos[i].Symbol], r.bySymbol[pos[j].Symbol], reviewMinDays)
			pair := Correlation{A: pos[i].Symbol, B: pos[j].Symbol, Days: sharedDays(r.bySymbol[pos[i].Symbol], r.bySymbol[pos[j].Symbol])}
			if ok {
				pair.Rho = ptrTo(round(rho, 2))
				s := rho * sign(pos[i].Qty) * sign(pos[j].Qty)
				signed[[2]int{i, j}] = s
				switch {
				case s >= clusterRho:
					// Group only after every pair has been measured.
				case s <= -clusterRho:
					hedges = append(hedges, hedge{i, j, rho})
				}
			}
			out.Correlations = append(out.Correlations, pair)
		}
	}
	members := [][]int{}
	for i := range pos {
		added := false
		for k, m := range members {
			valid := true
			for _, j := range m {
				v, ok := signed[[2]int{min(i, j), max(i, j)}]
				if !ok || v < clusterRho {
					valid = false
					break
				}
			}
			if valid {
				members[k] = append(m, i)
				added = true
				break
			}
		}
		if !added {
			members = append(members, []int{i})
		}
	}
	for _, m := range members {
		if len(m) < 2 {
			continue
		}
		cl := Cluster{Rho: 1, Days: reviewDays}
		pct := 0.0
		for _, i := range m {
			cl.Symbols = append(cl.Symbols, pos[i].Symbol)
			pct += weight[i]
			for _, j := range m {
				if s, ok := signed[[2]int{i, j}]; ok {
					cl.Rho = min(cl.Rho, s)
					cl.Days = min(cl.Days, sharedDays(r.bySymbol[pos[i].Symbol], r.bySymbol[pos[j].Symbol]))
				}
			}
		}
		cl.Rho, cl.Pct = round(cl.Rho, 2), round(pct*100, 1)
		out.Clusters = append(out.Clusters, cl)
	}
	slices.SortFunc(out.Clusters, func(a, b Cluster) int {
		return cmp.Or(cmp.Compare(b.Pct, a.Pct), cmp.Compare(a.Symbols[0], b.Symbols[0]))
	})

	// Stops.
	uncoveredW, atRisk := 0.0, 0.0
	for i, q := range pos {
		if p.missingMarks[q.Symbol] || q.StopLoss == nil || q.Qty > 0 && *q.StopLoss >= q.MarkPrice || q.Qty < 0 && *q.StopLoss <= q.MarkPrice {
			out.Stops.Uncovered = append(out.Stops.Uncovered, q.Symbol)
			uncoveredW += weight[i]
			continue
		}
		out.Stops.Covered++
		atRisk += math.Abs(q.Qty) * math.Abs(q.MarkPrice-*q.StopLoss)
	}
	out.Stops.UncoveredPct, out.Stops.AtRiskUsd = round(uncoveredW*100, 1), round(atRisk, 2)

	for _, q := range pos {
		if p.missingMarks[q.Symbol] {
			out.Notes = append(out.Notes, fmt.Sprintf("%s has no current mark: its holding, equity, exposure and concentration use entry-price estimates; whole-book risk and account drawdown are unavailable.", q.Symbol))
			out.Drawdown30dPct = nil
		}
	}
	// Notes: facts in reading order.
	note := func(format string, args ...any) { out.Notes = append(out.Notes, fmt.Sprintf(format, args...)) }
	switch {
	case weight[0] >= 0.5:
		note("%s is %s of your exposure.", pos[0].Symbol, pct(weight[0]*100))
	case n >= 4 && top3 >= 0.75:
		note("Your top three positions are %s of your exposure.", pct(top3*100))
	}
	for _, cl := range out.Clusters {
		note("%s have measured pairwise co-movement (ρ ≥ %.2f) and are %s of your exposure.",
			list(cl.Symbols), cl.Rho, pct(cl.Pct))
	}
	for _, h := range hedges {
		note("%s %s and %s %s offset each other (ρ %.2f): a hedge, not two bets.",
			pos[h.a].Symbol, out.Holdings[h.a].Side, pos[h.b].Symbol, out.Holdings[h.b].Side, h.rho)
	}
	if out.BetaBtc != nil && len(out.Stress) > 0 && out.Stress[0].EstPnl != 0 {
		verb := "cost"
		if out.Stress[0].EstPnl > 0 {
			verb = "gain"
		}
		note("A 10%% BTC drop would %s about %s (%s of equity) at the book's beta of %.2f.",
			verb, Money(math.Abs(out.Stress[0].EstPnl)), pct(math.Abs(out.Stress[0].EstPnlPct)), *out.BetaBtc)
	}
	switch u := len(out.Stops.Uncovered); {
	case u == 1 && n == 1:
		note("Your only position has no valid current stop.")
	case u == 1:
		note("1 of %d positions has no valid current stop: %s of your exposure is unprotected.", n, pct(out.Stops.UncoveredPct))
	case u > 1:
		note("%d of %d positions have no valid current stop: %s of your exposure is unprotected.", u, n, pct(out.Stops.UncoveredPct))
	default:
		note("Every position has a valid stop; distance to stop levels is about %s before fees and slippage.", Money(out.Stops.AtRiskUsd))
	}
	for _, q := range pos {
		if a, _ := market.Lookup(q.Symbol); halted[q.Symbol] && a.QuoteFeed() {
			note("%s has no fresh quote: it is valued at its last quote and paper orders in it are paused.", q.Symbol)
		} else if halted[q.Symbol] {
			note("%s is halted on Binance: it is valued at its last trade and only orders that shrink the position fill.", q.Symbol)
		}
	}
	for _, sym := range missing {
		note("%s has no adequate daily price history; whole-book risk is unavailable.", sym)
	}
	if out.Window.Bars < reviewDays {
		note("Only %d daily closes were available, so the statistics are rough.", out.Window.Bars)
	}
	return out
}

// snapshot is the portfolio at the latest marks plus which pairs are halted.
func (e *Engine) snapshot(ctx context.Context, userID string) (Portfolio, map[string]bool, error) {
	marks, halted, err := e.marks(ctx)
	if err != nil {
		return Portfolio{}, nil, err
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return Portfolio{}, nil, err
	}
	defer tx.Rollback(ctx)
	acct, positions, err := load(ctx, tx, userID, false)
	if err != nil {
		return Portfolio{}, nil, err
	}
	return buildPortfolio(acct, positions, marks), halted, tx.Commit(ctx)
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

func ptrTo[T any](v T) *T { return &v }

// pct is "12.5%".
func pct(x float64) string { return strconv.FormatFloat(round(x, 1), 'f', 1, 64) + "%" }

// list is "SOL, SUI and AVAX".
func list(symbols []string) string {
	if len(symbols) < 2 {
		return strings.Join(symbols, "")
	}
	return strings.Join(symbols[:len(symbols)-1], ", ") + " and " + symbols[len(symbols)-1]
}

// count is "two" … "nine", then digits.
func count(n int) string {
	words := []string{"", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	if n > 0 && n < len(words) {
		return words[n]
	}
	return strconv.Itoa(n)
}

func sharedDays(a, b []float64) int {
	n := 0
	for i := range min(len(a), len(b)) {
		if !math.IsNaN(a[i]) && !math.IsNaN(b[i]) && !math.IsInf(a[i], 0) && !math.IsInf(b[i], 0) {
			n++
		}
	}
	return n
}
