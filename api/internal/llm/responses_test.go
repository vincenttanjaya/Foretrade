package llm_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

// Responses stream events, as documented: each data line repeats its type.
func rev(typ, rest string) string {
	if rest != "" {
		rest = "," + rest
	}
	return named(typ, `{"type":"`+typ+`"`+rest+`}`)
}
func created() string {
	return rev("response.created", `"response":{"id":"resp_1","object":"response","status":"in_progress","output":[]}`)
}
func itemDone(i int, item string) string {
	return rev("response.output_item.done", fmt.Sprintf(`"output_index":%d,"item":%s`, i, item))
}
func completed() string {
	return rev("response.completed", `"response":{"id":"resp_1","status":"completed"}`)
}

func respText(parts ...string) answer {
	events := []string{created(), rev("response.output_item.added", `"output_index":0,"item":{"id":"msg_9","type":"message","status":"in_progress","role":"assistant","content":[]}`)}
	for _, p := range parts {
		events = append(events, rev("response.output_text.delta", fmt.Sprintf(`"item_id":"msg_9","output_index":0,"content_index":0,"delta":%q`, p)))
	}
	text := strings.Join(parts, "")
	return stream(append(events, itemDone(0, fmt.Sprintf(`{"id":"msg_9","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":%q}]}`, text)),
		completed())...)
}

// An API key request: stateless and streamed, with a ceiling, instructions,
// reasoning with encrypted content (effort on by default) and plain,
// non-strict function tools.
func TestResponsesRequest(t *testing.T) {
	f := newFake(t, respText("Hel", "lo"), respText("x"))
	c := f.client(llm.Settings{Provider: "openai", Model: "gpt-x", APIKey: "sk-proj-secret"})
	var deltas []string
	text, err := c.Agent(t.Context(), llm.AgentRequest{System: "desk", Messages: user("hi"), Tools: []llm.Tool{(&recorder{}).tool("quote", "1")}},
		func(e llm.Event) error { deltas = append(deltas, e.Text); return nil })
	if err != nil || text != "Hello" || strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("%q %v %q", text, err, deltas)
	}
	r := f.requests()[0]
	want := `{"include":["reasoning.encrypted_content"],"input":[{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],` +
		`"instructions":"desk","max_output_tokens":8192,"model":"gpt-x","reasoning":{"effort":"medium"},"store":false,"stream":true,` +
		`"tools":[{"description":"Looks up quote.","name":"quote","parameters":{"type":"object"},"strict":false,"type":"function"}]}`
	if r.Path != "/responses" || r.Header.Get("Authorization") != "Bearer sk-proj-secret" || compact(r.Body) != want {
		t.Fatalf("request %s %v:\n%s\nwant\n%s", r.Path, r.Header, compact(r.Body), want)
	}
	off := f.client(llm.Settings{Provider: "openai", Model: "gpt-x", APIKey: "k", Effort: "off"})
	if _, err := off.Complete(t.Context(), "", "x", llm.High); err != nil {
		t.Fatal(err)
	}
	if b := f.requests()[1].Body; b["reasoning"] != nil || b["include"] != nil || b["instructions"] != nil {
		t.Fatalf("effort off: %v", b)
	}
}

// turn1 is a reasoning item (encrypted), text, a function call, and a
// reasoning item without encrypted content, which can't be replayed.
var turn1 = []string{created(),
	itemDone(0, `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"gAAAAB-enc=="}`),
	rev("response.output_text.delta", `"item_id":"msg_1","output_index":1,"content_index":0,"delta":"Checking."`),
	itemDone(1, `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"Checking."}]}`),
	rev("response.output_item.added", `"output_index":2,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_abc","name":"quote"}`),
	rev("response.function_call_arguments.delta", `"item_id":"fc_1","output_index":2,"delta":"{\"symbol\":"`),
	itemDone(2, `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"symbol\":\"BTC\"}","call_id":"call_abc","name":"quote"}`),
	itemDone(3, `{"id":"rs_2","type":"reasoning","summary":[]}`),
	completed(),
}

// A tool loop sends this run's output items back verbatim, then the
// function_call_output keyed by call_id.
func TestResponsesToolLoop(t *testing.T) {
	f := newFake(t, stream(turn1...), respText("BTC is 100k."))
	c := f.client(llm.Settings{Provider: "openai", Model: "gpt-x", APIKey: "k"})
	rec := &recorder{}
	text, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("BTC?"), Tools: []llm.Tool{rec.tool("quote", "100000")}}, nil)
	if err != nil || text != "BTC is 100k." || !slices.Equal(rec.inputs, []string{`quote:{"symbol":"BTC"}`}) {
		t.Fatalf("%q %v %q", text, err, rec.inputs)
	}
	second := f.requests()[1].Raw
	want := `"input":[{"content":[{"text":"BTC?","type":"input_text"}],"role":"user","type":"message"},` +
		`{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"gAAAAB-enc=="},` +
		`{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"Checking."}]},` +
		`{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"symbol\":\"BTC\"}","call_id":"call_abc","name":"quote"},` +
		`{"call_id":"call_abc","output":"100000","type":"function_call_output"}]`
	if !strings.Contains(second, want) {
		t.Fatalf("second request:\n%s\nwant\n%s", second, want)
	}
}

// With effort off a reasoning model's reasoning comes back unencrypted and is
// dropped, so the items after it go back without their ids: the API refuses
// an fc_/msg_ id whose reasoning item is missing.
func TestResponsesToolLoopEffortOff(t *testing.T) {
	f := newFake(t, stream(created(),
		itemDone(0, `{"id":"rs_1","type":"reasoning","summary":[]}`),
		itemDone(1, `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"text":"Checking."}]}`),
		itemDone(2, `{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"symbol\":\"BTC\"}","call_id":"call_1","name":"quote"}`),
		completed()), respText("BTC is 100k."))
	c := f.client(llm.Settings{Provider: "openai", Model: "gpt-x", APIKey: "k", Effort: "off"})
	rec := &recorder{}
	text, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("BTC?"), Tools: []llm.Tool{rec.tool("quote", "100000")}}, nil)
	if err != nil || text != "BTC is 100k." || len(rec.inputs) != 1 {
		t.Fatalf("%q %v %q", text, err, rec.inputs)
	}
	second := f.requests()[1]
	if second.Body["reasoning"] != nil || second.Body["include"] != nil {
		t.Fatalf("effort off: %s", second.Raw)
	}
	want := `[{"content":[{"text":"BTC?","type":"input_text"}],"role":"user","type":"message"},` +
		`{"content":[{"annotations":[],"text":"Checking.","type":"output_text"}],"role":"assistant","status":"completed","type":"message"},` +
		`{"arguments":"{\"symbol\":\"BTC\"}","call_id":"call_1","name":"quote","status":"completed","type":"function_call"},` +
		`{"call_id":"call_1","output":"100000","type":"function_call_output"}]`
	if got := compact(second.Body["input"]); got != want {
		t.Fatalf("second input:\n%s\nwant\n%s", got, want)
	}
}

// tokens is a ChatGPT credential: Refresh rotates the token.
type tokens struct {
	mu        sync.Mutex
	cur       string
	refreshes int
}

func (s *tokens) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, nil
}

func (s *tokens) Refresh(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes++
	s.cur = fmt.Sprintf("tok-%d", s.refreshes+1)
	return s.cur, nil
}

// ChatGPT plan: the bearer comes from the TokenSource and a 401 forces one
// refresh and one more attempt, even with NoRetries; the body holds only the
// allowed fields; tools go in a namespace and its replay keeps the namespace.
func TestChatGPTInference(t *testing.T) {
	call := strings.Replace(turn1[6], `"name":"quote"}`, `"name":"quote","namespace":"foretrade"}`, 1)
	f := newFake(t, failure(401, `{"detail":"Could not validate credentials"}`),
		stream(turn1[0], turn1[1], call, completed()), respText("Done."))
	ts := &tokens{cur: "tok-1"}
	c, err := llm.NewFromSettings(llm.Settings{Provider: "chatgpt", Model: "gpt-plan", BaseURL: f.srv.URL, ChatGPT: ts})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	text, err := c.Agent(t.Context(), llm.AgentRequest{System: "desk", Messages: user("BTC?"), Tools: []llm.Tool{rec.tool("quote", "100000")},
		NoRetries: true}, nil)
	if err != nil || text != "Done." || ts.refreshes != 1 || len(rec.inputs) != 1 {
		t.Fatalf("%q %v, %d refreshes, ran %q", text, err, ts.refreshes, rec.inputs)
	}
	reqs := f.requests()
	if len(reqs) != 3 || reqs[0].Header.Get("Authorization") != "Bearer tok-1" || reqs[1].Header.Get("Authorization") != "Bearer tok-2" ||
		reqs[2].Header.Get("Authorization") != "Bearer tok-2" {
		t.Fatalf("bearer tokens: %d requests", len(reqs))
	}
	for _, r := range reqs {
		var keys []string
		for k := range r.Body {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if strings.Join(keys, ",") != "include,input,instructions,model,reasoning,store,stream,tools" {
			t.Fatalf("fields outside the allowlist: %v", keys)
		}
		if path(r.Body, "tools", 0, "type") != "namespace" || path(r.Body, "tools", 0, "name") != "foretrade" ||
			path(r.Body, "tools", 0, "tools", 0, "name") != "quote" || path(r.Body, "tools", 0, "tools", 0, "strict") != false {
			t.Fatalf("tools: %v", r.Body["tools"])
		}
	}
	if !strings.Contains(reqs[2].Raw, `"name":"quote","namespace":"foretrade"}`) {
		t.Fatalf("the namespaced call was not replayed: %s", reqs[2].Raw)
	}

	// A second 401 after the forced refresh is the answer.
	f.push(failure(401, `{"error":{"message":"bad token","type":"invalid_request_error","code":"subscription_sharing_invalid_user"}}`),
		failure(401, `{"error":{"message":"bad token","type":"invalid_request_error","code":"subscription_sharing_invalid_user"}}`))
	var pe *llm.ProviderError
	if _, err := c.Complete(t.Context(), "", "x", ""); !errors.As(err, &pe) || pe.Status != 401 || ts.refreshes != 2 ||
		!strings.Contains(pe.Msg, "sign in again") {
		t.Fatalf("second 401: %v, %d refreshes", err, ts.refreshes)
	}
}

// When the namespace form is refused, the tools go top-level, from then on.
func TestChatGPTPlainToolsFallback(t *testing.T) {
	f := newFake(t,
		failure(400, `{"error":{"message":"Invalid value: 'namespace'. Supported values are: 'function'.","type":"invalid_request_error","param":"tools[0].type","code":"invalid_value"}}`),
		respText("ok"), respText("again"))
	c, _ := llm.NewFromSettings(llm.Settings{Provider: "chatgpt", Model: "m", BaseURL: f.srv.URL, ChatGPT: &tokens{cur: "t"}})
	req := llm.AgentRequest{Messages: user("x"), Tools: []llm.Tool{(&recorder{}).tool("quote", "1")}}
	for _, want := range []string{"ok", "again"} {
		if text, err := c.Agent(t.Context(), req, nil); err != nil || text != want {
			t.Fatalf("%q %v", text, err)
		}
	}
	reqs := f.requests()
	if len(reqs) != 3 || path(reqs[0].Body, "tools", 0, "type") != "namespace" || path(reqs[1].Body, "tools", 0, "type") != "function" ||
		path(reqs[2].Body, "tools", 0, "name") != "quote" {
		t.Fatalf("fallback: %d requests", len(reqs))
	}
}

// Plan errors are never retried below 500; 503 backs off and retries.
// Stream failures and incomplete answers are errors; a max_output_tokens
// cut is a max_tokens stop.
func TestResponsesErrors(t *testing.T) {
	limit := `{"error":{"message":"You've hit your usage limit.","type":"usage_limit","code":"subscription_sharing_usage_limit_exceeded"}}`
	f := newFake(t,
		answer{status: 429, body: limit, retryAfter: "0"},
		answer{status: 503, body: `{"error":{"code":"subscription_sharing_usage_unavailable","message":"try later"}}`, retryAfter: "0"}, respText("back"),
		stream(created(), rev("response.failed", `"response":{"id":"resp_1","status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}`)),
		stream(created(), rev("response.incomplete", `"response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`)),
		stream(created(), rev("response.incomplete", `"response":{"incomplete_details":{"reason":"content_filter"}}`)),
		stream(created(), rev("error", `"code":"server_error","message":"The server had an error","param":null`)),
		stream(created(), turn1[2]),
	)
	c := f.client(llm.Settings{Provider: "chatgpt", Model: "m", ChatGPT: &tokens{cur: "t"}})
	var pe *llm.ProviderError
	if _, err := c.Complete(t.Context(), "", "x", ""); !errors.As(err, &pe) || pe.Status != 429 || !strings.Contains(err.Error(), "chatgpt.com/settings/usage") ||
		len(f.requests()) != 1 {
		t.Fatalf("usage limit: %v after %d requests", err, len(f.requests()))
	}
	if text, err := c.Complete(t.Context(), "", "x", ""); err != nil || text != "back" {
		t.Fatalf("503: %q %v", text, err)
	}
	for _, want := range []string{"subscription_sharing_usage_limit_exceeded", "max_tokens", "content_filter", "server_error", "response.completed"} {
		if _, err := c.Complete(t.Context(), "", "x", ""); !errors.Is(err, llm.ErrUpstream) || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q: %v", want, err)
		}
	}
}

// Images and every document format go as input_image and input_file data URLs.
func TestResponsesAttachments(t *testing.T) {
	f := newFake(t, respText("ok"))
	docs := []llm.Document{{Format: "pdf", Name: "Document 1 (report)", Bytes: []byte("%PDF")}, {Format: "csv", Name: "Document 2", Bytes: []byte("a")},
		{Format: "docx", Name: "Document 3", Bytes: []byte("PK")}, {Format: "xlsx", Name: "Document 4", Bytes: []byte("PK")}}
	m := llm.Message{Text: "Read", Images: []llm.Image{{Format: "jpeg", Bytes: []byte{1}}}, Documents: docs}
	if _, err := f.client(llm.Settings{Provider: "openai", Model: "m", APIKey: "k"}).CompleteWith(t.Context(), "", m, ""); err != nil {
		t.Fatal(err)
	}
	want := `[{"text":"Read","type":"input_text"},` +
		`{"file_data":"data:application/pdf;base64,JVBERg==","filename":"Document 1 (report).pdf","type":"input_file"},` +
		`{"file_data":"data:text/csv;base64,YQ==","filename":"Document 2.csv","type":"input_file"},` +
		`{"file_data":"data:application/vnd.openxmlformats-officedocument.wordprocessingml.document;base64,UEs=","filename":"Document 3.docx","type":"input_file"},` +
		`{"file_data":"data:application/vnd.openxmlformats-officedocument.spreadsheetml.sheet;base64,UEs=","filename":"Document 4.xlsx","type":"input_file"},` +
		`{"detail":"auto","image_url":"data:image/jpeg;base64,AQ==","type":"input_image"}]`
	if got := compact(path(f.requests()[0].Body, "input", 0, "content")); got != want {
		t.Fatalf("content:\n%s\nwant\n%s", got, want)
	}
}

// The ChatGPT catalogue is {models:[{slug, display_name, visibility}]}: listed
// ones, in server order. An API key gets the usual data list.
func TestResponsesModels(t *testing.T) {
	f := newFake(t,
		answer{body: `{"models":[{"slug":"gpt-b","display_name":"GPT B","visibility":"list"},{"slug":"gpt-hidden","display_name":"Hidden","visibility":"hide"},{"slug":"gpt-a","display_name":"GPT A","visibility":"list"}]}`},
		answer{body: `{"object":"list","data":[{"id":"gpt-x","object":"model","owned_by":"openai"}]}`})
	plan, err := llm.ListModels(t.Context(), llm.Settings{Provider: "chatgpt", BaseURL: f.srv.URL, ChatGPT: &tokens{cur: "t"}})
	if err != nil || compact(plan) != `[{"id":"gpt-b","label":"GPT B"},{"id":"gpt-a","label":"GPT A"}]` || f.requests()[0].Header.Get("Authorization") != "Bearer t" {
		t.Fatalf("plan: %v %v", plan, err)
	}
	key, err := llm.ListModels(t.Context(), llm.Settings{Provider: "openai", APIKey: "k", BaseURL: f.srv.URL})
	if err != nil || compact(key) != `[{"id":"gpt-x","label":"gpt-x"}]` || f.requests()[1].Path != "/models" {
		t.Fatalf("key: %v %v", key, err)
	}
}
