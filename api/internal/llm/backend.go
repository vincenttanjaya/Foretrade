package llm

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// backend is one wire protocol: converse (Bedrock), chat (OpenAI Chat
// Completions), messages (Anthropic) or responses (OpenAI Responses).
type backend interface {
	// turn makes one model call. A nil onEvent marks a one-shot completion,
	// which a backend may serve without streaming; onRaw may be nil.
	turn(ctx context.Context, c *call, onEvent func(Event) error, onRaw func()) (reply, error)
	models(ctx context.Context) ([]ModelInfo, error)
}

// call is one model request in provider-neutral form.
type call struct {
	system    string
	turns     []turn
	tools     []Tool
	effort    Effort // "" sends none
	maxTokens int
	noRetries bool
}

// turn is one message. A backend renders it on its own wire.
type turn struct {
	role  string // "user" | "assistant"
	parts []part
	// native (assistant) is the backend's own content from earlier in this
	// run, replayed verbatim instead of parts: signed reasoning, opaque ids.
	// It never outlives one Agent run, so it never reaches another backend.
	native any
}

// part is one piece of a user turn; an assistant turn from history is text.
type part struct {
	text   string
	image  *Image
	doc    *Document
	result *toolResult
}

// reply is one finished model call.
type reply struct {
	text   string
	calls  []toolCall
	native any // the assistant content to replay later in the run; nil when empty
	stop   string
}

const (
	stopEnd       = "end"
	stopTools     = "tools"
	stopMaxTokens = "max_tokens"
)

// ProviderError is an explicit rejection by the provider: an error response
// or an error event in the stream. It wraps ErrUpstream.
type ProviderError struct {
	Status int    // HTTP status; 0 for an error event inside a stream
	Code   string // the provider's error code or type, when it sent one
	Msg    string // the provider's message, any API key removed
	param  string // the request field it names, when it sent one
}

func (e *ProviderError) Error() string {
	s := e.Msg
	if e.Code != "" {
		s = e.Code + ": " + s
	}
	if e.Status != 0 {
		s = fmt.Sprintf("HTTP %d %s", e.Status, s)
	}
	return s
}

func (e *ProviderError) Unwrap() error { return ErrUpstream }

func upstream(op string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrUpstream, op, err)
}

// toTurns converts text history and current-turn attachments, dropping empty
// messages and any leading assistant turn (every wire must start with the
// user), and merging consecutive same-role messages (Converse and Messages
// require strictly alternating roles).
func toTurns(history []Message) []turn {
	var out []turn
	for i, m := range history {
		role := "user"
		if m.Role == "assistant" {
			role = "assistant"
		}
		images, documents := m.Images, m.Documents
		if i != len(history)-1 || role != "user" {
			images, documents = nil, nil // historical and assistant turns remain text only
		}
		hasText := strings.TrimSpace(m.Text) != ""
		if (!hasText && len(images) == 0 && len(documents) == 0) || (len(out) == 0 && role != "user") {
			continue
		}
		var parts []part
		if hasText {
			parts = append(parts, part{text: m.Text})
		}
		parts = append(parts, attachmentParts(documents, images)...)
		if n := len(out); n > 0 && out[n-1].role == role {
			out[n-1].parts = append(out[n-1].parts, parts...)
			continue
		}
		out = append(out, turn{role: role, parts: parts})
	}
	return out
}

// userTurn is a one-turn request's content: the text (always, as Complete
// always sent it), then the attachments.
func userTurn(m Message) []turn {
	return []turn{{role: "user", parts: append([]part{{text: m.Text}}, attachmentParts(m.Documents, m.Images)...)}}
}

// attachmentParts are the documents, then the images.
func attachmentParts(documents []Document, images []Image) []part {
	var parts []part
	for i := range documents {
		parts = append(parts, part{doc: &documents[i]})
	}
	for i := range images {
		parts = append(parts, part{image: &images[i]})
	}
	return parts
}

func resultParts(results []toolResult) []part {
	parts := make([]part, len(results))
	for i := range results {
		parts[i] = part{result: &results[i]}
	}
	return parts
}

// withUserText appends text to the last turn when it is the user's, else as a
// new user turn. turns is not modified.
func withUserText(turns []turn, text string) []turn {
	out := slices.Clone(turns)
	if n := len(out); n > 0 && out[n-1].role == "user" {
		out[n-1].parts = slices.Concat(out[n-1].parts, []part{{text: text}})
		return out
	}
	return append(out, turn{role: "user", parts: []part{{text: text}}})
}

// joinText is the text parts of a turn, for wires that take one string.
func joinText(parts []part) string {
	var texts []string
	for _, p := range parts {
		if p.text != "" {
			texts = append(texts, p.text)
		}
	}
	return strings.Join(texts, "\n\n")
}
