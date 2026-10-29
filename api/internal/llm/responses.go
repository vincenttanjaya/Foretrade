package llm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
)

// responsesWire speaks the OpenAI Responses API (POST {base}/responses) for an
// API key and for a signed-in ChatGPT plan. Requests are stateless (store
// false): a tool loop replays this run's output items, encrypted reasoning
// included, as input.
//
// The ChatGPT plan route takes only model, instructions, input, tools,
// reasoning, include, store and stream, and its function tools in a namespace.
type responsesWire struct {
	endpoint
	name, model string
	chatgpt     bool
	plainTools  atomic.Bool // chatgpt: the namespace form was refused, send plain tools
}

const toolNamespace = "foretrade"

func (w *responsesWire) turn(ctx context.Context, c *call, onEvent func(Event) error, onRaw func()) (reply, error) {
	plain := !w.chatgpt || w.plainTools.Load()
	body, err := w.body(c, plain)
	if err != nil {
		return reply{}, upstream(w.name, err)
	}
	s := &responsesStream{}
	read := func(event string, data []byte) error { return s.accept(event, data, onEvent) }
	err = w.stream(ctx, w.name, "/responses", body, c.noRetries, onRaw, read)
	var pe *ProviderError
	if !plain && len(c.tools) > 0 && errors.As(err, &pe) && pe.Status == 400 &&
		(strings.HasPrefix(pe.param, "tools") || strings.Contains(pe.Msg, "namespace")) {
		w.plainTools.Store(true) // the documented form was refused: top-level tools, as Codex sends
		if body, err = w.body(c, true); err != nil {
			return reply{}, upstream(w.name, err)
		}
		err = w.stream(ctx, w.name, "/responses", body, c.noRetries, onRaw, read)
	}
	if err != nil {
		return reply{}, err
	}
	r, err := s.reply()
	if err != nil {
		return reply{}, upstream(w.name, err)
	}
	return r, nil
}

func (w *responsesWire) body(c *call, plainTools bool) ([]byte, error) {
	var input []any
	for _, t := range c.turns {
		if items, ok := t.native.([]json.RawMessage); ok {
			for _, it := range items {
				input = append(input, it)
			}
			continue
		}
		if t.role == "assistant" {
			input = append(input, obj{"type": "message", "role": "assistant",
				"content": []obj{{"type": "output_text", "text": joinText(t.parts)}}})
			continue
		}
		var content []obj
		for _, p := range t.parts {
			switch {
			case p.result != nil:
				input = append(input, obj{"type": "function_call_output", "call_id": p.result.ID, "output": p.result.Out})
			case p.image != nil:
				content = append(content, obj{"type": "input_image", "detail": "auto",
					"image_url": dataURL(imageMIME(p.image.Format), p.image.Bytes)})
			case p.doc != nil:
				content = append(content, obj{"type": "input_file", "filename": fileName(p.doc),
					"file_data": dataURL(docMIME[p.doc.Format], p.doc.Bytes)})
			default:
				content = append(content, obj{"type": "input_text", "text": p.text})
			}
		}
		if len(content) > 0 {
			input = append(input, obj{"type": "message", "role": "user", "content": content})
		}
	}
	req := obj{"model": w.model, "store": false, "stream": true, "input": input}
	if c.system != "" {
		req["instructions"] = c.system
	}
	if !w.chatgpt {
		req["max_output_tokens"] = c.maxTokens
	}
	if c.effort != "" { // non-reasoning models reject both
		req["reasoning"] = obj{"effort": c.effort}
		req["include"] = []string{"reasoning.encrypted_content"}
	}
	if len(c.tools) > 0 {
		var tools []obj
		for _, t := range c.tools {
			tools = append(tools, obj{"type": "function", "name": t.Name, "description": t.Description,
				"parameters": t.Schema, "strict": false}) // today's schemas are not strict-mode schemas
		}
		if plainTools {
			req["tools"] = tools
		} else {
			req["tools"] = []obj{{"type": "namespace", "name": toolNamespace, "description": "Foretrade app tools", "tools": tools}}
		}
	}
	return jsonBody(req)
}

// responsesStream keeps each finished output item verbatim, by output index.
type responsesStream struct {
	items []indexed
	text  strings.Builder
	done  bool
	stop  string
}

type indexed struct {
	index int
	item  json.RawMessage
}

func (s *responsesStream) accept(event string, data []byte, onEvent func(Event) error) error {
	var ev struct {
		Type        string          `json:"type"`
		Delta       string          `json:"delta"`
		OutputIndex int             `json:"output_index"`
		Item        json.RawMessage `json:"item"`
		Response    struct {
			Error             json.RawMessage `json:"error"`
			IncompleteDetails struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return errors.New("malformed stream event")
	}
	switch cmp.Or(event, ev.Type) {
	case "response.output_text.delta":
		s.text.WriteString(ev.Delta)
		return emit(onEvent, ev.Delta)
	case "response.output_item.done":
		if len(ev.Item) == 0 {
			return errors.New("output item missing")
		}
		s.items = append(s.items, indexed{ev.OutputIndex, ev.Item})
	case "response.completed":
		s.done, s.stop = true, stopEnd
	case "response.incomplete":
		if r := ev.Response.IncompleteDetails.Reason; r != "max_output_tokens" {
			return errors.New("response incomplete: " + cmp.Or(r, "no reason given"))
		}
		s.done, s.stop = true, stopMaxTokens
	case "response.failed":
		pe := errorFrom(ev.Response.Error)
		pe.Msg = cmp.Or(pe.Msg, "response failed")
		return pe
	case "error":
		return errorFrom(data)
	}
	return nil // created, in_progress, reasoning summaries, argument deltas…
}

func (s *responsesStream) reply() (reply, error) {
	if !s.done {
		return reply{}, errors.New("response ended before response.completed")
	}
	r := reply{text: s.text.String(), stop: s.stop}
	slices.SortStableFunc(s.items, func(a, b indexed) int { return a.index - b.index })
	var native []json.RawMessage
	dropped := false // a reasoning item was left out (effort off: none come encrypted)
	for _, it := range s.items {
		var item struct {
			Type             string  `json:"type"`
			CallID           string  `json:"call_id"`
			Name             string  `json:"name"`
			Arguments        string  `json:"arguments"`
			EncryptedContent *string `json:"encrypted_content"`
		}
		if err := json.Unmarshal(it.item, &item); err != nil {
			return reply{}, errors.New("malformed output item")
		}
		switch item.Type {
		case "function_call":
			if item.CallID == "" || item.Name == "" {
				return reply{}, errors.New("tool call missing call_id or name")
			}
			r.calls = append(r.calls, objectArgs(item.CallID, item.Name, item.Arguments))
		case "reasoning":
			if item.EncryptedContent == nil {
				dropped = true
				continue // without store, only encrypted reasoning can be sent back
			}
		}
		raw := it.item
		if dropped && item.Type != "reasoning" {
			// The API refuses an fc_/msg_ id whose reasoning item is missing;
			// without the id it is plain input, as Codex replays it.
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				return reply{}, errors.New("malformed output item")
			}
			delete(m, "id")
			b, err := json.Marshal(m)
			if err != nil {
				return reply{}, err
			}
			raw = b
		}
		native = append(native, raw)
	}
	if len(r.calls) > 0 {
		r.stop = stopTools
	}
	if len(native) > 0 {
		r.native = native
	}
	return r, nil
}

func (w *responsesWire) models(ctx context.Context) ([]ModelInfo, error) {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct { // the ChatGPT plan's catalogue
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := w.getJSON(ctx, w.name, "/models", &list); err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, m := range list.Models {
		if m.Visibility == "list" {
			out = append(out, ModelInfo{ID: m.Slug, Label: cmp.Or(m.DisplayName, m.Slug)})
		}
	}
	for _, m := range list.Data {
		out = append(out, ModelInfo{ID: m.ID, Label: m.ID})
	}
	return out, nil
}
