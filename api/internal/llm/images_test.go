package llm_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func TestAgentCurrentImagesAcrossModes(t *testing.T) {
	for _, flat := range []bool{false, true} {
		t.Run(fmt.Sprint(flat), func(t *testing.T) {
			c, srv := llmtest.New(t, llmtest.Queue(t,
				llmtest.Reply{Reasoning: "data", Tools: []llmtest.ToolUse{{ID: "t", Name: "read"}}},
				llmtest.Reply{}, llmtest.Reply{Text: "Answer"},
			))
			current := []byte{1, 2, 3, 4}
			req := llm.AgentRequest{Flatten: flat, MaxTurns: 1, Messages: []llm.Message{
				{Role: "user", Text: "Prior [image: old.png]", Images: []llm.Image{{Format: "png", Bytes: []byte{9, 9}}}},
				{Role: "assistant", Text: "Prior answer", Images: []llm.Image{{Format: "jpeg", Bytes: []byte{8, 8}}}},
				{Role: "user", Text: "Current", Images: []llm.Image{{Format: "webp", Bytes: current}}},
			}, Tools: []llm.Tool{{Name: "read", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (string, error) { return "data", nil }}}, Nudge: func() string { return "Write the answer" }}
			if _, err := c.Agent(t.Context(), req, nil); err != nil {
				t.Fatal(err)
			}
			if len(srv.Requests()) != 3 {
				t.Fatal("missing tool/final/nudge request")
			}
			for i, r := range srv.Requests() {
				count := 0
				for _, m := range r.Messages {
					for _, b := range m.Content {
						if img, ok := b["image"].(map[string]any); ok {
							count++
							if img["format"] != "webp" || img["source"].(map[string]any)["bytes"] != base64.StdEncoding.EncodeToString(current) {
								t.Fatal("wrong/historical image")
							}
						}
					}
				}
				if count != 1 {
					t.Fatalf("request %d, flatten %v: %d image blocks", i, flat, count)
				}
			}
		})
	}
}

func TestAgentCurrentDocumentsAcrossModes(t *testing.T) {
	for _, flat := range []bool{false, true} {
		t.Run(fmt.Sprint(flat), func(t *testing.T) {
			c, srv := llmtest.New(t, llmtest.Queue(t,
				llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "t", Name: "read"}}}, llmtest.Reply{Text: "Answer"},
			))
			pdf, csv := []byte("%PDF-1.4"), []byte("a,b\n1,2\n")
			req := llm.AgentRequest{Flatten: flat, MaxTurns: 2, Messages: []llm.Message{
				{Role: "user", Text: "Prior [document: old.pdf]", Documents: []llm.Document{{Format: "pdf", Name: "Document 1 (old)", Bytes: []byte("%PDF-old")}}},
				{Role: "assistant", Text: "Prior answer"},
				{Role: "user", Text: "Current", Images: []llm.Image{{Format: "gif", Bytes: []byte{7}}}, Documents: []llm.Document{
					{Format: "pdf", Name: "Document 1 (report)", Bytes: pdf}, {Format: "csv", Name: "Document 2", Bytes: csv},
				}},
			}, Tools: []llm.Tool{{Name: "read", Schema: map[string]any{"type": "object"}, Run: func(context.Context, json.RawMessage) (string, error) { return "data", nil }}}}
			if _, err := c.Agent(t.Context(), req, nil); err != nil {
				t.Fatal(err)
			}
			if len(srv.Requests()) != 2 {
				t.Fatalf("requests %d", len(srv.Requests()))
			}
			for i, r := range srv.Requests() {
				var got []string
				images := 0
				for _, m := range r.Messages {
					for _, b := range m.Content {
						if doc, ok := b["document"].(map[string]any); ok {
							got = append(got, fmt.Sprint(doc["format"], "|", doc["name"], "|", doc["source"].(map[string]any)["bytes"]))
						}
						if b["image"] != nil {
							images++
						}
					}
				}
				want := []string{"pdf|Document 1 (report)|" + base64.StdEncoding.EncodeToString(pdf), "csv|Document 2|" + base64.StdEncoding.EncodeToString(csv)}
				if fmt.Sprint(got) != fmt.Sprint(want) || images != 1 {
					t.Fatalf("request %d, flatten %v: documents %v images %d", i, flat, got, images)
				}
			}
		})
	}
}

// blockKinds lists a request's last-message blocks as text|document|image, with each document's format and name.
func blockKinds(r llmtest.Request) []string {
	var out []string
	for _, b := range r.Messages[len(r.Messages)-1].Content {
		switch {
		case b["text"] != nil:
			out = append(out, "text")
		case b["document"] != nil:
			d := b["document"].(map[string]any)
			out = append(out, fmt.Sprint("document|", d["format"], "|", d["name"], "|", d["source"].(map[string]any)["bytes"]))
		case b["image"] != nil:
			i := b["image"].(map[string]any)
			out = append(out, fmt.Sprint("image|", i["format"], "|", i["source"].(map[string]any)["bytes"]))
		}
	}
	return out
}

// One completion with a document: the text first, then the document block,
// in a single user turn.
func TestCompleteWithDocument(t *testing.T) {
	c, srv := llmtest.New(t, llmtest.Queue(t, llmtest.Reply{Text: "Read."}, llmtest.Reply{Text: "Seen."}))
	pdf := []byte("%PDF-1.4 note")
	text, err := c.CompleteWith(t.Context(), "Read the user's material", llm.Message{Role: "user", Text: "Material D1",
		Documents: []llm.Document{{Format: "pdf", Name: "Document 1 (Q3 note)", Bytes: pdf}}}, llm.Medium)
	if err != nil || text != "Read." {
		t.Fatalf("%q %v", text, err)
	}
	r := srv.Requests()[0]
	want := "[text document|pdf|Document 1 (Q3 note)|" + base64.StdEncoding.EncodeToString(pdf) + "]"
	if got := blockKinds(r); len(r.Messages) != 1 || fmt.Sprint(got) != want || r.Prompt != "Material D1" || r.System != "Read the user's material" {
		t.Fatalf("blocks %v prompt %q", got, r.Prompt)
	}
	if _, err := c.CompleteWith(t.Context(), "", llm.Message{Text: "Chart", Images: []llm.Image{{Format: "jpeg", Bytes: []byte{1, 2}}}}, llm.Low); err != nil {
		t.Fatal(err)
	}
	if got := blockKinds(srv.Requests()[1]); fmt.Sprint(got) != "[text image|jpeg|AQI=]" {
		t.Fatalf("image blocks %v", got)
	}
}

// The JSON retry is a fresh one-turn request, so it carries the file again.
func TestCompleteJSONWithRetryResendsAttachments(t *testing.T) {
	c, srv := llmtest.New(t, llmtest.Queue(t, llmtest.Reply{Text: "not json"}, llmtest.Reply{Text: `{"summary":"ok"}`}))
	var out struct{ Summary string }
	m := llm.Message{Role: "user", Text: "Read", Documents: []llm.Document{{Format: "csv", Name: "Document 1 (prices)", Bytes: []byte("a\n")}},
		Images: []llm.Image{{Format: "png", Bytes: []byte{7}}}}
	if err := c.CompleteJSONWith(t.Context(), "sys", m, llm.Medium, &out); err != nil || out.Summary != "ok" {
		t.Fatalf("%+v %v", out, err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	for i, r := range reqs {
		if got := blockKinds(r); len(r.Messages) != 1 || fmt.Sprint(got) != "[text document|csv|Document 1 (prices)|YQo= image|png|Bw==]" {
			t.Fatalf("request %d blocks %v", i, got)
		}
	}
	if !strings.HasPrefix(reqs[1].Prompt, "Read\n\nYour previous reply could not be used: ") || m.Text != "Read" {
		t.Fatalf("retry prompt %q", reqs[1].Prompt)
	}
}
