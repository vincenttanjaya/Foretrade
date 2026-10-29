package llm

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// TestLiveBedrock makes three real Sonnet calls: one CompleteJSON, and an
// Agent run with MaxTurns 1 so the tool turn is followed by the forced final
// turn (tools still declared, model told to stop). Run with LIVE_BEDROCK=1 and
// the standard AWS credentials; AWS_REGION picks the region (default us-east-1).
func TestLiveBedrock(t *testing.T) {
	if os.Getenv("LIVE_BEDROCK") != "1" {
		t.Skip("LIVE_BEDROCK=1 not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	c := New(cfg, "global.anthropic.claude-sonnet-5-5")

	var out struct {
		Answer int `json:"answer"`
	}
	if err := c.CompleteJSON(ctx, "You reply with JSON only.", `What is 6*7? Reply as {"answer": <number>}.`, Low, &out); err != nil || out.Answer != 42 {
		t.Fatalf("CompleteJSON: %v %+v", err, out)
	}

	var events []string
	add := Tool{
		Name: "add", Description: "Adds two integers and returns the sum.",
		Schema: map[string]any{"type": "object", "required": []string{"a", "b"},
			"properties": map[string]any{"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "integer"}}},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct{ A, B int }
			if err := json.Unmarshal(input, &in); err != nil {
				return "", err
			}
			return strconv.Itoa(in.A + in.B), nil
		},
	}
	text, err := c.Agent(ctx, AgentRequest{
		System:   "Use the add tool for arithmetic. Never compute sums yourself.",
		Messages: []Message{{Role: "user", Text: "What is 1234 + 4321? Use the tool, then reply with just the number."}},
		Tools:    []Tool{add}, Effort: Medium, MaxTurns: 1,
	}, func(e Event) error {
		events = append(events, e.Kind)
		return nil
	})
	if err != nil {
		t.Fatalf("Agent: %v", err)
	}
	t.Logf("agent answer %q, events %v", text, compact(events))
	if !strings.Contains(text, "5555") || !slices.Contains(events, "tool_call") || !slices.Contains(events, "tool_result") {
		t.Fatalf("answer %q, events %v", text, compact(events))
	}
}

// compact collapses runs of delta events for the log.
func compact(xs []string) []string {
	var out []string
	for _, x := range xs {
		if len(out) == 0 || out[len(out)-1] != x {
			out = append(out, x)
		}
	}
	return out
}
