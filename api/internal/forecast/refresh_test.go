package forecast

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// stored inserts a completed forecast of user's with this result; parent may be "".
func stored(t *testing.T, e *apptest.Env, user string, d Draft, res Result, parent string) string {
	t.Helper()
	var p *string
	if parent != "" {
		p = &parent
	}
	var id string
	if err := e.DB.QueryRow(context.Background(), `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result,
		completed_at, price_rule, parent_id) values ($1, $2, $3, $4, $5, 'completed', $6, now(), $7, $8) returning id`,
		user, d.Shape, d.Question, d.Outcomes, d.ResolutionDate, res, d.PriceRule, p).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

var nvdaDraft = Draft{Shape: "categorical", Question: "Which company will be the largest by market cap on 2026-12-31?",
	Outcomes: []string{"Nvidia", "Apple", "Another company"}, ResolutionDate: "2099-12-31"}

func nvdaResult() Result {
	return Result{Probabilities: ps("Nvidia", 0.81, "Apple", 0.15, "Another company", 0.04), Confidence: "medium", Market: nvdaMarket()}
}

type forecastBody struct{ Forecast Forecast }

// POST /market re-prices the stored market from Gamma and stores it; a closed
// market or Gamma down keeps the stored odds (200); only the caller's own
// completed report qualifies (an ordered one: handlers_test).
func TestRefreshMarket(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	yes, closed := 0.88, false
	e.PolymarketBase = gammaEvents(t, "largest-company-end-of-december-2026", func() []map[string]any {
		return []map[string]any{gammaMarket(nvdaQ, "2027-01-01T00:00:00Z", yes, 1.4e6, func(m map[string]any) { m["closed"] = closed })}
	})
	deep := nvdaResult() // the deep research fields are part of the stored result and survive the read-modify-write
	deep.Sources = []Source{{ID: "S1", Title: "Nvidia tops $5tn", URL: "https://reuters.com/n", Source: "Reuters"}}
	deep.Findings = []Finding{{ID: "F1", Claim: "Nvidia's market cap passed $5tn on 1 October.", SourceIDs: []string{"S1"}, AsOf: ptr("2026-10-01"),
		Angle: "A1", Direction: "Nvidia", Strength: "moderate"},
		{ID: "F2", Claim: "The user's note ranks Nvidia first.", SourceIDs: []string{"D1"}, Direction: "Nvidia", Strength: "slight", Origin: "material", Ref: "p. 2"}}
	deep.Evidence = []Evidence{{Claim: "The user's note ranks Nvidia first.", Source: "Your material · note.pdf", Stance: "supports", AsOf: "2026-09", MaterialID: "D1"}}
	deep.Watch = []Watch{{Signal: "Q3 earnings", Detail: "Nvidia reports in November.", Branches: []Branch{{"Beat", "Nvidia firmer", "Nvidia"}}}}
	deep.Research = Research{Queries: []string{"q"}, Status: "live", Depth: "deep", Rounds: 2, Searches: 17, Candidates: 40,
		Angles: []Angle{{ID: "A1", Kind: "comparables", Label: "Past leaders", Queries: []string{"q"}, Findings: 1}}}
	id := stored(t, e, e.User, nvdaDraft, deep, "")
	note := []Material{readMaterial("D1", "note.pdf")}
	if _, err := e.DB.Exec(context.Background(), `update forecasts set materials = $2 where id = $1`, id, note); err != nil {
		t.Fatal(err)
	}

	w := e.Do("POST", "/api/forecasts/"+id+"/market", map[string]any{})
	f := apptest.JSON[forecastBody](t, w).Forecast
	m := f.Result.Market
	if w.Code != 200 || m.YesPrice != 0.88 || m.EdgePp != -7 || m.Signal != "no" || time.Since(m.AsOf) > time.Minute || *m.VolumeUSD != 1.4e6 ||
		f.ID != id || len(f.History) != 1 || strings.Contains(w.Body.String(), "marketCheckedAt") {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	if got := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast; len(got.Materials) != 1 || got.Materials[0].Summary != note[0].Summary ||
		len(got.Result.Findings) != 2 || got.Result.Findings[1].Origin != "material" || got.Result.Findings[1].Ref != "p. 2" || got.Result.Evidence[0].MaterialID != "D1" {
		t.Fatalf("the refresh lost the materials or their findings and evidence: %+v %+v", got.Materials, got.Result.Findings)
	}
	if got := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast.Result; len(got.Findings) != 2 || got.Findings[0].Direction != "Nvidia" ||
		*got.Findings[0].AsOf != "2026-10-01" || len(got.Watch) != 1 || got.Watch[0].Branches[0].If != "Beat" || got.Research.Depth != "deep" ||
		got.Research.Rounds != 2 || got.Research.Candidates != 40 || got.Research.Angles[0].Findings != 1 {
		t.Fatalf("the refresh lost deep research fields: %+v %+v %+v", got.Research, got.Findings, got.Watch)
	}
	if got := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast.Result.Market; got.YesPrice != 0.88 || !got.AsOf.Equal(m.AsOf) {
		t.Fatalf("not stored: %+v", got)
	}

	closed, yes = true, 0.5
	if w := e.Do("POST", "/api/forecasts/"+id+"/market", map[string]any{}); w.Code != 200 ||
		apptest.JSON[forecastBody](t, w).Forecast.Result.Market.YesPrice != 0.88 {
		t.Fatalf("closed market keeps the stored odds: %d %s", w.Code, w.Body)
	}
	e.PolymarketBase = "http://127.0.0.1:1"
	if w := e.Do("POST", "/api/forecasts/"+id+"/market", map[string]any{}); w.Code != 200 ||
		apptest.JSON[forecastBody](t, w).Forecast.Result.Market.YesPrice != 0.88 {
		t.Fatalf("gamma down keeps the stored odds: %d %s", w.Code, w.Body)
	}

	other := e.Session(storetest.User(t, e.DB))
	running, _ := (&handler{e.Deps}).insert(context.Background(), e.User, nvdaDraft, nil, nil)
	archived := stored(t, e, e.User, nvdaDraft, nvdaResult(), "")
	if _, err := e.DB.Exec(context.Background(), `update forecasts set archived_from_status = status, status = 'archived' where id = $1`, archived); err != nil {
		t.Fatal(err)
	}
	var replay string
	if err := e.DB.QueryRow(context.Background(), `insert into forecasts (shape, question, outcomes, resolution_date, status, result, as_of, anchor_p, replay_key)
		values ('binary', 'Replay?', '["Yes","No"]', '2026-09-01', 'completed', '{}', '2026-08-01', 0.5, $1) returning id`, "r5-"+e.User).Scan(&replay); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, id string
		as       bool // as the other user
		code     int
		msg      string
	}{
		{"another user's", id, true, 404, ""},
		{"unknown", "00000000-0000-0000-0000-000000000000", false, 404, ""},
		{"replay", replay, false, 404, ""},
		{"archived", archived, false, 404, ""},
		{"running", running.ID, false, 409, "Only a completed report has market odds."},
	} {
		cookie := e.Cookie
		if c.as {
			cookie = other
		}
		if w := e.DoAs(cookie, "POST", "/api/forecasts/"+c.id+"/market", map[string]any{}); w.Code != c.code || !strings.Contains(w.Body.String(), c.msg) {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
}

// With no market (or rematch: true) POST /market searches again: the query
// writer and the judge, at 0 credits, then marketCheckedAt; within 10 minutes
// of that search it answers from the stored result with no model call.
func TestRematchGate(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		noQueries, llmtest.Reply{Text: `{"index": 0, "match": "exact"}`},
		noQueries, llmtest.Reply{Text: `{"index": null}`},
	)})
	base, seen := gamma(t, map[string][]map[string]any{"fed-decision-in-december": {
		gammaMarket("Fed decreases interest rates by 25 bps after December 2099 meeting?", "2099-12-10T12:00:00Z", 0.61, 1e6)}})
	e.PolymarketBase = base
	d := Draft{Shape: "binary", Question: "Will the Fed cut rates by 25 bps at its December 2099 meeting?", Outcomes: yesNo, ResolutionDate: "2099-12-10"}
	id := stored(t, e, e.User, d, Result{Probabilities: ps("Yes", 0.7, "No", 0.3), Confidence: "medium"}, "")
	before := credits(t, e)

	f := apptest.JSON[forecastBody](t, e.Do("POST", "/api/forecasts/"+id+"/market", map[string]any{})).Forecast
	r := f.Result
	if r.Market == nil || r.Market.YesPrice != 0.61 || r.Market.EdgePp != 9 || r.Market.Signal != "yes" || r.MarketCheckedAt == nil ||
		time.Since(*r.MarketCheckedAt) > time.Minute || len(e.Bedrock.Requests()) != 2 || len(seen()) != 1 {
		t.Fatalf("matched: %+v (%d model calls)", r, len(e.Bedrock.Requests()))
	}
	if credits(t, e) != before {
		t.Fatalf("a rematch costs no credits: %d → %d", before, credits(t, e))
	}
	// Within 10 minutes: the stored result, no model call, no search.
	if w := e.Do("POST", "/api/forecasts/"+id+"/market", map[string]any{"rematch": true}); w.Code != 200 ||
		apptest.JSON[forecastBody](t, w).Forecast.Result.Market.YesPrice != 0.61 || len(e.Bedrock.Requests()) != 2 || len(seen()) != 1 {
		t.Fatalf("gated: %d %s", w.Code, w.Body)
	}
	if _, err := e.DB.Exec(context.Background(), `update forecasts set result = jsonb_set(result, '{marketCheckedAt}', to_jsonb($2::text)) where id = $1`,
		id, time.Now().Add(-11*time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	r = apptest.JSON[forecastBody](t, e.Do("POST", "/api/forecasts/"+id+"/market", map[string]any{"rematch": true})).Forecast.Result
	if r.Market != nil || r.MarketCheckedAt == nil || time.Since(*r.MarketCheckedAt) > time.Minute || len(e.Bedrock.Requests()) != 4 {
		t.Fatalf("searched again after 10 minutes (the judge found none): %+v", r)
	}
}
