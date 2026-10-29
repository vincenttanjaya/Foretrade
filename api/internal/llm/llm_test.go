package llm

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestExtractJSON(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"a":1}`, `{"a":1}`},
		{"```json\n{\"a\": {\"b\": [1, 2]}}\n```", `{"a": {"b": [1, 2]}}`},
		{`Sure! Here it is: {"s": "brace } inside \" quote {"} trailing {"x":2}`, `{"s": "brace } inside \" quote {"}`},
		{`see {the plan} below: {"ok":true}`, `{"ok":true}`}, // first balanced span is not JSON
		{`no object here`, ``},
		{`{"unterminated": 1`, ``},
	} {
		if got := extractJSON(tc.in); got != tc.want {
			t.Errorf("extractJSON(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	var out struct{ A int }
	if err := DecodeJSON("```json\n{\"a\": 7}\n```", &out); err != nil || out.A != 7 {
		t.Fatalf("DecodeJSON: %v %+v", err, out)
	}
	if err := DecodeJSON(`{"a": "x"}`, &out); err == nil {
		t.Fatal("type mismatch must fail")
	}
	// encoding/json keeps the last copy of a repeated key (case-insensitively),
	// which would hide a contradictory answer.
	var dist struct {
		Probabilities []struct {
			Outcome string
			P       float64
		}
	}
	for _, in := range []string{
		`{"probabilities":[{"outcome":"Yes","p":0.2,"p":0.95},{"outcome":"No","p":0.05}]}`,
		`{"probabilities":[{"outcome":"Yes","p":0.2},{"outcome":"No","p":0.8}],"probabilities":[{"outcome":"Yes","p":0.95},{"outcome":"No","p":0.05}]}`,
		`{"probabilities":[{"outcome":"Yes","p":0.2,"P":0.95},{"outcome":"No","p":0.05}]}`,
	} {
		if err := DecodeJSON(in, &dist); err == nil {
			t.Errorf("duplicate key accepted: %s → %+v", in, dist)
		}
	}
	var nested map[string]any
	if err := DecodeJSON(`{"a":{"x":1},"b":{"x":2},"c":[{"x":3},{"x":4}]}`, &nested); err != nil {
		t.Fatalf("same key in different objects: %v", err)
	}
}

func TestToTurns(t *testing.T) {
	got := converseMessages(toTurns([]Message{
		{Role: "assistant", Text: "orphan greeting"}, // must not lead
		{Role: "user", Text: "a"},
		{Role: "user", Text: "b"},
		{Role: "assistant", Text: "  "}, // empty: dropped
		{Role: "assistant", Text: "c"},
		{Role: "user", Text: "d"},
	}))
	roles := ""
	for _, m := range got {
		roles += string(m.Role)[:1]
	}
	if roles != "uau" || len(got[0].Content) != 2 || textOf(got[1].Content) != "c" {
		t.Fatalf("roles %q, messages %+v", roles, got)
	}
}

// The accumulator must rebuild reasoning (with signature), text and tool use
// in provider order, emit only text deltas, and collect the tool call.
func TestAccumulator(t *testing.T) {
	i := func(n int32) *int32 { return &n }
	events := []types.ConverseStreamOutput{
		&types.ConverseStreamOutputMemberMessageStart{},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: i(0),
			Delta: &types.ContentBlockDeltaMemberReasoningContent{Value: &types.ReasoningContentBlockDeltaMemberText{Value: "think"}}}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: i(0),
			Delta: &types.ContentBlockDeltaMemberReasoningContent{Value: &types.ReasoningContentBlockDeltaMemberSignature{Value: "sig"}}}},
		&types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: i(0)}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: i(1),
			Delta: &types.ContentBlockDeltaMemberText{Value: "Checking "}}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: i(1),
			Delta: &types.ContentBlockDeltaMemberText{Value: "BTC."}}},
		&types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: i(1)}},
		&types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{ContentBlockIndex: i(2),
			Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{ToolUseId: aws.String("t1"), Name: aws.String("quote")}}}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: i(2),
			Delta: &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: aws.String(`{"symbol":`)}}}},
		&types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: i(2),
			Delta: &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: aws.String(`"BTC","n":12345678901234567}`)}}}},
		&types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: i(2)}},
		&types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonToolUse}},
	}
	acc := &accumulator{blocks: map[int32]*streamBlock{}}
	var deltas string
	for _, e := range events {
		if err := acc.accept(e, func(ev Event) error { deltas += ev.Text; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	content, err := acc.content()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := content[0].(*types.ContentBlockMemberReasoningContent)
	if !ok {
		t.Fatalf("block 0 = %T", content[0])
	}
	rt := r.Value.(*types.ReasoningContentBlockMemberReasoningText).Value
	if aws.ToString(rt.Text) != "think" || aws.ToString(rt.Signature) != "sig" {
		t.Fatalf("reasoning = %+v", rt)
	}
	if deltas != "Checking BTC." || textOf(content) != "Checking BTC." {
		t.Fatalf("deltas %q, text %q", deltas, textOf(content))
	}
	if _, ok := content[2].(*types.ContentBlockMemberToolUse); !ok || acc.stop != types.StopReasonToolUse {
		t.Fatalf("block 2 = %T, stop %s", content[2], acc.stop)
	}
	if len(acc.calls) != 1 || acc.calls[0].Name != "quote" || string(acc.calls[0].Input) != `{"symbol":"BTC","n":12345678901234567}` {
		t.Fatalf("calls = %+v", acc.calls)
	}

	empty := &accumulator{blocks: map[int32]*streamBlock{}}
	empty.accept(&types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{ContentBlockIndex: i(0),
		Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{ToolUseId: aws.String("t2"), Name: aws.String("overview")}}}}, nil)
	if err := empty.accept(&types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: i(0)}}, nil); err != nil ||
		string(empty.calls[0].Input) != "{}" {
		t.Fatalf("no-argument tool: %v %+v", err, empty.calls)
	}
}

// Every malformed stream is refused, never half-assembled.
func TestAccumulatorRejects(t *testing.T) {
	i := func(n int32) *int32 { return &n }
	start := func(idx *int32, id, name string) types.ConverseStreamOutput {
		return &types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{ContentBlockIndex: idx,
			Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{ToolUseId: aws.String(id), Name: aws.String(name)}}}}
	}
	delta := func(idx *int32, d types.ContentBlockDelta) types.ConverseStreamOutput {
		return &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: idx, Delta: d}}
	}
	stop := func(idx *int32) types.ConverseStreamOutput {
		return &types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: idx}}
	}
	text := func(s string) types.ContentBlockDelta { return &types.ContentBlockDeltaMemberText{Value: s} }
	reasoning := func(d types.ReasoningContentBlockDelta) types.ContentBlockDelta {
		return &types.ContentBlockDeltaMemberReasoningContent{Value: d}
	}
	toolInput := func(s string) types.ContentBlockDelta {
		return &types.ContentBlockDeltaMemberToolUse{Value: types.ToolUseBlockDelta{Input: aws.String(s)}}
	}
	messageStop := &types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonEndTurn}}

	for name, events := range map[string][]types.ConverseStreamOutput{
		"no index":                {delta(nil, text("a"))},
		"negative index":          {delta(i(-1), text("a"))},
		"content after stop":      {delta(i(0), text("a")), stop(i(0)), delta(i(0), text("b"))},
		"content after message":   {messageStop, delta(i(0), text("a"))},
		"tool without id":         {start(i(0), "", "quote")},
		"tool without name":       {start(i(0), "t1", "")},
		"unsupported start":       {&types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{ContentBlockIndex: i(0), Start: &types.ContentBlockStartMemberImage{}}}},
		"text into tool":          {start(i(0), "t1", "quote"), delta(i(0), text("a"))},
		"tool delta into text":    {delta(i(0), text("a")), delta(i(0), toolInput("{}"))},
		"reasoning into text":     {delta(i(0), text("a")), delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberText{Value: "x"}))},
		"redacted then text":      {delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte{1}})), delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberText{Value: "x"}))},
		"redacted then sig":       {delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte{1}})), delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberSignature{Value: "s"}))},
		"text then redacted":      {delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberText{Value: "x"})), delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte{1}}))},
		"unknown reasoning":       {delta(i(0), reasoning(&types.UnknownUnionMember{Tag: "future"}))},
		"unsupported delta":       {delta(i(0), &types.ContentBlockDeltaMemberCitation{})},
		"empty block":             {stop(i(0))},
		"invalid tool JSON":       {start(i(0), "t1", "quote"), delta(i(0), toolInput(`{"a":`)), stop(i(0))},
		"tool input not object":   {start(i(0), "t1", "quote"), delta(i(0), toolInput(`"BTC"`)), stop(i(0))},
		"unknown event":           {&types.UnknownUnionMember{Tag: "future"}},
		"bad index on block stop": {stop(nil)},
		"bad index on start":      {start(nil, "t1", "quote")},
	} {
		acc := &accumulator{blocks: map[int32]*streamBlock{}}
		var err error
		for _, e := range events {
			if err = acc.accept(e, func(Event) error { return nil }); err != nil {
				break
			}
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Redacted reasoning is kept as bytes for replay.
	acc := &accumulator{blocks: map[int32]*streamBlock{}}
	for _, e := range []types.ConverseStreamOutput{
		delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte{1, 2}})),
		delta(i(0), reasoning(&types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte{3}})),
		stop(i(0)), &types.ConverseStreamOutputMemberMetadata{}, messageStop,
	} {
		if err := acc.accept(e, nil); err != nil {
			t.Fatal(err)
		}
	}
	content, err := acc.content()
	if err != nil {
		t.Fatal(err)
	}
	red := content[0].(*types.ContentBlockMemberReasoningContent).Value.(*types.ReasoningContentBlockMemberRedactedContent)
	if string(red.Value) != "\x01\x02\x03" {
		t.Fatalf("redacted = %v", red.Value)
	}

	// A stream that ends early, or with a hole or an open block, is incomplete.
	for name, acc := range map[string]*accumulator{
		"no message stop": {blocks: map[int32]*streamBlock{}},
		"hole":            {stopped: true, blocks: map[int32]*streamBlock{1: {done: true}}},
		"open block":      {stopped: true, blocks: map[int32]*streamBlock{0: {}}},
	} {
		if _, err := acc.content(); err == nil {
			t.Errorf("%s: content accepted", name)
		}
	}
	if (callbackError{errors.New("gone")}).Error() != "gone" {
		t.Error("callbackError must keep the caller's message")
	}
}
