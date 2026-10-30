package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// The prompt says what the tools really do: an earlier file is
// gone, a null medianYears is "not within 30 years", the 0 sentinels, the
// quote feeds' candles and the outlook card.
func TestPromptTruth(t *testing.T) {
	if earlierAttachments([]llm.Message{{Role: "user", Text: "What is BTC doing?"}, {Role: "assistant", Text: "It rose [1]."}}) ||
		!earlierAttachments([]llm.Message{{Role: "user", Text: "Read this\n\n[image: chart.png]"}}) ||
		!earlierAttachments([]llm.Message{{Role: "user", Text: "x\n\n[document: q3.pdf]"}}) {
		t.Error("earlierAttachments")
	}
	p := systemPrompt(time.Now(), "")
	for _, want := range []string{
		"Use range24hSource: unavailable high/low 0 sentinels are not measured bounds;",
		"timeToTarget.medianYears null means not within 30 years under the view: say that, never 'unavailable'.",
		"offer draft_forecast (the draft costs 1 credit); call it when the user asks for the desk's number or a forecast (price_outlook can add the outlook card for a universe asset)",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	for _, gone := range []string{"low0sentinels", "add a volatility range"} {
		if strings.Contains(p, gone) {
			t.Errorf("system prompt still says %q", gone)
		}
	}
	for _, tool := range (&agentRun{d: &app.Deps{}}).tools() {
		switch tool.Name {
		case "price_outlook":
			if !strings.Contains(tool.Description, "the median years under the view (medianYears null means not within 30 years under the view: say that, never 'unavailable';") {
				t.Errorf("price_outlook description %q", tool.Description)
			}
		case "get_asset_analysis":
			if !strings.HasPrefix(tool.Description, "Technical snapshot of one asset from Binance crypto, Hyperliquid perp or Yahoo quote-feed candles: ") {
				t.Errorf("get_asset_analysis description %q", tool.Description)
			}
		}
	}
}

// The prompt and the tools say true things: one gain figure
// (the card's pAbove), 3–5 scenarios, pEnd as the odds, the extrapolated
// median, the desk's run time and the tools as they are.
func TestPromptTruthG19(t *testing.T) {
	p := systemPrompt(time.Now(), "")
	for _, want := range []string{
		"5. **Scenarios**: a table of the same 3–5 scenarios you passed to price_outlook",
		"6. **Derived answers**: P(gain) and P(loss) are the card's view.pAbove and 1 − pAbove (they include each scenario's spread); if you show the weight sums, label them (\"scenarios above today: 70% of the weight\", view.pAboveByWeights).",
		"then pass 3–5 scenarios", "(the card's view.pAbove and 1 − pAbove), from my subjective scenarios",
		"Quote pEnd as the odds; give pTouch only labelled", "When medianExtrapolated is true, say the median assumes the scenarios' returns carry on past their horizon.",
		"It shows a draft card with Run (5 credits, usually four to eight minutes).",
		"quote the market price as crowd odds with its [n]; offer draft_forecast (the draft costs 1 credit); call it when the user asks for the desk's number or a forecast.",
		"The user wants odds or a forecast of a future event: offer draft_forecast (the draft costs 1 credit); call it when the user asks for the desk's number or a forecast, with the idea.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	for _, gone := range []string{"the sums of the scenario weights)", "about a minute", "3-4 scenarios", "4–5 scenarios", "P(reaching it)"} {
		if strings.Contains(p, gone) {
			t.Errorf("system prompt still says %q", gone)
		}
	}
	for _, tool := range (&agentRun{d: &app.Deps{}}).tools() {
		d, _ := json.Marshal(tool.Schema)
		for name, want := range map[string]string{
			"price_outlook":      "pEnd (ends at or above it: the card's headline odds) and pTouch (trades there at any point)",
			"draft_forecast":     "the desk runs (5 credits, usually four to eight minutes)",
			"set_drawdown_alert": "a Yahoo quote-feed listing's own lifetime high in its currency",
			"get_derivatives":    "Context only: crypto paper trades fill at Binance spot; stock and index perps at this perp's mark.",
		} {
			if tool.Name == name && !strings.Contains(tool.Description, want) {
				t.Errorf("%s description lacks %q", name, want)
			}
		}
		if tool.Name == "price_outlook" && (!strings.Contains(tool.Description, "medianExtrapolated: the median assumes the scenarios' returns continue past their horizon: say so") ||
			!strings.Contains(string(d), "Your view: 3–5 scenarios at horizon_days")) {
			t.Errorf("price_outlook: %s %s", tool.Description, d)
		}
	}
	twenty := 20.0
	o := market.PriceOutlook{Symbol: "TSM", TimeToTarget: &market.TimeToTarget{Multiple: 10, MedianYears: &twenty, MedianExtrapolated: true}}
	if s := outlookSummary(o, 0); s != "TSM by when · 10× median about 20 years (extrapolated)" {
		t.Errorf("chip %q", s)
	}
}
