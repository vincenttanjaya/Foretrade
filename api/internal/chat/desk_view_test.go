package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// tsmNumeric is the forecast package's slice-2 fixture (synthetic prices,
// dumped against the fakes): TSM 5 years, a capped-drift view that leads.
func tsmNumeric(t *testing.T, edit func(view map[string]any)) report {
	t.Helper()
	raw, err := os.ReadFile("../forecast/testdata/run-tsm-numeric.json")
	if err != nil {
		t.Fatal(err)
	}
	var wrap struct {
		Forecast struct {
			ID, Question, Shape, ResolutionDate string
			Outcomes                            []string
			Result                              map[string]any
		}
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		t.Fatal(err)
	}
	f := wrap.Forecast
	if edit != nil {
		edit(f.Result["view"].(map[string]any))
	}
	b, _ := json.Marshal(f.Result)
	r := report{ID: f.ID, Question: f.Question, Shape: f.Shape, ResolutionDate: f.ResolutionDate, Outcomes: f.Outcomes}
	if err := json.Unmarshal(b, &r.Result); err != nil {
		t.Fatal(err)
	}
	return r
}

// An ordered report whose view leads is cited, leaned and carded by its view
// (gap round G14), the forecast page's reading, never by its top range.
func TestDeskStatesTheView(t *testing.T) {
	r := tsmNumeric(t, nil)
	ref := r.ref()
	// Today's level at its own precision, as main's lead writes it.
	if got := ref.citeAs(); got != "Forecast desk: median $611, 62% chance at or above today's $485.12" {
		t.Fatalf("citeAs %q", got)
	}
	if got := ref.lean(); got != "up: 62% chance at or above today" {
		t.Fatalf("lean %q", got)
	}
	card, _ := json.Marshal(r.card())
	var c struct {
		Data struct {
			Headline struct{ Outcome string }
			View     struct {
				Median struct{ Value float64 }
				Basis  string
			}
		}
	}
	json.Unmarshal(card, &c)
	if math.Round(c.Data.View.Median.Value) != 611 || c.Data.View.Basis != "capped_drift" || c.Data.Headline.Outcome != "≥ $1,100" {
		t.Fatalf("card %s", card)
	}
	p := r.contextPrompt()
	for _, want := range []string{r.view().Lead, `"basis":"capped_drift"`, `"reference":{"asOf":"2026-10-05T13:30:00Z"`, `"scenarios":[`,
		`cite it exactly as "Forecast desk: median $611, 62% chance at or above today's $485.12"`, `"lean":"up: 62% chance at or above today"`,
		"quote its view's lead for the desk's numbers, not the summary", "only when the desk's lean is up or down, call propose_trade on that side"} {
		if !strings.Contains(p, want) {
			t.Errorf("context prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "≥ $1,100 20%") || !strings.Contains(p, "median") {
		t.Errorf("context prompt states the top range: %s", p)
	}
	if b, _ := json.Marshal(ref); !strings.Contains(string(b), `"view":{"median":{"value":610.88`) {
		t.Errorf("trade card forecast %s", b)
	}

	// No basis, flat: the 80% range, and no lean.
	r = tsmNumeric(t, func(v map[string]any) { v["basis"] = map[string]any{"kind": "none", "label": "x"} })
	if ref := r.ref(); ref.citeAs() != "Forecast desk: 80% range ≈$242–≈$1,480" || ref.lean() != "no lean: the desk states no median" ||
		!strings.Contains(r.contextPrompt(), `The lean here is "no lean: the desk states no median"`) {
		t.Fatalf("no basis: %q %q", ref.citeAs(), ref.lean())
	}
	// A bucket headline keeps the top outcome, as before.
	r = tsmNumeric(t, func(v map[string]any) { v["headline"] = "bucket" })
	if ref := r.ref(); ref.citeAs() != "Forecast desk: ≥ $1,100 20%" || ref.lean() != "≥ $1,100 20%" || ref.headlineWords() != "≥ $1,100 20%" {
		t.Fatalf("bucket: %q %q", ref.citeAs(), ref.lean())
	}
	if p := r.contextPrompt(); strings.Contains(p, `"view"`) || strings.Contains(p, "numeric report") || !strings.Contains(p, "call propose_trade on the side of the desk's lean") {
		t.Errorf("bucket context prompt changed: %s", p)
	}
	// A binary report has no view anywhere.
	bin := report{ID: "b", Question: "Will BTC close above $120k?", Shape: "binary", Outcomes: []string{"Yes", "No"}}
	bin.Result.Probabilities = []outcomeP{{"Yes", 0.3}, {"No", 0.7}}
	if ref := bin.ref(); ref.View != nil || ref.citeAs() != "Forecast desk: Yes 30% (leans No)" || ref.lean() != "No 70%" {
		t.Fatalf("binary %+v", ref)
	}
	if b, _ := json.Marshal(bin.card()); strings.Contains(string(b), `"view"`) {
		t.Fatalf("binary card %s", b)
	}
}

// get_forecasts and get_forecast state the view in their chips, items and
// cite lines, and the draft card carries the view's reading.
func TestDeskViewTools(t *testing.T) {
	const id = "a0000000-0000-4000-8000-0000000000aa"
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{tool("a", "get_forecasts", `{}`), tool("b", "get_forecast", `{"id":"`+id+`"}`)}},
		llmtest.Reply{Text: "TSM: compounding. **View:** bullish."},
	)})
	r := tsmNumeric(t, nil)
	result, _ := json.Marshal(r.Result)
	outcomes, _ := json.Marshal(r.Outcomes)
	if _, err := e.DB.Exec(context.Background(), `
		insert into forecasts (id, user_id, shape, question, outcomes, resolution_date, status, result, completed_at)
		values ($1, $2, 'ordered', $3, $4, $5, 'completed', $6, '2026-10-08T00:00:00Z')`, id, e.User, r.Question, outcomes, r.ResolutionDate, result); err != nil {
		t.Fatal(err)
	}
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Where will TSM be in 2031?"}`).Body.String())
	var chips []string
	for _, ev := range find(evs, "tool_result") {
		var x struct{ Summary string }
		json.Unmarshal(ev.Data, &x)
		chips = append(chips, x.Summary)
	}
	if !slices.Equal(chips, []string{"1 forecast · latest median $611, 62% chance at or above today's $485.12",
		"median $611, 62% chance at or above today's $485.12 · resolves " + r.ResolutionDate}) {
		t.Errorf("chips %q", chips)
	}
	sent := string(e.Bedrock.Requests()[1].Body)
	for _, want := range []string{`\"lean\":\"up: 62% chance at or above today\"`, `\"view\":{\"median\":{\"value\":610.88`,
		`cite it as \"Forecast desk: median $611, 62% chance at or above today's $485.12\"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("tool results lack %q", want)
		}
	}
	if cards := cardsOf(evs); len(cards) != 1 || fmt.Sprint(cards[0].Data["view"].(map[string]any)["basis"]) != "capped_drift" {
		t.Fatalf("cards %+v", cards)
	}

	h := handler{d: e.Deps}
	run, err := h.draftRun(context.Background(), e.User, r.Question, time.Now().Add(-time.Hour))
	if err != nil || run == nil || run.ViewHeadline != "median $611, 62% chance at or above today's $485.12" || run.View == nil || run.Headline.Outcome != "≥ $1,100" {
		t.Fatalf("draft run %+v %v", run, err)
	}
}
