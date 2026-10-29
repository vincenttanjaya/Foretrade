package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func TestPortfolioToolModelRequestContract(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: "Basket construction opens a build card. **View:** neutral."})})
	e.Do("POST", "/api/chat/stream", `{"message":"Explain how I can build a mixed asset basket"}`)
	reqs := e.Bedrock.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model requests: %d", len(reqs))
	}
	var wire struct {
		ToolConfig struct {
			Tools []struct {
				ToolSpec struct {
					Name, Description string
					InputSchema       struct{ JSON map[string]any } `json:"inputSchema"`
				} `json:"toolSpec"`
			} `json:"tools"`
		} `json:"toolConfig"`
	}
	if err := json.Unmarshal(reqs[0].Body, &wire); err != nil {
		t.Fatal(err)
	}
	for _, tool := range wire.ToolConfig.Tools {
		if tool.ToolSpec.Name != "build_portfolio" {
			continue
		}
		d := tool.ToolSpec.Description
		if strings.Contains(d, "180-day") || !strings.Contains(d, "common") || !strings.Contains(d, "BTC") || !strings.Contains(d, "SP500") || !strings.Contains(d, "persisted") || !strings.Contains(d, "2 credits") {
			t.Errorf("model received stale portfolio promise: %q", d)
		}
		props := tool.ToolSpec.InputSchema.JSON["properties"].(map[string]any)
		prompt := props["prompt"].(map[string]any)
		if prompt["minLength"] != float64(3) || prompt["maxLength"] != float64(500) {
			t.Errorf("brief bounds changed: %v", prompt)
		}
		return
	}
	t.Fatal("build_portfolio absent from actual model request")
}
