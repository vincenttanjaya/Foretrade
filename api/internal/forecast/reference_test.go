package forecast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/attach/attachtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

// R1 under the outlook's lognormal: on_date is the end distribution,
// by_date the reflection principle's doubling (no reference when spot is
// already beyond: not decided, and not 0.99), and both stay inside
// [0.01, 0.99].
func TestReferenceP(t *testing.T) {
	o := market.OutlookFrom("BTC", 100_000, 0.5, 14)
	end := 1 - below(o, 105_000) // P(close above 105k)
	for _, c := range []struct {
		name string
		rule PriceRule
		o    market.PriceOutlook
		want float64
	}{
		{"on_date above", PriceRule{"BTC", "above", 105_000, "on_date"}, o, end},
		{"on_date below", PriceRule{"BTC", "below", 105_000, "on_date"}, o, 1 - end},
		{"on_date above, in the money", PriceRule{"BTC", "above", 94_000, "on_date"}, o, 1 - below(o, 94_000)},
		{"by_date above: twice the end chance", PriceRule{"BTC", "above", 105_000, "by_date"}, o, 2 * end},
		{"by_date below", PriceRule{"BTC", "below", 95_000, "by_date"}, o, 2 * below(o, 95_000)},
		{"by_date at the threshold: not yet beyond, doubling caps at 1", PriceRule{"BTC", "above", 100_000, "by_date"}, o, 0.99},
		{"far out of the money clamps to 0.01", PriceRule{"BTC", "above", 400_000, "on_date"}, o, 0.01},
		{"deep in the money clamps to 0.99", PriceRule{"BTC", "below", 400_000, "on_date"}, o, 0.99},
		{"flat series: σ floored at 1%", PriceRule{"BTC", "above", 100_100, "on_date"}, market.OutlookFrom("BTC", 100_000, 0, 14),
			0.5 * math.Erfc(math.Log(1.001)/(0.01*math.Sqrt(14.0/365))/math.Sqrt2)},
	} {
		if got, ok := referenceP(c.rule, c.o); !ok || !near(got, c.want) {
			t.Errorf("%s: %v %v, want %v", c.name, got, ok, c.want)
		}
	}
	for name, rule := range map[string]PriceRule{
		"by_date already above": {"BTC", "above", 99_000, "by_date"},
		"by_date already below": {"BTC", "below", 101_000, "by_date"},
		"by_date far beyond":    {"BTC", "above", 1, "by_date"},
	} {
		if got, ok := referenceP(rule, o); ok {
			t.Errorf("%s: %v, want no reference", name, got)
		}
	}
	for name, rule := range map[string]PriceRule{ // on_date beyond is still a real question: the close on the date counts
		"on_date already above": {"BTC", "above", 99_000, "on_date"},
		"on_date already below": {"BTC", "below", 101_000, "on_date"},
	} {
		if got, ok := referenceP(rule, o); !ok || got <= 0.5 || got >= 0.99 {
			t.Errorf("%s: %v %v", name, got, ok)
		}
	}
	if end <= 0.01 || 2*end >= 0.99 {
		t.Fatalf("the fixture must stay off the clamps: %v", end)
	}
}

// reference prices only binary questions with a valid rule, as of the run
// (live: the latest bars; past a year with the same 90-day σ), and a market
// data failure is no reference rather than a failed run.
func TestReference(t *testing.T) {
	b := markettest.New(t)
	h := &handler{&app.Deps{Market: testMarketClient(b.URL)}}
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
	today := now.Truncate(24 * time.Hour)
	rule := PriceRule{"BTC", "above", 105_000, "on_date"}
	d := Draft{Shape: "binary", Outcomes: yesNo, ResolutionDate: "2026-10-21", PriceRule: &rule}

	ref := h.reference(ctx, d, now, today, nil)
	o, err := h.d.Market.Outlook(ctx, "BTC", 14)
	if err != nil || ref == nil {
		t.Fatalf("live reference %+v: %v", ref, err)
	}
	if *ref != (Reference{Kind: "volatility", PYes: refP(t, rule, o), Spot: o.Spot, VolAnnPct: o.VolAnnPct, HorizonDays: 14, AsOf: now}) || o.Spot != markettest.Price("BTCUSDT") {
		t.Fatalf("live reference %+v, outlook %+v", ref, o)
	}

	long := d
	long.ResolutionDate = "2028-10-06" // 730 days: the outlook clamps at 365, R1 keeps the true horizon
	if r := h.reference(ctx, long, now, today, nil); r == nil || r.HorizonDays != 730 ||
		!near(r.PYes, refP(t, rule, market.OutlookFrom("BTC", o.Spot, o.VolAnnPct/100, 730))) || r.PYes <= ref.PYes {
		t.Fatalf("two-year reference %+v (14 days: %v)", r, ref.PYes)
	}

	for name, x := range map[string]Draft{
		"no rule":         {Shape: "binary", Outcomes: yesNo, ResolutionDate: "2026-10-21"},
		"not binary":      {Shape: "ordered", Outcomes: []string{"Low", "High"}, ResolutionDate: "2026-10-21", PriceRule: &rule},
		"invalid rule":    {Shape: "binary", Outcomes: yesNo, ResolutionDate: "2026-10-21", PriceRule: &PriceRule{"BTC", "sideways", 1, "on_date"}},
		"stock symbol":    {Shape: "binary", Outcomes: yesNo, ResolutionDate: "2026-10-21", PriceRule: &PriceRule{"NVDA", "above", 1, "on_date"}},
		"unreadable date": {Shape: "binary", Outcomes: yesNo, ResolutionDate: "soon", PriceRule: &rule},
	} {
		if r := h.reference(ctx, x, now, today, nil); r != nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// A by_date rule whose spot is already beyond the threshold is not decided
	// (only closes from the creation day count) and has no reference: never a
	// 0.99 the guard would reset to. The log says why.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	crossed := d
	crossed.PriceRule = &PriceRule{"BTC", "above", markettest.Price("BTCUSDT") * 0.9, "by_date"}
	r := h.reference(ctx, crossed, now, today, nil)
	slog.SetDefault(previous)
	if r != nil || !strings.Contains(logs.String(), `msg="forecast without a volatility reference" symbol=BTC op=above mode=by_date reason="spot already beyond a by_date threshold; only closes from the creation day count"`) {
		t.Fatalf("by_date already beyond: %+v, logs %q", r, logs.String())
	}
	crossed.PriceRule = &PriceRule{"BTC", "above", markettest.Price("BTCUSDT") * 1.1, "by_date"}
	if r := h.reference(ctx, crossed, now, today, nil); r == nil || r.PYes >= 0.99 {
		t.Fatalf("by_date not yet reached: %+v", r)
	}
	b.Down("", true)
	if r := (&handler{&app.Deps{Market: testMarketClient(b.URL)}}).reference(ctx, d, now, today, nil); r != nil {
		t.Fatalf("market down: %+v", r)
	}
}

func TestReferencePrompt(t *testing.T) {
	ref := &Reference{Kind: "volatility", PYes: 0.8123, Spot: 117250.5, VolAnnPct: 41.234, HorizonDays: 14, AsOf: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)}
	d := Draft{Shape: "binary", ResolutionDate: "2026-07-29", PriceRule: &PriceRule{"BTC", "above", 110_000, "on_date"}}
	want := "\nR1 · Volatility-only reference (server-computed from Binance daily closes, zero-drift lognormal; tool data, safe to cite as R1): " +
		"P(Yes) 81.2% that the BTC daily close on 2026-07-29 is above the threshold; spot $117250.5, threshold $110,000, horizon 14 days, " +
		"90-day realized volatility 41.2%/yr, as of 2026-07-15 00:00 UTC.\n"
	if got := referencePrompt(ref, d); got != want {
		t.Fatalf("on_date:\n%q\nwant\n%q", got, want)
	}
	d.PriceRule = &PriceRule{"DOGE", "below", 0.215, "by_date"}
	if got := referencePrompt(ref, d); !strings.Contains(got, "zero-drift lognormal, first passage by the reflection principle;") ||
		!strings.Contains(got, "that any DOGE daily close through 2026-07-29 is below the threshold;") || !strings.Contains(got, "threshold $0.215,") ||
		strings.Count(got, "\n") != 2 || strings.Contains(got, "untrusted") {
		t.Fatalf("by_date: %q", got)
	}
	if referencePrompt(nil, d) != "" {
		t.Fatal("no R1, no line")
	}
}

// The prompts no longer tilt toward 50%: the scoring framing, the skeptic and
// quant wording, the base-rate's R1 sentence, and the aggregator's two ways
// to combine.
func TestPromptsDoNotHedge(t *testing.T) {
	event, legend := frame{Kind: "event"}, Draft{Shape: "binary"}
	spec := specialistPrompt(roles[2], event, false, nil)
	agg, agg1 := strings.Replace(aggregatorPrompt(legend, event, false), directionLegend(legend), "yes | no | context", 1),
		strings.Replace(aggregatorPrompt(legend, event, true), directionLegend(legend), "yes | no | context", 1)
	all := spec + agg + agg1 + roles[0].Brief + roles[1].Brief + baseRatePrice + baseRatePriceR1
	for _, gone := range []string{"prefer honest uncertainty", "bold claims", "only modestly", "have not been empirically calibrated",
		"pulling overconfident numbers back", "%!", "%%"} {
		if strings.Contains(all, gone) {
			t.Errorf("prompt still says %q", gone)
		}
	}
	for _, c := range []struct{ prompt, want string }{
		{spec, "Members forecast independently and an aggregator combines you. Your probabilities are scored (Brier and log loss) when the question resolves, so be calibrated: as sharp as your evidence supports and no sharper. Moving toward 50% is a claim too: it costs as much as overconfidence when the evidence does not support it.\n"},
		{spec, "Your value is pricing the regimes, catalysts and tails others ignore, in both directions: say when the evidence supports a sharper number than the others give, not only a softer one."},
		{roles[1].Brief, "move away from the baseline for a stated reason, as far as that evidence supports."},
		{roles[0].Brief, "adjust for case specifics as far as the evidence supports, and say by how much;"},
		{baseRatePriceR1, "R1 is the measured volatility-only probability for this threshold: start from it as your reference class."},
		{agg, "Produce the final probability estimate and its audit trail. Your final probabilities are scored (Brier and log loss) at resolution: be calibrated, as sharp as the evidence supports and no sharper. On past price questions this desk lost to a volatility-only formula by pulling its numbers toward 50%; do not repeat that.\n"},
		{agg, "How to combine:\n- Start from the ensemble and move from it, in either direction, for a stated reason:"},
		{agg, "- Do not go beyond the most extreme specialist unless they rely on independent, strong evidence pointing the same way.\n- M1, when present"},
		{agg1, "How to combine:\n- Start from R1 (the volatility-only reference) and the quant's tool-computed probabilities, not from the simple average: members without tool data are averaged into the ensemble and pull it toward 50%."},
		{agg1, "A move toward 50% needs a reason exactly like a move away."},
		{agg1, "R1 counts as a member for this rule."},
		{agg1, `"referenceNote": {"reason": "<only when the prompt gives R1 and your final P(Yes) is on the other side of 50% from R1`},
		{agg1, "For this question the vocabulary is: yes | no | context."},
		// Both rounds reach the prompts: round 9's materials beside round 8b's R1.
		{spec, "- The user's own material: S0 (the user's notes) and D1..Dn (files the user attached,"},
		{agg1, "You receive the question, the user's notes (S0) and attached materials (D1..Dn) when present, the research"},
		{agg1, "The user's notes and materials (S0, D ids and the findings read from them) do not count as cited evidence here.\n"},
		{agg, `"sourceId": "<an F, S, D or M id>" | null`},
	} {
		if !strings.Contains(c.prompt, c.want) {
			t.Errorf("prompt lacks %q", c.want)
		}
	}
	if strings.Contains(agg, "Start from R1") || strings.Contains(agg1, "Start from the ensemble") {
		t.Fatal("each aggregator variant has one way to combine")
	}
	if !strings.Contains(agg, `'No material change'>",`+"\n"+`  "referenceNote": {"reason": "<only when`) || !strings.HasSuffix(strings.Split(agg, "\nGive 3-6 drivers")[0], `e.g. F3, S2>"]}`+"\n}") {
		t.Fatal("the reply template gains referenceNote as its last field")
	}
}

// refP is R1 for a rule that has one.
func refP(t *testing.T, rule PriceRule, o market.PriceOutlook) float64 {
	t.Helper()
	p, ok := referenceP(rule, o)
	if !ok {
		t.Fatalf("no reference for %+v at spot %v", rule, o.Spot)
	}
	return p
}

func refResult(yes, r float64, note *ReferenceNote) Result {
	return Result{Probabilities: ps("Yes", yes, "No", 1-yes), Flags: []Flag{}, ReferenceNote: note, Reference: &Reference{Kind: "volatility", PYes: r}}
}

// The shrink guard: a move toward 50% from R1 (or across it) needs a cited
// finding or source of this run; a sharper number or one within the slack
// needs nothing.
func TestShrinkGuard(t *testing.T) {
	sources := []Source{{ID: "S1"}, {ID: "S2"}}
	findings := []Finding{{ID: "F1", SourceIDs: []string{"S1"}}}
	cited := cleanReferenceNote(&ReferenceNote{Reason: " An ETF decision lands next week (F1). ", IDs: []string{"f1", "[S9]", "F1"}}, sources, findings)
	if cited == nil || cited.Reason != "An ETF decision lands next week (F1)." || len(cited.IDs) != 1 || cited.IDs[0] != "F1" {
		t.Fatalf("clean note: %+v", cited)
	}
	// S0 (the user's own context) is not evidence: a user cannot keep a hedge by writing it.
	unknown := cleanReferenceNote(&ReferenceNote{Reason: "A rumour.", IDs: []string{"S9", "M1", "R1", "S0"}}, sources, findings)
	if unknown == nil || len(unknown.IDs) != 0 || cleanReferenceNote(&ReferenceNote{IDs: []string{"S1"}}, sources, findings) != nil || cleanReferenceNote(nil, sources, findings) != nil {
		t.Fatalf("notes: %+v", unknown)
	}
	// Nor is the user's material (round 9): its D ids and the findings read from it.
	mat := cleanReferenceNote(&ReferenceNote{Reason: "The user's broker note says so.", IDs: []string{"D1", "F2"}}, sources,
		append(slices.Clip(findings), Finding{ID: "F2", SourceIDs: []string{"D1"}, Origin: "material"}))
	if mat == nil || len(mat.IDs) != 0 {
		t.Fatalf("material note: %+v", mat)
	}
	for _, c := range []struct {
		name            string
		yes, r          float64
		note            *ReferenceNote
		triggered, kept bool
	}{
		{"hedged with a cited finding: kept", 0.69, 0.76, cited, true, true},
		{"hedged without a note: reset", 0.69, 0.76, nil, true, false},
		{"hedged citing only an id the run does not have: reset", 0.69, 0.76, unknown, true, false},
		{"sharper than R1: no trigger", 0.85, 0.76, nil, false, false},
		{"sharper on the No side: no trigger", 0.15, 0.24, nil, false, false},
		{"within the 2-point slack: no trigger", 0.745, 0.76, nil, false, false},
		{"across 50%: reset", 0.45, 0.62, nil, true, false},
		{"across 50% the other way, cited: kept", 0.58, 0.40, cited, true, true},
		{"on the 50% line from a lean: reset", 0.5, 0.6, nil, true, false},
		{"R1 at 50%: nothing to shrink toward", 0.53, 0.5, nil, false, false},
	} {
		res := refResult(c.yes, c.r, c.note)
		triggered, kept := shrinkGuard(&res)
		if triggered != c.triggered || kept != c.kept {
			t.Errorf("%s: triggered %v kept %v", c.name, triggered, kept)
			continue
		}
		reset := triggered && !kept
		want := c.yes
		if reset {
			want = c.r
		}
		if !near(res.Probabilities[0].P, want) || !near(res.Probabilities[1].P, 1-want) || (res.Guard == "shrink") != reset ||
			(len(res.Flags) == 1 && res.Flags[0] == Flag{"low_information", guardNote}) != reset || (res.ReferenceNote != nil) != kept {
			t.Errorf("%s: %+v", c.name, res)
		}
		// Never away from R1, never past it.
		if math.Abs(res.Probabilities[0].P-c.r) > math.Abs(c.yes-c.r)+1e-12 {
			t.Errorf("%s: moved away from R1: %v", c.name, res.Probabilities[0].P)
		}
	}
	plain := Result{Probabilities: ps("Yes", 0.5, "No", 0.5), ReferenceNote: cited}
	if triggered, _ := shrinkGuard(&plain); triggered || plain.ReferenceNote != nil || plain.Guard != "" {
		t.Fatalf("no R1, no guard: %+v", plain)
	}
}

// The brief's counterfactual: the guard alone (no referenceNote) on the
// embedded replay seed moves 11 of 24 answers to the formula and scores
// Brier ≈ 0.1825, 27.0% better than a coin flip and level with the formula
// (0.1814).
func TestShrinkGuardOnReplaySeed(t *testing.T) {
	var seed []SeedRow
	if err := json.Unmarshal(replaySeed, &seed); err != nil || len(seed) != 24 {
		t.Fatalf("seed: %d rows, %v", len(seed), err)
	}
	before := SeedSummary(seed)
	moved := 0
	for i := range seed {
		res := seed[i].Result
		if res.Reference != nil || res.Guard != "" {
			t.Fatalf("the stored seed predates R1: %s", seed[i].Key)
		}
		res.Probabilities, res.Flags, res.Reference = append([]OutcomeP(nil), res.Probabilities...), []Flag{}, &Reference{Kind: "volatility", PYes: seed[i].AnchorP}
		if triggered, kept := shrinkGuard(&res); triggered && !kept {
			moved++
		}
		seed[i].Result = res
	}
	var rows []resolved
	for _, r := range seed {
		rows = append(rows, resolved{shape: "binary", outcomes: yesNo, probs: r.Result.Probabilities, outcome: *r.Outcome, anchorP: ptr(r.AnchorP)})
	}
	tr := score(rows)
	if moved != 11 || tr.N != 24 || math.Abs(*tr.Brier-0.1825) > 5e-5 || math.Abs(*tr.AnchorBrier-0.1814) > 5e-5 ||
		math.Abs(*tr.Skill-0.270) > 5e-4 || math.Abs(*tr.AnchorSkill) > 0.01 || tr.AnchorN != 24 {
		t.Fatalf("guarded seed: moved %d, %+v", moved, tr)
	}
	after := SeedSummary(seed)
	if !strings.HasPrefix(before, "Replay score over 24 resolved questions: desk Brier 0.1945 (90% range ") || !strings.Contains(before, "skill vs the coin flip +22.2%.") ||
		!strings.Contains(before, "Volatility-only formula Brier 0.1814 over 24 paired questions, skill vs the formula -7.2% (paired 90% range of desk − formula ") ||
		!strings.HasSuffix(before, "The shrink guard moved 0 of 24 answers to the formula.") {
		t.Fatalf("stored seed summary: %s", before)
	}
	if !strings.Contains(after, "desk Brier 0.1825") || !strings.Contains(after, "skill vs the coin flip +27.0%.") || !strings.Contains(after, "skill vs the formula -0.6%") ||
		!strings.HasSuffix(after, "The shrink guard moved 11 of 24 answers to the formula.") {
		t.Fatalf("guarded seed summary: %s", after)
	}
	if s := SeedSummary(nil); s != "Replay score: no resolved questions. The shrink guard moved 0 of 0 answers to the formula." {
		t.Fatalf("empty summary: %s", s)
	}
}

// A live price-rule run: R1 reaches the panel and the aggregator, the
// aggregator's hedge toward 50% is kept because it cites S1, R1 is on the
// result and stored as the row's anchor_p (not a replay row), and an update
// without a note is reset to R1.
func TestLiveRunReference(t *testing.T) {
	brave := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/web/search" {
			fmt.Fprint(w, `{"web":{"results":[]}}`)
			return
		}
		fmt.Fprintf(w, `{"results":[{"title":"ETF decision due (%[1]s)","url":"https://desk.localhost/%[2]s","description":"SEC","page_age":%[3]q,"profile":{"name":"Desk"}}]}`,
			r.URL.Query().Get("q"), strings.ReplaceAll(r.URL.Query().Get("q"), " ", "-"), time.Now().UTC().Add(-5*time.Hour).Format("2006-01-02T15:04:05"))
	}
	var aggregator atomic.Pointer[string] // the second run's reply drops the note
	aggregator.Store(ptr(`{"probabilities":[{"outcome":"Yes","p":0.5}],"summary":"We call it even.","confidence":"medium",
		"referenceNote":{"reason":"The ETF decision (S1) is binary for price.","ids":["S1"]}}`))
	route := panel(t, map[string]llmtest.Reply{
		"queries":   {Text: `{"queries":["bitcoin etf decision"]}`},
		"base_rate": {Text: opinion(0.5, "Coin flip.")},
		"quant":     {Text: opinion(0.6, "Lognormal anchor.")},
		"skeptic":   {Text: opinion(0.45, "Catalysts.")},
	}, false)
	e := apptest.New(t, Register, apptest.Options{Brave: brave, Reply: func(r llmtest.Request) llmtest.Reply {
		if strings.HasPrefix(r.System, "Write up to 2 short Polymarket") {
			return noQueries
		}
		if strings.HasPrefix(r.System, "You are the aggregator") {
			return llmtest.Reply{Text: *aggregator.Load()}
		}
		return route(r)
	}})
	e.PolymarketBase, _ = gamma(t, nil)
	date := todayUTC().AddDate(0, 0, 30)
	rule := PriceRule{"BTC", "above", 90_000, "on_date"}
	d := Draft{Shape: "binary", Question: fmt.Sprintf("Will the Binance BTC/USDT daily close on %s be above $90,000?", date.Format(time.DateOnly)),
		Outcomes: []string{"Yes", "No"}, ResolutionDate: date.Format(time.DateOnly), Category: "crypto", PriceRule: &rule}
	o, err := e.Market.Outlook(context.Background(), "BTC", 30)
	if err != nil {
		t.Fatal(err)
	}
	want := refP(t, rule, o)
	if want < 0.6 {
		t.Fatalf("the fixture must lean Yes: %v", want)
	}

	f := waitRun(t, e, create(t, e, d))
	r := f.Result
	if f.Status != "completed" || r.Reference == nil || r.Reference.PYes != want || r.Reference.HorizonDays != 30 || r.Guard != "" ||
		!near(r.Probabilities[0].P, 0.5) || r.ReferenceNote == nil || r.ReferenceNote.IDs[0] != "S1" || f.Replay != nil {
		t.Fatalf("cited hedge: %+v %+v", f, r.Reference)
	}
	var anchor *float64
	if err := e.DB.QueryRow(context.Background(), `select anchor_p from forecasts where id = $1`, f.ID).Scan(&anchor); err != nil || anchor == nil || *anchor != want {
		t.Fatalf("anchor_p %v: %v", anchor, err)
	}
	body := e.Do("GET", "/api/forecasts/"+f.ID, nil).Body.String()
	if !strings.Contains(body, `"reference":{"kind":"volatility","pYes":`) || !strings.Contains(body, `"referenceNote":{"reason":"The ETF decision (S1) is binary for price.","ids":["S1"]}`) ||
		strings.Contains(body, `"guard"`) || !strings.Contains(body, `"replay":null`) {
		t.Fatalf("get: %s", body)
	}
	saw := 0
	for _, req := range e.Bedrock.Requests() {
		if !strings.HasPrefix(req.System, "You are the") || strings.HasPrefix(req.System, "You are the research assistant") {
			continue
		}
		saw++
		if !strings.Contains(req.Prompt, fmt.Sprintf("P(Yes) %.1f%% that the BTC daily close on %s is above the threshold;", want*100, d.ResolutionDate)) {
			t.Errorf("%.40s: no R1 line: %s", req.System, req.Prompt)
		}
		if strings.HasPrefix(req.System, "You are the Base-rate") && !strings.Contains(req.System, baseRatePriceR1) ||
			strings.HasPrefix(req.System, "You are the aggregator") && !strings.Contains(req.System, combineR1) {
			t.Errorf("%.40s: not the R1 variant", req.System)
		}
	}
	if saw != 4 {
		t.Fatalf("panel calls with R1: %d", saw)
	}

	// The same hedge with no note: the server resets it to R1 and says so.
	aggregator.Store(ptr(`{"probabilities":[{"outcome":"Yes","p":0.5}],"summary":"We call it even.","confidence":"medium"}`))
	g := waitRun(t, e, create(t, e, d))
	if r := g.Result; r.Guard != "shrink" || r.Probabilities[0].P != want || r.ReferenceNote != nil || r.Flags[len(r.Flags)-1].Note != guardNote ||
		r.Market != nil {
		t.Fatalf("uncited hedge: %+v", r)
	}
}

// R1 (round 8b) and the user's materials (round 9) reach the same prompts:
// every panel member and the aggregator see the R1 line before S0 and the
// D1..Dn block. A hedge toward 50% that cites only the user's file (its D id
// and the finding read from it) is not evidence, so the server resets it.
func TestReferenceWithMaterials(t *testing.T) {
	date := todayUTC().AddDate(0, 0, 30)
	rule := PriceRule{"BTC", "above", 90_000, "on_date"}
	d := Draft{Shape: "binary", Question: fmt.Sprintf("Will the Binance BTC/USDT daily close on %s be above $90,000?", date.Format(time.DateOnly)),
		Outcomes: []string{"Yes", "No"}, ResolutionDate: date.Format(time.DateOnly), Category: "crypto", PriceRule: &rule, Context: "I hold BTC."}
	e := apptest.New(t, Register, apptest.Options{Reply: materialPanel(t, func(llmtest.Request) llmtest.Reply {
		return digestReply("A broker note on bitcoin.", "The note expects bitcoin to fall below $90,000 next month.")
	}, map[string]llmtest.Reply{
		"base_rate": {Text: opinion(0.5, "Coin flip.")},
		"quant":     {Text: opinion(0.6, "Lognormal anchor.")},
		"skeptic":   {Text: opinion(0.45, "The file is one broker's view.")},
		"aggregator": {Text: `{"probabilities":[{"outcome":"Yes","p":0.5}],"summary":"We call it even.","confidence":"medium",
			"referenceNote":{"reason":"The user's broker note (D1, F1) expects a fall.","ids":["D1","F1"]}}`},
	})})
	o, err := e.Market.Outlook(context.Background(), "BTC", 30)
	if err != nil {
		t.Fatal(err)
	}
	want := refP(t, rule, o)
	if want < 0.6 {
		t.Fatalf("the fixture must lean Yes: %v", want)
	}
	w := e.Do("POST", "/api/forecasts", materialBody(d, map[string]any{"attachments": []any{
		attachtest.Doc([]byte("date,view\n2026-09-30,bearish\n"), "text/csv", "note.csv")}}))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	f := waitRun(t, e, apptest.JSON[struct{ Forecast Forecast }](t, w).Forecast.ID)
	r := f.Result
	if f.Status != "completed" || len(f.Materials) != 1 || f.Materials[0].Status != "read" || r.Reference == nil || r.Reference.PYes != want ||
		r.Guard != "shrink" || r.ReferenceNote != nil || !near(r.Probabilities[0].P, want) || !slices.Contains(r.Flags, Flag{"low_information", guardNote}) ||
		len(r.Findings) != 1 || r.Findings[0].Origin != "material" {
		t.Fatalf("material-only hedge: %s %+v %+v", f.Status, r, f.Materials)
	}
	saw := 0
	for _, req := range e.Bedrock.Requests() {
		if !strings.HasPrefix(req.System, "You are the") {
			continue
		}
		saw++
		r1 := strings.Index(req.Prompt, fmt.Sprintf("P(Yes) %.1f%% that the BTC daily close on %s is above the threshold;", want*100, d.ResolutionDate))
		s0, mats := strings.Index(req.Prompt, "I hold BTC."), strings.Index(req.Prompt, "D1..Dn · Your materials")
		if r1 < 0 || s0 < r1 || mats < s0 || !strings.Contains(req.Prompt, `{"id":"F1","material":"D1"`) {
			t.Errorf("%.40s: R1 %d, S0 %d, materials %d", req.System, r1, s0, mats)
		}
	}
	if saw != 4 {
		t.Fatalf("panel calls: %d", saw)
	}
}

// A live by_date run whose spot is already beyond the threshold: no R1 (the
// question is not decided yet, and a 0.99 reference would let the guard
// overrule the desk), so the result has no reference, no guard and no
// anchor_p, the desk's own number stands, and the prompts are the ones
// without R1.
func TestLiveRunByDateAlreadyBeyond(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: panel(t, map[string]llmtest.Reply{
		"base_rate":  {Text: opinion(0.7, "Close to the line.")},
		"quant":      {Text: opinion(0.8, "Already above; closes from today count.")},
		"skeptic":    {Text: opinion(0.6, "A pullback is possible.")},
		"aggregator": {Text: `{"probabilities":[{"outcome":"Yes","p":0.7}],"summary":"We lean Yes.","confidence":"medium"}`},
	}, false)})
	date := todayUTC().AddDate(0, 0, 30)
	rule := PriceRule{"BTC", "above", math.Floor(markettest.Price("BTCUSDT") * 0.95), "by_date"}
	d := Draft{Shape: "binary", Question: fmt.Sprintf("Will any Binance BTC/USDT daily close through %s be above $%s?", date.Format(time.DateOnly), money(rule.Threshold)),
		Outcomes: []string{"Yes", "No"}, ResolutionDate: date.Format(time.DateOnly), Category: "crypto", PriceRule: &rule}
	f := waitRun(t, e, create(t, e, d))
	r := f.Result
	if f.Status != "completed" || r.Reference != nil || r.Guard != "" || r.ReferenceNote != nil || !near(r.Probabilities[0].P, 0.7) ||
		slices.Contains(r.Flags, Flag{"low_information", guardNote}) {
		t.Fatalf("by_date beyond: %s %+v", f.Status, r)
	}
	var anchor *float64
	if err := e.DB.QueryRow(context.Background(), `select anchor_p from forecasts where id = $1`, f.ID).Scan(&anchor); err != nil || anchor != nil {
		t.Fatalf("anchor_p %v: %v", anchor, err)
	}
	if body := e.Do("GET", "/api/forecasts/"+f.ID, nil).Body.String(); !strings.Contains(body, `"reference":null`) || strings.Contains(body, `"guard"`) {
		t.Fatalf("get: %s", body)
	}
	saw := 0
	for _, req := range e.Bedrock.Requests() {
		if !strings.HasPrefix(req.System, "You are the") {
			continue
		}
		saw++
		if strings.Contains(req.Prompt, "R1 · Volatility-only reference") ||
			strings.HasPrefix(req.System, "You are the Base-rate") && !strings.Contains(req.System, baseRatePrice) ||
			strings.HasPrefix(req.System, "You are the aggregator") && !strings.Contains(req.System, combineEnsemble) {
			t.Errorf("%.40s: an R1 prompt without R1", req.System)
		}
	}
	if saw != 4 {
		t.Fatalf("panel calls: %d", saw)
	}
}
