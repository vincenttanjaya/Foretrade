package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// A round count alone must not let request seven reach the SDK.
func TestResearchDispatchRequestLimit(t *testing.T) {
	c, srv := llmtest.New(t, func(llmtest.Request) llmtest.Reply {
		return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "read", Name: "look", Input: `{}`}}}
	})
	marked := 0
	_, err := c.Agent(t.Context(), llm.AgentRequest{
		Messages: user("research"), Tools: []llm.Tool{{Name: "look", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (string, error) { return "stored", nil }}},
		MaxTurns: 9, MaxRequests: 6, NoRetries: true,
		BeforeRequest: func(context.Context) error { marked++; return nil },
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "request limit") || len(srv.Requests()) != 6 || marked != 6 {
		t.Fatalf("request seven was not refused: err=%v requests=%d marked=%d", err, len(srv.Requests()), marked)
	}
}

// Four tool rounds, the final turn and the recovery turn all pass the same
// authorization boundary; the marker is already present when HTTP receives it.
func TestResearchDispatchFinalAndNudge(t *testing.T) {
	marks, received, ran := 0, 0, 0
	c, srv := llmtest.New(t, func(r llmtest.Request) llmtest.Reply {
		received++
		if marks != received {
			t.Errorf("request %d arrived before marker %d", received, marks)
		}
		var body struct {
			Inference struct {
				MaxTokens int `json:"maxTokens"`
			} `json:"inferenceConfig"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil || body.Inference.MaxTokens != 4096 || r.Effort != "low" {
			t.Errorf("bounded request %d: tokens=%d effort=%q err=%v", received, body.Inference.MaxTokens, r.Effort, err)
		}
		if received <= 4 {
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: fmt.Sprint(received), Name: "look"}}}
		}
		if received == 5 {
			return llmtest.Reply{}
		}
		return llmtest.Reply{Text: "Recovered report."}
	})
	text, err := c.Agent(t.Context(), llm.AgentRequest{
		Messages: user("research"), Tools: []llm.Tool{{Name: "look", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (string, error) { ran++; return "stored", nil }}},
		MaxTurns: 4, MaxRequests: 6, MaxTokens: 4096, Effort: llm.Low, Flatten: true, NoRetries: true,
		BeforeRequest: func(context.Context) error { marks++; return nil },
		Nudge:         func() string { return "Write the report." },
	}, nil)
	if err != nil || text != "Recovered report." || len(srv.Requests()) != 6 || marks != 6 || ran != 4 {
		t.Fatalf("final/nudge budget: text=%q err=%v requests=%d marks=%d tools=%d", text, err, len(srv.Requests()), marks, ran)
	}
}

func TestResearchDispatchAuthorityBeforeEveryRequest(t *testing.T) {
	for _, denyAt := range []int{1, 2, 5, 6} {
		t.Run(fmt.Sprint(denyAt), func(t *testing.T) {
			denied := errors.New("lease revoked")
			received, marks := 0, 0
			c, srv := llmtest.New(t, func(llmtest.Request) llmtest.Reply {
				received++
				if received <= 4 {
					return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: fmt.Sprint(received), Name: "look"}}}
				}
				return llmtest.Reply{}
			})
			_, err := c.Agent(t.Context(), llm.AgentRequest{
				Messages: user("research"), Tools: []llm.Tool{{Name: "look", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (string, error) { return "stored", nil }}},
				MaxTurns: 4, MaxRequests: 6, Flatten: true, NoRetries: true, Nudge: func() string { return "Write." },
				BeforeRequest: func(context.Context) error {
					marks++
					if marks == denyAt {
						return denied
					}
					return nil
				},
			}, nil)
			if err != denied || len(srv.Requests()) != denyAt-1 || marks != denyAt {
				t.Fatalf("denied boundary %d: err=%v requests=%d marks=%d", denyAt, err, len(srv.Requests()), marks)
			}
		})
	}
}

// A returned SDK error is not itself evidence of a settled inference.
func TestResearchDispatchSettlement(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reply     llmtest.Reply
		transport bool
		want      llm.RequestOutcome
	}{
		{"rejected", llmtest.Reply{Status: http.StatusTooManyRequests}, false, llm.RequestOutcome{Settled: true}},
		{"private", llmtest.Reply{Reasoning: "private"}, false, llm.RequestOutcome{Produced: true, Settled: true}},
		{"malformed", llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "bad", Name: "look", Input: `{"bad":`}}}, false, llm.RequestOutcome{Produced: true}},
		{"transport", llmtest.Reply{}, true, llm.RequestOutcome{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := llmtest.New(t, llmtest.Queue(t, tc.reply))
			if tc.transport {
				cfg := srv.Config()
				cfg.HTTPClient = doFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("lost transport") })
				c = llm.New(cfg, llmtest.Model)
			}
			var outcomes []llm.RequestOutcome
			_, _ = c.Agent(t.Context(), llm.AgentRequest{Messages: user("research"), NoRetries: true,
				OnRequestDone: func(o llm.RequestOutcome) { outcomes = append(outcomes, o) },
			}, nil)
			if len(outcomes) != 1 || outcomes[0] != tc.want {
				t.Fatalf("settlement facts=%+v want=%+v", outcomes, tc.want)
			}
		})
	}
}
