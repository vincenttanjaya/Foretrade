package research

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The index is fetched once per hour at most; a failed refresh keeps the last
// good value, and nothing fetched yet reads nil.
func TestFearGreed(t *testing.T) {
	var calls atomic.Int32
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"name":"Fear and Greed Index","data":[{"value":"71","value_classification":"Greed","timestamp":"1759449600"}]}`)
	}))
	defer srv.Close()
	s := New(Config{}, nil, nil)
	s.fngURL = srv.URL
	ctx := context.Background()

	fail.Store(true)
	if fg := s.FearGreed(ctx); fg != nil {
		t.Fatalf("never fetched: %+v", fg)
	}
	s.pulse.at = time.Time{} // the hour is up
	fail.Store(false)
	want := FearGreed{Value: 71, Label: "Greed", AsOf: time.Unix(1759449600, 0).UTC(), Source: "alternative.me"}
	if fg := s.FearGreed(ctx); fg == nil || *fg != want {
		t.Fatalf("first fetch: %+v", fg)
	}
	if fg := s.FearGreed(ctx); fg == nil || *fg != want || calls.Load() != 2 {
		t.Fatalf("second call within the hour: %+v, %d upstream calls", fg, calls.Load())
	}
	s.pulse.at = time.Now().Add(-2 * time.Hour)
	fail.Store(true)
	if fg := s.FearGreed(ctx); fg == nil || *fg != want || calls.Load() != 3 {
		t.Fatalf("failed refresh: %+v, %d upstream calls", fg, calls.Load())
	}
}
