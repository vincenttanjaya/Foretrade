package llm_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func TestProviders(t *testing.T) {
	var ids []string
	for _, p := range llm.Providers {
		ids = append(ids, p.ID+":"+p.Wire)
		if p.Label == "" || p.NeedsKey && (p.KeyURL == "" || p.KeyEnv == "") || p.Wire != "converse" && p.ID != "custom" && !strings.HasPrefix(p.DefaultBaseURL, "http") {
			t.Errorf("%+v", p)
		}
		if got, ok := llm.ProviderByID(p.ID); !ok || got != p {
			t.Errorf("ProviderByID(%q)", p.ID)
		}
	}
	want := "openai:responses chatgpt:responses anthropic:messages bedrock:converse openrouter:chat gemini:chat groq:chat " +
		"deepseek:chat xai:chat mistral:chat ollama:chat lmstudio:chat custom:chat"
	if strings.Join(ids, " ") != want {
		t.Fatalf("presets: %s", strings.Join(ids, " "))
	}
	for id, note := range map[string]string{"ollama": "host.docker.internal", "lmstudio": "host.docker.internal", "chatgpt": "owner-only"} {
		if p, _ := llm.ProviderByID(id); !strings.Contains(p.Note, note) {
			t.Errorf("%s note: %q", id, p.Note)
		}
	}
	if _, ok := llm.ProviderByID("nope"); ok {
		t.Fatal("unknown preset found")
	}
}

// Settings that can't work are refused, and the provider in place stays.
func TestConfigureRejects(t *testing.T) {
	f := newFake(t, chatText("still here"))
	c := f.client(llm.Settings{Provider: "groq", Model: "kept", APIKey: "k"})
	for name, s := range map[string]llm.Settings{
		"unknown preset":     {Provider: "nope", Model: "m"},
		"missing key":        {Provider: "openai", Model: "m"},
		"chatgpt signed out": {Provider: "chatgpt", Model: "m"},
		"custom without URL": {Provider: "custom", Model: "m"},
		"no model":           {Provider: "groq", APIKey: "k"},
		"bad effort":         {Provider: "groq", Model: "m", APIKey: "k", Effort: "max"},
		"bad URL":            {Provider: "custom", Model: "m", BaseURL: "ftp://models.example"},
	} {
		if err := c.Configure(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := llm.NewFromSettings(s); err == nil {
			t.Errorf("%s: NewFromSettings accepted", name)
		}
	}
	if text, err := c.Complete(t.Context(), "", "x", ""); err != nil || text != "still here" || c.Model() != "kept" {
		t.Fatalf("the old provider must stay: %q %v %q", text, err, c.Model())
	}
}

// Accessors: the preset's default model, the cutoff, the settings as given;
// switched off, every call is ErrNotConfigured.
func TestClientState(t *testing.T) {
	cutoff := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	s := llm.Settings{Provider: "anthropic", APIKey: "sk-ant", KnowledgeCutoff: cutoff}
	c, err := llm.NewFromSettings(s)
	if err != nil || !c.Configured() || c.Model() != "claude-sonnet-5-5" || !c.KnowledgeCutoff().Equal(cutoff) || c.Settings() != s {
		t.Fatalf("anthropic: %v %q %v %+v", err, c.Model(), c.KnowledgeCutoff(), c.Settings())
	}
	bedrock := llm.New(aws.Config{Region: "eu-west-1"}, "global.anthropic.claude-sonnet-5-5")
	if !bedrock.Configured() || bedrock.Model() != "global.anthropic.claude-sonnet-5-5" || bedrock.Settings().Provider != "bedrock" ||
		bedrock.Settings().AWSRegion != "eu-west-1" || !bedrock.KnowledgeCutoff().Equal(llm.DefaultKnowledgeCutoff) {
		t.Fatalf("bedrock: %+v", bedrock.Settings())
	}
	for _, off := range []*llm.Client{{}, c} {
		if err := off.Configure(llm.Settings{}); err != nil {
			t.Fatal(err)
		}
		_, complete := off.Complete(t.Context(), "", "x", "")
		_, agent := off.Agent(t.Context(), llm.AgentRequest{Messages: user("x")}, nil)
		var out struct{}
		for _, err := range []error{complete, agent, off.Test(t.Context()), off.CompleteJSON(t.Context(), "", "x", "", &out)} {
			if !errors.Is(err, llm.ErrNotConfigured) || !errors.Is(err, llm.ErrUpstream) {
				t.Fatalf("off: %v", err)
			}
		}
		if off.Configured() || off.Model() != "" || off.Settings() != (llm.Settings{}) || !off.KnowledgeCutoff().Equal(llm.DefaultKnowledgeCutoff) {
			t.Fatalf("off: %+v", off.Settings())
		}
	}
	if _, err := llm.ListModels(t.Context(), llm.Settings{}); !errors.Is(err, llm.ErrNotConfigured) {
		t.Fatalf("ListModels off: %v", err)
	}
}

// Test is one tiny completion with no retries; its error is the provider's
// message without the key.
func TestTest(t *testing.T) {
	f := newFake(t, chatText("OK"), failure(500, `{"error":{"message":"upstream sk-or-v1-secret failed","type":"server_error"}}`))
	c := f.client(llm.Settings{Provider: "openrouter", Model: "m", APIKey: "sk-or-v1-secret"})
	if err := c.Test(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := f.requests()[0]
	if path(r.Body, "messages", 0, "content") != "Reply OK" || path(r.Body, "reasoning", "effort") != "low" {
		t.Fatalf("test request: %s", r.Raw)
	}
	err := c.Test(t.Context())
	if err == nil || len(f.requests()) != 2 || strings.Contains(err.Error(), "sk-or-v1-secret") || !strings.Contains(err.Error(), "upstream [redacted] failed") {
		t.Fatalf("failed test: %v after %d requests", err, len(f.requests()))
	}
}

// Retries happen only before the stream starts: network errors, 5xx and
// 429 up to two more times; never with NoRetries, past a long Retry-After,
// or after an event arrived.
func TestRetries(t *testing.T) {
	five := func() answer { return answer{status: 500, body: `{"error":{"message":"boom"}}`, retryAfter: "0"} }
	for _, tc := range []struct {
		name     string
		answers  []answer
		once     bool
		ok       bool
		requests int
	}{
		{"two failures, then an answer", []answer{five(), {drop: true}, chatText("ok")}, false, true, 3},
		{"three failures", []answer{five(), five(), five()}, false, false, 3},
		{"NoRetries", []answer{five()}, true, false, 1},
		{"Retry-After too long", []answer{{status: 429, body: `{}`, retryAfter: "120"}}, false, false, 1},
		{"a broken stream", []answer{stream(chunk(`{"content":"par"}`, ""))}, false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, tc.answers...)
			c := f.client(llm.Settings{Provider: "groq", Model: "m", APIKey: "k"})
			_, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("x"), NoRetries: tc.once}, nil)
			if (err == nil) != tc.ok || len(f.requests()) != tc.requests {
				t.Fatalf("%v after %d requests", err, len(f.requests()))
			}
		})
	}
}

// Configure swaps atomically: a run in flight finishes its tool loop on the
// provider it started with, new calls use the new one, and concurrent swaps
// and calls are race-free.
func TestConfigureSwap(t *testing.T) {
	pause := make(chan struct{})
	a := newFake(t, answer{pause: pause, events: []string{
		chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"quote","arguments":"{}"}}]}`, ""),
		chunk(`{}`, "tool_calls")}}, chatText("A done."))
	b := newFake(t, chatText("B answer."))
	c := a.client(llm.Settings{Provider: "groq", Model: "model-a", APIKey: "k"})
	started := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		text, err := c.Agent(t.Context(), llm.AgentRequest{Messages: user("x"), Tools: []llm.Tool{(&recorder{}).tool("quote", "1")},
			OnProviderEvent: func() { once.Do(func() { close(started) }) }}, nil)
		if err == nil && text != "A done." {
			err = fmt.Errorf("in-flight run got %q", text)
		}
		done <- err
	}()
	<-started
	if err := c.Configure(llm.Settings{Provider: "ollama", Model: "model-b", BaseURL: b.srv.URL}); err != nil {
		t.Fatal(err)
	}
	if text, err := c.Complete(t.Context(), "", "x", ""); err != nil || text != "B answer." || c.Model() != "model-b" {
		t.Fatalf("after the swap: %q %v %q", text, err, c.Model())
	}
	close(pause)
	if err := <-done; err != nil || len(a.requests()) != 2 || path(a.requests()[1].Body, "model") != "model-a" {
		t.Fatalf("in-flight run: %v, provider A saw %d requests", err, len(a.requests()))
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		for _, e := range chatText("ok").events {
			io.WriteString(w, e)
		}
	}))
	defer ok.Close()
	if err := c.Configure(llm.Settings{Provider: "ollama", Model: "m", BaseURL: ok.URL}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 10 {
				if i%2 == 0 {
					c.Configure(llm.Settings{Provider: []string{"ollama", "lmstudio"}[j%2], Model: fmt.Sprint("m", j), BaseURL: ok.URL})
					continue
				}
				if text, err := c.Complete(t.Context(), "", "x", ""); err != nil || text != "ok" {
					t.Errorf("concurrent call: %q %v", text, err)
				}
				_ = c.Model() + c.Settings().Provider
			}
		})
	}
	wg.Wait()
}

// Bedrock from settings: the endpoint override, the region, and the effort
// gate (only Sonnet 5.5 by default; "on" and "off" decide for any model).
func TestBedrockSettings(t *testing.T) {
	for k, v := range map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_EC2_METADATA_DISABLED": "true",
		"AWS_CONFIG_FILE": "/nonexistent", "AWS_SHARED_CREDENTIALS_FILE": "/nonexistent", "AWS_PROFILE": ""} {
		t.Setenv(k, v)
	}
	_, srv := llmtest.New(t, func(llmtest.Request) llmtest.Reply { return llmtest.Reply{Text: "ok"} })
	for i, tc := range []struct{ effort, model, want string }{
		{"", "", "medium"},
		{"off", "", ""},
		{"", "anthropic.claude-haiku-4-5", ""},
		{"on", "anthropic.claude-haiku-4-5", "medium"},
	} {
		c, err := llm.NewFromSettings(llm.Settings{Provider: "bedrock", Model: tc.model, Effort: tc.effort, AWSRegion: "us-east-1",
			BaseURL: aws.ToString(srv.Config().BaseEndpoint)})
		if err != nil {
			t.Fatal(err)
		}
		if text, err := c.Complete(t.Context(), "", "x", ""); err != nil || text != "ok" {
			t.Fatalf("case %d: %q %v", i, text, err)
		}
		if r := srv.Requests()[i]; r.Effort != tc.want {
			t.Errorf("case %d: effort %q, want %q", i, r.Effort, tc.want)
		}
	}
	if _, err := llm.ListModels(t.Context(), llm.Settings{Provider: "bedrock"}); err == nil {
		t.Fatal("Bedrock listing must say to type the id")
	}
}
