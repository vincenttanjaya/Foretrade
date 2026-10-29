package forecast

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// bars are daily candles opening on consecutive days from first, with these closes.
func bars(first string, closes ...float64) []market.Candle {
	out := make([]market.Candle, len(closes))
	for i, c := range closes {
		out[i] = market.Candle{T: day(first).AddDate(0, 0, i).Unix(), C: c}
	}
	return out
}

func TestDecide(t *testing.T) {
	from, res := day("2026-10-01"), day("2026-10-03")
	after := day("2026-10-05") // every candle through the resolution date has closed
	closeOf := func(d string) time.Time { return day(d).Add(24*time.Hour - time.Millisecond) }
	for _, c := range []struct {
		name    string
		rule    PriceRule
		candles []market.Candle
		now     time.Time
		want    string // "" = undecided
		at      string
	}{
		{"on_date above", PriceRule{"SOL", "above", 130, "on_date"}, bars("2026-10-01", 150, 150, 131), after, "Yes", "2026-10-03"},
		{"on_date below", PriceRule{"SOL", "below", 130, "on_date"}, bars("2026-10-01", 100, 100, 131), after, "No", "2026-10-03"},
		{"on_date equality is No", PriceRule{"SOL", "above", 130, "on_date"}, bars("2026-10-01", 1, 1, 130), after, "No", "2026-10-03"},
		{"on_date below Yes", PriceRule{"SOL", "below", 130, "on_date"}, bars("2026-10-01", 200, 200, 129.9), after, "Yes", "2026-10-03"},
		{"on_date candle still open", PriceRule{"SOL", "above", 130, "on_date"}, bars("2026-10-01", 1, 1, 200), day("2026-10-03").Add(23 * time.Hour), "", ""},
		{"by_date early Yes", PriceRule{"SOL", "above", 130, "by_date"}, bars("2026-10-01", 120, 140), day("2026-10-03").Add(time.Hour), "Yes", "2026-10-02"},
		{"by_date pending", PriceRule{"SOL", "above", 130, "by_date"}, bars("2026-10-01", 120, 125), day("2026-10-03").Add(time.Hour), "", ""},
		{"by_date No after the date", PriceRule{"SOL", "above", 130, "by_date"}, bars("2026-10-01", 120, 125, 130), after, "No", "2026-10-03"},
		{"by_date below", PriceRule{"SOL", "below", 100, "by_date"}, bars("2026-09-30", 50, 120, 99, 120), after, "Yes", "2026-10-02"},
		{"missing candle", PriceRule{"SOL", "above", 130, "by_date"}, append(bars("2026-10-01", 120), bars("2026-10-03", 140)...), after, "", ""},
		{"missing resolution candle", PriceRule{"SOL", "above", 130, "on_date"}, bars("2026-10-01", 140, 140), after, "", ""},
	} {
		got, at, ok := decide(c.rule, c.candles, from, res, c.now)
		if got != c.want || ok != (c.want != "") || (ok && !at.Equal(closeOf(c.at))) {
			t.Errorf("%s: %q %v %v", c.name, got, at, ok)
		}
	}
}

func TestScore(t *testing.T) {
	yesNo := []string{"Yes", "No"}
	bin := func(yes float64, outcome string) resolved {
		return resolved{shape: "binary", outcomes: yesNo, probs: ps("Yes", yes, "No", 1-yes), outcome: outcome}
	}
	if tr := score(nil); tr.N != 0 || tr.Brier != nil || tr.AnchorBrier != nil || tr.HitRate != nil || len(tr.Bins) != 5 ||
		tr.Bins[4].Lo != 0.8 || tr.Bins[4].Hi != 1 || tr.Bins[0].MeanP != nil {
		t.Fatalf("empty record: %+v", tr)
	}

	// Binary: Brier is the classic (p − o)², baseline 0.25.
	tr := score([]resolved{bin(0.7, "Yes"), bin(0.2, "Yes")})
	if tr.N != 2 || !near(*tr.Brier, (0.09+0.64)/2) || !near(*tr.BaselineBrier, 0.25) || !near(*tr.HitRate, 0.5) ||
		!near(*tr.LogLoss, -(math.Log(0.7)+math.Log(0.2))/2) || tr.AnchorBrier != nil {
		t.Fatalf("binary: %+v", tr)
	}
	// Bin edges: 0.2 lands in the second bin, 1.0 in the last; one pair per binary forecast.
	tr = score([]resolved{bin(0.2, "No"), bin(1.0, "Yes"), bin(0.99, "No")})
	if tr.Bins[0].N != 0 || tr.Bins[1].N != 1 || tr.Bins[4].N != 2 || !near(*tr.Bins[4].MeanP, 0.995) || !near(*tr.Bins[4].Observed, 0.5) ||
		!near(*tr.Bins[1].Observed, 0) || tr.Bins[2].Observed != nil {
		t.Fatalf("bins: %+v", tr.Bins)
	}

	// Three outcomes: (1/3)·Σ(p − o)², baseline 2/9, one reliability pair per outcome, log loss floored at 0.01.
	three := resolved{shape: "categorical", outcomes: []string{"A", "B", "C"}, probs: ps("A", 0.5, "B", 0.5, "C", 0.0), outcome: "C"}
	tr = score([]resolved{three})
	if !near(*tr.Brier, (0.25+0.25+1)/3) || !near(*tr.BaselineBrier, 2.0/9) || !near(*tr.LogLoss, -math.Log(0.01)) || *tr.HitRate != 0 ||
		tr.Bins[0].N != 1 || tr.Bins[2].N != 2 || !near(*tr.Bins[0].Observed, 1) {
		t.Fatalf("categorical: %+v", tr)
	}

	// Anchor Brier on the rows with an anchor: mean (anchorP − o_yes)².
	a, b := bin(0.6, "Yes"), bin(0.6, "No")
	a.anchorP, b.anchorP = ptr(0.7), ptr(0.4)
	if tr := score([]resolved{a, b}); tr.AnchorBrier == nil || !near(*tr.AnchorBrier, (0.09+0.16)/2) || tr.AnchorN != 2 ||
		!near(*tr.AnchorSkill, 1-(0.16+0.36)/(0.09+0.16)) {
		t.Fatalf("anchor: %+v", tr)
	}
	if tr := score([]resolved{bin(0.6, "Yes")}); tr.AnchorBrier != nil || tr.AnchorN != 0 || tr.AnchorSkill != nil {
		t.Fatalf("no anchor, no anchor fields: %+v", tr)
	}
}

// Round 8b: the anchor fields pair the desk with the anchor on the anchored
// binary rows only (a user's record mixes runs with and without R1), while
// brier and skill stay over every row against the coin flip.
func TestScorePairsTheAnchoredSubset(t *testing.T) {
	bin := func(yes float64, outcome string, anchor *float64) resolved {
		return resolved{shape: "binary", outcomes: []string{"Yes", "No"}, probs: ps("Yes", yes, "No", 1-yes), outcome: outcome, anchorP: anchor}
	}
	rows := []resolved{bin(0.75, "Yes", ptr(0.81)), bin(0.3, "No", ptr(0.27)), bin(0.9, "No", nil),
		{shape: "categorical", outcomes: []string{"A", "B", "C"}, probs: ps("A", 0.5, "B", 0.3, "C", 0.2), outcome: "A", anchorP: ptr(0.9)}}
	tr := score(rows)
	desk, anchor := (0.0625+0.09)/2, (0.0361+0.0729)/2
	if tr.N != 4 || tr.AnchorN != 2 || !near(*tr.AnchorBrier, anchor) || !near(*tr.AnchorSkill, 1-desk/anchor) || tr.AnchorDiffCI90 != nil {
		t.Fatalf("paired subset: %+v", tr)
	}
	all := (0.0625 + 0.09 + 0.81 + (0.25+0.09+0.04)/3) / 4
	if !near(*tr.Brier, all) || !near(*tr.Skill, 1-all/((0.25*3+2.0/9)/4)) {
		t.Fatalf("brier and skill stay over every row: %+v", tr)
	}
	// Level with the anchor on the pairs: skill 0, whatever the unpaired rows score.
	if tr := score([]resolved{bin(0.8, "Yes", ptr(0.8)), bin(0.1, "Yes", nil)}); !near(*tr.AnchorSkill, 0) || tr.AnchorN != 1 {
		t.Fatalf("level: %+v", tr)
	}
}

// The honest headline: skill against the coin flip and bootstrap intervals
// that are stable between calls, contain the sample mean and need n ≥ 5.
// Round 8b: the claim says nothing until enough questions resolve, then
// ahead, behind or no_edge by where the basis's whole 90% range lies, in
// either direction; skill only when ahead.
func TestClaim(t *testing.T) {
	rows := func(n int, yes float64, outcome string, anchor *float64) []resolved {
		var rs []resolved
		for range n {
			rs = append(rs, resolved{shape: "binary", outcomes: yesNo, probs: ps("Yes", yes, "No", 1-yes), outcome: outcome, anchorP: anchor})
		}
		return rs
	}
	cat := func(n int) []resolved {
		var rs []resolved
		for range n {
			rs = append(rs, resolved{shape: "categorical", outcomes: []string{"A", "B", "C"}, probs: ps("A", 0.8, "B", 0.1, "C", 0.1), outcome: "A"})
		}
		return rs
	}
	for _, c := range []struct {
		name   string
		rows   []resolved
		status string
		basis  string // "" for null
		n      int
	}{
		{"nothing resolved", nil, "too_few", "", 0},
		{"49 anchored rows: too few, however good", rows(49, 0.9, "Yes", ptr(0.7)), "too_few", "", 49},
		{"sharper than the formula on every pair", rows(50, 0.9, "Yes", ptr(0.7)), "ahead", "formula", 50},
		{"behind the formula on every pair", rows(50, 0.6, "Yes", ptr(0.7)), "behind", "formula", 50},
		{"pairs either side of the formula: no edge", slices.Concat(rows(40, 0.9, "Yes", ptr(0.7)), rows(10, 0.9, "No", ptr(0.7))), "no_edge", "formula", 50},
		{"no anchors: vs the uniform forecast", slices.Concat(rows(30, 0.9, "Yes", nil), cat(20)), "ahead", "uniform", 50},
		{"no anchors, worse than uniform", rows(60, 0.1, "Yes", nil), "behind", "uniform", 60},
		{"no anchors, either side of uniform", slices.Concat(rows(35, 0.9, "Yes", nil), rows(15, 0.9, "No", nil)), "no_edge", "uniform", 50},
		{"mixed: some anchored rows, too few of them", slices.Concat(rows(60, 0.9, "Yes", nil), rows(10, 0.9, "Yes", ptr(0.7))), "too_few", "", 10},
		{"mixed: enough anchored rows, scored on the pairs", slices.Concat(rows(30, 0.1, "Yes", nil), rows(50, 0.9, "Yes", ptr(0.7))), "ahead", "formula", 50},
	} {
		tr := score(c.rows)
		cl := tr.Claim
		if cl.Status != c.status || (cl.Basis == nil) != (c.basis == "") || cl.Basis != nil && *cl.Basis != c.basis || cl.N != c.n || cl.MinN != 50 {
			t.Errorf("%s: %+v (basis %v)", c.name, cl, cl.Basis)
			continue
		}
		switch {
		case c.status != "ahead" && cl.Skill != nil:
			t.Errorf("%s: a skill without an edge: %v", c.name, *cl.Skill)
		case c.status == "ahead" && c.basis == "formula" && (cl.Skill == nil || *cl.Skill != *tr.AnchorSkill || *cl.Skill <= 0):
			t.Errorf("%s: formula skill %v", c.name, cl.Skill)
		case c.status == "ahead" && c.basis == "uniform" && (cl.Skill == nil || *cl.Skill != *tr.Skill):
			t.Errorf("%s: uniform skill %v", c.name, cl.Skill)
		}
	}

	// The stored replay seed: 24 anchored questions, so no claim yet.
	var seed []SeedRow
	if err := json.Unmarshal(replaySeed, &seed); err != nil {
		t.Fatal(err)
	}
	var rs []resolved
	for _, r := range seed {
		rs = append(rs, resolved{shape: r.Draft.Shape, outcomes: r.Draft.Outcomes, probs: r.Result.Probabilities, outcome: *r.Outcome, anchorP: ptr(r.AnchorP)})
	}
	b, _ := json.Marshal(score(rs).Claim)
	if string(b) != `{"status":"too_few","basis":null,"minN":50,"n":24,"skill":null}` {
		t.Fatalf("seed claim: %s", b)
	}
}

func TestSkillAndIntervals(t *testing.T) {
	bin := func(yes float64, outcome string, anchor float64) resolved {
		return resolved{shape: "binary", outcomes: []string{"Yes", "No"}, probs: ps("Yes", yes, "No", 1-yes), outcome: outcome, anchorP: &anchor}
	}
	var rows []resolved
	for i := range 12 {
		rows = append(rows, bin(0.7, []string{"Yes", "No", "Yes"}[i%3], 0.6))
	}
	tr := score(rows)
	brier := (8*0.09 + 4*0.49) / 12.0
	if !near(*tr.Skill, 1-brier/0.25) || tr.BrierCI90 == nil || tr.AnchorDiffCI90 == nil {
		t.Fatalf("record: %+v", tr)
	}
	lo, hi := tr.BrierCI90[0], tr.BrierCI90[1]
	if !(lo < brier && brier < hi) || lo < 0.09 || hi > 0.49 {
		t.Fatalf("Brier interval [%v, %v] around %v", lo, hi, brier)
	}
	gap := brier - (8*0.16+4*0.36)/12.0
	if d := tr.AnchorDiffCI90; !(d[0] < gap && gap < d[1]) {
		t.Fatalf("anchor gap interval %v around %v", d, gap)
	}
	if again := score(rows); *again.BrierCI90 != *tr.BrierCI90 || *again.AnchorDiffCI90 != *tr.AnchorDiffCI90 {
		t.Fatal("intervals must be deterministic (fixed seed)")
	}
	plain := slices.Clone(rows)
	for i := range plain {
		plain[i].anchorP = nil
	}
	if tr := score(plain); tr.AnchorDiffCI90 != nil || tr.BrierCI90 == nil || tr.AnchorN != 0 {
		t.Fatalf("anchorDiffCi90 needs anchors: %+v", tr)
	}
	if tr := score(rows[:4]); tr.BrierCI90 != nil || tr.AnchorDiffCI90 != nil || tr.Skill == nil {
		t.Fatalf("n < 5: %+v", tr)
	}
	if tr := score(nil); tr.Skill != nil {
		t.Fatalf("n = 0: %+v", tr)
	}
	if tr := score([]resolved{bin(0.1, "Yes", 0.5)}); !near(*tr.Skill, 1-0.81/0.25) {
		t.Fatalf("worse than a coin flip is negative skill: %v", *tr.Skill)
	}
}

func TestPriceRuleDraftAndValidate(t *testing.T) {
	today := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	rule := &PriceRule{Symbol: " sol", Op: "Above", Threshold: 130, Mode: "on_date"}
	d := repair(Draft{Shape: "binary", Question: "Will the Binance SOL/USDT daily close on 2026-12-31 be above $130?", ResolutionDate: "2026-12-31",
		PriceRule: rule}, "sol 130", today)
	if d.PriceRule == nil || *d.PriceRule != (PriceRule{"SOL", "above", 130, "on_date"}) {
		t.Fatalf("valid rule: %+v", d.PriceRule)
	}
	for name, r := range map[string]PriceRule{
		"unknown symbol": {"SHIB", "above", 1, "on_date"}, "bad op": {"SOL", "crosses", 1, "on_date"}, "bad mode": {"SOL", "above", 1, "eventually"},
		"zero": {"SOL", "above", 0, "by_date"}, "infinite": {"SOL", "above", math.Inf(1), "by_date"}, "NaN": {"SOL", "above", math.NaN(), "by_date"},
	} {
		if d := repair(Draft{Shape: "binary", PriceRule: &r}, "x", today); d.PriceRule != nil {
			t.Errorf("%s kept: %+v", name, d.PriceRule)
		}
	}
	if d := repair(Draft{Shape: "categorical", Outcomes: []string{"A", "B"}, PriceRule: rule}, "x", today); d.PriceRule != nil {
		t.Fatal("a non-binary question has no price rule")
	}

	ok := Draft{Shape: "binary", Question: "Will SOL close above $130 on 2026-12-31?", Outcomes: []string{"Yes", "No"}, ResolutionDate: "2026-12-31",
		PriceRule: &PriceRule{"sol", "below", 130, "by_date"}}
	if err := validate(&ok, today); err != nil || ok.PriceRule.Symbol != "SOL" {
		t.Fatalf("valid: %v %+v", err, ok.PriceRule)
	}
	for _, d := range []Draft{
		{Shape: "binary", Question: ok.Question, Outcomes: []string{"Yes", "No"}, ResolutionDate: "2026-12-31", PriceRule: &PriceRule{"SHIB", "above", 1, "on_date"}},
		{Shape: "categorical", Question: ok.Question, Outcomes: []string{"A", "B"}, ResolutionDate: "2026-12-31", PriceRule: &PriceRule{"SOL", "above", 1, "on_date"}},
	} {
		var bad *app.BadRequest
		if err := validate(&d, today); !errors.As(err, &bad) || bad.Msg != "priceRule is invalid." {
			t.Errorf("%s rule: %v", d.Shape, err)
		}
	}
}

func TestInsertSeedIdempotent(t *testing.T) {
	pool := storetest.Schema(t)
	ctx := context.Background()
	qs, err := ReplayQuestions(ctx, testMarketClient(markettest.New(t).URL), day("2026-06-30"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := json.Marshal(seedRows(qs[:3]))
	for range 2 {
		if _, err := insertSeed(ctx, pool, seed); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	var created time.Time
	if err := pool.QueryRow(ctx, "select count(*), max(created_at) from forecasts where user_id is null and status = 'completed'").Scan(&n, &created); err != nil {
		t.Fatal(err)
	}
	// qs[:3] is BTC at the three dates, i = 0..2: the newest is 2026-09-09 00:02 UTC.
	if n != 3 || !created.Equal(day("2026-09-09").Add(2*time.Minute)) {
		t.Fatalf("seed rows: %d, newest %v", n, created)
	}
	if n, err := insertSeed(ctx, pool, []byte("[]")); err != nil || n != 0 {
		t.Fatalf("empty seed: %d %v", n, err)
	}
}

// seedRows turns replay questions into seed rows with a scripted result: P(Yes) = 0.6.
func seedRows(qs []ReplayQuestion) []SeedRow {
	out := make([]SeedRow, len(qs))
	for i, q := range qs {
		out[i] = SeedRow{Key: q.Key, AsOf: q.AsOf.Format(time.DateOnly), Draft: q.Draft, PriceRule: *q.Draft.PriceRule,
			Result: Result{Probabilities: ps("Yes", 0.6, "No", 0.4), Summary: "We lean Yes."}, Outcome: q.Outcome, ResolvedAt: q.ResolvedAt, AnchorP: q.AnchorP}
	}
	return out
}

// completed inserts a completed forecast of user's with P(first outcome) = p,
// created two days before markettest's newest bar (2026-09-29).
func completed(t *testing.T, e *apptest.Env, user string, d Draft, p float64) string {
	t.Helper()
	probs := []OutcomeP{{d.Outcomes[0], p}}
	for _, o := range d.Outcomes[1:] {
		probs = append(probs, OutcomeP{o, (1 - p) / float64(len(d.Outcomes)-1)})
	}
	var id string
	if err := e.DB.QueryRow(context.Background(), `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result,
		completed_at, price_rule, created_at) values ($1, $2, $3, $4, $5, 'completed', $6, now(), $7, $8) returning id`,
		user, d.Shape, d.Question, d.Outcomes, d.ResolutionDate, Result{Probabilities: probs}, d.PriceRule,
		markettest.End.AddDate(0, 0, -2).Add(5*time.Hour)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestResolveAndTrackRecord(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	qs, err := ReplayQuestions(ctx, e.Market, e.LLM.KnowledgeCutoff(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := json.Marshal(seedRows(qs))
	if _, err := insertSeed(ctx, e.DB, seed); err != nil {
		t.Fatal(err)
	}

	type record struct{ Replay, Mine TrackRecord }
	w := e.Do("GET", "/api/forecasts/track-record", nil)
	tr := apptest.JSON[record](t, w)
	yes := 0
	for _, q := range qs {
		if *q.Outcome == "Yes" {
			yes++
		}
	}
	wantBrier := (float64(yes)*0.16 + float64(24-yes)*0.36) / 24
	if w.Code != 200 || tr.Replay.N != 24 || !near(*tr.Replay.Brier, wantBrier) || *tr.Replay.BaselineBrier != 0.25 || tr.Replay.AnchorBrier == nil ||
		tr.Replay.Bins[3].N != 24 || tr.Mine.N != 0 || tr.Mine.Brier != nil || len(tr.Mine.Bins) != 5 {
		t.Fatalf("track record: %d %s", w.Code, w.Body)
	}
	if tr.Replay.Skill == nil || !near(*tr.Replay.Skill, 1-wantBrier/0.25) || tr.Replay.BrierCI90 == nil || tr.Replay.AnchorDiffCI90 == nil {
		t.Fatalf("replay skill and intervals: %s", w.Body)
	}
	if c := tr.Replay.Claim; c.Status != "too_few" || c.N != 24 || c.MinN != 50 || c.Basis != nil || c.Skill != nil || tr.Mine.Claim.Status != "too_few" || tr.Mine.Claim.N != 0 {
		t.Fatalf("claims: %s", w.Body)
	}
	for _, k := range []string{`"anchorBrier":null`, `"meanP":null`, `"lo":0.8,"hi":1`, `"skill":null,"brierCi90":null,"anchorDiffCi90":null`,
		`"claim":{"status":"too_few","basis":null,"minN":50,"n":24,"skill":null}`, `"anchorN":0,"anchorSkill":null,"claim":{"status":"too_few","basis":null,"minN":50,"n":0,"skill":null}`} {
		if !strings.Contains(w.Body.String(), k) {
			t.Errorf("track record JSON lacks %s: %s", k, w.Body)
		}
	}

	// Replay rows: listed under scope=replay, readable by anyone, never archived.
	list := apptest.JSON[struct{ Forecasts []Summary }](t, e.Do("GET", "/api/forecasts?scope=replay", nil)).Forecasts
	if len(list) != 24 || !list[0].Replay || list[0].ResolvedOutcome == nil || list[0].Headline == nil || list[0].Headline.P != 0.6 {
		t.Fatalf("replay list: %+v", list)
	}
	if mine := apptest.JSON[struct{ Forecasts []Summary }](t, e.Do("GET", "/api/forecasts", nil)).Forecasts; len(mine) != 0 {
		t.Fatalf("replay rows leak into mine: %+v", mine)
	}
	if w := e.Do("GET", "/api/forecasts?scope=everyone", nil); w.Code != 400 {
		t.Fatalf("bad scope: %d", w.Code)
	}
	other := e.Session(storetest.User(t, e.DB))
	w = e.DoAs(other, "GET", "/api/forecasts/"+list[0].ID, nil)
	f := apptest.JSON[struct{ Forecast Forecast }](t, w).Forecast
	if w.Code != 200 || f.Replay == nil || f.Replay.AsOf != "2026-09-09" || f.Resolution == nil || f.Resolution.By != "auto" || f.PriceRule == nil ||
		f.Resolution.ResolvedAt.UTC().Format(time.DateOnly) != "2026-09-23" {
		t.Fatalf("replay row for another user: %d %s", w.Code, w.Body)
	}
	if w := e.DoAs(other, "DELETE", "/api/forecasts/"+list[0].ID, nil); w.Code != 404 {
		t.Fatalf("archive a replay row: %d", w.Code)
	}
	if w := e.Do("POST", "/api/forecasts/"+list[0].ID+"/resolve", map[string]any{"outcome": "No"}); w.Code != 404 {
		t.Fatalf("resolve a replay row: %d", w.Code)
	}

	// Manual resolution of the user's own forecasts.
	cats := Draft{Shape: "categorical", Question: "Which central bank cuts first?", Outcomes: []string{"Fed", "ECB", "BoE"}, ResolutionDate: "2099-01-01"}
	id := completed(t, e, e.User, cats, 0.5)
	for _, c := range []struct {
		who  string
		body any
		code int
	}{
		{"other", map[string]any{"outcome": "Fed"}, 404},
		{"me", map[string]any{"outcome": "SNB"}, 400},
		{"me", `{"outcome":`, 400},
		{"me", map[string]any{"outcome": "ECB"}, 200},
	} {
		cookie := e.Cookie
		if c.who == "other" {
			cookie = other
		}
		if w := e.DoAs(cookie, "POST", "/api/forecasts/"+id+"/resolve", c.body); w.Code != c.code {
			t.Errorf("resolve %v as %s: %d %s", c.body, c.who, w.Code, w.Body)
		}
	}
	w = e.Do("GET", "/api/forecasts/"+id, nil)
	f = apptest.JSON[struct{ Forecast Forecast }](t, w).Forecast
	if f.Resolution == nil || f.Resolution.Outcome != "ECB" || f.Resolution.By != "manual" || f.Replay != nil {
		t.Fatalf("resolved: %s", w.Body)
	}
	tr = apptest.JSON[record](t, e.Do("GET", "/api/forecasts/track-record", nil))
	if tr.Mine.N != 1 || !near(*tr.Mine.Brier, (0.25+0.5625+0.0625)/3) || *tr.Mine.HitRate != 0 || tr.Mine.AnchorBrier != nil || tr.Replay.N != 24 {
		t.Fatalf("mine after resolving: %+v", tr.Mine)
	}
	if w := e.Do("POST", "/api/forecasts/"+id+"/resolve", map[string]any{"outcome": nil}); w.Code != 200 || !strings.Contains(w.Body.String(), `"resolution":null`) {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	if tr := apptest.JSON[record](t, e.Do("GET", "/api/forecasts/track-record", nil)); tr.Mine.N != 0 {
		t.Fatalf("cleared resolution still scored: %+v", tr.Mine)
	}

	// A price rule resolves itself; a running forecast cannot be resolved.
	sol := Draft{Shape: "binary", Question: "Will SOL close above $130?", Outcomes: []string{"Yes", "No"}, ResolutionDate: "2026-09-29",
		PriceRule: &PriceRule{"SOL", "above", 130, "on_date"}}
	auto := completed(t, e, e.User, sol, 0.7)
	if w := e.Do("POST", "/api/forecasts/"+auto+"/resolve", map[string]any{"outcome": "Yes"}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "This forecast resolves automatically from Binance data.") {
		t.Fatalf("price rule: %d %s", w.Code, w.Body)
	}
	running, _ := (&handler{e.Deps}).insert(ctx, e.User, cats, nil, nil)
	if w := e.Do("POST", "/api/forecasts/"+running.ID+"/resolve", map[string]any{"outcome": "Fed"}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "Only a completed forecast can be resolved.") {
		t.Fatalf("running: %d %s", w.Code, w.Body)
	}

	// The resolver settles due price rules from the fake Binance closes (markettest's newest bar opens 2026-10-01).
	byDate := sol
	byDate.ResolutionDate, byDate.PriceRule = "2099-01-01", &PriceRule{"SOL", "below", 1e9, "by_date"}
	early := completed(t, e, e.User, byDate, 0.4)
	future := sol
	future.ResolutionDate = "2099-01-01"
	pending := completed(t, e, e.User, future, 0.4)
	n, err := (&handler{e.Deps}).resolveDue(ctx, markettest.End.Add(time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("resolveDue: %d %v", n, err)
	}
	want := "No"
	if markettest.Close("SOLUSDT", 2) > 130 { // the 2026-09-29 bar
		want = "Yes"
	}
	for id, outcome := range map[string]string{auto: want, early: "Yes", pending: ""} {
		f := apptest.JSON[struct{ Forecast Forecast }](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast
		if got := ""; f.Resolution != nil {
			got = f.Resolution.Outcome
			if outcome != got || f.Resolution.By != "auto" {
				t.Errorf("%s resolved %+v, want %q", f.ResolutionDate, f.Resolution, outcome)
			}
		} else if outcome != "" {
			t.Errorf("%s unresolved, want %q", f.Question, outcome)
		}
	}
	if f := apptest.JSON[struct{ Forecast Forecast }](t, e.Do("GET", "/api/forecasts/"+early, nil)).Forecast; f.Resolution == nil ||
		f.Resolution.ResolvedAt.UTC().Format(time.DateOnly) != "2026-09-29" {
		t.Fatalf("by_date resolves on its creation day's close: %+v", f.Resolution)
	}
}

func TestDraftPriceRule(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Text: `{"shape":"binary","question":"Will the Binance SOL/USDT daily close on 2099-12-31 be above $130?","outcomes":["Yes","No"],
			"resolutionCriteria":"Binance SOLUSDT 1d close.","resolutionDate":"2099-12-31","category":"crypto","priceRule":{"symbol":"sol","op":"above","threshold":130,"mode":"on_date"}}`},
		llmtest.Reply{Text: `{"shape":"categorical","question":"Which chain has more fees in 2099: Solana or Ethereum?","outcomes":["Solana","Ethereum"],
			"resolutionDate":"2099-12-31","category":"crypto","priceRule":{"symbol":"SOL","op":"above","threshold":130,"mode":"on_date"}}`},
	)})
	w := e.Do("POST", "/api/forecasts/draft", `{"idea":"SOL above 130 by year end"}`)
	d := apptest.JSON[struct{ Draft Draft }](t, w).Draft
	if w.Code != 200 || d.PriceRule == nil || *d.PriceRule != (PriceRule{"SOL", "above", 130, "on_date"}) {
		t.Fatalf("draft: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(e.Bedrock.Requests()[0].System, `"priceRule": {"symbol": "...", "op": "above" | "below"`) {
		t.Fatal("the draft prompt must ask for a priceRule")
	}
	if w := e.Do("POST", "/api/forecasts/draft", `{"idea":"Solana vs Ethereum fees"}`); !strings.Contains(w.Body.String(), `"priceRule":null`) {
		t.Fatalf("non-binary draft: %s", w.Body)
	}
	bad := map[string]any{"shape": "binary", "question": d.Question, "outcomes": []string{"Yes", "No"}, "resolutionDate": "2099-12-31",
		"priceRule": map[string]any{"symbol": "SHIB", "op": "above", "threshold": 1, "mode": "on_date"}}
	if w := e.Do("POST", "/api/forecasts", bad); w.Code != 400 || !strings.Contains(w.Body.String(), "priceRule is invalid.") {
		t.Fatalf("invalid rule: %d %s", w.Code, w.Body)
	}
}

// A lineage counts once in "mine": only its newest completed run is scored.
func TestTrackRecordCountsALineageOnce(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	d := Draft{Shape: "binary", Question: "Will the ECB cut rates in 2099?", Outcomes: yesNo, ResolutionDate: "2099-01-01"}
	parent, child := completed(t, e, e.User, d, 0.2), completed(t, e, e.User, d, 0.9)
	if _, err := e.DB.Exec(context.Background(), `update forecasts set parent_id = $1 where id = $2`, parent, child); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent, child} {
		if w := e.Do("POST", "/api/forecasts/"+id+"/resolve", map[string]any{"outcome": "Yes"}); w.Code != 200 {
			t.Fatalf("resolve: %d %s", w.Code, w.Body)
		}
	}
	tr := apptest.JSON[struct{ Mine TrackRecord }](t, e.Do("GET", "/api/forecasts/track-record", nil)).Mine
	if tr.N != 1 || !near(*tr.Brier, 0.01) {
		t.Fatalf("mine: %+v", tr)
	}
}

// stake buys a paper position on forecast id through the ledger (no Gamma call).
func stake(t *testing.T, e *apptest.Env, id, outcome, side string, price, cost float64) {
	t.Helper()
	if _, err := e.Paper.Stake(context.Background(), e.User, cost, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into market_positions (user_id, forecast_id, outcome, side, question, url, price, contracts, cost)
			values ($1, $2, $3, $4, 'Q?', 'https://polymarket.com/event/q', $5, $6, $7)`, e.User, id, outcome, side, price, cost/price, cost)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Resolving by hand settles the paper positions on the run and the runs it
// updates in the same transaction; afterwards the outcome can be confirmed
// again but not cleared or changed. The resolver settles price rules the same way.
func TestResolveSettlesPositions(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	d := Draft{Shape: "binary", Question: "Will the ECB cut rates in 2099?", Outcomes: yesNo, ResolutionDate: "2099-01-01"}
	parent := completed(t, e, e.User, d, 0.6)
	child := completed(t, e, e.User, d, 0.7)
	if _, err := e.DB.Exec(ctx, `update forecasts set parent_id = $1 where id = $2`, parent, child); err != nil {
		t.Fatal(err)
	}
	stake(t, e, parent, "Yes", "yes", 0.5, 100) // pays 200
	stake(t, e, child, "Yes", "no", 0.25, 50)   // loses
	before, err := e.Paper.Portfolio(ctx, e.User)
	if err != nil || before.PredictionStakes != 150 {
		t.Fatalf("staked: %+v %v", before, err)
	}

	if w := e.Do("POST", "/api/forecasts/"+child+"/resolve", map[string]any{"outcome": "Yes"}); w.Code != 200 {
		t.Fatalf("resolve: %d %s", w.Code, w.Body)
	}
	after, _ := e.Paper.Portfolio(ctx, e.User)
	if !near(after.Cash, before.Cash+200) || !near(after.RealizedPnl, before.RealizedPnl+100-50) || after.PredictionStakes != 0 ||
		!near(after.Equity, before.Equity+50) || after.Revision != before.Revision+2 {
		t.Fatalf("settled: %+v, before %+v", after, before)
	}
	type row struct {
		Status string
		Payout float64
	}
	rows, _ := e.DB.Query(ctx, `select status, payout from market_positions where user_id = $1 order by created_at`, e.User)
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil || len(got) != 2 || got[0] != (row{"won", 200}) || got[1] != (row{"lost", 0}) {
		t.Fatalf("rows: %+v %v", got, err)
	}
	if w := e.Do("POST", "/api/forecasts/"+child+"/resolve", map[string]any{"outcome": "Yes"}); w.Code != 200 {
		t.Fatalf("confirming the same outcome: %d %s", w.Code, w.Body)
	}
	for _, body := range []map[string]any{{"outcome": nil}, {"outcome": "No"}} {
		if w := e.Do("POST", "/api/forecasts/"+child+"/resolve", body); w.Code != 409 ||
			!strings.Contains(w.Body.String(), "Paper positions on this forecast have settled, so its resolution can't change.") {
			t.Fatalf("%v after settling: %d %s", body, w.Code, w.Body)
		}
	}
	if again, _ := e.Paper.Portfolio(ctx, e.User); again.Cash != after.Cash || again.Revision != after.Revision {
		t.Fatalf("settled twice: %+v", again)
	}
	// Without positions a resolution still clears.
	free := completed(t, e, e.User, d, 0.5)
	e.Do("POST", "/api/forecasts/"+free+"/resolve", map[string]any{"outcome": "No"})
	if w := e.Do("POST", "/api/forecasts/"+free+"/resolve", map[string]any{"outcome": nil}); w.Code != 200 {
		t.Fatalf("clear without positions: %d %s", w.Code, w.Body)
	}

	// The resolver: a by_date rule that has already hit settles at once.
	rule := Draft{Shape: "binary", Question: "Will SOL close below $1e9?", Outcomes: yesNo, ResolutionDate: "2099-01-01",
		PriceRule: &PriceRule{"SOL", "below", 1e9, "by_date"}}
	auto := completed(t, e, e.User, rule, 0.4)
	stake(t, e, auto, "Yes", "no", 0.5, 20)
	pre, _ := e.Paper.Portfolio(ctx, e.User)
	if n, err := (&handler{e.Deps}).resolveDue(ctx, markettest.End.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("resolveDue: %d %v", n, err)
	}
	post, _ := e.Paper.Portfolio(ctx, e.User)
	var status string
	if err := e.DB.QueryRow(ctx, `select status from market_positions where forecast_id = $1`, auto).Scan(&status); err != nil || status != "lost" ||
		!near(post.RealizedPnl, pre.RealizedPnl-20) || post.Cash != pre.Cash || post.PredictionStakes != 0 {
		t.Fatalf("auto-settled: %s %v, %+v → %+v", status, err, pre, post)
	}
}
