package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

type lifecycleGate struct {
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
	start    sync.Once
	finish   sync.Once
	response sync.Once
	calls    atomic.Int32
}

func (g *lifecycleGate) open() { g.finish.Do(func() { close(g.release) }) }

// The fetch owner really passes through market.Client and the HTTP boundary.
// Its context remains alive while another reader cancels its own wait.
func lifecycleSource(t *testing.T) (*handler, Strategy, *lifecycleGate) {
	t.Helper()
	end := time.Now().Unix() / 3600 * 3600
	var bars []market.Candle
	for i := -150; i <= 0; i++ {
		bars = append(bars, market.Candle{T: end + int64(i)*3600, O: 10, H: 11, L: 9, C: 10, V: 1})
	}
	g := &lifecycleGate{entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer g.response.Do(func() { close(g.returned) })
		if r.URL.Path != "/api/v3/klines" {
			t.Errorf("unexpected source path: %s", r.URL.Path)
			http.Error(w, "unexpected source", http.StatusInternalServerError)
			return
		}
		g.calls.Add(1)
		g.start.Do(func() { close(g.entered) })
		<-g.release
		json.NewEncoder(w).Encode(candleRows(bars))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(g.open) // open before server cleanup waits for handlers
	h := &handler{d: &app.Deps{Market: testMarketClient(srv.URL)}}
	s := Strategy{ID: "lifecycle", Spec: crossSpec(10, false), CreatedAt: time.Unix(end-4*3600, 0).UTC(), Community: &CommunityInfo{}}
	return h, s, g
}

type lifecycleObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *lifecycleObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func lifecycleWait(t *testing.T, done <-chan struct{}, name string) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(time.Second):
		t.Errorf("%s did not finish while its barrier remained held", name)
		return false
	}
}

// Quiescence is the reservation count, not an assumed delay after cancellation.
func lifecycleWaitRefs(t *testing.T, cache *forwardCache, e *forwardEntry, want int) bool {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		cache.mu.Lock()
		refs := e.refs
		cache.mu.Unlock()
		if refs == want {
			return true
		}
		select {
		case <-deadline.C:
			t.Errorf("cache reservations = %d, want %d after canceled work drained", refs, want)
			return false
		default:
			runtime.Gosched()
		}
	}
}

// A fetch-wide mutex makes a canceled same-key reader wait for somebody else's
// market request, and can substitute that later success for the timeout result.
func TestForwardCanceledSameKeyWaitKeepsOwnerResult(t *testing.T) {
	h, s, gate := lifecycleSource(t)
	ownerCtx, ownerCancel := context.WithCancel(t.Context())
	t.Cleanup(ownerCancel)
	ownerResult := make(chan *Forward, 1)
	go func() { ownerResult <- h.forward(ownerCtx, s) }()
	if !lifecycleWait(t, gate.entered, "owner source request") {
		return
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	observed := &lifecycleObservedContext{Context: ctx, observed: make(chan struct{})}
	waiterResult := make(chan *Forward, 1)
	go func() { waiterResult <- h.forward(observed, s) }()
	lifecycleWait(t, observed.observed, "same-key reader reaching a cancellable wait")
	cancel()
	var waiter *Forward
	select {
	case waiter = <-waiterResult:
	case <-time.After(time.Second):
		t.Error("canceled same-key reader waited for the unrelated fetch owner")
		gate.open()
		select {
		case waiter = <-waiterResult:
		case <-time.After(time.Second):
			t.Fatal("same-key reader did not drain after releasing its owner")
		}
	}
	if waiter == nil || waiter.Reason == nil || *waiter.Reason != "timeout" || waiter.Metrics != nil || waiter.AsOf != nil {
		t.Errorf("canceled reader must return an unavailable timeout before the owner completes: %+v", waiter)
	}
	gate.open()
	var owner *Forward
	select {
	case owner = <-ownerResult:
	case <-time.After(time.Second):
		t.Fatal("independent fetch owner did not drain")
	}
	if owner == nil || owner.Metrics == nil || owner.Reason != nil {
		t.Fatalf("canceling the waiter damaged the owner's measured result: %+v", owner)
	}
	cached := h.forward(t.Context(), s)
	if cached == nil || cached.Metrics == nil || cached.Reason != nil || *cached.Metrics != *owner.Metrics || !slices.Equal(cached.Equity, owner.Equity) {
		t.Errorf("waiter cancellation damaged the owner's cached result: %+v", cached)
	}
	e := h.fwd.entry(forwardKey(s.ID, "saved", s.CreatedAt))
	if !e.failedAt.IsZero() {
		t.Error("a canceled waiter was recorded as a market failure")
	}
}

// An idle cache full of recently completed entries must evict rather than grow.
func TestForwardCacheFreshEntriesStayBounded(t *testing.T) {
	var cache forwardCache
	now := time.Now()
	for i := 0; i < 2001; i++ {
		e := cache.entry(fmt.Sprintf("fresh/%d", i))
		if e == nil {
			t.Fatal("completed inactive entries must leave room for a new identity")
		}
		e.at = now
		e.val = emptyForward(now, "saved", "too_new")
	}
	if n := len(cache.m); n > 2000 {
		t.Errorf("fresh entries grew the cache to %d identities; the maximum is 2000", n)
	}
}

// The old cache's soft limit grows when every entry is occupied; eviction of
// one of those occupied identities would also lose its singleflight ownership.
func TestForwardCacheActiveEntriesStayBounded(t *testing.T) {
	var cache forwardCache
	var held []*forwardEntry
	t.Cleanup(func() {
		for _, e := range held {
			e.mu.Unlock()
		}
	})
	for i := 0; i < 2000; i++ {
		e := cache.entry(fmt.Sprintf("active/%d", i))
		if e == nil {
			t.Fatal("cache refused a slot below its capacity")
		}
		e.mu.Lock()
		held = append(held, e)
	}
	if got := cache.entry("overflow"); got != nil {
		t.Error("cache admitted a new identity while all 2000 existing identities were occupied")
	}
	if n := len(cache.m); n != 2000 {
		t.Errorf("occupied cache has %d identities, want 2000", n)
	}
	for i, e := range held {
		if got := cache.entry(fmt.Sprintf("active/%d", i)); got != e {
			t.Fatalf("occupied identity %d lost its original computation", i)
		}
	}
}

// Waiting behind an unrelated request for the same rules counts against the
// list's own five-second budget. Its response belongs to that completed read.
func TestForwardListDeadlineIncludesSameKeyWait(t *testing.T) {
	h, s, gate := lifecycleSource(t)
	ownerCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	ownerResult := make(chan *Forward, 1)
	go func() { ownerResult <- h.forward(ownerCtx, s) }()
	if !lifecycleWait(t, gate.entered, "owner source request") {
		return
	}
	list := []Strategy{s}
	done := make(chan struct{})
	var workersDone <-chan struct{}
	go func() {
		workersDone = h.forwards(t.Context(), list)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Error("list exceeded its five-second budget while waiting for an unrelated fetch owner")
		gate.open()
		if !lifecycleWait(t, done, "list draining after its owner completed") {
			return
		}
	}
	f := list[0].Forward
	if f == nil || f.Reason == nil || *f.Reason != "timeout" || f.Metrics != nil || !f.Since.Equal(s.CreatedAt) {
		t.Errorf("expired list must return the original window as unavailable timeout: %+v", f)
	}
	e := h.fwd.entry(forwardKey(s.ID, "saved", s.CreatedAt))
	lifecycleWaitRefs(t, &h.fwd, e, 1) // only the independently gated owner remains
	before, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	gate.open()
	select {
	case owner := <-ownerResult:
		if owner == nil || owner.Metrics == nil {
			t.Fatalf("independent owner did not complete: %+v", owner)
		}
	case <-time.After(time.Second):
		t.Fatal("independent owner did not drain")
	}
	lifecycleWaitRefs(t, &h.fwd, e, 0)
	if !lifecycleWait(t, workersDone, "all same-key list workers exiting") {
		return
	}
	// A later successful read may use the independent owner's cache; it must
	// not change the timeout response already delivered to the earlier list.
	if cached := h.forward(t.Context(), s); cached == nil || cached.Metrics == nil {
		t.Fatalf("later reader lost the independent owner's result: %+v", cached)
	}
	after, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after, before) {
		t.Error("later successful computation mutated a list already returned at its deadline")
	}
}

type lifecycleCacheResult struct {
	value  *Forward
	reason string
}

func lifecycleValue(value float64) *Forward {
	return &Forward{ForwardSummary: ForwardSummary{Basis: "saved", Metrics: &Metrics{TotalReturnPct: value}},
		Equity: []Point{}, Benchmark: []Point{}, Trades: []Trade{}}
}

// Reserve all six real cache admission slots; callers of get still use its
// ownership, loading channels, cancellation and publication machinery.
func lifecycleBlockWorkers(t *testing.T, cache *forwardCache) (func(), <-chan lifecycleCacheResult) {
	t.Helper()
	entered := make(chan struct{}, 6)
	gate := make(chan struct{})
	results := make(chan lifecycleCacheResult, 6)
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	for i := 0; i < 6; i++ {
		go func() {
			v, reason := cache.get(t.Context(), fmt.Sprintf("worker-owner/%d", i), func() (*Forward, int64, error) {
				entered <- struct{}{}
				<-gate
				return lifecycleValue(7), time.Now().Add(time.Hour).Unix(), nil
			})
			results <- lifecycleCacheResult{v, reason}
		}()
	}
	for i := 0; i < 6; i++ {
		if !lifecycleWait(t, entered, "six independent admitted loaders") {
			t.Fatal("could not establish the admission barrier")
		}
	}
	return release, results
}

func lifecycleJoinWorkers(t *testing.T, release func(), results <-chan lifecycleCacheResult) {
	t.Helper()
	release()
	for i := 0; i < 6; i++ {
		select {
		case r := <-results:
			if r.reason != "" || r.value == nil || r.value.Metrics == nil || r.value.Metrics.TotalReturnPct != 7 {
				t.Errorf("waiter cancellation damaged an independently admitted loader: %+v", r)
			}
		case <-time.After(time.Second):
			t.Fatal("independently admitted loader did not drain")
		}
	}
}

// Omitting the post-load context check publishes an apparently successful
// canceled run or records the caller's cancellation as an upstream failure.
func TestForwardCacheCanceledSuccessfulLoadCannotPublish(t *testing.T) {
	for _, keepMeasured := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%v", keepMeasured), func(t *testing.T) {
			var cache forwardCache
			e := cache.entry("canceled-success")
			var before *Forward
			if keepMeasured {
				before = lifecycleValue(7)
				e.val = before
				e.at = time.Now().Add(-time.Hour)
			}
			originalAt := e.at
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			v, reason := cache.get(ctx, "canceled-success", func() (*Forward, int64, error) {
				cancel() // cancellation precedes a successful loader return deterministically
				return lifecycleValue(99), time.Now().Add(time.Hour).Unix(), nil
			})
			if reason != "timeout" || v != before {
				t.Errorf("canceled successful load = %+v / %q, want prior value / timeout", v, reason)
			}
			e.mu.Lock()
			if e.val != before || !e.at.Equal(originalAt) || !e.failedAt.IsZero() || e.failedReason != "" || e.loading != nil {
				t.Error("canceled successful loader published data, changed freshness, or recorded a market failure")
			}
			e.mu.Unlock()
			lifecycleWaitRefs(t, &cache, e, 0)
			fresh, retryReason := cache.get(t.Context(), "canceled-success", func() (*Forward, int64, error) {
				return lifecycleValue(9), time.Now().Add(time.Hour).Unix(), nil
			})
			if retryReason != "" || fresh == nil || fresh.Metrics == nil || fresh.Metrics.TotalReturnPct != 9 {
				t.Errorf("canceled run prevented an immediate fresh computation: %+v / %q", fresh, retryReason)
			}
		})
	}
}

// An uncancellable admission send, or a seventh slot, starts market work
// after its caller has given up while the six independent owners remain alive.
func TestForwardCacheWorkerAdmissionHonorsCancellation(t *testing.T) {
	var cache forwardCache
	release, owners := lifecycleBlockWorkers(t, &cache)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := &lifecycleObservedContext{Context: ctx, observed: make(chan struct{})}
	loaded := make(chan struct{}, 1)
	result := make(chan lifecycleCacheResult, 1)
	go func() {
		v, reason := cache.get(observed, "seventh", func() (*Forward, int64, error) {
			loaded <- struct{}{}
			return lifecycleValue(99), 0, nil
		})
		result <- lifecycleCacheResult{v, reason}
	}()
	lifecycleWait(t, observed.observed, "seventh reader reaching global admission")
	cancel()
	select {
	case r := <-result:
		if r.value != nil || r.reason != "timeout" {
			t.Errorf("queued seventh reader = %+v, want unavailable timeout", r)
		}
	case <-time.After(time.Second):
		t.Error("queued seventh reader could not cancel while six owners held admission")
		release()
		select {
		case <-result:
		case <-time.After(time.Second):
			t.Fatal("seventh reader did not drain")
		}
	}
	select {
	case <-loaded:
		t.Error("a seventh loader ran while six independent owners occupied all slots")
	default:
	}
	e := cache.entry("seventh")
	lifecycleWaitRefs(t, &cache, e, 0)
	e.mu.Lock()
	if e.val != nil || !e.failedAt.IsZero() || e.loading != nil {
		t.Error("canceled admission published a run or recorded a market failure")
	}
	e.mu.Unlock()
	lifecycleJoinWorkers(t, release, owners)
}

// Reservation ownership must enforce the hard bound even when no state mutex
// is held. A full cache still admits readers of an already active identity.
func TestForwardCacheReservedIdentitiesRejectOverflow(t *testing.T) {
	var cache forwardCache
	var held []*forwardEntry
	t.Cleanup(func() {
		for _, e := range held {
			if e != nil {
				cache.release(e)
			}
		}
	})
	for i := 0; i < 2000; i++ {
		e, _ := cache.reserve(fmt.Sprintf("reserved/%d", i))
		if e == nil {
			t.Fatal("cache refused a reservation below 2000 identities")
		}
		held = append(held, e)
	}
	loaded := false
	v, reason := cache.get(t.Context(), "overflow", func() (*Forward, int64, error) {
		loaded = true
		return lifecycleValue(99), 0, nil
	})
	if v != nil || reason != "busy" || loaded || len(cache.m) != 2000 {
		t.Errorf("reserved cache overflow = %+v / %q, loaded=%v, entries=%d; want busy with 2000 identities", v, reason, loaded, len(cache.m))
	}
	for i, want := range held {
		e, _ := cache.reserve(fmt.Sprintf("reserved/%d", i))
		if e != want {
			t.Fatalf("full cache lost active reservation %d", i)
		}
		cache.release(e)
	}
	v, reason = cache.get(t.Context(), "reserved/0", func() (*Forward, int64, error) {
		return lifecycleValue(7), 0, nil
	})
	if reason != "" || v == nil || v.Metrics == nil || v.Metrics.TotalReturnPct != 7 {
		t.Errorf("full cache refused an existing reserved identity: %+v / %q", v, reason)
	}
	cache.release(held[0])
	held[0] = nil
	newEntry, _ := cache.reserve("overflow")
	if newEntry == nil || len(cache.m) != 2000 {
		t.Fatal("releasing the only inactive identity must permit one bounded admission")
	}
	held[0] = newEntry
	for i := 1; i < 2000; i++ {
		if cache.m[fmt.Sprintf("reserved/%d", i)] != held[i] {
			t.Fatalf("new admission evicted still-reserved identity %d", i)
		}
	}
}

// Returning a list at cancellation cannot authorize its worker to publish a
// later successful source response or mutate the list already delivered.
func TestForwardCanceledListOwnerDoesNotPublishOrMutate(t *testing.T) {
	h, s, gate := lifecycleSource(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	list := []Strategy{s}
	done := make(chan struct{})
	var workersDone <-chan struct{}
	go func() {
		workersDone = h.forwards(ctx, list)
		close(done)
	}()
	if !lifecycleWait(t, gate.entered, "list-owned source request") {
		return
	}
	e := h.fwd.entry(forwardKey(s.ID, "saved", s.CreatedAt))
	e.mu.Lock()
	loading := e.loading
	e.mu.Unlock()
	if loading == nil {
		t.Fatal("list-owned computation must hold loading ownership at the source barrier")
	}
	cancel()
	if !lifecycleWait(t, done, "canceled list response") {
		return
	}
	wantForwardReason(t, list[0].Forward, "timeout")
	before, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if !lifecycleWait(t, loading, "canceled list loader") || !lifecycleWaitRefs(t, &h.fwd, e, 0) {
		return
	}
	e.mu.Lock()
	if e.val != nil || !e.at.IsZero() || !e.failedAt.IsZero() || e.loading != nil {
		t.Error("canceled list owner published cache data or a market failure")
	}
	e.mu.Unlock()
	gate.open()
	if !lifecycleWait(t, gate.returned, "source returning after list cancellation") {
		return
	}
	if !lifecycleWait(t, workersDone, "all canceled list workers exiting") {
		return
	}
	after, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after, before) {
		t.Error("late source response mutated the canceled list")
	}
	fresh := h.forward(t.Context(), s)
	if fresh == nil || fresh.Metrics == nil || fresh.Reason != nil || gate.calls.Load() != 2 {
		t.Errorf("canceled owner was cached or backed off instead of fetched afresh: %+v, source calls %d", fresh, gate.calls.Load())
	}
	after, err = json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after, before) {
		t.Error("a later measured read mutated the canceled list")
	}
}

// Admission waiting consumes the list's own five seconds across all missing
// rows; independent owners cannot authorize those rows to fetch after expiry.
func TestForwardListDeadlineIncludesGlobalAdmission(t *testing.T) {
	h, s, gate := lifecycleSource(t)
	release, owners := lifecycleBlockWorkers(t, &h.fwd)
	list := make([]Strategy, 8)
	for i := range list {
		list[i] = s
		list[i].ID = fmt.Sprintf("queued-list/%d", i)
	}
	done := make(chan struct{})
	var workersDone <-chan struct{}
	go func() {
		workersDone = h.forwards(t.Context(), list)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Error("list exceeded five seconds while six independent computations occupied global admission")
		release()
		gate.open()
		if !lifecycleWait(t, done, "admission-blocked list draining") {
			return
		}
	}
	for _, row := range list {
		wantForwardReason(t, row.Forward, "timeout")
		if !row.Forward.Since.Equal(s.CreatedAt) || row.Forward.AsOf != nil {
			t.Errorf("admission timeout changed window or invented source time: %+v", row.Forward)
		}
		key := forwardKey(row.ID, "saved", row.CreatedAt)
		h.fwd.mu.Lock()
		e := h.fwd.m[key]
		h.fwd.mu.Unlock()
		if e != nil {
			e.mu.Lock()
			loading := e.loading
			e.mu.Unlock()
			if loading != nil {
				lifecycleWait(t, loading, "expired list admission")
			}
			lifecycleWaitRefs(t, &h.fwd, e, 0)
			e.mu.Lock()
			if e.val != nil || !e.failedAt.IsZero() || e.loading != nil {
				t.Error("expired list admission published a run or market failure")
			}
			e.mu.Unlock()
		}
	}
	if n := gate.calls.Load(); n != 0 {
		t.Errorf("admission-blocked list started %d upstream requests before owners released", n)
	}
	before, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleJoinWorkers(t, release, owners)
	if !lifecycleWait(t, workersDone, "all admission-blocked list workers exiting") {
		return
	}
	after, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after, before) || gate.calls.Load() != 0 {
		t.Error("releasing independent admission owners resumed or mutated an expired list")
	}
}

// Forward retrieval must retain the shared market cache's cancellable wait;
// changing its context to Background would block behind the live fetch owner.
func TestForwardSharedMarketCacheWaitHonorsCancellation(t *testing.T) {
	h, s, gate := lifecycleSource(t)
	ownerCtx, ownerCancel := context.WithCancel(t.Context())
	t.Cleanup(ownerCancel)
	ownerResult := make(chan error, 1)
	go func() {
		_, err := h.d.Market.Candles(ownerCtx, "BTC", "1h", 1000)
		ownerResult <- err
	}()
	if !lifecycleWait(t, gate.entered, "shared market source request") {
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	observed := &lifecycleObservedContext{Context: ctx, observed: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, _, err := h.runForward(observed, s.Spec, s.CreatedAt, "saved")
		result <- err
	}()
	if !lifecycleWait(t, observed.observed, "forward reader reaching the shared market cache wait") {
		cancel()
		gate.open()
		return
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("shared cache waiter error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Error("forward reader ignored cancellation while the shared market fetch remained blocked")
		gate.open()
		select {
		case <-result:
		case <-time.After(time.Second):
			t.Fatal("forward reader did not drain after releasing the shared source")
		}
	}
	gate.open()
	select {
	case err := <-ownerResult:
		if err != nil {
			t.Errorf("waiter cancellation damaged the shared market owner: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shared market owner did not drain")
	}
	if n := gate.calls.Load(); n != 1 {
		t.Errorf("same market cache key issued %d source requests, want one surviving owner's request", n)
	}
}
