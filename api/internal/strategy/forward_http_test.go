package strategy

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

func TestForwardSourceAndFailureOnReads(t *testing.T) {
	var h *handler
	e := apptest.New(t, func(mux *http.ServeMux, d *app.Deps) {
		h = &handler{d: d}
		mux.Handle("GET /api/strategies", d.Auth.Require(http.HandlerFunc(h.list)))
		mux.Handle("GET /api/strategies/{id}", d.Auth.Require(http.HandlerFunc(h.get)))
	}, apptest.Options{})
	id := saved(t, e, e.User, "Source", 12, 1.2, 400)
	// Postgres retains microseconds. The eligible open follows this publication.
	since := markettest.End.Add(-20 * 24 * time.Hour).Add(123 * time.Microsecond)
	if _, err := e.DB.Exec(t.Context(), "update strategies set created_at=$2 where id=$1", id, since); err != nil {
		t.Fatal(err)
	}
	read := func() Strategy {
		t.Helper()
		w := e.Do("GET", "/api/strategies/"+id, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("detail: %d %s", w.Code, w.Body)
		}
		return apptest.JSON[got](t, w).Strategy
	}
	original := read()
	f := original.Forward
	wantAsOf := markettest.End.Add(4 * time.Hour)
	if f == nil || f.Metrics == nil || !f.Since.Equal(since) || f.AsOf == nil || !f.AsOf.Equal(wantAsOf) || !f.Stale || f.Reason != nil ||
		f.Metrics.StartT != since.Unix() || f.Metrics.EndT != markettest.End.Unix() || original.Metrics.TotalReturnPct != 7 {
		t.Fatalf("source close and independent saved backtest: %+v, backtest %+v", f, original.Metrics)
	}
	for _, tr := range f.Trades {
		if time.Unix(tr.EntryT, 0).Before(since) {
			t.Fatalf("HTTP response admits pre-publication fill: %+v before %s", tr, since)
		}
	}
	entry := h.fwd.entry(forwardKey(id, "saved", since))
	entry.at = entry.at.Add(-forwardTTL)
	entry.next = 0
	e.Binance.Down("", true)
	h.d.Market = testMarketClient(e.Binance.URL)
	stale := read()
	if stale.Forward.Reason == nil || *stale.Forward.Reason != "market_unavailable" || !stale.Forward.Stale ||
		!stale.Forward.AsOf.Equal(wantAsOf) || stale.Forward.Metrics.EndT != f.Metrics.EndT || !reflect.DeepEqual(stale.Forward.Equity, f.Equity) ||
		!reflect.DeepEqual(stale.Metrics, original.Metrics) {
		t.Fatalf("failed detail refresh replaced measured history: %+v", stale)
	}
	w := e.Do("GET", "/api/strategies?scope=mine", nil)
	list := apptest.JSON[struct{ Strategies []Summary }](t, w).Strategies
	if w.Code != http.StatusOK || len(list) != 1 || list[0].Forward == nil {
		t.Fatalf("summary: %d %s", w.Code, w.Body)
	}
	sum := list[0].Forward
	if !sum.Since.Equal(since) || sum.AsOf == nil || !sum.AsOf.Equal(wantAsOf) || !sum.Stale || sum.Reason == nil || *sum.Reason != "market_unavailable" ||
		!reflect.DeepEqual(sum.Metrics, f.Metrics) || !reflect.DeepEqual(list[0].Metrics, original.Metrics) {
		t.Fatalf("summary lost independent source or failure metadata: %+v", list[0])
	}
	if f.Reason != nil { // caller-owned metadata cannot be retroactively changed
		t.Fatal("later read mutated earlier HTTP result")
	}
}

func TestForwardMissingHistoryOnReads(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	id := saved(t, e, e.User, "Missing history", 12, 1.2, 400)
	since := markettest.End.Add(-900 * 24 * time.Hour).Add(987 * time.Microsecond)
	if _, err := e.DB.Exec(context.Background(), "update strategies set created_at=$2 where id=$1", id, since); err != nil {
		t.Fatal(err)
	}
	w := e.Do("GET", "/api/strategies/"+id, nil)
	s := apptest.JSON[got](t, w).Strategy
	if w.Code != http.StatusOK {
		t.Fatalf("detail unavailable: %d %s", w.Code, w.Body)
	}
	wantForwardReason(t, s.Forward, "history_unavailable")
	if !s.Forward.Since.Equal(since) || s.Forward.AsOf == nil || !s.Forward.AsOf.Equal(markettest.End.Add(4*time.Hour)) ||
		s.Metrics.TotalReturnPct != 7 {
		t.Fatalf("unavailable forward shifted anchor or replaced backtest: %+v", s)
	}
	w = e.Do("GET", "/api/strategies?scope=mine", nil)
	l := apptest.JSON[struct{ Strategies []Summary }](t, w).Strategies
	if w.Code != http.StatusOK || len(l) != 1 || l[0].Forward == nil || l[0].Forward.Metrics != nil || l[0].Forward.Reason == nil ||
		*l[0].Forward.Reason != "history_unavailable" || !l[0].Forward.Since.Equal(since) || !l[0].Forward.AsOf.Equal(*s.Forward.AsOf) || l[0].Metrics.TotalReturnPct != 7 {
		t.Fatalf("summary must preserve exact unavailable window and source: %d %s", w.Code, w.Body)
	}
}
