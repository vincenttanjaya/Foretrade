package arena

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

const (
	warmup      = 300 // bars before the window: SMA200 and 90-day volatility, like market's dailyBars
	stepTimeout = 90 * time.Second
	runTimeout  = 60 * time.Minute
	benchAfter  = 3     // consecutive failed steps bench an agent
	minOrder    = 10.0  // USD, the paper minimum order
	minOrderPct = 0.005 // of equity: weight drift alone must not churn fees
	maxText     = 1500  // runes of a step's rationale
	noteText    = 300   // runes of a rationale carried into the next steps
)

var (
	errAllBenched = errors.New("every agent is benched")
	errStopped    = errors.New("run is no longer running") // cancelled or recovered elsewhere
)

// tape is the whole window plus warm-up, for every universe asset. Only the
// runner holds it; agents get views.
type tape struct {
	cadence  string
	iv       int64 // seconds per bar
	start    int64 // B_0.T, the first decision time
	n        int   // window bars = steps
	universe []string
	bars     map[string][]market.Candle // cadence bars, oldest first
	daily    map[string][]market.Candle // the same slices for 1d runs
	warm     map[string]int             // index of B_0 in bars[sym]
}

// view is everything an agent may read at now: slices cut to bars closed by now.
type view struct {
	now         int64
	bars, daily map[string][]market.Candle
}

func (t *tape) view(now int64) view {
	v := view{now: now, bars: map[string][]market.Candle{}, daily: map[string][]market.Candle{}}
	for _, sym := range t.universe {
		v.bars[sym] = closed(t.bars[sym], t.iv, now)
		v.daily[sym] = closed(t.daily[sym], 86400, now)
	}
	return v
}

// closed keeps the bars that closed at or before now (T+iv <= now).
func closed(c []market.Candle, iv, now int64) []market.Candle {
	return c[:sort.Search(len(c), func(i int) bool { return c[i].T+iv > now })]
}

// bar is window bar B_k of sym.
func (t *tape) bar(sym string, k int) market.Candle { return t.bars[sym][t.warm[sym]+k] }

// dailyCloses feeds AnalyzeCandles' returns and volatility. In a 4h run they
// are the closed daily closes plus the newest closed 4h close when it is
// later, as live Analyze sees a still-open daily bar at the current price.
func dailyCloses(v view, sym string, iv int64) []float64 {
	d := market.Closes(v.daily[sym])
	b, dd := v.bars[sym], v.daily[sym]
	if iv != 86400 && len(b) > 0 && (len(dd) == 0 || b[len(b)-1].T+iv > dd[len(dd)-1].T+86400) {
		d = append(d, b[len(b)-1].C)
	}
	return d
}

// newTape keeps the core assets whose bars hold every window bar (B_k.T = start
// + k·iv) and at least one warm-up bar; a pair Binance halted mid-window
// drops out here. BTC must stay: it is the baseline. Core only, so the
// per-step prompt and the run's price stay flat as the universe grows.
func newTape(cadence string, iv, start int64, n int, bars, daily map[string][]market.Candle) (*tape, error) {
	t := &tape{cadence: cadence, iv: iv, start: start, n: n,
		bars: map[string][]market.Candle{}, daily: map[string][]market.Candle{}, warm: map[string]int{}}
	for _, a := range market.Core {
		c := bars[a.Symbol]
		i0 := sort.Search(len(c), func(i int) bool { return c[i].T >= start })
		if i0 == 0 || i0+n > len(c) {
			continue
		}
		complete := true
		for k := range n {
			complete = complete && c[i0+k].T == start+int64(k)*iv
		}
		if !complete {
			continue
		}
		t.universe = append(t.universe, a.Symbol)
		t.bars[a.Symbol], t.warm[a.Symbol], t.daily[a.Symbol] = c, i0, c
		if iv != 86400 {
			t.daily[a.Symbol] = daily[a.Symbol]
		}
	}
	if !slices.Contains(t.universe, "BTC") {
		return nil, fmt.Errorf("%w: BTC has no complete bars for this window", market.ErrUpstream)
	}
	return t, nil
}

// load fetches the window and its warm-up for every core asset in parallel (the
// caller bounds it with a timeout). Any Binance failure fails the load.
func load(ctx context.Context, m *market.Client, c cadence, start time.Time, n int) (*tape, error) {
	end := start.Add(time.Duration(int64(n)*c.iv) * time.Second)
	days := int(int64(n) * c.iv / 86400)
	bars, daily := make([][]market.Candle, len(market.Core)), make([][]market.Candle, len(market.Core))
	errs := make([]error, len(market.Core))
	var wg sync.WaitGroup
	for i, a := range market.Core {
		wg.Go(func() {
			bars[i], errs[i] = m.CandlesBefore(ctx, a.Symbol, c.id, end, n+warmup)
			if errs[i] == nil && c.iv != 86400 {
				daily[i], errs[i] = m.CandlesBefore(ctx, a.Symbol, "1d", end, days+warmup)
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	bm, dm := map[string][]market.Candle{}, map[string][]market.Candle{}
	for i, a := range market.Core {
		bm[a.Symbol], dm[a.Symbol] = bars[i], daily[i]
	}
	return newTape(c.id, c.iv, start.Unix(), n, bm, dm)
}

type holding struct{ qty, avg float64 }

// agent is one portfolio in the race. The per-step fields are reset before
// every step and written only by that agent's decide.
type agent struct {
	system  string
	cash    float64
	pos     map[string]*holding
	fails   int // consecutive failed steps
	benched bool
	notes   []string // last three rationales

	targets []Target // staged by place_orders; nil = hold
	tools   []ToolLog
	text    string
	err     error
}

func newAgent(row agentRow, universe []string) *agent {
	return &agent{system: system(row, universe), cash: startingCash, pos: map[string]*holding{}}
}

// runner steps every agent through the window in lockstep.
type runner struct {
	id     string // for logs
	llm    *llm.Client
	news   *newsDesk
	t      *tape
	agents []*agent
	// save stores step k's rows and reports whether the run is still running
	// (Postgres in production, a recorder in tests).
	save func(ctx context.Context, k int, rows []Step) (bool, error)
}

// loop runs the steps. At step k every active agent decides in parallel from
// the view at B_k.T; then Go fills the staged orders at B_k.O and marks at
// B_k.C. An agent never sees the bar it trades on.
func (r *runner) loop(ctx context.Context) error {
	for k := range r.t.n {
		began := time.Now()
		v := r.t.view(r.t.start + int64(k)*r.t.iv)
		table := marketTable(r.t.universe, r.t.cadence, r.t.iv, v)
		var wg sync.WaitGroup
		for _, a := range r.agents {
			a.targets, a.tools, a.text, a.err = nil, []ToolLog{}, "", nil
			if a.benched {
				continue
			}
			wg.Go(func() {
				defer func() {
					if p := recover(); p != nil {
						a.targets, a.err = nil, fmt.Errorf("panic: %v", p)
					}
				}()
				r.decide(ctx, a, k, v, table)
			})
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		rows := make([]Step, len(r.agents))
		for i, a := range r.agents {
			rows[i] = a.snapshot(r.t.universe, k, r.t.start+int64(k)*r.t.iv, r.t.price(k, false), a.execute(r.t.price(k, true)))
			if a.err != nil {
				slog.Warn("arena agent step failed", "run", r.id, "agent", i, "step", k, "err", a.err)
			}
			a.track()
		}
		ok, err := r.save(ctx, k, rows)
		if err != nil {
			return err
		}
		if !ok {
			return errStopped
		}
		r.news.mu.Lock()
		calls := r.news.calls
		r.news.mu.Unlock()
		slog.Info("arena step", "run", r.id, "step", k+1, "of", r.t.n, "ms", time.Since(began).Milliseconds(), "braveCalls", calls)
		if !slices.ContainsFunc(r.agents, func(a *agent) bool { return !a.benched }) {
			return errAllBenched
		}
	}
	return nil
}

// decide makes one bounded model call. Orders staged by its tools are kept
// only when the call returns cleanly, so a failed or timed-out call holds.
func (r *runner) decide(ctx context.Context, a *agent, k int, v view, table string) {
	header := fmt.Sprintf("Step %d of %d · Simulated time: %s UTC · cadence %s", k+1, r.t.n,
		time.Unix(v.now, 0).UTC().Format("2006-01-02 15:04"), r.t.cadence)
	a.decide(ctx, r.llm, r.newTurn(a, v), header, table, fmt.Sprintf("Step %d", k+1))
}

// decide makes one bounded model call on s and keeps its outcome on the
// agent; label names the step in the note it keeps for later turns.
func (a *agent) decide(ctx context.Context, model *llm.Client, s *turn, header, table, label string) {
	ctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	text, err := model.Agent(ctx, llm.AgentRequest{
		System:   a.system,
		Messages: []llm.Message{{Role: "user", Text: s.message(a, header, table)}},
		Tools:    s.tools(), Effort: llm.Low, MaxTurns: 3, MaxTokens: 4096,
		NoRetries: s.live, // durable live claims allow four actual requests, including the final turn
	}, s.onEvent)
	a.tools = s.log
	if err != nil {
		a.err = err
		return
	}
	a.targets, a.text = s.staged, clip(strings.TrimSpace(text), maxText)
	if a.text != "" {
		a.notes = append(a.notes, fmt.Sprintf("%s: %s", label, clip(a.text, noteText)))
		a.notes = a.notes[max(len(a.notes)-3, 0):]
	}
}

// price is a lookup of window bar B_k's open (fills) or close (marks).
func (t *tape) price(k int, open bool) func(string) float64 {
	return func(sym string) float64 {
		if open {
			return t.bar(sym, k).O
		}
		return t.bar(sym, k).C
	}
}

// execute fills the staged targets at price (B_k.O in the arena, the live
// price on the live desk) with the paper engine's slippage and fee: sells
// first, then buys capped by cash. Long-only and fully funded, so equity can
// never go negative. An asset price knows nothing of (0) is valued at cost.
func (a *agent) execute(price func(string) float64) []Fill {
	fills := []Fill{}
	if a.targets == nil {
		return fills
	}
	eq := a.cash
	for sym, h := range a.pos {
		eq += h.qty * cmp.Or(price(sym), h.avg)
	}
	type order struct {
		sym string
		qty float64 // > 0
	}
	var sells, buys []order
	for _, tg := range a.targets {
		o, held := price(tg.Symbol), 0.0
		if h := a.pos[tg.Symbol]; h != nil {
			held = h.qty
		}
		delta := tg.WeightPct/100*eq/o - held
		if tg.WeightPct == 0 {
			delta = -held
		}
		switch {
		case math.Abs(delta)*o < minOrder, tg.WeightPct != 0 && math.Abs(delta)*o < minOrderPct*eq: // 0 always closes
		case delta < 0:
			sells = append(sells, order{tg.Symbol, -delta})
		default:
			buys = append(buys, order{tg.Symbol, delta})
		}
	}
	for _, s := range sells {
		fills = append(fills, a.book(s.sym, -s.qty, paper.FillPrice(price(s.sym), false)))
	}
	for _, b := range buys {
		px, q := paper.FillPrice(price(b.sym), true), b.qty
		if q*px*(1+paper.FeeRate) > a.cash {
			q = a.cash / (px * (1 + paper.FeeRate))
		}
		if q*px >= minOrder {
			fills = append(fills, a.book(b.sym, q, px))
		}
	}
	return fills
}

// book applies a signed quantity at px to the agent's books.
func (a *agent) book(sym string, q, px float64) Fill {
	h := a.pos[sym]
	if h == nil {
		h = &holding{}
	}
	f := paper.Book(h.qty, h.avg, q, px)
	a.cash = max(a.cash+f.CashDelta, 0) // an all-in buy can leave -1e-12 of float noise
	if f.Qty == 0 {
		delete(a.pos, sym)
	} else {
		a.pos[sym] = &holding{f.Qty, f.AvgPrice}
	}
	side := "buy"
	if q < 0 {
		side = "sell"
	}
	return Fill{Symbol: sym, Side: side, Qty: math.Abs(q), Price: px, Notional: math.Abs(q) * px, Fee: f.Fee, RealizedPnl: f.Realized}
}

// snapshot is the agent's full state after step k at time at, marked at
// mark (B_k.C in the arena, the live price on the live desk). Holdings are
// listed in universe order, then any held asset outside it (a halted pair).
func (a *agent) snapshot(universe []string, k int, at int64, mark func(string) float64, fills []Fill) Step {
	st := Step{Step: k, T: at, Cash: a.cash, Equity: a.cash, Positions: []Position{},
		Targets: a.targets, Fills: fills, Tools: a.tools, Reasoning: a.text}
	order := slices.Clone(universe)
	for _, sym := range slices.Sorted(maps.Keys(a.pos)) {
		if !slices.Contains(order, sym) {
			order = append(order, sym)
		}
	}
	for _, sym := range order {
		if h := a.pos[sym]; h != nil {
			c := cmp.Or(mark(sym), h.avg)
			st.Equity += h.qty * c
			st.Positions = append(st.Positions, Position{Symbol: sym, Qty: h.qty, AvgPrice: h.avg, Mark: c})
		}
	}
	for i := range st.Positions {
		st.Positions[i].WeightPct = st.Positions[i].Qty * st.Positions[i].Mark / st.Equity * 100
	}
	if a.err != nil {
		msg := "Model error; held."
		if errors.Is(a.err, context.DeadlineExceeded) {
			msg = "No decision within 90 seconds; held."
		}
		st.Error = &msg
	}
	return st
}

// track benches the agent after benchAfter consecutive failed steps.
func (a *agent) track() {
	if a.err == nil {
		a.fails = 0
		return
	}
	if a.fails++; a.fails >= benchAfter {
		a.benched = true
	}
}

// benchmarks are the unranked baselines, from the tape at creation: BTC and
// an equal-weight basket of the universe, bought at B_0.O like an agent's
// fill and never rebalanced. Index 0 is the starting cash, then one mark per
// step at B_k.C.
func benchmarks(t *tape) map[string][]float64 {
	hold := func(syms []string) []float64 {
		qty := map[string]float64{}
		for _, s := range syms {
			qty[s] = startingCash / float64(len(syms)) / (paper.FillPrice(t.bar(s, 0).O, true) * (1 + paper.FeeRate))
		}
		v := []float64{startingCash}
		for k := range t.n {
			sum := 0.0
			for _, s := range syms {
				sum += qty[s] * t.bar(s, k).C
			}
			v = append(v, sum)
		}
		return v
	}
	return map[string][]float64{"btc": hold([]string{"BTC"}), "basket": hold(t.universe)}
}

// baselineFills are a baseline's entry fills: startingCash split over n
// assets, each buy paying the fee on top of its notional.
func baselineFills(n int) []Fill {
	notional := startingCash / float64(n) / (1 + paper.FeeRate)
	fills := make([]Fill, n)
	for i := range fills {
		fills[i] = Fill{Side: "buy", Notional: notional, Fee: notional * paper.FeeRate}
	}
	return fills
}

// metrics scores an equity curve: eq[0] = startingCash, then one mark per
// finished step; cash[k] is the cash at mark k. vsBtcPct/vsBasketPct are left
// to the caller.
func metrics(cadence string, iv int64, eq, cash []float64, fills []Fill) Metrics {
	s := len(eq) - 1
	st := market.Stats(eq, cadence)
	m := Metrics{TotalReturnPct: (eq[s]/eq[0] - 1) * 100, AnnualizedReturnPct: market.AnnualizedPct(eq[0], eq[s], float64(int64(s)*iv)/86400),
		Sharpe: st.Sharpe, Sortino: st.Sortino, MaxDrawdownPct: st.MaxDrawdownPct, VolatilityAnnPct: st.VolAnnPct, Fills: len(fills)}
	if a := m.AnnualizedReturnPct; a != nil && m.MaxDrawdownPct > 0 {
		c := round2(*a / m.MaxDrawdownPct)
		m.Calmar = &c
	}
	sells, wins, traded, mean := 0, 0, 0.0, 0.0
	for _, f := range fills {
		traded += f.Notional
		m.FeesUsd += f.Fee
		if f.Side == "sell" {
			sells++
			if f.RealizedPnl > 0 {
				wins++
			}
		}
	}
	if sells > 0 {
		w := round2(float64(wins) / float64(sells) * 100)
		m.WinRatePct = &w
	}
	for _, e := range eq {
		mean += e / float64(len(eq))
	}
	m.TurnoverPct = traded / mean * 100
	for k := 1; k <= s; k++ {
		m.ExposurePct += (eq[k] - cash[k]) / eq[k] * 100 / float64(s)
	}
	for _, p := range []*float64{&m.TotalReturnPct, &m.Sharpe, &m.Sortino, &m.MaxDrawdownPct, &m.VolatilityAnnPct,
		&m.TurnoverPct, &m.ExposurePct, &m.FeesUsd} {
		*p = round2(*p)
	}
	if a := m.AnnualizedReturnPct; a != nil {
		*a = round2(*a)
	}
	return m
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
