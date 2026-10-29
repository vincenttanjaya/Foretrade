package llm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// chatWire speaks OpenAI Chat Completions (POST {base}/chat/completions), the
// protocol most providers and local servers share.
//
// ponytail: no max_tokens: providers count reasoning in it differently and
// some reject it, so each provider's default ceiling applies; add a
// per-preset field if someone needs one. Mistral rejects a user message right
// after tool messages, which only the block-mode budget note sends.
type chatWire struct {
	endpoint
	name, model string // name: the preset id, for errors and its quirks
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	ExtraContent json.RawMessage `json:"extra_content,omitempty"` // Gemini's thought signature
}

// chatAssistant is a streamed assistant message, rebuilt for replay with every
// opaque field the provider sent: DeepSeek needs reasoning_content back in a
// tool loop, OpenRouter reasoning_details in order, Gemini extra_content,
// Mistral its thinking chunk in content.
type chatAssistant struct {
	Role             string            `json:"role"`
	Content          any               `json:"content"` // a string, or Mistral's thinking and text chunks
	ToolCalls        []chatToolCall    `json:"tool_calls,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
}

func (w *chatWire) turn(ctx context.Context, c *call, onEvent func(Event) error, onRaw func()) (reply, error) {
	body, err := w.body(c)
	if err != nil {
		return reply{}, upstream(w.name, err)
	}
	s := &chatStream{}
	if err := w.stream(ctx, w.name, "/chat/completions", body, c.noRetries, onRaw, func(_ string, data []byte) error {
		return s.accept(data, onEvent)
	}); err != nil {
		return reply{}, err
	}
	r, err := s.reply()
	if err != nil {
		return reply{}, upstream(w.name, err)
	}
	return r, nil
}

func (w *chatWire) body(c *call) ([]byte, error) {
	var msgs []any
	if c.system != "" {
		msgs = append(msgs, obj{"role": "system", "content": c.system})
	}
	for _, t := range c.turns {
		switch {
		case t.native != nil:
			msgs = append(msgs, t.native)
			continue
		case t.role == "assistant":
			msgs = append(msgs, obj{"role": "assistant", "content": joinText(t.parts)})
			continue
		}
		var content []obj
		textOnly := true
		for _, p := range t.parts {
			switch {
			case p.result != nil:
				msgs = append(msgs, obj{"role": "tool", "tool_call_id": p.result.ID, "content": p.result.Out})
			case p.image != nil:
				textOnly = false
				content = append(content, obj{"type": "image_url", "image_url": obj{"url": dataURL(imageMIME(p.image.Format), p.image.Bytes)}})
			case p.doc != nil && p.doc.Format == "pdf" && w.name == "openrouter":
				textOnly = false
				content = append(content, obj{"type": "file", "file": obj{"filename": fileName(p.doc), "file_data": dataURL(docMIME["pdf"], p.doc.Bytes)}})
			case p.doc != nil:
				text, err := docText(p.doc)
				if err != nil {
					return nil, err
				}
				content = append(content, obj{"type": "text", "text": "<file name=" + attr(fileName(p.doc)) + ">\n" + text + "\n</file>"})
			default:
				content = append(content, obj{"type": "text", "text": p.text})
			}
		}
		switch {
		case len(content) == 0:
		case textOnly: // a plain string: what every server accepts
			var texts []string
			for _, p := range content {
				texts = append(texts, p["text"].(string))
			}
			msgs = append(msgs, obj{"role": "user", "content": strings.Join(texts, "\n\n")})
		default:
			msgs = append(msgs, obj{"role": "user", "content": content})
		}
	}
	req := obj{"model": w.model, "stream": true, "messages": msgs}
	if c.effort != "" {
		switch w.name {
		case "openrouter":
			req["reasoning"] = obj{"effort": c.effort}
		case "deepseek": // takes low, high and max, not medium
			e := c.effort
			if e == Medium {
				e = High
			}
			req["reasoning_effort"] = e
		default:
			req["reasoning_effort"] = c.effort
		}
	}
	if len(c.tools) > 0 {
		var tools []obj
		for _, t := range c.tools {
			fn := obj{"name": t.Name, "parameters": t.Schema}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			tools = append(tools, obj{"type": "function", "function": fn})
		}
		req["tools"] = tools
	}
	return jsonBody(req)
}

// chatStream rebuilds the assistant message from data-only chunks.
type chatStream struct {
	text, reasoning strings.Builder
	thinking        strings.Builder // Mistral's thinking chunks
	details         []json.RawMessage
	calls           []*chatSlot
	finish          string
}

type chatSlot struct {
	index    *int
	id, name string
	args     strings.Builder
	extra    json.RawMessage
}

func (s *chatStream) accept(data []byte, onEvent func(Event) error) error {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          json.RawMessage   `json:"content"`
				ReasoningContent string            `json:"reasoning_content"`
				ReasoningDetails []json.RawMessage `json:"reasoning_details"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
					ExtraContent json.RawMessage `json:"extra_content"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return errors.New("malformed stream chunk")
	}
	if len(chunk.Error) > 0 && string(chunk.Error) != "null" { // OpenRouter fails mid-stream this way
		return errorFrom(data)
	}
	if len(chunk.Choices) == 0 {
		return nil // the usage chunk
	}
	ch := chunk.Choices[0]
	d := ch.Delta
	text, thinking, err := chatContent(d.Content)
	if err != nil {
		return errors.New("malformed stream chunk")
	}
	s.text.WriteString(text)
	if err := emit(onEvent, text); err != nil {
		return err
	}
	s.thinking.WriteString(thinking)
	s.reasoning.WriteString(d.ReasoningContent) // delta.reasoning (Groq, Ollama) is not replayed
	s.details = append(s.details, d.ReasoningDetails...)
	for _, tc := range d.ToolCalls {
		slot := s.slot(tc.Index, tc.ID, tc.Function.Name)
		if tc.ID != "" {
			slot.id = tc.ID
		}
		if tc.Function.Name != "" {
			slot.name = tc.Function.Name // set, not appended: some providers repeat it
		}
		slot.args.WriteString(tc.Function.Arguments)
		if len(tc.ExtraContent) > 0 {
			slot.extra = tc.ExtraContent
		}
	}
	if ch.FinishReason != "" {
		s.finish = ch.FinishReason
	}
	return nil
}

// chatContent is a delta's text and thinking. Content is a string, or while
// a Mistral model reasons a list of thinking chunks (text chunks inside) and
// text chunks.
// ponytail: only text inside thinking is kept; replay other chunk kinds if
// Mistral starts sending them there.
func chatContent(raw json.RawMessage) (string, string, error) {
	if len(raw) == 0 || raw[0] != '[' {
		var s *string // absent, null or a string
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &s); err != nil {
				return "", "", err
			}
		}
		if s == nil {
			return "", "", nil
		}
		return *s, "", nil
	}
	var chunks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking []struct {
			Text string `json:"text"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(raw, &chunks); err != nil {
		return "", "", err
	}
	var text, thinking strings.Builder
	for _, c := range chunks {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "thinking":
			for _, t := range c.Thinking {
				thinking.WriteString(t.Text)
			}
		}
	}
	return text.String(), thinking.String(), nil
}

// slot finds a call by index, else by id; a fragment with neither id nor name
// continues the last call, anything else opens a new one.
func (s *chatStream) slot(index *int, id, name string) *chatSlot {
	for _, c := range s.calls {
		if index != nil && c.index != nil && *c.index == *index || index == nil && id != "" && c.id == id {
			return c
		}
	}
	if index == nil && id == "" && name == "" && len(s.calls) > 0 {
		return s.calls[len(s.calls)-1]
	}
	c := &chatSlot{index: index}
	s.calls = append(s.calls, c)
	return c
}

var callSeq atomic.Uint32

func (s *chatStream) reply() (reply, error) {
	if s.finish == "" {
		return reply{}, errors.New("response ended before finish_reason")
	}
	r := reply{text: s.text.String(), stop: stopEnd}
	if s.finish == "length" {
		r.stop = stopMaxTokens
	}
	msg := chatAssistant{Role: "assistant", Content: r.text, ReasoningContent: s.reasoning.String(), ReasoningDetails: s.details}
	thinking := s.thinking.String()
	if thinking != "" { // Mistral asks for its thinking back, before the text
		chunks := []obj{{"type": "thinking", "thinking": []obj{{"type": "text", "text": thinking}}}}
		if r.text != "" {
			chunks = append(chunks, obj{"type": "text", "text": r.text})
		}
		msg.Content = chunks
	}
	for _, c := range s.calls {
		if c.id == "" {
			c.id = fmt.Sprintf("call%05d", callSeq.Add(1)%100000) // 9 alphanumerics: what Mistral accepts
		}
		call := objectArgs(c.id, c.name, c.args.String())
		tc := chatToolCall{ID: c.id, Type: "function", ExtraContent: c.extra}
		// The arguments the run used, {} for invalid ones: servers such as Ollama
		// parse history arguments and refuse the request otherwise.
		tc.Function.Name, tc.Function.Arguments = c.name, string(call.Input)
		msg.ToolCalls = append(msg.ToolCalls, tc)
		r.calls = append(r.calls, call)
	}
	if len(r.calls) > 0 {
		r.stop = stopTools // whatever finish_reason says
	}
	if r.text != "" || len(r.calls) > 0 || thinking != "" || msg.ReasoningContent != "" || len(msg.ReasoningDetails) > 0 {
		b, err := jsonBody(msg)
		if err != nil {
			return reply{}, err
		}
		r.native = b
	}
	return r, nil
}

func (w *chatWire) models(ctx context.Context) ([]ModelInfo, error) {
	var list struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"` // OpenRouter
		} `json:"data"`
	}
	if err := w.getJSON(ctx, w.name, "/models", &list); err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, m := range list.Data {
		out = append(out, ModelInfo{ID: m.ID, Label: cmp.Or(m.Name, m.ID)})
	}
	return out, nil
}
