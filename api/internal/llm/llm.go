// Package llm calls the app's AI provider: one-shot completions, JSON
// completions, and a streaming tool-use agent. The provider is a preset
// (settings.go) on one of four wires: Bedrock Converse (converse.go), OpenAI
// Chat Completions (chat.go), Anthropic Messages (messages.go) or OpenAI
// Responses (responses.go).
//
// Rules every wire keeps: effort is sent only when the preset or settings ask
// for it; no temperature/top_p/thinking budget/forced tool choice; assistant
// content — signed or encrypted reasoning included — is replayed unchanged
// inside a tool loop (a Flatten run replays its turns as text instead) and
// never outside the run; consecutive same-role messages are merged.
package llm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
)

type Effort string

const (
	Low    Effort = "low"
	Medium Effort = "medium"
	High   Effort = "high"
)

var ErrUpstream = errors.New("ai provider unavailable")

const (
	defaultMaxTokens = 8192 // includes reasoning; a ceiling, not a billed minimum
	defaultMaxTurns  = 6
)

// finalTurn is appended after the last allowed tool turn. Bedrock rejects a
// conversation holding toolUse blocks without a toolConfig, so the tools stay
// declared and the model is told to stop using them.
const finalTurn = "You have used your tool budget for this message. Do not call any more tools. " +
	"Answer the user now with the information you already have."

type Tool struct {
	Name, Description string
	Schema            map[string]any                                                   // JSON Schema (type: object)
	Run               func(ctx context.Context, input json.RawMessage) (string, error) // error → sent to the model as an error result
}

// Message is text history plus transient attachments on the current user turn.
// Callers never load attachment bytes from persisted history; Role is "user" or "assistant".
type Message struct {
	Role      string
	Text      string
	Images    []Image
	Documents []Document
}

// Image is validated in-memory user content, never saved history.
type Image struct {
	Format string // png | jpeg | webp | gif
	Bytes  []byte
}

// Document is a validated in-memory user file, never saved history. Name must
// already satisfy Converse's document-name rules and be unique in the message.
// Wires without native docx/xlsx support get their text (docs.go).
type Document struct {
	Format string // pdf | csv | txt | md | html | docx | xlsx
	Name   string
	Bytes  []byte
}

type Event struct {
	Kind      string // "delta" | "tool_call" | "tool_result"
	Text      string // delta
	ToolID    string
	ToolName  string
	ToolInput json.RawMessage
	Output    string // tool_result
	IsError   bool   // tool_result
}

type AgentRequest struct {
	System      string
	Messages    []Message
	Tools       []Tool
	Effort      Effort
	MaxTurns    int // tool turns; default 6
	MaxTokens   int // per model call; default 8192
	MaxRequests int // all provider dispatches, including final and nudge; zero is unbounded
	// BeforeRequest runs immediately before each provider call, after the
	// request limit and context checks. Its error aborts without dispatch or wrapping.
	BeforeRequest func(context.Context) error
	// OnRequestDone observes actual provider outcomes, not authorization attempts.
	// It runs on the Agent goroutine, including when a call fails.
	OnRequestDone func(RequestOutcome)

	// Flatten never sends tool call/result blocks: after a tool round the
	// next call re-sends Messages with this run (the text the user has seen,
	// each tool call and its wrapped result) appended to the last user
	// message as text, because Bedrock withholds the whole streamed response
	// of a call holding a toolResult block (see transcript). Tools stay
	// declared; signed reasoning is not replayed. Every wire supports it.
	Flatten bool
	// Visible (Flatten) is the answer text the user has seen so far, for a
	// caller that holds back or drops some of the deltas; nil counts all the
	// model's text as seen. Called between turns, on the Agent's goroutine.
	Visible func() string
	// Nudge is called when the model ends the run (including an exhausted
	// tool budget with Flatten): a non-empty note gives the model one more
	// turn with the note as the last user text. It does not reset or consume
	// the tool budget. At most once per run; called on the Agent's goroutine.
	Nudge func() string
	// OnProviderEvent observes every raw event before parsing, including private or malformed content.
	// It runs on the Agent goroutine, and can track whether an errored run produced anything.
	OnProviderEvent func()
	// NoRetries: one upstream attempt per turn. The default retries before any
	// event (Bedrock: the configured SDK retryer). A ChatGPT token refresh
	// after a 401 is not a retry: the rejected attempt was never admitted.
	NoRetries bool
}

type RequestOutcome struct {
	Produced bool // any raw event, before decoding or visible-text suppression
	Settled  bool // complete response or explicit provider rejection; never ambiguous transport/decode failure
}

// Client is the app's one AI provider. Configure swaps it at runtime; each
// call keeps the provider it started with.
type Client struct{ cur atomic.Pointer[active] }

// active is one configured provider.
type active struct {
	id     string   // the preset id, naming the provider in errors
	s      Settings // as configured, secrets included
	model  string
	effort bool // send the reasoning effort
	b      backend
}

// effortFor is the effort to send for e: none when effort is off, else e or Medium.
func (a *active) effortFor(e Effort) Effort {
	if !a.effort {
		return ""
	}
	return cmp.Or(e, Medium)
}

// New returns a Bedrock client for model through the Converse API.
func New(cfg aws.Config, model string) *Client {
	c := &Client{}
	c.cur.Store(&active{id: "bedrock", s: Settings{Provider: "bedrock", Model: model, AWSRegion: cfg.Region},
		model: model, effort: sonnetEffort(model), b: newConverse(cfg, model)})
	return c
}

func (c *Client) Complete(ctx context.Context, system, prompt string, effort Effort) (string, error) {
	return c.CompleteWith(ctx, system, Message{Role: "user", Text: prompt}, effort)
}

// CompleteWith is Complete with the user's attachments on its one user turn:
// the text block, then the documents, then the images (m.Role is ignored).
func (c *Client) CompleteWith(ctx context.Context, system string, m Message, effort Effort) (string, error) {
	a := c.cur.Load()
	if a == nil {
		return "", ErrNotConfigured
	}
	r, err := a.b.turn(ctx, &call{system: system, turns: userTurn(m), effort: a.effortFor(effort), maxTokens: defaultMaxTokens}, nil, nil)
	if err != nil {
		return "", err
	}
	if r.stop == stopMaxTokens {
		return "", upstream(a.id, errors.New("max_tokens reached before the answer finished"))
	}
	return r.text, nil
}

// CompleteJSON asks for a JSON object and unmarshals the first one in the
// reply. On a parse failure it retries once, telling the model what was wrong.
func (c *Client) CompleteJSON(ctx context.Context, system, prompt string, effort Effort, out any) error {
	return c.CompleteJSONWith(ctx, system, Message{Role: "user", Text: prompt}, effort, out)
}

// CompleteJSONWith is CompleteJSON over CompleteWith. Its one parse-failure
// retry sends the attachments again: it is a fresh one-turn request.
func (c *Client) CompleteJSONWith(ctx context.Context, system string, m Message, effort Effort, out any) error {
	text, err := c.CompleteWith(ctx, system, m, effort)
	if err != nil {
		return err
	}
	perr := DecodeJSON(text, out)
	if perr == nil {
		return nil
	}
	m.Text += "\n\nYour previous reply could not be used: " + perr.Error() +
		". Reply with only the JSON object, no prose and no code fences."
	if text, err = c.CompleteWith(ctx, system, m, effort); err != nil {
		return err
	}
	if perr = DecodeJSON(text, out); perr != nil {
		return upstream("completion", fmt.Errorf("model reply is not the requested JSON: %w", perr))
	}
	return nil
}

// DecodeJSON unmarshals the first JSON object in a model reply into out,
// ignoring prose and code fences around it.
func DecodeJSON(text string, out any) error {
	obj := extractJSON(text)
	if obj == "" {
		return errors.New("no JSON object found")
	}
	if err := noDuplicateKeys(json.NewDecoder(strings.NewReader(obj))); err != nil {
		return fmt.Errorf("invalid JSON object: %v", err)
	}
	if err := json.Unmarshal([]byte(obj), out); err != nil {
		return fmt.Errorf("invalid JSON object: %v", err)
	}
	return nil
}

// noDuplicateKeys rejects an object that repeats a key: encoding/json would
// silently keep the last copy, hiding a contradictory answer. Keys are compared
// case-insensitively, as encoding/json matches struct fields.
// ponytail: drop for json/v2, which rejects duplicates, once it leaves GOEXPERIMENT.
func noDuplicateKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') && tok != json.Delim('[') {
		return nil
	}
	seen := map[string]bool{}
	for dec.More() {
		if tok == json.Delim('{') {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			key := strings.ToLower(k.(string))
			if seen[key] {
				return fmt.Errorf("duplicate key %q", k)
			}
			seen[key] = true
		}
		if err := noDuplicateKeys(dec); err != nil {
			return err
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}

// extractJSON returns the first balanced {...} that is valid JSON. Prose or
// ```json fences around it are ignored; braces inside strings are skipped.
func extractJSON(s string) string {
	for start := strings.IndexByte(s, '{'); start >= 0; {
		if end := balancedEnd(s, start); end > 0 && json.Valid([]byte(s[start:end])) {
			return s[start:end]
		}
		next := strings.IndexByte(s[start+1:], '{')
		if next < 0 {
			break
		}
		start += next + 1
	}
	return ""
}

// balancedEnd is the index just past the brace closing s[start], or -1.
func balancedEnd(s string, start int) int {
	depth, inString, escaped := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case escaped:
			escaped = false
		case inString && ch == '\\':
			escaped = true
		case ch == '"':
			inString = !inString
		case inString:
		case ch == '{':
			depth++
		case ch == '}':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// Agent runs a streaming tool loop: stream a turn, run the tools it asked
// for, feed the results back (as tool blocks, or as text with Flatten),
// repeat. After MaxTurns tool turns it asks for a final answer. Text deltas
// of every turn are emitted; the return value is the text of the final turn.
// The whole run stays on the provider it started with.
func (c *Client) Agent(ctx context.Context, req AgentRequest, onEvent func(Event) error) (string, error) {
	if onEvent == nil {
		onEvent = func(Event) error { return nil }
	}
	maxTurns := cmp.Or(req.MaxTurns, defaultMaxTurns)
	msgs := toTurns(req.Messages)
	if len(msgs) == 0 {
		return "", errors.New("agent: no user message")
	}
	a := c.cur.Load()
	if a == nil {
		return "", ErrNotConfigured
	}
	tools := make(map[string]Tool, len(req.Tools))
	for _, t := range req.Tools {
		tools[t.Name] = t
	}
	input := &call{system: req.System, tools: req.Tools, effort: a.effortFor(req.Effort),
		maxTokens: cmp.Or(req.MaxTokens, defaultMaxTokens), noRetries: req.NoRetries}

	var flat *transcript
	if req.Flatten {
		flat = &transcript{base: msgs, visible: req.Visible}
	}
	nudged, budgetNoted := false, false
	toolTurns, requests := 0, 0
	for {
		if toolTurns == maxTurns && !budgetNoted && len(req.Tools) > 0 {
			budgetNoted = true
			if flat != nil {
				flat.notes = append(flat.notes, finalTurn)
			} else {
				last := &msgs[len(msgs)-1]
				last.parts = append(last.parts, part{text: finalTurn})
			}
		}
		input.turns = msgs
		if flat != nil {
			input.turns = flat.messages()
		}
		if req.MaxRequests > 0 || req.BeforeRequest != nil {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		if req.MaxRequests > 0 && requests >= req.MaxRequests {
			return "", fmt.Errorf("agent: request limit reached (%d)", req.MaxRequests)
		}
		if req.BeforeRequest != nil {
			if err := req.BeforeRequest(ctx); err != nil {
				return "", err
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		requests++
		produced := false
		r, err := a.b.turn(ctx, input, onEvent, func() {
			produced = true
			if req.OnProviderEvent != nil {
				req.OnProviderEvent()
			}
		})
		if req.OnRequestDone != nil {
			// A returned error alone does not prove that an unobserved response
			// did not exist. A decoded provider rejection is the narrow no-event
			// proof; context, transport and malformed stream errors stay unknown.
			var rejection *ProviderError
			settled := err == nil || !produced && errors.As(err, &rejection)
			req.OnRequestDone(RequestOutcome{Produced: produced, Settled: settled})
		}
		if err != nil {
			return "", err
		}
		if flat == nil && r.native != nil {
			msgs = append(msgs, turn{role: "assistant", native: r.native})
		}
		if r.stop != stopTools || toolTurns >= maxTurns {
			note := ""
			if req.Nudge != nil && !nudged && (flat != nil || r.stop != stopTools) {
				note = req.Nudge()
			}
			if note == "" {
				if r.stop == stopTools && r.text == "" {
					return "", upstream(a.id+" agent", errors.New("model kept calling tools past its budget"))
				}
				return r.text, nil
			}
			nudged = true
			if flat != nil {
				flat.record(r.text, nil)
				flat.notes = append(flat.notes, note)
			} else {
				msgs = withUserText(msgs, note)
			}
			continue
		}
		results, err := runTools(ctx, r.calls, tools, onEvent)
		if err != nil {
			return "", err
		}
		if flat != nil {
			flat.record(r.text, results)
		} else {
			msgs = append(msgs, turn{role: "user", parts: resultParts(results)})
		}
		toolTurns++
	}
}

type toolCall struct {
	ID, Name string
	Input    json.RawMessage // always a JSON object
	invalid  bool            // the model's arguments were not a JSON object; Input is {}
}

type toolResult struct {
	toolCall
	Out    string // never empty
	Failed bool
}

// runTools executes the turn's tool calls in order and returns their results.
// Only an onEvent error aborts.
func runTools(ctx context.Context, calls []toolCall, tools map[string]Tool, onEvent func(Event) error) ([]toolResult, error) {
	var results []toolResult
	for _, call := range calls {
		if err := onEvent(Event{Kind: "tool_call", ToolID: call.ID, ToolName: call.Name, ToolInput: call.Input}); err != nil {
			return nil, err
		}
		out, err := "", fmt.Errorf("unknown tool %q", call.Name)
		if t, ok := tools[call.Name]; ok {
			if call.invalid {
				err = errors.New("the tool arguments were not a valid JSON object; call the tool again with valid JSON")
			} else {
				out, err = t.Run(ctx, call.Input)
			}
		}
		failed := err != nil
		if failed {
			out = "Error: " + err.Error()
		}
		if out == "" {
			out = "(no output)" // Bedrock rejects empty text blocks
		}
		results = append(results, toolResult{call, out, failed})
		if err := onEvent(Event{Kind: "tool_result", ToolID: call.ID, ToolName: call.Name, Output: out, IsError: failed}); err != nil {
			return nil, err
		}
	}
	return results, nil
}
