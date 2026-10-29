package chat

import (
	"context"
	"encoding/json"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"strings"
	"testing"
	"time"
)

func TestReviewPortfolioTool(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	a := &agentRun{d: e.Deps, userID: e.User}
	for _, tool := range a.tools() {
		if tool.Name == "review_portfolio" {
			out, err := tool.Run(context.Background(), json.RawMessage(`{"userID":"foreign"}`))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "untrusted_web_content") {
				t.Fatal("review notes lack untrusted delimiters")
			}
			var r paper.Review
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatal(err)
			}
			if r.Equity != 100000 || r.BetaBtc != nil || a.card != nil {
				t.Fatalf("invalid fresh caller review: %s", out)
			}
			if label(tool.Name, nil) != "Reviewing your portfolio" {
				t.Fatal("wrong chip")
			}
			return
		}
	}
	t.Fatal("review_portfolio tool missing")
}
func TestReviewPromptPolicy(t *testing.T) {
	p := systemPrompt(time.Now(), "")
	for _, needle := range []string{"review_portfolio", "measured facts", "at most one", "untrusted", "no action"} {
		if !strings.Contains(p, needle) {
			t.Errorf("prompt missing %q", needle)
		}
	}
}

func TestReviewPortfolioReachesModelWithoutMutation(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Tools: []llmtest.ToolUse{tool("review1", "review_portfolio", `{}`)}}, llmtest.Reply{Text: "Measured facts: no open positions. Interpretation: no action warranted."})})
	w := e.Do("POST", "/api/chat/stream", map[string]any{"message": "Review my portfolio"})
	if w.Code != 200 {
		t.Fatalf("stream %d %s", w.Code, w.Body)
	}
	reqs := e.Bedrock.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model turns %d", len(reqs))
	}
	if !strings.Contains(string(reqs[1].Body), `untrusted_web_content`) {
		t.Fatal("review notes lack untrusted delimiters")
	}
	if !strings.Contains(string(reqs[1].Body), `\"coverage\"`) || !strings.Contains(string(reqs[1].Body), `\"betaBtc\":null`) || !strings.Contains(string(reqs[1].Body), `\"revision\":0`) {
		t.Fatalf("model did not see actual null/coverage/revision: %s", reqs[1].Body)
	}
	p, err := e.Paper.Portfolio(context.Background(), e.User)
	if err != nil || p.Revision != 0 || len(p.Positions) != 0 {
		t.Fatalf("review mutated book: %+v %v", p, err)
	}
	credits, err := e.Usage.Get(context.Background(), e.User)
	if err != nil || credits.Used != 2 {
		t.Fatalf("review charged extra: %+v %v", credits, err)
	}
	for _, ev := range parseSSE(t, w.Body.String()) {
		if ev.Name == "card" {
			t.Fatal("read tool created a card")
		}
	}
}
