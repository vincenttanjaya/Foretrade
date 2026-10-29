// Package llmtest is a fake Bedrock runtime for tests: an httptest server that
// speaks the Converse and ConverseStream wire formats, so the real llm.Client
// and the AWS SDK under it run end to end without network.
package llmtest

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go/eventstream"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

// Model is the production inference profile; llm sends the effort field for it.
const Model = "global.anthropic.claude-sonnet-5-5"

// Request is one decoded Bedrock call.
type Request struct {
	Stream   bool      // ConverseStream (llm.Agent), else Converse
	System   string    // the system text blocks, joined
	Prompt   string    // the text blocks of the last message, joined with "\n"
	Effort   string    // additionalModelRequestFields.output_config.effort; "" when absent
	Tools    []string  // declared tool names
	Messages []Message // the conversation as sent
	// AfterTools: the call follows a tool round, its last message holding
	// toolResult blocks or an llm Flatten run's <run_transcript>.
	AfterTools bool
	Body       []byte // the raw JSON body
}

// Message is one wire message; Content holds its blocks as decoded JSON.
type Message struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

// Reply is one scripted model turn.
type Reply struct {
	Text       string    // Converse: the message text; stream: sent as two deltas
	Deltas     []string  // stream: more text, sent as exactly these deltas (after Text's)
	AfterFirst func()    // stream: called once the first text delta is flushed, before the rest; it may block
	Reasoning  string    // stream: a reasoning block (signature "sig-"+Reasoning) streamed before the text
	Tools      []ToolUse // stream: tool calls after the text; the stop reason becomes tool_use
	Stop       string    // stop reason override, e.g. "max_tokens"
	Status     int       // non-zero: fail the call with this HTTP status
	StreamErr  string    // stream: a modelStreamErrorException with this message right after messageStart
}

type ToolUse struct{ ID, Name, Input string }

type Server struct {
	srv   *httptest.Server
	reply func(Request) Reply

	mu   sync.Mutex
	reqs []Request
}

// New starts a fake Bedrock that answers every call with reply, and returns
// an llm client for Model pointed at it.
func New(t testing.TB, reply func(Request) Reply) (*llm.Client, *Server) {
	t.Helper()
	s := &Server{reply: reply}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return llm.New(s.Config(), Model), s
}

// Config points the AWS SDK at the fake, with test credentials and no retries.
func (s *Server) Config() aws.Config {
	creds := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	})
	return aws.Config{Region: "us-east-1", Credentials: creds, BaseEndpoint: aws.String(s.srv.URL),
		Retryer: func() aws.Retryer { return aws.NopRetryer{} }}
}

// Requests returns the calls received so far, in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// Queue answers calls with replies in order; a call past the end fails the
// test and gets a 500.
func Queue(t testing.TB, replies ...Reply) func(Request) Reply {
	var mu sync.Mutex
	return func(Request) Reply {
		mu.Lock()
		defer mu.Unlock()
		if len(replies) == 0 {
			t.Errorf("llmtest: unexpected Bedrock call")
			return Reply{Status: http.StatusInternalServerError}
		}
		r := replies[0]
		replies = replies[1:]
		return r
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var wire struct {
		System     []struct{ Text string } `json:"system"`
		Messages   []Message               `json:"messages"`
		Additional struct {
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		} `json:"additionalModelRequestFields"`
		ToolConfig struct {
			Tools []struct {
				ToolSpec struct{ Name string } `json:"toolSpec"`
			} `json:"tools"`
		} `json:"toolConfig"`
	}
	json.Unmarshal(body, &wire)
	req := Request{Stream: strings.HasSuffix(r.URL.Path, "/converse-stream"), Effort: wire.Additional.OutputConfig.Effort,
		Messages: wire.Messages, Body: body}
	var system []string
	for _, b := range wire.System {
		system = append(system, b.Text)
	}
	req.System = strings.Join(system, "\n")
	for _, t := range wire.ToolConfig.Tools {
		req.Tools = append(req.Tools, t.ToolSpec.Name)
	}
	if n := len(wire.Messages); n > 0 {
		var texts []string
		for _, b := range wire.Messages[n-1].Content {
			if t, ok := b["text"].(string); ok {
				texts = append(texts, t)
				req.AfterTools = req.AfterTools || strings.HasPrefix(t, "<run_transcript>")
			}
			req.AfterTools = req.AfterTools || b["toolResult"] != nil
		}
		req.Prompt = strings.Join(texts, "\n")
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()

	rep := s.reply(req)
	switch {
	case rep.Status != 0:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-Errortype", "ValidationException")
		w.WriteHeader(rep.Status)
		fmt.Fprint(w, `{"message":"llmtest: scripted failure"}`)
	case req.Stream:
		writeStream(w, rep)
	default:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"output":     map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"text": rep.Text}}}},
			"stopReason": cmp.Or(rep.Stop, "end_turn"),
			"usage":      map[string]any{"inputTokens": 1, "outputTokens": 1, "totalTokens": 2},
			"metrics":    map[string]any{"latencyMs": 1},
		})
	}
}

// writeStream sends rep as ConverseStream event-stream frames.
func writeStream(w http.ResponseWriter, rep Reply) {
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	enc := eventstream.NewEncoder()
	frame := func(kind, name string, payload any) {
		var m eventstream.Message
		if kind == "exception" {
			m.Headers.Set(eventstream.ExceptionTypeHeader, eventstream.StringValue(name))
		} else {
			m.Headers.Set(eventstream.EventTypeHeader, eventstream.StringValue(name))
		}
		m.Headers.Set(eventstream.ContentTypeHeader, eventstream.StringValue("application/json"))
		m.Headers.Set(eventstream.MessageTypeHeader, eventstream.StringValue(kind))
		m.Payload, _ = json.Marshal(payload)
		enc.Encode(w, m)
	}
	type obj = map[string]any
	frame("event", "messageStart", obj{"role": "assistant"})
	if rep.StreamErr != "" {
		frame("exception", "modelStreamErrorException", obj{"message": rep.StreamErr})
		return
	}
	i := 0
	delta := func(d obj) { frame("event", "contentBlockDelta", obj{"contentBlockIndex": i, "delta": d}) }
	stop := func() { frame("event", "contentBlockStop", obj{"contentBlockIndex": i}); i++ }
	if rep.Reasoning != "" {
		delta(obj{"reasoningContent": obj{"text": rep.Reasoning}})
		delta(obj{"reasoningContent": obj{"signature": "sig-" + rep.Reasoning}})
		stop()
	}
	var parts []string
	if rep.Text != "" {
		runes := []rune(rep.Text)
		parts = append(parts, string(runes[:len(runes)/2]), string(runes[len(runes)/2:]))
	}
	first := true
	for _, part := range append(parts, rep.Deltas...) {
		if part == "" {
			continue
		}
		delta(obj{"text": part})
		if first && rep.AfterFirst != nil {
			http.NewResponseController(w).Flush()
			rep.AfterFirst()
		}
		first = false
	}
	if !first {
		stop()
	}
	for _, tu := range rep.Tools {
		frame("event", "contentBlockStart", obj{"contentBlockIndex": i, "start": obj{"toolUse": obj{"toolUseId": tu.ID, "name": tu.Name}}})
		if tu.Input != "" {
			delta(obj{"toolUse": obj{"input": tu.Input}})
		}
		stop()
	}
	reason := "end_turn"
	if len(rep.Tools) > 0 {
		reason = "tool_use"
	}
	frame("event", "messageStop", obj{"stopReason": cmp.Or(rep.Stop, reason)})
	frame("event", "metadata", obj{"usage": obj{"inputTokens": 1, "outputTokens": 1, "totalTokens": 2}, "metrics": obj{"latencyMs": 1}})
}
