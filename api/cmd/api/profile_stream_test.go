package main

import (
	"bufio"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func TestProfileMemoryStreamDurability(t *testing.T) {
	release := make(chan struct{})
	blocked := make(chan struct{})
	var once sync.Once
	var calls atomic.Int64
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		if !strings.Contains(r.System, "STREAM_OWNED") || strings.Contains(r.System, "CLIENT_FAKE") || !strings.Contains(r.System, "5% of equity") {
			t.Errorf("unowned/inconsistent profile snapshot: %s", r.System)
		}
		if calls.Add(1) == 1 {
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "memory", Name: "remember", Input: `{"text":"I prefer 4h charts"}`}}}
		}
		return llmtest.Reply{Text: "Suggesting a memory; tap Confirm to save it. **View:** Preferences are context only.", AfterFirst: func() { close(blocked); <-release }}
	}})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	c := s.signIn(t)
	p := profileObject(t, c.call("GET", "/api/me/profile", nil, 200), "profile")
	prefs := profilePreferences(p["revision"])
	prefs["riskProfile"] = "conservative"
	prefs["customPrompt"] = "STREAM_OWNED"
	c.call("PUT", "/api/me/profile", prefs, 200)
	req := c.request("POST", "/api/chat/stream", map[string]any{"message": "Remember my 4h preference", "context": map[string]any{"symbol": "BTC", "userId": someID, "profile": map[string]string{"customPrompt": "CLIENT_FAKE", "riskProfile": "aggressive"}}})
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	event := ""
	var card struct {
		Type string
		Data struct{ ProposalID, Text, Status string }
	}
	chatID := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if event == "chat" {
				var chat struct{ ChatID string }
				if err = json.Unmarshal([]byte(data), &chat); err != nil {
					t.Fatal(err)
				}
				chatID = chat.ChatID
			}
			if event == "card" && strings.Contains(data, `"type":"memory"`) {
				if err = json.Unmarshal([]byte(data), &card); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
	}
	if card.Type != "memory" || card.Data.Status != "open" || card.Data.ProposalID == "" {
		t.Fatalf("streaming card missing: %+v %v", card, scanner.Err())
	}
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("model fixture did not pause before persistence")
	}
	body := map[string]string{"proposalId": card.Data.ProposalID, "text": card.Data.Text}
	c.call("POST", "/api/me/profile/memories", body, 404)
	current := profileObject(t, c.call("GET", "/api/me/profile", nil, 200), "profile")
	if len(current["memories"].([]any)) != 0 {
		t.Fatal("streaming card saved before assistant persistence")
	}
	once.Do(func() { close(release) })
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if event == "done" && strings.HasPrefix(line, "data: ") {
			done = true
			value := decode(t, []byte(strings.TrimPrefix(line, "data: ")))
			if str(t, value, "messageId") == "" {
				t.Fatal("done lacks durable assistant ID")
			}
		}
	}
	if err = scanner.Err(); err != nil || !done {
		t.Fatalf("durable done: %v %v", done, err)
	}
	confirmed := c.call("POST", "/api/me/profile/memories", body, 201)
	memory := profileObject(t, confirmed, "memory")
	c.call("POST", "/api/me/profile/memories", body, 409)
	history := c.call("GET", "/api/chats/"+chatID, nil, 200)
	if !strings.Contains(string(mustProfileJSON(t, history)), `"status":"confirmed"`) {
		t.Fatalf("durable history: %v", history)
	}
	if calls.Load() != 2 || num(t, c.call("GET", "/api/me", nil, 200), "credits.used") != 2 {
		t.Fatalf("extra memory call/charge: %d", calls.Load())
	}
	c.call("DELETE", "/api/me/profile/memories/"+memory["id"].(string), map[string]any{}, 200)
	c.call("DELETE", "/api/chats/"+chatID, map[string]any{}, 204)
	c.call("POST", "/api/me/profile/memories", body, 409)
}

func mustProfileJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
