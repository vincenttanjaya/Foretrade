package llm_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

// chunk is one Chat Completions stream chunk with choices[0].delta and finish_reason.
func chunk(delta, finish string) string {
	f := "null"
	if finish != "" {
		f = `"` + finish + `"`
	}
	return sse(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` +
		delta + `,"finish_reason":` + f + `}]}`)
}

func chatText(parts ...string) answer {
	events := []string{chunk(`{"role":"assistant","content":""}`, "")}
	for _, p := range parts {
		events = append(events, chunk(fmt.Sprintf(`{"content":%q}`, p), ""))
	}
	return stream(append(events, chunk(`{}`, "stop"),
		sse(`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`), sse("[DONE]"))...)
}

// A streamed answer: deltas emitted as they come; the request is a plain
// Chat Completions call with a Bearer key and no ceiling, effort or tools.
func TestChatStreamedText(t *testing.T) {
	f := newFake(t, chatText("Hello", ", world"), chatText("OK"))
	c := f.client(llm.Settings{Provider: "groq", Model: "llama-x", APIKey: "gsk-secret"})
	var deltas []string
	text, err := c.Agent(t.Context(), llm.AgentRequest{System: "be brief", Messages: user("hi")}, func(e llm.Event) error {
		deltas = append(deltas, e.Text)
		return nil
	})
	if err != nil || text != "Hello, world" || !slices.Equal(deltas, []string{"Hello", ", world"}) {
		t.Fatalf("agent: %q %v %q", text, err, deltas)
	}
	if text, err := c.Complete(t.Context(), "", "ping", llm.High); err != nil || text != "OK" {
		t.Fatalf("complete: %q %v", text, err)
	}
	r := f.requests()[0]
	if r.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer gsk-secret" ||
		compact(r.Body) != `{"messages":[{"content":"be brief","role":"system"},{"content":"hi","role":"user"}],"model":"llama-x","stream":true}` {
		t.Fatalf("request: %s %v %s", r.Path, r.Header, r.Raw)
	}
	if b := f.requests()[1].Body; path(b, "messages", 0, "role") != "user" || b["reasoning_effort"] != nil {
		t.Fatalf("complete request: %v", b)
	}

	// Local servers take no key: no Authorization header at all.
	f.push(chatText("local"))
	local := f.client(llm.Settings{Provider: "ollama", Model: "qwen"})
	if _, err := local.Complete(t.Context(), "", "x", ""); err != nil || f.requests()[2].Header.Get("Authorization") != "" {
		t.Fatalf("ollama: %v %v", err, f.requests()[2].Header)
	}
}

// Two-turn tool loops: each provider's opaque fields come back verbatim in
// the replayed assistant message, tool results follow as tool messages in
// call order, and missing call ids are invented in the 9-character form.
func TestChatToolLoopReplay(t *testing.T) {
	for _, tc := range []struct {
		name, preset string
		turn1        []string
		ran          []string
		replay       string // the assistant message of request 2; "ID1"/"ID2" stand for invented ids
	}{
		{"deepseek reasoning_content, fragmented arguments", "deepseek", []string{
			chunk(`{"role":"assistant","content":null,"reasoning_content":"Need the "}`, ""),
			chunk(`{"reasoning_content":"price."}`, ""),
			chunk(`{"content":"Checking."}`, ""),
			chunk(`{"tool_calls":[{"index":0,"id":"call_0_abc","type":"function","function":{"name":"quote","arguments":""}}]}`, ""),
			chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"symbol\":"}}]}`, ""),
			chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"BTC\",\"n\":12345678901234567}"}}]}`, ""),
			chunk(`{}`, "tool_calls"), sse("[DONE]"),
		}, []string{`quote:{"symbol":"BTC","n":12345678901234567}`},
			`{"role":"assistant","content":"Checking.","tool_calls":[{"id":"call_0_abc","type":"function","function":{"name":"quote","arguments":"{\"symbol\":\"BTC\",\"n\":12345678901234567}"}}],"reasoning_content":"Need the price."}`},
		{"gemini extra_content, parallel calls without ids", "gemini", []string{
			chunk(`{"role":"assistant","tool_calls":[{"index":0,"type":"function","function":{"name":"quote","arguments":"{\"symbol\":\"BTC\"}"},"extra_content":{"google":{"thought_signature":"c2lnLUE="}}},`+
				`{"index":1,"type":"function","function":{"name":"news","arguments":"{\"q\":"}}]}`, ""),
			chunk(`{"tool_calls":[{"index":1,"function":{"arguments":"\"ETH\"}"}}]}`, ""),
			chunk(`{}`, "tool_calls"),
		}, []string{`quote:{"symbol":"BTC"}`, `news:{"q":"ETH"}`},
			`{"role":"assistant","content":"","tool_calls":[{"id":"ID1","type":"function","function":{"name":"quote","arguments":"{\"symbol\":\"BTC\"}"},"extra_content":{"google":{"thought_signature":"c2lnLUE="}}},` +
				`{"id":"ID2","type":"function","function":{"name":"news","arguments":"{\"q\":\"ETH\"}"}}]}`},
		{"openrouter reasoning_details", "openrouter", []string{
			chunk(`{"role":"assistant","content":"","reasoning":"Think","reasoning_details":[{"type":"reasoning.text","text":"Think","signature":"s1","format":"anthropic-claude-v1","index":0}]}`, ""),
			chunk(`{"tool_calls":[{"index":0,"id":"toolu_01","type":"function","function":{"name":"quote","arguments":"{}"}}]}`, ""),
			chunk(`{}`, "tool_calls"),
		}, []string{`quote:{}`},
			`{"role":"assistant","content":"","tool_calls":[{"id":"toolu_01","type":"function","function":{"name":"quote","arguments":"{}"}}],"reasoning_details":[{"type":"reasoning.text","text":"Think","signature":"s1","format":"anthropic-claude-v1","index":0}]}`},
		{"mistral thinking chunks, then a closing chunk with text, then string content", "mistral", []string{
			chunk(`{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Need "}]}]}`, ""),
			chunk(`{"content":[{"type":"thinking","thinking":[{"type":"text","text":"a quote."}],"closed":true},{"type":"text","text":"Checking"}]}`, ""),
			chunk(`{"content":"."}`, ""),
			chunk(`{"tool_calls":[{"id":"call_m1","type":"function","function":{"name":"quote","arguments":"{}"},"index":0}]}`, ""),
			chunk(`{}`, "tool_calls"),
		}, []string{`quote:{}`},
			`{"role":"assistant","content":[{"thinking":[{"text":"Need a quote.","type":"text"}],"type":"thinking"},{"text":"Checking.","type":"text"}],` +
				`"tool_calls":[{"id":"call_m1","type":"function","function":{"name":"quote","arguments":"{}"}}]}`},
		{"ollama whole calls without index", "ollama", []string{
			chunk(`{"role":"assistant","content":"","reasoning":"hmm","tool_calls":[{"id":"call_a1","function":{"name":"quote","arguments":"{\"symbol\":\"BTC\"}"}}]}`, ""),
			chunk(`{"role":"assistant","content":"","tool_calls":[{"id":"call_a2","function":{"name":"quote","arguments":"{\"symbol\":\"ETH\"}"}}]}`, ""),
			chunk(`{}`, "tool_calls"),
		}, []string{`quote:{"symbol":"BTC"}`, `quote:{"symbol":"ETH"}`},
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_a1","type":"function","function":{"name":"quote","arguments":"{\"symbol\":\"BTC\"}"}},{"id":"call_a2","type":"function","function":{"name":"quote","arguments":"{\"symbol\":\"ETH\"}"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, stream(tc.turn1...), chatText("Done."))
			c := f.client(llm.Settings{Provider: tc.preset, Model: "m", APIKey: "k"})
			rec := &recorder{}
			var ids []string
			text, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("BTC?"),
				Tools: []llm.Tool{rec.tool("quote", "100000"), rec.tool("news", "calm")}}, func(e llm.Event) error {
				if e.Kind == "tool_call" {
					ids = append(ids, e.ToolID)
				}
				return nil
			})
			if err != nil || text != "Done." || !slices.Equal(rec.inputs, tc.ran) {
				t.Fatalf("agent: %q %v, ran %q", text, err, rec.inputs)
			}
			for _, id := range ids {
				if !regexp.MustCompile(`^(call|toolu)[_0-9a-z]+$`).MatchString(id) {
					t.Fatalf("call id %q", id)
				}
			}
			want := tc.replay
			for i, id := range ids {
				if regexp.MustCompile(`^call\d{5}$`).MatchString(id) {
					want = strings.Replace(want, fmt.Sprintf("ID%d", i+1), id, 1)
				}
			}
			second := f.requests()[1]
			msgs := path(second.Body, "messages").([]any)
			if got := compactOrdered(t, second.Raw, 1); got != want {
				t.Fatalf("replayed assistant message:\n%s\nwant\n%s", got, want)
			}
			for i, id := range ids {
				if m := msgs[2+i]; path(m, "role") != "tool" || path(m, "tool_call_id") != id || path(m, "content") == nil {
					t.Fatalf("tool message %d: %v", i, m)
				}
			}
			if len(msgs) != 2+len(ids) || len(path(second.Body, "tools").([]any)) != 2 {
				t.Fatalf("second request: %s", second.Raw)
			}
		})
	}
}

// compactOrdered is messages[i] of a raw request body, compact, key order kept.
func compactOrdered(t *testing.T, raw string, i int) string {
	t.Helper()
	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil || i >= len(body.Messages) {
		t.Fatalf("body %s: %v", raw, err)
	}
	return string(body.Messages[i])
}

// Arguments that are not a JSON object reach neither the tool nor the
// event as such: the event shows {} and the model gets an error result. The
// replay carries {} too, since servers such as Ollama parse history arguments.
func TestChatInvalidArguments(t *testing.T) {
	f := newFake(t, stream(
		chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"quote","arguments":"{\"symbol\":"}}]}`, ""),
		chunk(`{}`, "tool_calls")), chatText("Sorry."))
	c := f.client(llm.Settings{Provider: "ollama", Model: "small"})
	rec := &recorder{}
	var events []string
	if _, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("x"), Tools: []llm.Tool{rec.tool("quote", "1")}}, func(e llm.Event) error {
		events = append(events, e.Kind+":"+string(e.ToolInput)+e.Output)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rec.inputs) != 0 || len(events) < 2 || events[0] != "tool_call:{}" || !strings.HasPrefix(events[1], "tool_result:Error: the tool arguments were not a valid JSON object") {
		t.Fatalf("ran %q, events %q", rec.inputs, events)
	}
	if args := path(f.requests()[1].Body, "messages", 1, "tool_calls", 0, "function", "arguments"); args != "{}" {
		t.Fatalf("replayed arguments %q, want {}", args)
	}
}

// Flatten on Chat Completions: no tool messages and no replay; the run goes
// back as transcript text in the user message, tools still declared.
func TestChatFlatten(t *testing.T) {
	f := newFake(t, stream(
		chunk(`{"role":"assistant","content":"Checking.","reasoning_content":"private"}`, ""),
		chunk(`{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"quote","arguments":"{}"}}]}`, ""),
		chunk(`{}`, "tool_calls")), chatText("BTC is 100k."))
	c := f.client(llm.Settings{Provider: "deepseek", APIKey: "k"})
	rec := &recorder{}
	text, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("BTC?"), Tools: []llm.Tool{rec.tool("quote", "100000")}, Flatten: true}, nil)
	if err != nil || text != "BTC is 100k." {
		t.Fatalf("%q %v", text, err)
	}
	r := f.requests()[1]
	msgs := path(r.Body, "messages").([]any)
	content, _ := path(msgs[0], "content").(string)
	if len(msgs) != 1 || strings.Contains(r.Raw, `"tool_calls"`) || strings.Contains(r.Raw, `"role":"tool"`) || strings.Contains(r.Raw, "private") ||
		!strings.HasPrefix(content, "BTC?\n\n<run_transcript>") || !strings.Contains(content, `<tool_result name="quote" id="c1">`+"\n100000\n</tool_result>") ||
		path(r.Body, "tools", 0, "function", "name") != "quote" || path(r.Body, "model") != "deepseek-flash" {
		t.Fatalf("flattened request: %s", r.Raw)
	}
}

// Effort is sent per preset, in each provider's field; "off" and "on" override the preset.
func TestChatEffort(t *testing.T) {
	for _, tc := range []struct {
		preset, effort string
		req            llm.Effort
		want           string // the effort fields as compact JSON
	}{
		{"groq", "", llm.High, `{}`},
		{"groq", "on", llm.High, `{"reasoning_effort":"high"}`},
		{"gemini", "", "", `{"reasoning_effort":"medium"}`},
		{"gemini", "off", llm.Low, `{}`},
		{"openrouter", "", llm.Low, `{"reasoning":{"effort":"low"}}`},
		{"deepseek", "on", llm.Medium, `{"reasoning_effort":"high"}`}, // no medium there
		{"deepseek", "on", llm.Low, `{"reasoning_effort":"low"}`},
	} {
		f := newFake(t, chatText("ok"))
		c := f.client(llm.Settings{Provider: tc.preset, Model: "m", APIKey: "k", Effort: tc.effort})
		if _, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("x"), Effort: tc.req}, nil); err != nil {
			t.Fatal(err)
		}
		b := f.requests()[0].Body
		got := map[string]any{}
		for _, k := range []string{"reasoning_effort", "reasoning"} {
			if b[k] != nil {
				got[k] = b[k]
			}
		}
		if compact(got) != tc.want {
			t.Errorf("%s effort %q %q: %s, want %s", tc.preset, tc.effort, tc.req, compact(got), tc.want)
		}
	}
}

// Attachments on the current turn: images as data URLs, PDFs as file parts
// on OpenRouter (an error elsewhere, before any request), text and office
// documents as text.
func TestChatAttachments(t *testing.T) {
	docx, xlsx := llm.DocxFile(t, "Revenue grew."), llm.XlsxFile(t)
	m := llm.Message{Role: "user", Text: "Read these", Images: []llm.Image{{Format: "png", Bytes: []byte{1, 2}}}, Documents: []llm.Document{
		{Format: "pdf", Name: "Document 1 (report)", Bytes: []byte("%PDF-1.4")},
		{Format: "csv", Name: "Document 2", Bytes: []byte("a,b\n1,2\n")},
		{Format: "docx", Name: "Document 3", Bytes: docx},
		{Format: "xlsx", Name: "Document 4", Bytes: xlsx},
	}}
	f := newFake(t, chatText("ok"))
	if _, err := f.client(llm.Settings{Provider: "openrouter", Model: "m", APIKey: "k"}).CompleteWith(t.Context(), "", m, ""); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, p := range path(f.requests()[0].Body, "messages", 0, "content").([]any) {
		switch path(p, "type") {
		case "text":
			kinds = append(kinds, "text:"+path(p, "text").(string))
		case "file":
			kinds = append(kinds, "file:"+compact(p))
		case "image_url":
			kinds = append(kinds, "image:"+path(p, "image_url", "url").(string))
		}
	}
	want := []string{"text:Read these",
		`file:{"file":{"file_data":"data:application/pdf;base64,JVBERi0xLjQ=","filename":"Document 1 (report).pdf"},"type":"file"}`,
		"text:<file name=\"Document 2.csv\">\na,b\n1,2\n\n</file>",
		"text:<file name=\"Document 3.docx\">\nRevenue grew.\n\n</file>",
		"text:<file name=\"Document 4.xlsx\">\n## Prices\nSymbol\tPrice\nBTC\t100000\n\n</file>",
		"image:data:image/png;base64,AQI="}
	if !slices.Equal(kinds, want) {
		t.Fatalf("parts:\n%q\nwant\n%q", kinds, want)
	}

	plain := newFake(t)
	if _, err := plain.client(llm.Settings{Provider: "groq", Model: "m", APIKey: "k"}).CompleteWith(t.Context(), "", m, ""); !errors.Is(err, llm.ErrUpstream) ||
		!strings.Contains(err.Error(), "can't read PDF") || len(plain.requests()) != 0 {
		t.Fatalf("pdf elsewhere: %v, %d requests", err, len(plain.requests()))
	}
}

// Provider failures are *ProviderError: the status, code and message of the
// body, the key removed. A failed stream is ErrUpstream too.
func TestChatErrors(t *testing.T) {
	f := newFake(t,
		failure(401, `{"error":{"message":"Incorrect API key provided: sk-live-secret","type":"invalid_request_error","code":"invalid_api_key"}}`),
		stream(chunk(`{"content":"Hel"}`, ""), sse(`{"error":{"message":"Provider returned error","code":502}}`)),
		stream(chunk(`{"content":"cut off"}`, "")),
	)
	c := f.client(llm.Settings{Provider: "custom", Model: "m", APIKey: "sk-live-secret"})
	_, err := c.Complete(t.Context(), "", "x", "")
	var pe *llm.ProviderError
	if !errors.As(err, &pe) || !errors.Is(err, llm.ErrUpstream) || pe.Status != 401 || pe.Code != "invalid_api_key" ||
		strings.Contains(err.Error(), "sk-live-secret") || !strings.Contains(err.Error(), "Incorrect API key provided") {
		t.Fatalf("401: %v", err)
	}
	for _, want := range []string{"Provider returned error", "finish_reason"} {
		if _, err := c.Complete(t.Context(), "", "x", ""); !errors.Is(err, llm.ErrUpstream) || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q: %v", want, err)
		}
	}
}

func TestChatModels(t *testing.T) {
	f := newFake(t, answer{body: `{"object":"list","data":[{"id":"openai/gpt-x","name":"OpenAI: GPT X"},{"id":"llama3.2:3b","object":"model"}]}`})
	models, err := llm.ListModels(t.Context(), llm.Settings{Provider: "openrouter", APIKey: "k", BaseURL: f.srv.URL})
	if err != nil || compact(models) != `[{"id":"openai/gpt-x","label":"OpenAI: GPT X"},{"id":"llama3.2:3b","label":"llama3.2:3b"}]` {
		t.Fatalf("%v %v", models, err)
	}
	if r := f.requests()[0]; r.Method != "GET" || r.Path != "/models" || r.Header.Get("Authorization") != "Bearer k" {
		t.Fatalf("%+v", r)
	}
}
