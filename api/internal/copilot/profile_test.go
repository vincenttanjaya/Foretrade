package copilot

import (
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
)

func TestProfileExplicitCopilotStyleAndFivePercent(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"direction":"long","confidence":70}`})})
	p, err := profile.Load(t.Context(), e.DB, e.User)
	if err != nil {
		t.Fatal(err)
	}
	prefs := p.Preferences
	prefs.Style = new("scalp")
	prefs.RiskProfile = new("aggressive")
	prefs.CustomPrompt = "PRIVATE_CHAT_INSTRUCTIONS"
	if _, err = profile.Update(t.Context(), e.DB, e.User, prefs, p.Revision); err != nil {
		t.Fatal(err)
	}
	w := e.Do("POST", "/api/copilot/plan", `{"symbol":"SOL","style":"swing"}`)
	got := apptest.JSON[planResponse](t, w)
	if w.Code != 200 || got.Plan.Style != "swing" || got.Analysis.Interval != "4h" || got.Plan.SuggestedNotional != 5000 || got.Credits.Used != 1 {
		t.Fatalf("explicit style/size: %d %s", w.Code, w.Body)
	}
	r := e.Bedrock.Requests()[0]
	if !strings.Contains(r.Prompt, "Plan a swing trade") || strings.Contains(r.Prompt, "PRIVATE_CHAT_INSTRUCTIONS") {
		t.Fatalf("Copilot scope changed: %s", r.Prompt)
	}
}
