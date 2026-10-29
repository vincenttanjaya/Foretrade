package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

func forwardState(t *testing.T, f *Forward) map[string]any {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(b, &state); err != nil || state == nil {
		t.Fatalf("forward must carry its availability state: %s (%v)", b, err)
	}
	return state
}

func wantForwardReason(t *testing.T, f *Forward, reason string) {
	t.Helper()
	if f == nil || f.Metrics != nil || len(f.Equity) != 0 || len(f.Benchmark) != 0 || len(f.Trades) != 0 {
		t.Fatalf("unavailable %s run must have no measured result: %+v", reason, f)
	}
	if state := forwardState(t, f); state["reason"] != reason || state["early"] != true {
		t.Fatalf("unavailable state %v, want reason %s and early", state, reason)
	}
}

// The old draft calls a newest-only subset all performance since publication.
func TestForwardHistoryCapIsUnavailable(t *testing.T) {
	h, _ := fwdHandler(t)
	defer func(n int) { forwardMaxBars = n }(forwardMaxBars)
	forwardMaxBars = 3500
	since := markettest.End.Add(-40 * 24 * time.Hour)
	s := Strategy{ID: "cap", Spec: emaSpec(t, "15m"), CreatedAt: since, Community: &CommunityInfo{}}
	f := h.forward(t.Context(), s)
	wantForwardReason(t, f, "history_unavailable")
	if !f.Since.Equal(since) {
		t.Fatalf("history refusal shifted publication from %s to %s", since, f.Since)
	}
}

// Later bars cannot substitute for the missing anchored signal or warm-up.
func TestForwardMissingAnchoredHistoryIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		bars int
	}{
		{"listing after publication", 6000},
		{"signal exists but warm-up predates listing", 4995},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := fwdHandler(t)
			since := markettest.End.Add(-time.Duration(tc.bars) * 15 * time.Minute)
			s := Strategy{ID: tc.name, Spec: emaSpec(t, "15m"), CreatedAt: since, Community: &CommunityInfo{}}
			f := h.forward(t.Context(), s)
			wantForwardReason(t, f, "history_unavailable")
			if !f.Since.Equal(since) {
				t.Fatal("missing history changed the original since")
			}
		})
	}
}

func TestForwardTooNewHasNoInventedSourceTime(t *testing.T) {
	h, bin := fwdHandler(t)
	bin.Down("", true) // this state requires no source read
	since := time.Now().UTC().Add(time.Nanosecond)
	s := Strategy{ID: "new", Spec: emaSpec(t, "4h"), CreatedAt: since, Community: &CommunityInfo{}}
	f := h.forward(t.Context(), s)
	wantForwardReason(t, f, "too_new")
	state := forwardState(t, f)
	if !f.Since.Equal(since) || state["asOf"] != nil || state["stale"] != false {
		t.Fatalf("too-new state must keep exact save and no source time: %v", state)
	}
}

// candleRows mimics the external boundary; the replay and market client stay real.
func candleRows(c []market.Candle) [][]any {
	out := make([][]any, len(c))
	for i, k := range c {
		out[i] = []any{k.T * 1000, fmt.Sprint(k.O), fmt.Sprint(k.H), fmt.Sprint(k.L), fmt.Sprint(k.C), fmt.Sprint(k.V)}
	}
	return out
}

func forwardSource(t *testing.T, page func(*http.Request) []market.Candle) *handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/klines" {
			t.Errorf("unexpected source path: %s", r.URL.Path)
			http.Error(w, "unexpected path", 500)
			return
		}
		json.NewEncoder(w).Encode(candleRows(page(r)))
	}))
	t.Cleanup(srv.Close)
	return &handler{d: &app.Deps{Market: testMarketClient(srv.URL)}}
}

func TestForwardUsesActuallyClosedLastCandle(t *testing.T) {
	end := time.Now().Unix() / 3600 * 3600
	for _, tc := range []struct {
		name string
		last int64
		want int
	}{
		{"delayed final candle has closed", end - 2*3600, 3},
		{"current and future candles remain open", end + 3600, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bars []market.Candle
			for ts := end - 4*3600; ts <= tc.last; ts += 3600 {
				bars = append(bars, market.Candle{T: ts, O: 100, H: 101, L: 99, C: 100, V: 10})
			}
			h := forwardSource(t, func(*http.Request) []market.Candle { return bars })
			got, err := h.forwardCandles(t.Context(), smaCross(2), bars[0].T)
			if err != nil || len(got) != tc.want || !slices.Equal(got, bars[:tc.want]) {
				t.Fatalf("closed candles: %+v (%v), want first %d", got, err, tc.want)
			}
		})
	}
}

func TestForwardPagesAreOrderedAndDeduplicated(t *testing.T) {
	var bars []market.Candle
	for i := range 6 {
		bars = append(bars, market.Candle{T: 1700000000 + int64(i)*3600, O: 100, H: 101, L: 99, C: 100, V: 10})
	}
	var calls atomic.Int32
	h := forwardSource(t, func(r *http.Request) []market.Candle {
		calls.Add(1)
		if r.URL.Query().Get("endTime") == "" {
			return []market.Candle{bars[4], bars[3], bars[5], bars[4]}
		}
		return []market.Candle{bars[2], bars[0], bars[1], bars[2], bars[3]}
	})
	got, err := h.forwardCandles(t.Context(), smaCross(2), bars[0].T)
	if err != nil || !slices.Equal(got, bars) || calls.Load() != 2 {
		t.Fatalf("paged history: %+v (%v), source reads %d; want every ordered unique bar in two reads", got, err, calls.Load())
	}
}

func TestForwardHistoryGapCannotBecomeMeasuredPerformance(t *testing.T) {
	end := time.Now().Unix() / 3600 * 3600
	var bars []market.Candle
	for i := -250; i <= 0; i++ {
		if i == -3 { // inside the measured window, not merely older unavailable history
			continue
		}
		bars = append(bars, market.Candle{T: end + int64(i)*3600, O: 100, H: 101, L: 99, C: 100, V: 10})
	}
	h := forwardSource(t, func(*http.Request) []market.Candle { return bars })
	s := Strategy{ID: "gap", Spec: smaCross(2), CreatedAt: time.Unix(end-10*3600, 0), Community: &CommunityInfo{}}
	wantForwardReason(t, h.forward(t.Context(), s), "history_unavailable")
}

func TestForwardNonProgressHistoryIsUnavailable(t *testing.T) {
	end := time.Now().Unix() / 3600 * 3600
	var bars []market.Candle
	for i := -99; i <= 0; i++ {
		bars = append(bars, market.Candle{T: end + int64(i)*3600, O: 100, H: 101, L: 99, C: 100, V: 10})
	}
	var calls atomic.Int32
	h := forwardSource(t, func(*http.Request) []market.Candle {
		calls.Add(1)
		return bars // a broken source repeats the newest page for every endTime
	})
	s := Strategy{ID: "repeat", Spec: smaCross(2), CreatedAt: time.Unix(end-20*86400, 0), Community: &CommunityInfo{}}
	f := h.forward(t.Context(), s)
	wantForwardReason(t, f, "history_unavailable")
	if calls.Load() != 2 || f.AsOf == nil || f.AsOf.Unix() != end {
		t.Fatalf("non-progressing source must stop after one history page with its observed close: reads=%d forward=%+v", calls.Load(), f)
	}
}

func TestForwardPagingFailureKeepsObservedSourceTime(t *testing.T) {
	end := time.Now().Unix() / 3600 * 3600
	var bars []market.Candle
	for i := -250; i <= 0; i++ {
		bars = append(bars, market.Candle{T: end + int64(i)*3600, O: 100, H: 101, L: 99, C: 100, V: 10})
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("endTime") != "" {
			http.Error(w, "history source down", 500)
			return
		}
		json.NewEncoder(w).Encode(candleRows(bars))
	}))
	t.Cleanup(srv.Close)
	h := &handler{d: &app.Deps{Market: testMarketClient(srv.URL)}}
	s := Strategy{ID: "paging failure", Spec: smaCross(2), CreatedAt: time.Unix(end-20*86400, 0), Community: &CommunityInfo{}}
	f := h.forward(t.Context(), s)
	wantForwardReason(t, f, "market_unavailable")
	if f.AsOf == nil || f.AsOf.Unix() != end || f.Stale || calls.Load() != 2 {
		t.Fatalf("failed older page must keep the newest observed close without measuring partial history: %+v reads=%d", f.ForwardSummary, calls.Load())
	}
	h.forward(t.Context(), s)
	if calls.Load() != 2 {
		t.Fatal("failed history source was retried inside its backoff")
	}
}

func TestForwardHistoryFailureKeepsPreviousRun(t *testing.T) {
	h, _ := fwdHandler(t)
	since := markettest.End.Add(-40 * 24 * time.Hour)
	s := Strategy{ID: "history refresh", Spec: emaSpec(t, "15m"), CreatedAt: since, Community: &CommunityInfo{}}
	f := h.forward(t.Context(), s)
	if f.Metrics == nil {
		t.Fatal("initial anchored run must be measured")
	}
	e := h.fwd.entry(forwardKey(s.ID, "saved", since))
	e.at, e.next = e.at.Add(-forwardTTL), 0
	defer func(n int) { forwardMaxBars = n }(forwardMaxBars)
	forwardMaxBars = 3500
	stale := h.forward(t.Context(), s)
	if stale.Metrics != f.Metrics || stale.AsOf != f.AsOf || !stale.Stale || stale.Reason == nil || *stale.Reason != "history_unavailable" ||
		!slices.Equal(stale.Equity, f.Equity) || f.Reason != nil {
		t.Fatalf("incomplete refresh must retain the earlier source and measured result: old=%+v new=%+v", f.ForwardSummary, stale.ForwardSummary)
	}
}

func TestForwardCachedSourceLagRemainsVisible(t *testing.T) {
	h, bin := fwdHandler(t)
	bin.Down("", true)
	since := time.Now().Add(-30 * 24 * time.Hour)
	s := Strategy{ID: "cached source", Spec: emaSpec(t, "1h"), CreatedAt: since, Community: &CommunityInfo{}}
	// The result was fresh when computed, but its source has crossed a new bar
	// boundary while the 15-minute reuse policy still permits the cached run.
	oldClose := time.Unix(time.Now().Unix()/3600*3600-3600, 0).UTC()
	cached := emptyForward(since, "saved", "")
	cached.AsOf, cached.Metrics = &oldClose, &Metrics{EndT: oldClose.Unix() - 3600}
	e := h.fwd.entry(forwardKey(s.ID, "saved", since))
	e.at, e.next, e.val = time.Now(), time.Now().Unix()+3600, cached
	f := h.forward(t.Context(), s)
	if !f.Stale || f.Reason != nil || !f.AsOf.Equal(oldClose) || f.Metrics.EndT != cached.Metrics.EndT || cached.Stale {
		t.Fatalf("cache freshness hid source lag or changed the cached metadata: response=%+v cached=%+v", f.ForwardSummary, cached.ForwardSummary)
	}
}

// A successful refresh clock cannot replace the represented candle's close.
func TestForwardSourceTimeAndStaleRefreshAreResponseCopies(t *testing.T) {
	end := time.Now().Unix() / (4 * 3600) * (4 * 3600)
	var bars []market.Candle
	for i := -999; i <= 0; i++ {
		px := 100 + float64((i+999)%13)
		bars = append(bars, market.Candle{T: end + int64(i)*4*3600, O: px, H: px + 1, L: px - 1, C: px, V: 10})
	}
	var down atomic.Bool
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if down.Load() {
			http.Error(w, "down", 500)
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		json.NewEncoder(w).Encode(candleRows(bars[max(0, len(bars)-limit):]))
	}))
	t.Cleanup(srv.Close)
	h := &handler{d: &app.Deps{Market: testMarketClient(srv.URL)}}
	since := time.Unix(end-20*86400, 123000000).UTC()
	s := Strategy{ID: "freshness", Spec: emaSpec(t, "4h"), CreatedAt: since, Community: &CommunityInfo{}}
	f := h.forward(t.Context(), s)
	if f == nil || f.Metrics == nil {
		t.Fatalf("measured run missing: %+v", f)
	}
	wantAsOf := time.Unix(end, 0).UTC().Format(time.RFC3339)
	state := forwardState(t, f)
	if state["asOf"] != wantAsOf || state["stale"] != false || state["reason"] != nil || f.Metrics.EndT != end-4*3600 {
		t.Fatalf("fresh source must end at its represented close %s: %v", wantAsOf, state)
	}
	original, _ := json.Marshal(f)
	e := h.fwd.entry(forwardKey(s.ID, "saved", since))
	e.at = e.at.Add(-forwardTTL)
	// Allow a refresh despite the next bar not yet closing.
	e.next = 0
	down.Store(true)
	h.d.Market = testMarketClient(srv.URL)
	stale := h.forward(t.Context(), s)
	state = forwardState(t, stale)
	if state["asOf"] != wantAsOf || state["stale"] != true || state["reason"] != "market_unavailable" || !slices.Equal(stale.Equity, f.Equity) {
		t.Fatalf("failed refresh hid old source/result: %v", state)
	}
	if after, _ := json.Marshal(f); !slices.Equal(after, original) {
		t.Fatal("response metadata mutation changed the earlier response")
	}
	n := calls.Load()
	h.forward(context.Background(), s)
	if calls.Load() != n {
		t.Fatal("failed refresh retried before the one-minute backoff")
	}
	down.Store(false)
	e.failedAt = e.failedAt.Add(-forwardRetry)
	recovered := forwardState(t, h.forward(t.Context(), s))
	if recovered["asOf"] != wantAsOf || recovered["stale"] != false || recovered["reason"] != nil {
		t.Fatalf("recovered source retained failure metadata: %v", recovered)
	}
}
