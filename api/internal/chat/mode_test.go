package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func TestStreamResponseModes(t *testing.T) {
	for _, tc := range []struct {
		mode, effort string
		tokens       int
	}{
		{"fast", "low", 4096},
		{"quality", "medium", 8192},
		{"", "medium", 8192}, // old clients keep the existing research depth
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: "Hello."})})
			body := map[string]string{"message": "Hello"}
			if tc.mode != "" {
				body["mode"] = tc.mode
			}
			w := e.Do("POST", "/api/chat/stream", body)
			if w.Code != 200 || len(find(parseSSE(t, w.Body.String()), "done")) != 1 {
				t.Fatalf("stream failed: %d %s", w.Code, w.Body)
			}
			reqs := e.Bedrock.Requests()
			if len(reqs) != 1 || reqs[0].Effort != tc.effort {
				t.Fatalf("mode %q: %d calls, expected effort %q", tc.mode, len(reqs), tc.effort)
			}
			var wire struct {
				Inference struct {
					MaxTokens int `json:"maxTokens"`
				} `json:"inferenceConfig"`
			}
			if err := json.Unmarshal(reqs[0].Body, &wire); err != nil || wire.Inference.MaxTokens != tc.tokens {
				t.Fatalf("mode %q token ceiling = %d, want %d (%v)", tc.mode, wire.Inference.MaxTokens, tc.tokens, err)
			}
		})
	}
}

func TestFastModeFinishesAfterFourToolTurns(t *testing.T) {
	replies := []llmtest.Reply{}
	for i := 0; i < 4; i++ {
		replies = append(replies, llmtest.Reply{Tools: []llmtest.ToolUse{tool("portfolio", "get_portfolio", "{}")}})
	}
	replies = append(replies, llmtest.Reply{Text: "Your paper portfolio is empty."})
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, replies...)})
	w := e.Do("POST", "/api/chat/stream", map[string]string{"message": "Review my portfolio", "mode": "fast"})
	if w.Code != 200 || len(find(parseSSE(t, w.Body.String()), "done")) != 1 {
		t.Fatalf("stream failed: %d %s", w.Code, w.Body)
	}
	reqs := e.Bedrock.Requests()
	if len(reqs) != 5 || !strings.Contains(reqs[4].Prompt, "You have used your tool budget") {
		t.Fatalf("fast mode must finish from collected evidence after four tool turns: %d calls", len(reqs))
	}
}

func TestStreamRejectsUnknownModeBeforeSpending(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	w := e.Do("POST", "/api/chat/stream", map[string]string{"message": "Hello", "mode": "max"})
	if w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
		t.Fatalf("invalid mode: %d %s", w.Code, w.Body)
	}
	var chats, spent int
	if err := e.DB.QueryRow(context.Background(), "select count(*) from chats where user_id = $1", e.User).Scan(&chats); err != nil {
		t.Fatal(err)
	}
	credits, err := e.Usage.Get(context.Background(), e.User)
	if err != nil {
		t.Fatal(err)
	}
	spent = credits.Used
	if chats != 0 || spent != 0 || len(e.Bedrock.Requests()) != 0 {
		t.Fatalf("invalid mode changed state: %d chats, %d credits, %d calls", chats, spent, len(e.Bedrock.Requests()))
	}
}
