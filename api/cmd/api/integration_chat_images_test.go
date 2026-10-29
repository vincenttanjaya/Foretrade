package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func TestIntegrationChatImages(t *testing.T) {
	var imageCalls int
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(b.Bytes())
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		count := 0
		for _, m := range r.Messages {
			for _, block := range m.Content {
				if img, ok := block["image"].(map[string]any); ok {
					count++
					if img["format"] != "png" || img["source"].(map[string]any)["bytes"] != encoded {
						t.Error("wrong image bytes on wire")
					}
				}
			}
		}
		if strings.HasPrefix(r.Prompt, "Refresh this setup") {
			if count != 0 || !strings.Contains(string(r.Body), "[image: chart.png]") {
				t.Error("historical image bytes or missing marker")
			}
			return llmtest.Reply{Text: "History needs reattachment. **View:** neutral."}
		}
		if count != 1 || slices.Contains(r.Tools, "draft_forecast") {
			t.Errorf("image count %d, paid tool declared %v", count, slices.Contains(r.Tools, "draft_forecast"))
		}
		imageCalls++
		if !r.AfterTools {
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "f", Name: "draft_forecast", Input: `{"idea":"BTC tomorrow"}`}}}
		}
		if !strings.Contains(r.Prompt, "unknown tool") {
			t.Error("forced forecast call executed")
		}
		return llmtest.Reply{Text: "Review chart structure against live tools. **View:** neutral."}
	}})
	c := s.signIn(t)
	input := map[string]any{"kind": "image", "mediaType": "image/png", "name": "../chart.png", "data": encoded}
	c.fail("POST", "/api/chat/stream", map[string]any{"message": "Read this chart", "attachments": []any{input, input, input, input, input, input}}, 400, "bad_request")
	if n := s.count(t, "select count(*) from chats where user_id=$1", c.id); n != 0 {
		t.Fatal("invalid image created chat")
	}
	if n := s.count(t, "select coalesce(sum(credits),0) from ai_usage where user_id=$1", c.id); n != 0 {
		t.Fatal("invalid image charged")
	}
	evs := c.stream(map[string]any{"message": "Read this chart", "attachments": []any{input}})
	if imageCalls != 2 || evs[len(evs)-1].name != "done" || num(t, evs[len(evs)-1].data, "credits.used") != 3 {
		t.Fatalf("wire/charge %d %v", imageCalls, evs)
	}
	id := str(t, evs[0].data, "chatId")
	got := c.call("GET", "/api/chats/"+id, nil, 200)
	shape(t, got, "chat.messages.0.attachments.0", "kind mediaType name size")
	if str(t, got, "chat.title") != "Read this chart" || str(t, got, "chat.messages.0.content") != "Read this chart\n\n[image: chart.png]" || str(t, got, "chat.messages.0.attachments.0.name") != "chart.png" || num(t, got, "chat.messages.0.attachments.0.size") != float64(b.Len()) || len(list(t, got, "chat.messages.1.attachments")) != 0 {
		t.Fatalf("stored metadata %v", got)
	}
	var saved string
	if err := s.db.QueryRow(t.Context(), "select row_to_json(m)::text from chat_messages m where chat_id=$1 and role='user'", id).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved, encoded) {
		t.Fatal("database retained bytes")
	}
	c.stream(map[string]any{"chatId": id, "message": "Refresh this setup"})
	if n := s.count(t, "select coalesce(sum(credits),0) from ai_usage where user_id=$1", c.id); n != 5 {
		t.Fatalf("ordinary text cost changed: %d", n)
	}
	if n := s.count(t, "select count(*) from trades where user_id=$1", c.id); n != 0 {
		t.Fatal("image executed trade")
	}
}
