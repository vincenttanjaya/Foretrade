package forecast

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// oldTSM is the stored TSM report as round 9 left it: flat buckets, three
// members, no view.
func oldTSM() (Draft, Result) {
	d := tsmDraft()
	probs := probsAt(tsmOutcomes, .17, .19, .17, .16, .14, .17)
	return d, Result{Probabilities: probs, Confidence: "medium", Summary: "We stay close to the ensemble.",
		Specialists: []Specialist{{Role: "base_rate", Name: "Base-rate statistician", Probabilities: probs}, {Role: "quant", Name: "Market & quant analyst", Probabilities: probs}}}
}

// anchoredTSM is a round 9b result: a view on the measured basis, with its
// scenarios, methods and measured history.
func anchoredTSM() (Draft, Result) {
	d, r := oldTSM()
	r.Probabilities = probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20)
	r.View = deskView(d, r.Probabilities, tsmRef, 1825, ViewBasis{Kind: "capped_drift", Label: "Measured TSM history since 1997; drift +7.1%/yr"})
	r.Scenarios = sanitizeScenarios(d, r.Probabilities, tsmRef, 1825, []market.ScenarioLevel{{Name: "Bear", Kind: "bear", P: 0.3, Level: 346.2}, {Name: "Base", Kind: "base", P: 0.7, Level: 751.8}},
		nil, 0, nil, func(string) bool { return false })
	r.Methods = &Methods{Plan: "long_stock", WeightsFrom: "prior", Agreement: "diverse", Pool: r.Probabilities, Weights: []MethodWeight{{Role: "base_rate", Method: "history", Weight: 1}}}
	r.Measured = &Measured{Symbol: "TSM", Source: "Yahoo Finance daily closes (NYSE: TSM)", From: "1997-10-08", To: "2026-10-05", Sessions: 7300}
	for i := range r.Specialists {
		r.Specialists[i].Method, r.Specialists[i].Stats = "history", statsOf(d, r.Specialists[i].Probabilities, tsmRef, ptr(580.0))
	}
	return d, r
}

// TestGetDerivedView: an ordered row stored before round 9b reads with a
// derived view (no reference, no median claimed: §4.8's numbers on its
// ranges) and its members' stats; nothing is stored; a binary row has no
// view; the list's rows carry the view's brief, the stored one when present.
func TestGetDerivedView(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	d, r := oldTSM()
	id := stored(t, e, e.User, d, r, "")
	f := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast
	v := f.Result.View
	if v == nil || !v.Derived || v.Reference != nil || v.PAbove != nil || v.Basis.Kind != "none" || !within(v.Median.Value, 570.3, 0.5) || !within(v.P25.Value, 355.8, 0.5) ||
		!within(v.P75.Value, 917.0, 0.5) || !v.P10.Approx || !v.Flat || v.Headline != "view" || v.Lead != "The desk's 50% range for "+fmtDate(d.ResolutionDate)+" is $356–$917 and its 80% range ≈$247–≈$1,350." {
		t.Fatalf("derived view %+v", v)
	}
	if s := f.Result.Specialists[0].Stats; s == nil || !within(s.Median.Value, 570.3, 0.5) || s.PAbove != nil || s.StatedMedian != nil {
		t.Fatalf("derived stats %+v", s)
	}
	if len(f.History) != 1 || f.History[0].Median != nil {
		t.Fatalf("history %+v: no median without a basis", f.History)
	}
	var raw string
	if err := e.DB.QueryRow(context.Background(), `select result::text from forecasts where id = $1`, id).Scan(&raw); err != nil || strings.Contains(raw, `"view"`) || strings.Contains(raw, `"stats"`) {
		t.Fatalf("nothing is backfilled: %s %v", raw, err)
	}
	bin := stored(t, e, e.User, Draft{Shape: "binary", Question: "Will TSM close above $600?", Outcomes: yesNo, ResolutionDate: "2031-10-06"},
		Result{Probabilities: ps("Yes", 0.4, "No", 0.6)}, "")
	if f := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+bin, nil)).Forecast; f.Result.View != nil || strings.Contains(e.Do("GET", "/api/forecasts/"+bin, nil).Body.String(), `"view"`) {
		t.Fatalf("binary: %+v", f.Result.View)
	}
	ad, ar := anchoredTSM()
	ad.Question = "Where will TSM close on " + ad.ResolutionDate + "? (second)"
	fresh := stored(t, e, e.User, ad, ar, "")
	list := apptest.JSON[struct{ Forecasts []Summary }](t, e.Do("GET", "/api/forecasts", nil)).Forecasts
	views := map[string]*ViewBrief{}
	for _, s := range list {
		views[s.ID] = s.View
	}
	if b := views[id]; b == nil || b.Basis != "none" || b.PAbove != nil || !within(b.Median.Value, 570.3, 0.5) || b.Headline != "view" {
		t.Fatalf("old row brief %+v", b)
	}
	if b := views[fresh]; b == nil || b.Basis != "capped_drift" || b.PAbove == nil || !within(b.PAbove.Value, 0.621, 1e-3) || !within(b.Median.Value, 610.9, 0.5) {
		t.Fatalf("stored brief %+v", b)
	}
	if views[bin] != nil {
		t.Fatal("binary rows have no view")
	}
	body := e.Do("GET", "/api/forecasts", nil).Body.String()
	if !strings.Contains(body, `"basis":"none"`) {
		t.Fatalf("list %s", body)
	}
	g := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+fresh, nil)).Forecast
	if g.Result.View.Derived || g.Result.View.Basis.Kind != "capped_drift" || len(g.History) != 1 || g.History[0].Median == nil || !within(*g.History[0].Median, 610.9, 0.5) {
		t.Fatalf("a stored view is served as stored: %+v %+v", g.Result.View, g.History)
	}
}

// TestFlooredViewServed (the live check's P9): a run's final on the capped
// drift narrower than H1's σ, NVDA at ten years at half of it, is stored as
// the volatility floor widened it, and every readout comes from that stored
// final: GET's probabilities, view, lead and label, the list row's brief
// (chat's get_forecast passes the stored view as stored:
// TestGetForecastDeskView), with the methods record saying so. The chance
// is the stored final's own reading, so its 50% range starts below today.
func TestFlooredViewServed(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	var l liveLadder
	for _, c := range liveLadders {
		if c.name == "NVDA 10y" {
			l = c
		}
	}
	k, d, outcomes, bounds, _, _ := l.desk(t)
	d.Question = "What will be the Nasdaq closing price of NVDA on " + d.ResolutionDate + "?"
	k.d, k.fr.Plan = d, "long_stock"
	narrow := probsAt(outcomes, market.DriftMixture(l.spot, 10, 0.5*l.vol, l.mu).Buckets(l.edges)...)
	r, view := deskRun(k, []Specialist{{Role: "quant", Name: "Market & quant analyst", Method: "judgment", Probabilities: narrow}}, narrow)
	r.View, r.Summary = view, "We expect compounding."
	id := stored(t, e, e.User, d, r, "")
	f := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast
	v := f.Result.View
	fd, _ := newDist(bounds, probsOf(outcomes, f.Result.Probabilities))
	if tv(f.Result.Probabilities, r.Probabilities) > 1e-12 || tv(f.Result.Probabilities, narrow) < 0.1 || f.Result.Methods == nil || f.Result.Methods.Guard == nil ||
		*f.Result.Methods.Guard != "volatility" || f.Result.Methods.Note != volSentence {
		t.Fatalf("stored final %v, methods %+v", f.Result.Probabilities, f.Result.Methods)
	}
	if v == nil || v.Basis.Kind != "capped_drift" || v.PAbove == nil || v.PAbove.Value != 1-fd.cdf(l.spot) || !within(v.PAbove.Value, 0.70053, 1e-5) ||
		v.P25.Value >= l.spot || v.P25 != fd.quantile(0.25) || !strings.HasSuffix(v.Lead, "; 50% range $177–≈$2,080, 80% range ≈$58.9–≈$7,110; 70% chance it closes at or above $237.47.") ||
		strings.Contains(v.Basis.Label, "chance") {
		t.Fatalf("served view %+v: pAbove %+v, the stored final reads %.6f, its 25th percentile %+v", v, *v.PAbove, 1-fd.cdf(l.spot), fd.quantile(0.25))
	}
	var brief *ViewBrief
	for _, s := range apptest.JSON[struct{ Forecasts []Summary }](t, e.Do("GET", "/api/forecasts", nil)).Forecasts {
		if s.ID == id {
			brief = s.View
		}
	}
	if brief == nil || brief.Basis != "capped_drift" || brief.PAbove == nil || *brief.PAbove != *v.PAbove || brief.P10 != v.P10 || brief.Median != v.Median {
		t.Fatalf("list row %+v", brief)
	}
}

// TestHeldChanceServed (the live check's P8, now the backstop): a view on
// the capped drift stored with its chance held, NVDA at ten years with a
// final at half H1's σ as written (it reads 85% at or above today, the
// drift's lognormal 69.6%), is served as stored: GET's view and lead, and
// the list row's brief, state 70.6% (the drift's plus driftMassTol), never
// the final's reading, and the label says the chance is held. A run's final
// no longer reaches the view so narrow (TestFlooredViewServed).
func TestHeldChanceServed(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	var l liveLadder
	for _, c := range liveLadders {
		if c.name == "NVDA 10y" {
			l = c
		}
	}
	k, d, outcomes, _, ref, _ := l.desk(t)
	d.Question = "What will be the Nasdaq closing price of NVDA on " + d.ResolutionDate + "?"
	probs := probsAt(outcomes, market.DriftMixture(l.spot, 10, 0.5*l.vol, l.mu).Buckets(l.edges)...)
	r := Result{Probabilities: probs, Confidence: "medium", Summary: "We expect compounding."}
	r.View = deskView(d, probs, ref, l.days, k.basis(nil, probs))
	id := stored(t, e, e.User, d, r, "")
	const held = "; 71% chance it closes at or above $237.47."
	v := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast.Result.View
	if v == nil || v.Basis.Kind != "capped_drift" || v.PAbove == nil || !within(v.PAbove.Value, 0.7056, 1e-4) || !strings.HasSuffix(v.Lead, held) ||
		!strings.HasSuffix(v.Basis.Label, heldBothNote) {
		t.Fatalf("served view %+v", v)
	}
	var brief *ViewBrief
	for _, s := range apptest.JSON[struct{ Forecasts []Summary }](t, e.Do("GET", "/api/forecasts", nil)).Forecasts {
		if s.ID == id {
			brief = s.View
		}
	}
	if brief == nil || brief.Basis != "capped_drift" || brief.PAbove == nil || brief.PAbove.Value != v.PAbove.Value {
		t.Fatalf("list row %+v", brief)
	}
}

// fmtDate is a resolution date as a lead writes it ("Oct 6, 2031").
func fmtDate(date string) string {
	t, _ := time.Parse(time.DateOnly, date)
	return t.Format("Jan 2, 2006")
}

// TestRefreshKeepsDeskFields: POST /market rewrites the stored result; the
// round 9b fields survive it, and an older row's derived view is not stored.
func TestRefreshKeepsDeskFields(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	d, r := anchoredTSM()
	id := stored(t, e, e.User, d, r, "")
	od, or := oldTSM()
	od.Question += " (old)"
	old := stored(t, e, e.User, od, or, "")
	for _, x := range []string{id, old} {
		if w := e.Do("POST", "/api/forecasts/"+x+"/market", map[string]any{}); w.Code != 200 {
			t.Fatalf("refresh: %d %s", w.Code, w.Body)
		}
	}
	var res Result
	if err := e.DB.QueryRow(context.Background(), `select result from forecasts where id = $1`, id).Scan(&res); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal([]any{r.View, r.Scenarios, r.Methods, r.Measured, r.Specialists})
	b, _ := json.Marshal([]any{res.View, res.Scenarios, res.Methods, res.Measured, res.Specialists})
	if string(a) != string(b) || res.MarketCheckedAt == nil {
		t.Fatalf("the refresh lost round 9b fields:\n%s\n%s", a, b)
	}
	var raw string
	if err := e.DB.QueryRow(context.Background(), `select result::text from forecasts where id = $1`, old).Scan(&raw); err != nil || strings.Contains(raw, `"view"`) ||
		strings.Contains(raw, `"stats"`) || !strings.Contains(raw, "marketCheckedAt") {
		t.Fatalf("an older row's derived view was stored: %s %v", raw, err)
	}
}

// TestUpdatePriorMedian: an update's aggregator sees the previous desk
// median and 80% range (the ranges only when that view stated no median),
// and the lineage carries each run's median where its view stated one.
func TestUpdatePriorMedian(t *testing.T) {
	answer := specJSON([]float64{.17, .17, .15, .16, .15, .20}, 611)
	e := apptest.New(t, Register, apptest.Options{Reply: panel(t, map[string]llmtest.Reply{
		"base_rate": {Text: answer}, "quant": {Text: answer}, "skeptic": {Text: answer},
		"aggregator": {Text: `{"probabilities":` + mustJSON(probsAt(tsmOutcomes, .17, .17, .15, .16, .15, .20)) + `,"summary":"We expect compounding.","confidence":"medium","changeNote":"No material change."}`},
	}, false)})
	d, r := anchoredTSM()
	parent := stored(t, e, e.User, d, r, "")
	w := e.Do("POST", "/api/forecasts/"+parent+"/update", map[string]any{})
	child := apptest.JSON[forecastBody](t, w).Forecast.ID
	if w.Code != 201 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	f := waitRun(t, e, child)
	if f.Status != "completed" {
		t.Fatalf("update run: %+v", f)
	}
	var prompt string
	for _, q := range e.Bedrock.Requests() {
		if strings.HasPrefix(q.System, "You are the aggregator") {
			prompt = q.Prompt
		}
	}
	if !strings.Contains(prompt, "Previous desk median $611; 80% range $242–$1,480.") {
		t.Fatalf("aggregator prompt lacks the prior median: %s", prompt[strings.Index(prompt, "Previous desk"):])
	}
	if len(f.History) != 2 || f.History[0].Median == nil || !within(*f.History[0].Median, 610.9, 0.5) || f.History[1].Median != nil {
		t.Fatalf("lineage %+v: the methods are off in this env, so the update's view states no median", f.History)
	}
	if f.Result.View == nil || f.Result.View.Basis.Kind != "none" || f.Result.View.Derived || !strings.HasPrefix(f.Result.View.Lead, "The desk's 50% range") {
		t.Fatalf("an event-frame ordered run stores a view without a basis: %+v", f.Result.View)
	}
	if p := priorPrompt(&prior{at: frameDay, view: deskView(d, r.Probabilities, nil, 1825, noBasis)}, true); !strings.Contains(p, "Previous desk 50% range $363–$989; 80% range $242–$1,480.") {
		t.Fatalf("a prior without a basis quotes its ranges: %s", p)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The track record scores only the stored probabilities: the round 9b
// fields change nothing.
func TestTrackRecordIgnoresDeskFields(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	d, r := oldTSM()
	d.ResolutionDate = "2099-01-01"
	id := stored(t, e, e.User, d, r, "")
	if w := e.Do("POST", "/api/forecasts/"+id+"/resolve", map[string]any{"outcome": "$450 – $600"}); w.Code != 200 {
		t.Fatalf("resolve: %d %s", w.Code, w.Body)
	}
	before := e.Do("GET", "/api/forecasts/track-record", nil).Body.String()
	_, ar := anchoredTSM()
	ar.Probabilities = r.Probabilities
	if _, err := e.DB.Exec(context.Background(), `update forecasts set result = $2 where id = $1`, id, ar); err != nil {
		t.Fatal(err)
	}
	if after := e.Do("GET", "/api/forecasts/track-record", nil).Body.String(); after != before || !strings.Contains(before, `"n":1`) {
		t.Fatalf("track record changed:\n%s\n%s", before, after)
	}
}
