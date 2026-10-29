package paper

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// Missing marks make book weights estimates; an unknown basket comparison
// must add its own cause without erasing that valuation warning.
func TestOverlapPreservesEstimatedWeightsAndUnknownComparison(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	initial := NewEngine(pool, testMarketClient(bin.URL))
	if _, _, err := initial.Order(ctx, user, OrderRequest{Symbol: "APT", Side: "buy", Notional: ptr(1000)}); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/klines" && r.URL.Query().Get("symbol") == "SOLUSDT" {
			http.Error(w, "basket history unavailable", http.StatusInternalServerError)
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, bin.URL+r.URL.RequestURI(), nil)
		if err != nil {
			t.Error(err)
			return
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			http.Error(w, "fake unavailable", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if r.URL.Path != "/api/v3/ticker/24hr" {
			io.Copy(w, response.Body)
			return
		}
		var rows []map[string]any
		if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
			t.Error(err)
			return
		}
		filtered := []map[string]any{}
		for _, row := range rows {
			if row["symbol"] != "APTUSDT" {
				filtered = append(filtered, row)
			}
		}
		if err := json.NewEncoder(w).Encode(filtered); err != nil {
			t.Error(err)
		}
	}))
	defer proxy.Close()
	e := NewEngine(pool, testMarketClient(proxy.URL))
	review, err := e.Review(ctx, user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if review.Coverage.Reason == nil || !strings.Contains(*review.Coverage.Reason, "entry-price estimates") {
		t.Fatalf("fixture lost missing-mark disclosure: %+v", review.Coverage)
	}
	got, err := e.Overlap(ctx, user, map[string]float64{"SOL": .5, "BTC": .5}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Coverage.Reason == nil {
		t.Fatal("combined coverage reason missing")
	}
	reason := *got.Coverage.Reason
	if !strings.Contains(reason, *review.Coverage.Reason) || !strings.Contains(reason, "basket-to-book correlations are unavailable") {
		t.Fatalf("combined causes not preserved: %q", reason)
	}
	if got.Revision != review.Revision || len(got.Holdings) != 1 || got.Holdings[0].Symbol != "APT" || got.Holdings[0].BookPct != 100 {
		t.Fatalf("captured estimated book changed: %+v", got)
	}
	if !slices.Contains(got.Coverage.MissingSymbols, "APT") || !slices.Contains(got.Coverage.MissingSymbols, "SOL") {
		t.Fatalf("combined missing symbols lost: %+v", got.Coverage)
	}
	for _, pair := range got.Correlations {
		if pair.A == "SOL" && (pair.Rho != nil || pair.Days != 0) {
			t.Fatalf("unavailable comparison invented risk: %+v", pair)
		}
	}
	if review.BetaBtc != nil || review.VaR != nil || review.Drawdown30dPct != nil {
		t.Fatal("missing-mark risk fixture was not suppressed")
	}
}
