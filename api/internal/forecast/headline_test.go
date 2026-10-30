package forecast

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// loadFixtureView reads testdata/run-tsm-numeric.json: a GET
// /api/forecasts/{id} body dumped against the fakes (synthetic
// prices), TSM 5 years with a capped-drift view.
func loadFixtureView(t *testing.T) *DeskView {
	t.Helper()
	raw, err := os.ReadFile("testdata/run-tsm-numeric.json")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Forecast struct {
			Result struct {
				View *DeskView `json:"view"`
			} `json:"result"`
		} `json:"forecast"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Forecast.Result.View == nil {
		t.Fatalf("fixture: %v", err)
	}
	return body.Forecast.Result.View
}

// The view headline chat and the bell state: the median and the chance when
// the view leads on a basis, the 80% range when it is flat with none, and
// nothing (the top outcome stays) otherwise.
func TestViewHeadline(t *testing.T) {
	v := loadFixtureView(t)
	// Today's level prints at its own precision, as main's lead writes it (the fixture's stored lead predates that rule).
	if got, ok := ViewHeadline(v); !ok || got != "median $611, 62% chance at or above today's $485.12" {
		t.Fatalf("anchored: %q %v", got, ok)
	}
	if got := ViewLean(v); got != "up: 62% chance at or above today" {
		t.Fatalf("lean %q", got)
	}
	none := *v
	none.Basis = noBasis
	if got, ok := ViewHeadline(&none); !ok || got != "80% range ≈$242–≈$1,480" || ViewLean(&none) != "no lean: the desk states no median" {
		t.Fatalf("flat, no basis: %q %v %q", got, ok, ViewLean(&none))
	}
	none.Flat = false
	if _, ok := ViewHeadline(&none); ok || ViewLean(&none) != "" {
		t.Fatal("a view with no basis that is not flat states nothing")
	}
	bucket := *v
	bucket.Headline = "bucket"
	if _, ok := ViewHeadline(&bucket); ok {
		t.Fatal("a bucket headline keeps the top outcome")
	}
	if _, ok := ViewHeadline(nil); ok {
		t.Fatal("no view")
	}
	noRef := *v
	noRef.Reference, noRef.PAbove = nil, nil
	if got, _ := ViewHeadline(&noRef); got != "median $611" || !strings.HasPrefix(ViewLean(&noRef), "split: ") {
		t.Fatalf("no reference: %q %q", got, ViewLean(&noRef))
	}
	for p, want := range map[float64]string{0.40: "down: 40%", 0.47: "down: 47%", 0.50: "split: 50%", 0.53: "up: 53%", 0.9975: "up: >99%", 0.001: "down: <1%"} {
		w := *v
		w.PAbove = &Q{Value: p}
		if got := ViewLean(&w); got != want+" chance at or above today" {
			t.Errorf("pAbove %v: %q", p, got)
		}
	}
	derived := deskView(tsmDraft(), probsAt(tsmOutcomes, .17, .19, .17, .16, .14, .17), nil, 1826, noBasis)
	if got, ok := ViewHeadline(derived); !ok || got != "80% range ≈$247–≈$1,350" {
		t.Fatalf("derived: %q %v", got, ok)
	}
	if b := v.Brief(); b == nil || b.Median.Value != v.Median.Value || b.Basis != "capped_drift" || (*DeskView)(nil).Brief() != nil {
		t.Fatalf("brief %+v", b)
	}
}

// A chance that rounds to 0% or 100% reads "<1%" or ">99%" (the web's
// fmtChance table is the same).
func TestChanceWords(t *testing.T) {
	for p, want := range map[float64]string{0.001: "<1%", 0.0049: "<1%", 0.006: "1%", 0.62: "62%", 0.994: "99%", 0.996: ">99%", 0.9975: ">99%"} {
		if got := chanceWords(Q{Value: p}); got != want {
			t.Errorf("%v: %q, want %q", p, got, want)
		}
		if got := chanceWords(Q{Value: p, Approx: true}); got != map[bool]string{true: "≈" + want, false: want}[want[0] != '<' && want[0] != '>'] {
			t.Errorf("%v approx: %q", p, got)
		}
	}
	v := deskView(tsmDraft(), probsAt(tsmOutcomes, .001, .001, .002, .002, .004, .99), tsmRef, 1825, ViewBasis{Kind: "scenarios", Label: "x"})
	if !strings.Contains(v.Lead, "; >99% chance it closes at or above $485.") {
		t.Fatalf("lead %q", v.Lead)
	}
	v = deskView(tsmDraft(), probsAt(tsmOutcomes, .998, .0005, .0005, .0004, .0003, .0003), tsmRef, 1825, ViewBasis{Kind: "scenarios", Label: "x"})
	if !strings.Contains(v.Lead, "; <1% chance it closes at or above $485.") {
		t.Fatalf("lead %q", v.Lead)
	}
}

// An event summary keeps its call: over the 24 stored
// binary and categorical replay summaries the summary step changes none,
// though answerFirst would cut 5 of them; a numeric one still loses its
// process sentences.
func TestSummaryStepKeepsEventCalls(t *testing.T) {
	var seed []SeedRow
	if err := json.Unmarshal(replaySeed, &seed); err != nil || len(seed) != 24 {
		t.Fatalf("seed: %v %d", err, len(seed))
	}
	cut := 0
	for _, r := range seed {
		fr := frameOf(r.Draft, frameDay)
		if r.Draft.Shape == "ordered" || fr.numeric() {
			t.Fatalf("%s is not an event question", r.Key)
		}
		if got := summaryStep(fr, r.Result.Summary, ""); got != r.Result.Summary {
			t.Errorf("%s: %q → %q", r.Key, r.Result.Summary, got)
		}
		if answerFirst(r.Result.Summary, "") != r.Result.Summary {
			cut++
		}
	}
	if cut != 5 {
		t.Errorf("answerFirst would change %d of the stored summaries, the review counted 5", cut)
	}
	fr := frameOf(tsmDraft(), frameDay)
	if !fr.numeric() || summaryStep(fr, "We stay close to the ensemble. We expect compounding.", "Our median is $611.") != "We expect compounding." {
		t.Fatal("a numeric summary loses its process sentences")
	}
}
