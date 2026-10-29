package llmtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// The fake must speak what the real SDK reads: a Converse reply, a streamed
// turn with reasoning, text and a tool call, scripted HTTP and stream errors.
func TestFakeBedrockThroughClient(t *testing.T) {
	ctx := context.Background()
	c, srv := llmtest.New(t, llmtest.Queue(t,
		llmtest.Reply{Text: "plain"},
		llmtest.Reply{Reasoning: "hmm", Text: "Looking…", Tools: []llmtest.ToolUse{{ID: "t1", Name: "echo", Input: `{"x":1}`}}},
		llmtest.Reply{Text: "final"},
		llmtest.Reply{Status: 400},
		llmtest.Reply{StreamErr: "overloaded"},
	))
	if text, err := c.Complete(ctx, "be brief", "hi", llm.High); err != nil || text != "plain" {
		t.Fatalf("complete: %q %v", text, err)
	}
	echo := llm.Tool{Name: "echo", Schema: map[string]any{"type": "object"},
		Run: func(_ context.Context, in json.RawMessage) (string, error) { return string(in), nil }}
	var deltas string
	text, err := c.Agent(ctx, llm.AgentRequest{System: "agent", Messages: []llm.Message{{Role: "user", Text: "go"}}, Tools: []llm.Tool{echo}},
		func(e llm.Event) error { deltas += e.Text; return nil })
	if err != nil || text != "final" || deltas != "Looking…final" {
		t.Fatalf("agent: %q %v, deltas %q", text, err, deltas)
	}
	if _, err := c.Complete(ctx, "", "x", ""); !errors.Is(err, llm.ErrUpstream) {
		t.Fatalf("scripted 400: %v", err)
	}
	if _, err := c.Agent(ctx, llm.AgentRequest{Messages: []llm.Message{{Role: "user", Text: "go"}}}, nil); !errors.Is(err, llm.ErrUpstream) {
		t.Fatalf("stream exception: %v", err)
	}

	reqs := srv.Requests()
	if len(reqs) != 5 || reqs[0].Stream || reqs[0].System != "be brief" || reqs[0].Prompt != "hi" || reqs[0].Effort != "high" {
		t.Fatalf("converse request: %+v", reqs[0])
	}
	if r := reqs[1]; !r.Stream || r.System != "agent" || len(r.Tools) != 1 || r.Tools[0] != "echo" || r.Effort != "medium" || r.AfterTools {
		t.Fatalf("stream request: %+v", r)
	}
	if r := reqs[2]; len(r.Messages) != 3 || r.Messages[2].Role != "user" || r.Messages[2].Content[0]["toolResult"] == nil || !r.AfterTools {
		t.Fatalf("tool turn: %s", r.Body)
	}
}

// Scripted deltas arrive as given, and AfterFirst runs while the client
// already has the first one: the fake does not buffer a turn to its end.
func TestDeltasArriveLive(t *testing.T) {
	got := make(chan string, 4)
	c, _ := llmtest.New(t, llmtest.Queue(t, llmtest.Reply{Text: "ab", Deltas: []string{"c", "", "d"}, AfterFirst: func() {
		select {
		case d := <-got:
			if d != "a" {
				t.Errorf("first delta %q", d)
			}
		case <-time.After(5 * time.Second):
			t.Error("the first delta did not reach the client before the rest was sent")
		}
	}}))
	var deltas []string
	text, err := c.Agent(context.Background(), llm.AgentRequest{Messages: []llm.Message{{Role: "user", Text: "go"}}}, func(e llm.Event) error {
		deltas = append(deltas, e.Text)
		got <- e.Text
		return nil
	})
	if err != nil || text != "abcd" || len(deltas) != 4 || deltas[3] != "d" {
		t.Fatalf("%q %v, deltas %q", text, err, deltas)
	}
}

type failRecorder struct {
	testing.TB
	failed bool
}

func (f *failRecorder) Errorf(string, ...any) { f.failed = true }

func TestQueueExhausted(t *testing.T) {
	ft := &failRecorder{TB: t} // a call past the script must fail the test that owns the queue
	if r := llmtest.Queue(ft)(llmtest.Request{}); r.Status != 500 || !ft.failed {
		t.Fatalf("exhausted queue: %+v, failed %v", r, ft.failed)
	}
}
