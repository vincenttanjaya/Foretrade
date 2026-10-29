package strategy

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// ForwardSummary is how a strategy's rules did after they were fixed: the
// live-forward (out-of-sample) run next to its in-sample backtest (contract
// ForwardSummary).
type ForwardSummary struct {
	Since   time.Time  `json:"since"`
	Basis   string     `json:"basis"`   // published | saved | launch
	Metrics *Metrics   `json:"metrics"` // nil until the entire anchored window is available
	Early   bool       `json:"early"`   // no metrics, or under forwardMinDays or forwardMinTrades
	AsOf    *time.Time `json:"asOf"`    // last represented completed bar's close, never the refresh clock
	Stale   bool       `json:"stale"`
	Reason  *string    `json:"reason"` // too_new | history_unavailable | market_unavailable | timeout | busy
}

// Forward is the full run a strategy page charts (contract StrategyForward).
type Forward struct {
	ForwardSummary
	Equity    []Point `json:"equity"`
	Benchmark []Point `json:"benchmark"`
	Trades    []Trade `json:"trades"`
}

const (
	// A forward window under 30 days or with under 3 trades is early: say so
	// rather than let a lucky week read as a track record.
	forwardMinDays   = 30
	forwardMinTrades = 3

	forwardTTL     = 15 * time.Minute // a newer closed bar shows up at most this late
	forwardRetry   = time.Minute      // a failed run is not retried sooner
	forwardBudget  = 5 * time.Second  // forward enrichment returns cached or unavailable timeouts at this budget
	forwardWorkers = 6
	forwardEntries = 2000 // strict identity bound, including admitted computations
)

// forwardMaxBars caps the bars one run reads (about 104 days of 15m).
// A longer window is unavailable; a newest-only subset is not a forward run.
// ponytail: page cost grows with the window; keep older bars per pair if 15m windows grow past this.
var forwardMaxBars = 10_000

// sinceOf is when s's rules were fixed for its readers: a built-in's launch,
// a published strategy's publish (the claim the community sees), else when
// it was saved (where its stored backtest ends).
func sinceOf(s Strategy) (time.Time, string) {
	switch {
	case s.Community == nil:
		return labsCreated, "launch"
	case s.Community.PublishedAt != nil:
		return *s.Community.PublishedAt, "published"
	}
	return s.CreatedAt, "saved"
}

// lookback is the most bars back any of the spec's indicators reads.
func lookback(s Spec) int {
	n := 1
	for _, rs := range []*RuleSet{s.LongEntry, s.LongExit, s.ShortEntry, s.ShortExit} {
		if rs == nil {
			continue
		}
		for _, c := range slices.Concat(rs.All, rs.Any) {
			for _, in := range []Indicator{c.Left, c.Right} {
				n = max(n, in.Period+1, in.Slow+in.Signal)
			}
		}
	}
	return n
}

// warmBars is how many bars before the forward window warm its indicators:
// four lookbacks plus the floor reduce EMA and Wilder seed effects before replay.
func warmBars(s Spec) int { return min(max(4*lookback(s), 100), 2000) }

// firstOpen is the open time of the first bar opening at or after since.
func firstOpen(since time.Time, sec int64) int64 {
	t := since.Unix()
	if since.Nanosecond() != 0 {
		t++
	}
	return (t + sec - 1) / sec * sec
}

// forwardRun runs s live-forward from since over closed (oldest first, the
// warm-up included): flat at 100, its first signal read on the close of the
// bar since falls in (or the bar ending at since) and filled at the next
// open, the first one at or after since; buy & hold buys that same open. Nil
// until 3 bars from that bar have closed.
func forwardRun(s Spec, since time.Time, closed []market.Candle) *Result {
	sec := market.BarSeconds(s.Interval)
	i := sort.Search(len(closed), func(k int) bool { return closed[k].T >= firstOpen(since, sec)-sec })
	if len(closed)-i < 3 || closed[i].T != firstOpen(since, sec)-sec {
		return nil
	}
	res, err := backtestFrom(s, closed, i)
	if err != nil || res.Metrics.StartT != closed[i].T { // no silently shifted signal or warm-up
		return nil
	}
	return &res
}

func early(m *Metrics) bool {
	return m == nil || m.EndT-m.StartT < forwardMinDays*86400 || m.Trades < forwardMinTrades
}

var errForwardHistory = errors.New("anchored forward history unavailable")

// orderedClosed keeps every actually completed bar, including a delayed final
// row, and normalizes overlapping or reordered pages before replay.
func orderedClosed(c []market.Candle, sec, now int64) []market.Candle {
	byTime := make(map[int64]market.Candle, len(c))
	for _, k := range c {
		if k.T+sec <= now {
			byTime[k.T] = k
		}
	}
	out := make([]market.Candle, 0, len(byTime))
	for _, k := range byTime {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b market.Candle) int { return cmp.Compare(a.T, b.T) })
	return out
}

// forwardCandles returns a complete, contiguous history anchored at from.
// Partial history is returned with an error so its observed source time remains
// visible without presenting its newest subset as performance since publication.
func (h *handler) forwardCandles(ctx context.Context, s Spec, from int64) ([]market.Candle, error) {
	sec := market.BarSeconds(s.Interval)
	now := time.Now().Unix()
	latest, err := h.d.Market.Candles(ctx, s.Symbol, s.Interval, min(max(int((now-from)/sec)+2, 1), 3000, forwardMaxBars))
	if err != nil {
		return nil, err
	}
	out := orderedClosed(latest, sec, now)
	for len(out) > 0 && out[0].T > from && len(out) < forwardMaxBars {
		oldest := out[0].T
		older, err := h.d.Market.CandlesBefore(ctx, s.Symbol, s.Interval, time.Unix(out[0].T, 0),
			min(int((out[0].T-from)/sec)+1, forwardMaxBars-len(out), 3000))
		if err != nil {
			return out, err
		}
		out = orderedClosed(slices.Concat(older, out), sec, now)
		if out[0].T >= oldest { // empty, repeated or non-progressing history
			break
		}
	}
	out = out[sort.Search(len(out), func(k int) bool { return out[k].T >= from }):]
	if len(out) == 0 || out[0].T != from || len(out) > forwardMaxBars {
		return out, errForwardHistory
	}
	for i := 1; i < len(out); i++ {
		if out[i].T-out[i-1].T != sec {
			return out, errForwardHistory
		}
	}
	return out, nil
}

func emptyForward(since time.Time, basis, reason string) *Forward {
	f := &Forward{ForwardSummary: ForwardSummary{Since: since.UTC(), Basis: basis, Early: true},
		Equity: []Point{}, Benchmark: []Point{}, Trades: []Trade{}}
	if reason != "" {
		f.Reason = &reason
	}
	return f
}

// forwardResponse returns independent response metadata. Cached curves and
// metrics are immutable and retain their original source time and EndT.
func forwardResponse(f *Forward, since time.Time, basis string, sec int64, reason string) *Forward {
	if f == nil {
		return emptyForward(since, basis, reason)
	}
	copy := *f
	if f.AsOf != nil && f.AsOf.Unix() < time.Now().Unix()/sec*sec {
		copy.Stale = true
	}
	if reason != "" {
		copy.Reason = &reason
		copy.Stale = copy.Stale || copy.Metrics != nil
	}
	return &copy
}

// runForward computes s's forward from since and the unix time a newer bar
// closes (when the result can change). Nothing is fetched before 3 bars from
// the one since falls in have closed.
func (h *handler) runForward(ctx context.Context, s Spec, since time.Time, basis string) (*Forward, int64, error) {
	sec := market.BarSeconds(s.Interval)
	first := firstOpen(since, sec)
	f := emptyForward(since, basis, "too_new")
	if first+2*sec > time.Now().Unix() {
		return f, first + 2*sec, nil
	}
	closed, err := h.forwardCandles(ctx, s, first-sec-int64(warmBars(s))*sec)
	next := first + 2*sec
	if len(closed) > 0 {
		asOf := time.Unix(closed[len(closed)-1].T+sec, 0).UTC()
		f.AsOf = &asOf
		f.Stale = asOf.Unix() < time.Now().Unix()/sec*sec
		next = max(next, asOf.Unix()+sec)
	}
	if errors.Is(err, errForwardHistory) {
		reason := "history_unavailable"
		f.Reason = &reason
		return f, next, nil
	}
	if err != nil {
		reason := "market_unavailable"
		f.Reason = &reason
		return f, next, err
	}
	if res := forwardRun(s, since, closed); res != nil {
		f.Reason = nil
		f.Metrics, f.Early = &res.Metrics, early(&res.Metrics)
		f.Equity, f.Benchmark, f.Trades = res.Equity, res.Benchmark, res.Trades
	} else {
		reason := "history_unavailable"
		f.Reason = &reason
	}
	return f, next, nil
}

// forwardCache owns admitted identities and at most six computations. State
// locks never span market work; same-key readers wait on a cancellable channel.
// A reservation keeps an entry alive even while its owner waits for a worker.
type forwardCache struct {
	mu    sync.Mutex
	m     map[string]*forwardEntry
	slots chan struct{}
	clock uint64
}

type forwardEntry struct {
	mu           sync.Mutex
	at           time.Time // when val was computed
	val          *Forward  // immutable after publication
	next         int64     // unix time a newer bar closes
	failedAt     time.Time
	failedReason string
	loading      chan struct{}
	refs         int    // reservations, owned by forwardCache.mu
	used         uint64 // last admission, owned by forwardCache.mu
}

// forwardKey: a strategy's spec never changes, its since and basis do (a
// publish can land in the same second as the save, so to the microsecond).
func forwardKey(id, basis string, since time.Time) string {
	return id + "/" + basis + "/" + strconv.FormatInt(since.UnixMicro(), 10)
}

func (c *forwardCache) reserve(key string) (*forwardEntry, chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*forwardEntry{}
		c.slots = make(chan struct{}, forwardWorkers)
	}
	e := c.m[key]
	if e == nil {
		if len(c.m) >= forwardEntries {
			// Forget the least recently used inactive identity, even if its
			// result is fresh. Never lose a fetch or an admitted waiter.
			var victim string
			var oldest uint64
			for k, x := range c.m {
				if x.refs != 0 || !x.mu.TryLock() {
					continue
				}
				idle := x.loading == nil
				x.mu.Unlock()
				if idle && (victim == "" || x.used < oldest) {
					victim, oldest = k, x.used
				}
			}
			if victim == "" {
				return nil, c.slots
			}
			delete(c.m, victim)
		}
		e = &forwardEntry{}
		c.m[key] = e
	}
	c.clock++
	e.used, e.refs = c.clock, e.refs+1
	return e, c.slots
}

func (c *forwardCache) release(e *forwardEntry) {
	c.mu.Lock()
	e.refs--
	c.mu.Unlock()
}

// entry is an idle lookup used by cache diagnostics and tests. Computation
// callers hold a reservation for their entire request through get instead.
func (c *forwardCache) entry(key string) *forwardEntry {
	e, _ := c.reserve(key)
	if e != nil {
		c.release(e)
	}
	return e
}

// cached is a nonblocking deadline fallback: a slow fetch never owns a state
// lock, but a busy state transition need not delay a completed list response.
func (c *forwardCache) cached(key string) *Forward {
	c.mu.Lock()
	e := c.m[key]
	c.mu.Unlock()
	if e == nil || !e.mu.TryLock() {
		return nil
	}
	defer e.mu.Unlock()
	return e.val
}

func (c *forwardCache) get(ctx context.Context, key string, load func() (*Forward, int64, error)) (*Forward, string) {
	e, slots := c.reserve(key)
	if e == nil {
		return nil, "busy"
	}
	defer c.release(e)
	for {
		e.mu.Lock()
		now := time.Now()
		if ctx.Err() != nil {
			v := e.val
			e.mu.Unlock()
			return v, "timeout"
		}
		if now.Sub(e.failedAt) < forwardRetry {
			v, reason := e.val, e.failedReason
			e.mu.Unlock()
			return v, reason
		}
		if e.val != nil && (now.Unix() < e.next || now.Sub(e.at) < forwardTTL) {
			v := e.val
			e.mu.Unlock()
			return v, ""
		}
		if done := e.loading; done != nil {
			e.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				e.mu.Lock()
				v := e.val
				e.mu.Unlock()
				return v, "timeout"
			}
		}
		done := make(chan struct{})
		e.loading = done
		e.mu.Unlock()

		// Admission is cancellable and happens before any market work. It
		// shares the same caller budget as singleflight and lower caches.
		var f *Forward
		var next int64
		var err error
		select {
		case slots <- struct{}{}:
			if err = ctx.Err(); err == nil {
				f, next, err = load()
			}
			<-slots
		case <-ctx.Done():
			err = ctx.Err()
		}

		e.mu.Lock()
		reason := ""
		switch {
		case ctx.Err() != nil:
			// Even a non-cooperative successful loader cannot publish after
			// its owner cancels. The independent waiters may retry instead.
			reason = "timeout"
		case err != nil:
			reason = "market_unavailable"
			e.failedAt, e.failedReason = time.Now(), reason
			if f != nil && (e.val == nil || e.val.Metrics == nil) {
				e.val = f // the newest page may be available while an older page failed
			}
			slog.Warn("strategy forward run failed", "key", key, "err", err)
		case f.Reason != nil && *f.Reason != "too_new":
			reason = *f.Reason
			e.failedAt, e.failedReason = time.Now(), reason
			if e.val == nil || e.val.Metrics == nil {
				e.val = f // retain observed source time even without a measured run
			}
		default:
			e.at, e.val, e.next = time.Now(), f, next
			e.failedAt, e.failedReason = time.Time{}, ""
		}
		v := e.val
		e.loading = nil
		close(done)
		e.mu.Unlock()
		return v, reason
	}
}

// forward returns a distinct response's metadata over immutable cached data.
func (h *handler) forward(ctx context.Context, s Strategy) *Forward {
	since, basis := sinceOf(s)
	f, reason := h.fwd.get(ctx, forwardKey(s.ID, basis, since), func() (*Forward, int64, error) {
		return h.runForward(ctx, s.Spec, since, basis)
	})
	return forwardResponse(f, since, basis, market.BarSeconds(s.Spec.Interval), reason)
}

// forwards owns all writes to the returned slice. A fixed number of workers
// read an input snapshot and send results; none can mutate a delivered list.
// Its private completion handle closes after all owned workers exit. HTTP
// callers can return at the budget; verification can join work after releasing
// a controlled source without mistaking cache completion for worker completion.
func (h *handler) forwards(ctx context.Context, list []Strategy) <-chan struct{} {
	ctx, cancel := context.WithTimeout(ctx, forwardBudget)
	defer cancel()
	inputs := slices.Clone(list)
	jobs := make(chan int, len(inputs))
	for i := range inputs {
		jobs <- i
	}
	close(jobs)
	type result struct {
		i int
		f *Forward
	}
	results := make(chan result, min(len(inputs), forwardWorkers))
	var workers sync.WaitGroup
	for range min(len(inputs), forwardWorkers) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				f := h.forward(ctx, inputs[i])
				select {
				case results <- result{i, f}:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	complete := make([]bool, len(inputs))
	for n := 0; n < len(inputs); n++ {
		select {
		case r := <-results:
			list[r.i].Forward, complete[r.i] = r.f, true
		case <-ctx.Done():
			for i, s := range inputs {
				if !complete[i] {
					since, basis := sinceOf(s)
					f := h.fwd.cached(forwardKey(s.ID, basis, since))
					list[i].Forward = forwardResponse(f, since, basis, market.BarSeconds(s.Spec.Interval), "timeout")
				}
			}
			return workersDone
		}
	}
	return workersDone
}
