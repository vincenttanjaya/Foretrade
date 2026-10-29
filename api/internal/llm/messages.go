package llm

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// messagesWire speaks the Anthropic Messages API (POST {base}/v1/messages).
// Effort goes in output_config, as on Bedrock; thinking is never sent.
type messagesWire struct {
	endpoint
	name, model string
}

func (w *messagesWire) turn(ctx context.Context, c *call, onEvent func(Event) error, onRaw func()) (reply, error) {
	body, err := w.body(c)
	if err != nil {
		return reply{}, upstream(w.name, err)
	}
	s := &messagesStream{}
	if err := w.stream(ctx, w.name, "/v1/messages", body, c.noRetries, onRaw, func(event string, data []byte) error {
		return s.accept(event, data, onEvent)
	}); err != nil {
		return reply{}, err
	}
	r, err := s.reply()
	if err != nil {
		return reply{}, upstream(w.name, err)
	}
	return r, nil
}

func (w *messagesWire) body(c *call) ([]byte, error) {
	var msgs []obj
	for _, t := range c.turns {
		if blocks, ok := t.native.([]json.RawMessage); ok {
			msgs = append(msgs, obj{"role": "assistant", "content": blocks})
			continue
		}
		var content []obj
		for _, p := range t.parts {
			switch {
			case p.result != nil: // first in its turn: the loop puts results before any note
				b := obj{"type": "tool_result", "tool_use_id": p.result.ID, "content": p.result.Out}
				if p.result.Failed {
					b["is_error"] = true
				}
				content = append(content, b)
			case p.image != nil:
				content = append(content, obj{"type": "image", "source": obj{"type": "base64",
					"media_type": imageMIME(p.image.Format), "data": base64.StdEncoding.EncodeToString(p.image.Bytes)}})
			case p.doc != nil && p.doc.Format == "pdf":
				content = append(content, obj{"type": "document", "title": p.doc.Name, "source": obj{"type": "base64",
					"media_type": docMIME["pdf"], "data": base64.StdEncoding.EncodeToString(p.doc.Bytes)}})
			case p.doc != nil:
				text, err := docText(p.doc)
				if err != nil {
					return nil, err
				}
				content = append(content, obj{"type": "document", "title": p.doc.Name,
					"source": obj{"type": "text", "media_type": "text/plain", "data": text}})
			case strings.TrimSpace(p.text) != "": // empty text blocks are rejected
				content = append(content, obj{"type": "text", "text": p.text})
			}
		}
		msgs = append(msgs, obj{"role": t.role, "content": content})
	}
	req := obj{"model": w.model, "max_tokens": c.maxTokens, "stream": true, "messages": msgs}
	if c.system != "" {
		req["system"] = c.system
	}
	if c.effort != "" {
		req["output_config"] = obj{"effort": c.effort}
	}
	if len(c.tools) > 0 {
		var tools []obj
		for _, t := range c.tools {
			tools = append(tools, obj{"name": t.Name, "description": t.Description, "input_schema": t.Schema})
		}
		req["tools"] = tools
	}
	return jsonBody(req)
}

// messagesStream rebuilds the content blocks from the stream, in order, for
// verbatim replay: thinking with its signature, redacted thinking, text and
// tool_use with its exact input.
type messagesStream struct {
	blocks  []*messagesBlock
	text    strings.Builder
	calls   []toolCall
	stop    string
	stopped bool
}

type messagesBlock struct {
	fields map[string]json.RawMessage // content_block_start's block; finalized at stop
	typ    string
	buf    strings.Builder // text, thinking or partial tool input JSON
	sig    string
	done   bool
}

func (s *messagesStream) accept(event string, data []byte, onEvent func(Event) error) error {
	var ev struct {
		Index        *int            `json:"index"`
		ContentBlock json.RawMessage `json:"content_block"`
		Delta        struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return errors.New("malformed stream event")
	}
	switch event {
	case "content_block_start":
		if ev.Index == nil || *ev.Index != len(s.blocks) || s.stopped {
			return errors.New("invalid content block position")
		}
		b := &messagesBlock{}
		if json.Unmarshal(ev.ContentBlock, &b.fields) != nil || json.Unmarshal(b.fields["type"], &b.typ) != nil {
			return errors.New("malformed content block")
		}
		s.blocks = append(s.blocks, b)
	case "content_block_delta":
		b, err := s.block(ev.Index)
		if err != nil {
			return err
		}
		switch ev.Delta.Type {
		case "text_delta":
			b.buf.WriteString(ev.Delta.Text)
			s.text.WriteString(ev.Delta.Text)
			return emit(onEvent, ev.Delta.Text)
		case "input_json_delta":
			b.buf.WriteString(ev.Delta.PartialJSON)
		case "thinking_delta":
			b.buf.WriteString(ev.Delta.Thinking)
		case "signature_delta":
			b.sig = ev.Delta.Signature
		}
	case "content_block_stop":
		b, err := s.block(ev.Index)
		if err != nil {
			return err
		}
		b.done = true
		return s.finish(b)
	case "message_delta":
		if ev.Delta.StopReason != "" {
			s.stop = ev.Delta.StopReason
		}
	case "message_stop":
		s.stopped = true
	case "error": // can follow HTTP 200
		return errorFrom(data)
	}
	return nil // message_start, ping, and events added later
}

func (s *messagesStream) block(index *int) (*messagesBlock, error) {
	if index == nil || *index < 0 || *index >= len(s.blocks) || s.blocks[*index].done {
		return nil, errors.New("invalid content block position")
	}
	return s.blocks[*index], nil
}

func (s *messagesStream) finish(b *messagesBlock) error {
	set := func(k string, v any) { b.fields[k], _ = jsonBody(v) }
	switch b.typ {
	case "text":
		set("text", b.buf.String())
	case "thinking":
		set("thinking", b.buf.String())
		set("signature", b.sig)
	case "tool_use":
		var id, name string
		json.Unmarshal(b.fields["id"], &id)
		json.Unmarshal(b.fields["name"], &name)
		call := objectArgs(id, name, b.buf.String())
		if call.invalid || id == "" || name == "" {
			return errors.New("tool call without id or name, or with input that is not a JSON object") // a signed turn can't be replayed altered
		}
		b.fields["input"] = call.Input
		s.calls = append(s.calls, call)
	}
	return nil
}

func (s *messagesStream) reply() (reply, error) {
	if !s.stopped {
		return reply{}, errors.New("response ended before message_stop")
	}
	r := reply{text: s.text.String(), calls: s.calls, stop: stopEnd}
	switch {
	case len(s.calls) > 0:
		r.stop = stopTools
	case s.stop == "max_tokens" || s.stop == "model_context_window_exceeded":
		r.stop = stopMaxTokens
	case s.stop == "refusal" && strings.TrimSpace(r.text) == "":
		return reply{}, errors.New("the model refused to answer")
	}
	var native []json.RawMessage
	for _, b := range s.blocks {
		if !b.done {
			return reply{}, errors.New("response ended with incomplete content blocks")
		}
		raw, err := jsonBody(b.fields)
		if err != nil {
			return reply{}, err
		}
		native = append(native, raw)
	}
	if len(native) > 0 {
		r.native = native
	}
	return r, nil
}

func (w *messagesWire) models(ctx context.Context) ([]ModelInfo, error) {
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	// ponytail: one page of 1000; follow has_more/last_id if a provider passes that
	if err := w.getJSON(ctx, w.name, "/v1/models?limit=1000", &list); err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, m := range list.Data {
		out = append(out, ModelInfo{ID: m.ID, Label: cmp.Or(m.DisplayName, m.ID)})
	}
	return out, nil
}
