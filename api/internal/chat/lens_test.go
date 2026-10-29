package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
)

// The prompt infers investor, trader or hybrid from the question and keeps
// every rule under each; the number and possibility rules hold the line.
func TestLensPromptRules(t *testing.T) {
	p := systemPrompt(time.Now(), "")
	for _, want := range []string{
		"You serve investors and traders alike: the Lens section decides which one a question needs.",
		"## Lens: investor, trader or hybrid\nInfer what the user needs from the question and the chat so far",
		"Never name or announce the lens",
		"- **Investor**: words such as invest, hold, long term, valuation, fundamentals, 10x, multi-bagger",
		"a horizon in years, or meeting a chairman, CEO, founders or an IC. Write the investment memo",
		"thesis, valuation, scenarios with labelled subjective odds over a multi-year horizon, risks, catalysts",
		"- **Trader**: words such as entry, stop, target, today, this week, scalp, day trade, swing, leverage, setup, breakout, or a horizon in minutes, hours or days.",
		"the entry, stop and target with reward:risk, the size within the cap, what invalidates it and a horizon of hours to weeks",
		"- **Hybrid**: the question mixes both",
		"or no signal is clear and both apply. Lead with the long-term view in brief",
		"then the tactical plan: an entry zone, the stop or invalidation level, the size, and when to add or trim.",
		"- A quick factual question (a price, a level, a definition) stays short under every lens.",
		`When a "Lens for this message" line below names investor or trader, the user chose it`,
		"- Every lens keeps the data, citation, card, confirmation, paper-only and **View:** rules; only the shape and the horizon change.",
		"a hybrid answer gets the brief long-term view, then the plan, roughly 300–500 words",
		"- Never show a number you can't stand behind. A figure is unavailable, not a number to quote, when a tool leaves it null or its notes withhold or flag it",
		"(a lifetime high hundreds of times the price, a market cap far from price × shares). Say in one clause that it is unavailable and why",
		"when two sources disagree far beyond rounding, say so instead of picking one. Never patch a figure with an estimate.",
		`For an investor question that asks for a view (a 10x or multi-bagger, the odds of a gain or a loss, "should we invest", "is it worth it", a long-term hold, meeting prep), write an investment memo`,
		"call set_drawdown_alert. It shows an alert card; nothing is set until they tap Set alert.",
		"Never claim you placed, filled or executed anything.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// The lens rules sit with the other answer rules, before the tools that serve them.
	if i, j := strings.Index(p, "## Lens:"), strings.Index(p, "## Tools"); i < 0 || j < i {
		t.Error("the Lens section is not before the Tools section")
	}
	if strings.Count(p, "## Lens:") != 1 || strings.Contains(p, "Lens for this message:") {
		t.Error("the static prompt names a message's lens")
	}
}

// The request's lens reaches the model as one trusted line; auto (or none)
// adds nothing unless a saved trading style hints at the default.
func TestStreamLens(t *testing.T) {
	for _, tc := range []struct {
		lens, want string
	}{
		{"", ""},
		{"auto", ""},
		{"investor", "Lens for this message: investor, chosen by the user. Frame the answer as an investor"},
		{"trader", "Lens for this message: trader, chosen by the user. Frame the answer as a trader"},
	} {
		t.Run("lens="+tc.lens, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: "**View:** neutral."})})
			body := map[string]string{"message": "Should I buy NVDA?"}
			if tc.lens != "" {
				body["lens"] = tc.lens
			}
			if w := e.Do("POST", "/api/chat/stream", body); w.Code != 200 || len(find(parseSSE(t, w.Body.String()), "done")) != 1 {
				t.Fatalf("stream: %d %s", w.Code, w.Body)
			}
			sys := e.Bedrock.Requests()[0].System
			if tc.want == "" {
				if strings.Contains(sys, "Lens for this message:") {
					t.Fatalf("lens %q added a line", tc.lens)
				}
				return
			}
			if strings.Count(sys, "Lens for this message:") != 1 || !strings.Contains(sys, tc.want) {
				t.Fatalf("lens %q not passed: %.300s", tc.lens, sys[strings.LastIndex(sys, "\n\n"):])
			}
		})
	}
}

func TestStreamLensFollowsSavedStyle(t *testing.T) {
	const hint = "Lens for this message: inferred from the question. The user's saved trading style is swing (days to about two weeks): " +
		"when the question gives no signal, lean trader on that horizon; a clear investor or hybrid signal in the question still wins."
	for _, tc := range []struct {
		name, lens string
		enabled    bool
		want       string
	}{
		{"auto", "auto", true, hint},
		{"absent", "", true, hint},
		{"chosen", "investor", true, "Lens for this message: investor, chosen by the user."},
		{"personalization off", "auto", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: "**View:** neutral."})})
			p, err := profile.Load(t.Context(), e.DB, e.User)
			if err != nil {
				t.Fatal(err)
			}
			prefs := p.Preferences
			prefs.Enabled, prefs.Style, prefs.CustomPrompt = tc.enabled, new("swing"), "PROFILE_DATA"
			if _, err = profile.Update(t.Context(), e.DB, e.User, prefs, p.Revision); err != nil {
				t.Fatal(err)
			}
			body := map[string]string{"message": "What about SOL?"}
			if tc.lens != "" {
				body["lens"] = tc.lens
			}
			if w := e.Do("POST", "/api/chat/stream", body); w.Code != 200 {
				t.Fatalf("stream: %d %s", w.Code, w.Body)
			}
			sys := e.Bedrock.Requests()[0].System
			if tc.want == "" {
				if strings.Contains(sys, "Lens for this message:") || strings.Contains(sys, "PROFILE_DATA") {
					t.Fatal("personalization off still hints the lens")
				}
				return
			}
			line, data := strings.Index(sys, tc.want), strings.Index(sys, "<untrusted_profile>")
			if line < 0 || strings.Count(sys, "Lens for this message:") != 1 || data < line || (tc.lens == "investor" && strings.Contains(sys, "saved trading style")) {
				t.Fatalf("lens line %d, profile data %d: %s", line, data, sys[strings.Index(sys, "\n\nLens"):])
			}
		})
	}
}

func TestStreamRejectsUnknownLensBeforeSpending(t *testing.T) {
	for _, body := range []any{
		map[string]string{"message": "Hello", "lens": "hybrid"},
		map[string]string{"message": "Hello", "lens": "Investor"},
		map[string]any{"message": "Hello", "lens": 1},
	} {
		e := apptest.New(t, Register, apptest.Options{})
		w := e.Do("POST", "/api/chat/stream", body)
		if w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
			t.Fatalf("%v: %d %s", body, w.Code, w.Body)
		}
		if _, ok := body.(map[string]string); ok && !strings.Contains(w.Body.String(), "lens must be auto, investor or trader.") {
			t.Errorf("message %s", w.Body)
		}
		var chats int
		if err := e.DB.QueryRow(context.Background(), "select count(*) from chats where user_id = $1", e.User).Scan(&chats); err != nil {
			t.Fatal(err)
		}
		if chats != 0 || spent(t, e) != 0 || len(e.Bedrock.Requests()) != 0 {
			t.Fatalf("invalid lens changed state: %d chats, %d credits, %d calls", chats, spent(t, e), len(e.Bedrock.Requests()))
		}
	}
}
