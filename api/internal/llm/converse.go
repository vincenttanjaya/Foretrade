package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// converse calls Bedrock through the Converse API.
//
// Sonnet 5.5 rules: effort goes
// in AdditionalModelRequestFields.output_config; MaxTokens includes the
// model's private reasoning; no temperature/top_p/thinking budget/forced tool
// choice; assistant content blocks — signed reasoning included — are replayed
// unchanged inside a tool loop.
type converse struct {
	rt    *bedrockruntime.Client
	model string
}

func newConverse(cfg aws.Config, model string) *converse {
	return &converse{rt: bedrockruntime.NewFromConfig(cfg, plainBodies), model: model}
}

// sonnetEffort is Bedrock's default effort gate: only Sonnet 5.5 takes the field.
func sonnetEffort(model string) bool { return strings.Contains(model, "claude-sonnet-5-5") }

// plainBodies hides WriteTo on request bodies. smithy-go (v1.28.1, still in
// v1.28.2) closes the body as soon as response headers arrive, and its WriteTo
// then returns io.EOF instead of nil. Over HTTP/1.1 net/http may still be
// copying the body to io.Discard to check for extra bytes; it takes that EOF
// for a failed request write and closes the connection under the response:
// "use of closed network connection" mid-stream (TestRequestBodyClosedEarly).
// HTTP/2, which Bedrock negotiates, reads bodies with Read and is unaffected;
// the fake Bedrock in tests speaks HTTP/1.1.
// ponytail: drop once smithy-go's safeWriteToReadCloser returns nil at EOF.
func plainBodies(o *bedrockruntime.Options) { o.HTTPClient = readOnlyBody{o.HTTPClient} }

type readOnlyBody struct{ bedrockruntime.HTTPClient }

func (c readOnlyBody) Do(r *http.Request) (*http.Response, error) {
	if r.Body != nil && r.Body != http.NoBody { // a wrapped NoBody would be sent chunked
		r.Body = struct{ io.ReadCloser }{r.Body} // Read and Close only: a closed body reads as EOF
	}
	return c.HTTPClient.Do(r)
}

func noRetries(on bool) func(*bedrockruntime.Options) {
	return func(o *bedrockruntime.Options) {
		if on {
			o.Retryer = aws.NopRetryer{}
		}
	}
}

// rejected turns an SDK error carrying a decoded provider error into a
// *ProviderError; transport, context and decoding errors pass unchanged.
func rejected(err error) error {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return err
	}
	pe := &ProviderError{Code: api.ErrorCode(), Msg: api.ErrorMessage()}
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		pe.Status = re.HTTPStatusCode()
	}
	return pe
}

func (b *converse) turn(ctx context.Context, c *call, onEvent func(Event) error, onRaw func()) (reply, error) {
	if onEvent == nil {
		return b.complete(ctx, c)
	}
	content, calls, stop, err := b.stream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId:                      aws.String(b.model),
		System:                       systemBlocks(c.system),
		Messages:                     converseMessages(c.turns),
		InferenceConfig:              &types.InferenceConfiguration{MaxTokens: aws.Int32(int32(c.maxTokens))},
		ToolConfig:                   toolConfig(c.tools),
		AdditionalModelRequestFields: fields(c.effort),
	}, onEvent, c.noRetries, onRaw)
	if err != nil {
		return reply{}, err
	}
	r := reply{text: textOf(content), calls: calls, stop: converseStop(stop)}
	if len(content) > 0 { // Bedrock rejects an empty assistant message
		r.native = content
	}
	return r, nil
}

func (b *converse) complete(ctx context.Context, c *call) (reply, error) {
	out, err := b.rt.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId:                      aws.String(b.model),
		System:                       systemBlocks(c.system),
		Messages:                     converseMessages(c.turns),
		InferenceConfig:              &types.InferenceConfiguration{MaxTokens: aws.Int32(int32(c.maxTokens))},
		AdditionalModelRequestFields: fields(c.effort),
	}, noRetries(c.noRetries))
	if err != nil {
		return reply{}, upstream("bedrock converse", rejected(err))
	}
	if out.StopReason == types.StopReasonMaxTokens {
		return reply{stop: stopMaxTokens}, nil
	}
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return reply{}, upstream("bedrock converse", fmt.Errorf("unexpected output %T", out.Output))
	}
	return reply{text: textOf(msg.Value.Content), stop: converseStop(out.StopReason)}, nil
}

// models: listing Bedrock models needs the bedrock control-plane SDK.
// ponytail: free text only; add service/bedrock ListFoundationModels if asked.
func (b *converse) models(context.Context) ([]ModelInfo, error) {
	return nil, errors.New("Bedrock models can't be listed here: enter a model or inference profile id")
}

func converseStop(s types.StopReason) string {
	switch s {
	case types.StopReasonToolUse:
		return stopTools
	case types.StopReasonMaxTokens:
		return stopMaxTokens
	}
	return stopEnd
}

// fields carries the effort, when there is one to send.
func fields(effort Effort) document.Interface {
	if effort == "" {
		return nil
	}
	return document.NewLazyDocument(map[string]any{"output_config": map[string]any{"effort": string(effort)}})
}

func systemBlocks(system string) []types.SystemContentBlock {
	if system == "" {
		return nil
	}
	return []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: system}}
}

// converseMessages renders turns as Converse messages; a replayed assistant
// turn is its blocks as streamed.
func converseMessages(turns []turn) []types.Message {
	out := make([]types.Message, len(turns))
	for i, t := range turns {
		if blocks, ok := t.native.([]types.ContentBlock); ok {
			out[i] = types.Message{Role: types.ConversationRoleAssistant, Content: blocks}
			continue
		}
		role := types.ConversationRoleUser
		if t.role == "assistant" {
			role = types.ConversationRoleAssistant
		}
		blocks := []types.ContentBlock{}
		for _, p := range t.parts {
			blocks = append(blocks, converseBlock(p))
		}
		out[i] = types.Message{Role: role, Content: blocks}
	}
	return out
}

func converseBlock(p part) types.ContentBlock {
	switch {
	case p.result != nil:
		r := p.result
		block := types.ToolResultBlock{ToolUseId: aws.String(r.ID),
			Content: []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: r.Out}}}
		if r.Failed {
			block.Status = types.ToolResultStatusError
		}
		return &types.ContentBlockMemberToolResult{Value: block}
	case p.doc != nil:
		return &types.ContentBlockMemberDocument{Value: types.DocumentBlock{
			Format: types.DocumentFormat(p.doc.Format), Name: aws.String(p.doc.Name), Source: &types.DocumentSourceMemberBytes{Value: p.doc.Bytes},
		}}
	case p.image != nil:
		return &types.ContentBlockMemberImage{Value: types.ImageBlock{
			Format: types.ImageFormat(p.image.Format), Source: &types.ImageSourceMemberBytes{Value: p.image.Bytes},
		}}
	}
	return &types.ContentBlockMemberText{Value: p.text}
}

func toolConfig(tools []Tool) *types.ToolConfiguration {
	if len(tools) == 0 {
		return nil
	}
	cfg := &types.ToolConfiguration{}
	for _, t := range tools {
		cfg.Tools = append(cfg.Tools, &types.ToolMemberToolSpec{Value: types.ToolSpecification{
			Name:        aws.String(t.Name),
			Description: aws.String(t.Description),
			InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(t.Schema)},
		}})
	}
	return cfg
}

func textOf(content []types.ContentBlock) string {
	var sb strings.Builder
	for _, b := range content {
		if t, ok := b.(*types.ContentBlockMemberText); ok {
			sb.WriteString(t.Value)
		}
	}
	return sb.String()
}
