package main

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// Round 4: an answer survives the client leaving. The run continues on the
// server, GET /api/chats/{id} says pending until the assistant message is
// stored, and the message is charged. Chat's get_forecasts lists lineage heads
// only, so a superseded run never reaches the agent.
func TestIntegrationChatResilience(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) }) // never leave the fake Bedrock blocked
	var calls atomic.Int32
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, "You are the trading desk analyst") {
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		}
		switch calls.Add(1) {
		case 1: // the client is gone by the time this returns
			close(entered)
			<-release
			return llmtest.Reply{Text: "Checking your book.", Tools: []llmtest.ToolUse{{ID: "p", Name: "get_portfolio"}}}
		case 2:
			return llmtest.Reply{Text: "All cash, no positions. **View:** neutral."}
		case 3:
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "f", Name: "get_forecasts", Input: "{}"}}}
		}
		return llmtest.Reply{Text: "One live forecast. **View:** neutral."}
	}})
	c := s.signIn(t)

	// The client reads the chat event, then leaves while the model works.
	ctx, cancel := context.WithCancel(t.Context())
	req := c.request("POST", "/api/chat/stream", map[string]string{"message": "How is my book?"}).WithContext(ctx)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	var chatID string
	for chatID == "" && sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var ev struct{ ChatID string }
			json.Unmarshal([]byte(data), &ev)
			chatID = ev.ChatID
		}
	}
	<-entered
	cancel()
	resp.Body.Close()
	if chatID == "" {
		t.Fatal("no chat event")
	}

	got := c.call("GET", "/api/chats/"+chatID, nil, 200)
	shape(t, got, "chat", "id title messages pending")
	if at(t, got, "chat.pending") != true || len(list(t, got, "chat.messages")) != 1 {
		t.Fatalf("during the run: %v", got)
	}
	once.Do(func() { close(release) })
	eventually(t, 10*time.Second, "the stored answer", func() bool {
		return at(t, c.call("GET", "/api/chats/"+chatID, nil, 200), "chat.pending") == false
	})
	got = c.call("GET", "/api/chats/"+chatID, nil, 200)
	if m := list(t, got, "chat.messages"); len(m) != 2 || str(t, got, "chat.messages.1.content") != "Checking your book.\n\nAll cash, no positions. **View:** neutral." {
		t.Fatalf("after the run: %v", got)
	}
	if n := s.count(t, "select coalesce(sum(credits), 0) from ai_usage where user_id = $1", c.id); n != 2 {
		t.Fatalf("credits %d: an answer that ran is charged", n)
	}

	// An updated forecast reaches the agent once, as its newest run.
	var parent, child string
	if err := s.db.QueryRow(t.Context(), `insert into forecasts (user_id, shape, question, outcomes, resolution_criteria, resolution_date, status, result)
		values ($1, 'binary', 'Will SOL close above $210 next week?', '["Yes","No"]', 'Binance SOLUSDT daily close.', '2099-01-01', 'completed',
			'{"probabilities":[{"outcome":"Yes","p":0.28},{"outcome":"No","p":0.72}]}') returning id`, c.id).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(t.Context(), `insert into forecasts (user_id, shape, question, outcomes, resolution_criteria, resolution_date, status, result, parent_id)
		select user_id, shape, question, outcomes, resolution_criteria, resolution_date, 'completed',
			'{"probabilities":[{"outcome":"Yes","p":0.41},{"outcome":"No","p":0.59}]}', id from forecasts where id = $1 returning id`, parent).Scan(&child); err != nil {
		t.Fatal(err)
	}
	evs := c.stream(map[string]string{"message": "What does my forecast desk say?"})
	var summary string
	for _, e := range evs {
		if e.name == "tool_result" {
			summary = str(t, e.data, "summary")
		}
	}
	if summary != "1 forecast · latest Yes 41%" || evs[len(evs)-1].name != "done" {
		t.Fatalf("get_forecasts chip %q, events %v", summary, evs)
	}
	var sent string
	for _, r := range s.bedrock.Requests() {
		if strings.HasPrefix(r.System, "You are the trading desk analyst") {
			sent = string(r.Body) // the last chat call carries the tool result
		}
	}
	if !strings.Contains(sent, child) || strings.Contains(sent, parent) {
		t.Fatalf("get_forecasts must list the lineage head only:\n%s", sent)
	}
}
