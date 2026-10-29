package llm_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

// Anthropic Messages stream events, as documented.
func msgStart() string {
	return named("message_start", `{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","content":[],`+
		`"model":"claude-sonnet-5-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":1}}}`)
}
func blockStart(i int, block string) string {
	return named("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":%s}`, i, block))
}
func blockDelta(i int, delta string) string {
	return named("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":%s}`, i, delta))
}
func blockStop(i int) string {
	return named("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i))
}
func msgEnd(stop string) []string {
	return []string{named("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+stop+`","stop_sequence":null},"usage":{"output_tokens":15}}`),
		named("message_stop", `{"type":"message_stop"}`)}
}
func textDelta(s string) string { return fmt.Sprintf(`{"type":"text_delta","text":%q}`, s) }

func anthText(stop string, parts ...string) answer {
	events := []string{msgStart(), blockStart(0, `{"type":"text","text":""}`), named("ping", `{"type": "ping"}`)}
	for _, p := range parts {
		events = append(events, blockDelta(0, textDelta(p)))
	}
	return stream(append(append(events, blockStop(0)), msgEnd(stop)...)...)
}

// The request: x-api-key and version headers, the preset's default model,
// max_tokens, system, effort in output_config, tools with input_schema.
func TestMessagesRequest(t *testing.T) {
	f := newFake(t, anthText("end_turn", "Hel", "lo"), anthText("end_turn", "x"))
	s := llm.Settings{Provider: "anthropic", APIKey: "sk-ant-secret"}
	c := f.client(s)
	var deltas []string
	text, err := c.Agent(t.Context(), llm.AgentRequest{System: "desk", Messages: user("hi"), Effort: llm.High, MaxTokens: 16000,
		Tools: []llm.Tool{(&recorder{}).tool("quote", "1")}}, func(e llm.Event) error { deltas = append(deltas, e.Text); return nil })
	if err != nil || text != "Hello" || strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("%q %v %q", text, err, deltas)
	}
	r := f.requests()[0]
	if r.Path != "/v1/messages" || r.Header.Get("x-api-key") != "sk-ant-secret" || r.Header.Get("anthropic-version") != "2023-06-01" ||
		r.Header.Get("Authorization") != "" {
		t.Fatalf("request: %s %v", r.Path, r.Header)
	}
	want := `{"max_tokens":16000,"messages":[{"content":[{"text":"hi","type":"text"}],"role":"user"}],"model":"claude-sonnet-5-5",` +
		`"output_config":{"effort":"high"},"stream":true,"system":"desk","tools":[{"description":"Looks up quote.","input_schema":{"type":"object"},"name":"quote"}]}`
	if compact(r.Body) != want {
		t.Fatalf("body:\n%s\nwant\n%s", compact(r.Body), want)
	}

	// Effort off: no output_config. A base URL ending in /v1 still works.
	s.Effort, s.BaseURL = "off", f.srv.URL+"/v1/"
	off, err := llm.NewFromSettings(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := off.Complete(t.Context(), "", "x", llm.High); err != nil {
		t.Fatal(err)
	}
	if r := f.requests()[1]; r.Path != "/v1/messages" || r.Body["output_config"] != nil || r.Body["max_tokens"] != 8192.0 {
		t.Fatalf("effort off: %s %s", r.Path, r.Raw)
	}
}

// A tool loop replays the assistant blocks verbatim (thinking with its
// signature, redacted thinking, text, tool_use with exact input) and answers
// with tool_result blocks, errors marked.
func TestMessagesToolLoop(t *testing.T) {
	sig := "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds+sig=="
	f := newFake(t, stream(append([]string{msgStart(),
		blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
		blockDelta(0, `{"type":"thinking_delta","thinking":"Need the "}`),
		blockDelta(0, `{"type":"thinking_delta","thinking":"price <now>."}`),
		blockDelta(0, `{"type":"signature_delta","signature":"`+sig+`"}`),
		blockStop(0),
		blockStart(1, `{"type":"redacted_thinking","data":"EmwKAhgBEgy3va3pzix/LafPsn4a"}`),
		blockStop(1),
		blockStart(2, `{"type":"text","text":""}`),
		blockDelta(2, textDelta("Checking.")),
		blockStop(2),
		blockStart(3, `{"type":"tool_use","id":"toolu_01","name":"quote","input":{}}`),
		blockDelta(3, `{"type":"input_json_delta","partial_json":""}`),
		blockDelta(3, `{"type":"input_json_delta","partial_json":"{\"symbol\": \"BTC\","}`),
		blockDelta(3, `{"type":"input_json_delta","partial_json":" \"n\": 12345678901234567}"}`),
		blockStop(3),
		blockStart(4, `{"type":"tool_use","id":"toolu_02","name":"boom","input":{}}`),
		blockStop(4),
	}, msgEnd("tool_use")...)...), anthText("end_turn", "BTC is 100k."))
	c := f.client(llm.Settings{Provider: "anthropic", APIKey: "k"})
	rec := &recorder{}
	tools := []llm.Tool{rec.tool("quote", "100000"), failing("boom")}
	text, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("BTC?"), Tools: tools}, nil)
	if err != nil || text != "BTC is 100k." || len(rec.inputs) != 1 || rec.inputs[0] != `quote:{"symbol": "BTC", "n": 12345678901234567}` {
		t.Fatalf("%q %v %q", text, err, rec.inputs)
	}
	second := f.requests()[1].Raw
	for _, want := range []string{
		`{"content":[{"signature":"` + sig + `","thinking":"Need the price <now>.","type":"thinking"},` +
			`{"data":"EmwKAhgBEgy3va3pzix/LafPsn4a","type":"redacted_thinking"},{"text":"Checking.","type":"text"},` +
			`{"id":"toolu_01","input":{"symbol":"BTC","n":12345678901234567},"name":"quote","type":"tool_use"},` +
			`{"id":"toolu_02","input":{},"name":"boom","type":"tool_use"}],"role":"assistant"}`,
		`{"content":[{"content":"100000","tool_use_id":"toolu_01","type":"tool_result"},` +
			`{"content":"Error: down","is_error":true,"tool_use_id":"toolu_02","type":"tool_result"}],"role":"user"}`,
	} {
		if !strings.Contains(second, want) {
			t.Errorf("second request lacks\n%s\nin\n%s", want, second)
		}
	}
}

// Attachments: image and PDF as base64 blocks, text and office documents as
// text documents.
func TestMessagesAttachments(t *testing.T) {
	f := newFake(t, anthText("end_turn", "ok"))
	m := llm.Message{Text: "Read", Images: []llm.Image{{Format: "webp", Bytes: []byte{1, 2}}}, Documents: []llm.Document{
		{Format: "pdf", Name: "Document 1 (report)", Bytes: []byte("%PDF-1.4")},
		{Format: "md", Name: "Document 2", Bytes: []byte("# Notes")},
		{Format: "docx", Name: "Document 3", Bytes: llm.DocxFile(t, "Revenue grew.")},
		{Format: "xlsx", Name: "Document 4", Bytes: llm.XlsxFile(t)},
	}}
	if _, err := f.client(llm.Settings{Provider: "anthropic", APIKey: "k"}).CompleteWith(t.Context(), "", m, ""); err != nil {
		t.Fatal(err)
	}
	want := `[{"text":"Read","type":"text"},` +
		`{"source":{"data":"JVBERi0xLjQ=","media_type":"application/pdf","type":"base64"},"title":"Document 1 (report)","type":"document"},` +
		`{"source":{"data":"# Notes","media_type":"text/plain","type":"text"},"title":"Document 2","type":"document"},` +
		`{"source":{"data":"Revenue grew.\n","media_type":"text/plain","type":"text"},"title":"Document 3","type":"document"},` +
		`{"source":{"data":"## Prices\nSymbol\tPrice\nBTC\t100000\n","media_type":"text/plain","type":"text"},"title":"Document 4","type":"document"},` +
		`{"source":{"data":"AQI=","media_type":"image/webp","type":"base64"},"type":"image"}]`
	if got := compact(path(f.requests()[0].Body, "messages", 0, "content")); got != want {
		t.Fatalf("content:\n%s\nwant\n%s", got, want)
	}
}

// An overloaded answer before the stream is retried; an error event inside
// the stream is not, and it is produced but not settled. A refusal without
// text and a max_tokens stop fail a completion; Test passes the latter.
func TestMessagesErrors(t *testing.T) {
	overloaded := `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	f := newFake(t,
		answer{status: 529, body: overloaded, retryAfter: "0"}, anthText("end_turn", "after retry"),
		stream(msgStart(), named("error", overloaded)),
		failure(400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`),
		anthText("refusal"),
		anthText("max_tokens", "cut"),
		anthText("max_tokens", "O"),
	)
	c := f.client(llm.Settings{Provider: "anthropic", APIKey: "k"})
	if text, err := c.Complete(t.Context(), "", "x", ""); err != nil || text != "after retry" || len(f.requests()) != 2 {
		t.Fatalf("retry: %q %v after %d requests", text, err, len(f.requests()))
	}
	for i, want := range []llm.RequestOutcome{{Produced: true}, {Settled: true}} {
		var outcomes []llm.RequestOutcome
		_, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("x"), OnRequestDone: func(o llm.RequestOutcome) { outcomes = append(outcomes, o) }}, nil)
		var pe *llm.ProviderError
		if !errors.As(err, &pe) || len(outcomes) != 1 || outcomes[0] != want {
			t.Fatalf("case %d: %v %+v", i, err, outcomes)
		}
		if i == 0 && (pe.Code != "overloaded_error" || pe.Status != 0) || i == 1 && (pe.Status != 400 || pe.Msg != "max_tokens: too large") {
			t.Fatalf("case %d: %+v", i, pe)
		}
	}
	if n := len(f.requests()); n != 4 {
		t.Fatalf("a mid-stream or 400 failure was retried: %d requests", n)
	}
	if _, err := c.Complete(t.Context(), "", "x", ""); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refusal: %v", err)
	}
	if _, err := c.Complete(t.Context(), "", "x", ""); !errors.Is(err, llm.ErrUpstream) || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("max_tokens: %v", err)
	}
	if err := c.Test(t.Context()); err != nil {
		t.Fatalf("Test with a max_tokens stop: %v", err)
	}
	if r := f.requests()[6]; path(r.Body, "messages", 0, "content", 0, "text") != "Reply OK" || r.Body["max_tokens"] != 64.0 ||
		path(r.Body, "output_config", "effort") != "low" {
		t.Fatalf("test request: %s", r.Raw)
	}
}

func TestMessagesModels(t *testing.T) {
	f := newFake(t, answer{body: `{"data":[{"type":"model","id":"claude-sonnet-5-5","display_name":"Claude Sonnet 5.5","created_at":"2026-01-01T00:00:00Z"}],"has_more":false,"first_id":"claude-sonnet-5-5","last_id":"claude-sonnet-5-5"}`})
	models, err := llm.ListModels(t.Context(), llm.Settings{Provider: "anthropic", APIKey: "k", BaseURL: f.srv.URL})
	if err != nil || compact(models) != `[{"id":"claude-sonnet-5-5","label":"Claude Sonnet 5.5"}]` || f.requests()[0].Path != "/v1/models?limit=1000" {
		t.Fatalf("%v %v %+v", models, err, f.requests())
	}
}
