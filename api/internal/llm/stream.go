package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyjson "github.com/aws/smithy-go/document/json"
	"github.com/aws/smithy-go/transport/http/protocol/restjson1"
)

// ownCodec gives a stream a restJson1 protocol of its own. smithy-go v1.28.1
// decodes every event stream of a client with one shared codec and buffer, so
// concurrent streams (two users chatting, the arena's agents) race and can
// corrupt each other's events (TestAgentConcurrentStreams).
// ponytail: drop once smithy-go keeps codec state per stream.
func ownCodec(o *bedrockruntime.Options) { o.Protocol = restjson1.New(nil) }

// stream runs one ConverseStream turn, emitting text deltas, and returns the
// assistant content blocks in provider order (ready to replay verbatim), the
// tool calls, and the stop reason.
func (b *converse) stream(ctx context.Context, input *bedrockruntime.ConverseStreamInput, onEvent func(Event) error, once bool, onProviderEvent func()) ([]types.ContentBlock, []toolCall, types.StopReason, error) {
	out, err := b.rt.ConverseStream(ctx, input, ownCodec, noRetries(once))
	if err != nil {
		return nil, nil, "", upstream("bedrock converse stream", rejected(err))
	}
	s := out.GetStream()
	defer s.Close()
	acc := &accumulator{blocks: map[int32]*streamBlock{}}
	for event := range s.Events() {
		if onProviderEvent != nil {
			onProviderEvent() // admission to the event stream precedes parsing or suppression
		}
		if err := acc.accept(event, onEvent); err != nil {
			var cb callbackError
			if errors.As(err, &cb) {
				return nil, nil, "", cb.err // the caller's own error, e.g. client went away
			}
			return nil, nil, "", upstream("bedrock converse stream", err)
		}
	}
	if err := s.Err(); err != nil {
		return nil, nil, "", upstream("bedrock converse stream", rejected(err))
	}
	content, err := acc.content()
	if err != nil {
		return nil, nil, "", upstream("bedrock converse stream", err)
	}
	return content, acc.calls, acc.stop, nil
}

// callbackError marks an onEvent failure so it is not reported as a provider error.
type callbackError struct{ err error }

func (e callbackError) Error() string { return e.err.Error() }

// accumulator rebuilds the assistant message from stream events. Reasoning
// (text + signature, or redacted bytes) is kept only so it can be replayed;
// it is never emitted.
type accumulator struct {
	blocks  map[int32]*streamBlock
	calls   []toolCall
	stop    types.StopReason
	stopped bool
}

type streamBlock struct {
	value    types.ContentBlock
	toolJSON strings.Builder
	done     bool
}

func (a *accumulator) block(index *int32) (*streamBlock, error) {
	if index == nil || *index < 0 || a.stopped {
		return nil, errors.New("invalid content block position")
	}
	b := a.blocks[*index]
	if b == nil {
		b = &streamBlock{}
		a.blocks[*index] = b
	}
	if b.done {
		return nil, errors.New("content received after block stop")
	}
	return b, nil
}

func (a *accumulator) accept(event types.ConverseStreamOutput, onEvent func(Event) error) error {
	switch v := event.(type) {
	case *types.ConverseStreamOutputMemberMessageStart:
	case *types.ConverseStreamOutputMemberContentBlockStart:
		b, err := a.block(v.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		switch start := v.Value.Start.(type) {
		case *types.ContentBlockStartMemberToolUse:
			if aws.ToString(start.Value.ToolUseId) == "" || aws.ToString(start.Value.Name) == "" {
				return errors.New("tool call missing id or name")
			}
			b.value = &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
				ToolUseId: start.Value.ToolUseId, Name: start.Value.Name, Type: start.Value.Type}}
		case nil: // text and reasoning blocks begin with their first delta
		default:
			return fmt.Errorf("unsupported content block start %T", start)
		}
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		b, err := a.block(v.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		switch d := v.Value.Delta.(type) {
		case *types.ContentBlockDeltaMemberText:
			if b.value == nil {
				b.value = &types.ContentBlockMemberText{}
			}
			text, ok := b.value.(*types.ContentBlockMemberText)
			if !ok {
				return errors.New("mixed content block types")
			}
			text.Value += d.Value
			if err := onEvent(Event{Kind: "delta", Text: d.Value}); err != nil {
				return callbackError{err}
			}
		case *types.ContentBlockDeltaMemberToolUse:
			if _, ok := b.value.(*types.ContentBlockMemberToolUse); !ok {
				return errors.New("tool delta without tool start")
			}
			b.toolJSON.WriteString(aws.ToString(d.Value.Input))
		case *types.ContentBlockDeltaMemberReasoningContent:
			return acceptReasoning(b, d.Value)
		default:
			return fmt.Errorf("unsupported content delta %T", d)
		}
	case *types.ConverseStreamOutputMemberContentBlockStop:
		b, err := a.block(v.Value.ContentBlockIndex)
		if err != nil {
			return err
		}
		if b.value == nil {
			return errors.New("empty content block")
		}
		b.done = true
		if tool, ok := b.value.(*types.ContentBlockMemberToolUse); ok {
			return a.finishTool(tool, b.toolJSON.String())
		}
	case *types.ConverseStreamOutputMemberMessageStop:
		a.stopped, a.stop = true, v.Value.StopReason
	case *types.ConverseStreamOutputMemberMetadata:
	default:
		return fmt.Errorf("unsupported stream event %T", event)
	}
	return nil
}

func acceptReasoning(b *streamBlock, delta types.ReasoningContentBlockDelta) error {
	if b.value == nil {
		b.value = &types.ContentBlockMemberReasoningContent{}
	}
	r, ok := b.value.(*types.ContentBlockMemberReasoningContent)
	if !ok {
		return errors.New("mixed content block types")
	}
	switch d := delta.(type) {
	case *types.ReasoningContentBlockDeltaMemberText:
		text, err := reasoningText(r)
		if err != nil {
			return err
		}
		*text.Text += d.Value
	case *types.ReasoningContentBlockDeltaMemberSignature:
		text, err := reasoningText(r)
		if err != nil {
			return err
		}
		text.Signature = aws.String(aws.ToString(text.Signature) + d.Value)
	case *types.ReasoningContentBlockDeltaMemberRedactedContent:
		if r.Value == nil {
			r.Value = &types.ReasoningContentBlockMemberRedactedContent{}
		}
		red, ok := r.Value.(*types.ReasoningContentBlockMemberRedactedContent)
		if !ok {
			return errors.New("mixed reasoning block types")
		}
		red.Value = append(red.Value, d.Value...)
	default:
		return fmt.Errorf("unsupported reasoning delta %T", d)
	}
	return nil
}

func reasoningText(r *types.ContentBlockMemberReasoningContent) (*types.ReasoningTextBlock, error) {
	if r.Value == nil {
		r.Value = &types.ReasoningContentBlockMemberReasoningText{Value: types.ReasoningTextBlock{Text: aws.String("")}}
	}
	text, ok := r.Value.(*types.ReasoningContentBlockMemberReasoningText)
	if !ok {
		return nil, errors.New("mixed reasoning block types")
	}
	return &text.Value, nil
}

// finishTool parses the streamed input. A no-argument call may stream zero
// bytes, which becomes {}. The replayed block keeps arbitrary-precision
// numbers so the signed history is not silently altered.
func (a *accumulator) finishTool(tool *types.ContentBlockMemberToolUse, raw string) error {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return errors.New("tool input is not valid JSON") // raw model arguments stay out of errors and logs
	}
	if _, ok := parsed.(map[string]any); !ok {
		return errors.New("tool input is not a JSON object")
	}
	var preserved any
	if err := smithyjson.NewDecoder().DecodeJSONInterface(parsed, &preserved); err != nil {
		return errors.New("tool input could not be preserved")
	}
	tool.Value.Input = document.NewLazyDocument(preserved)
	a.calls = append(a.calls, toolCall{ID: aws.ToString(tool.Value.ToolUseId), Name: aws.ToString(tool.Value.Name), Input: json.RawMessage(raw)})
	return nil
}

// content returns the blocks ordered by ContentBlockIndex, the provider's order.
func (a *accumulator) content() ([]types.ContentBlock, error) {
	if !a.stopped {
		return nil, errors.New("response ended before message stop")
	}
	out := make([]types.ContentBlock, len(a.blocks))
	for i := range out {
		b := a.blocks[int32(i)]
		if b == nil || !b.done {
			return nil, errors.New("response ended with incomplete content blocks")
		}
		out[i] = b.value
	}
	return out, nil
}
