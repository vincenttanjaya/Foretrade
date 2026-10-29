package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func TestIntegrationAccountReads(t *testing.T) {
	for _, withImage := range []bool{false, true} {
		t.Run(fmt.Sprint(withImage), func(t *testing.T) {
			var foreignID string
			calls := 0
			names := []string{"get_trades", "get_pnl_attribution", "get_automations", "get_autopilots", "get_ai_portfolios"}
			s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
				calls++
				if !r.AfterTools {
					for _, name := range names {
						if !slices.Contains(r.Tools, name) {
							t.Errorf("missing actual declaration %s", name)
						}
					}
					if slices.Contains(r.Tools, "get_portfolio_review") || !slices.Contains(r.Tools, "review_portfolio") {
						t.Error("canonical health tool changed")
					}
					if withImage && slices.Contains(r.Tools, "draft_forecast") {
						t.Error("image paid tool declared")
					}
					return llmtest.Reply{Tools: []llmtest.ToolUse{
						{ID: "t", Name: "get_trades", Input: fmt.Sprintf(`{"userId":%q}`, foreignID)},
						{ID: "p", Name: "get_pnl_attribution", Input: `{"range":"1d"}`},
						{ID: "a", Name: "get_automations", Input: `{}`},
						{ID: "r", Name: "get_autopilots", Input: `{}`},
						{ID: "f", Name: "get_ai_portfolios", Input: `{}`},
					}}
				}
				body := string(r.Body)
				for _, needle := range []string{"since_reset", "selected_range", "untrusted_web_content", "owned entry plan"} {
					if !strings.Contains(body, needle) {
						t.Errorf("actual owned result missing %q", needle)
					}
				}
				if strings.Contains(body, "PRIVATE") || strings.Contains(r.Prompt, "unknown tool") {
					t.Error("wrong owner or map execution")
				}
				images := 0
				for _, m := range r.Messages {
					for _, b := range m.Content {
						if _, ok := b["image"]; ok {
							images++
						}
					}
				}
				if (withImage && images != 1) || (!withImage && images != 0) {
					t.Errorf("current image count %d", images)
				}
				return llmtest.Reply{Text: "Your owned entry and fees are available. Holding needs no action. **View:** neutral."}
			}})
			c, other := s.signIn(t), s.signIn(t)
			foreignID = other.id
			c.call("POST", "/api/orders", map[string]any{"symbol": "ETH", "side": "buy", "notional": 1000, "note": "owned entry plan"}, 200)
			other.call("POST", "/api/orders", map[string]any{"symbol": "SOL", "side": "buy", "notional": 1000, "note": "PRIVATE fill"}, 200)
			input := map[string]any{"message": "Review my trades and automations"}
			if withImage {
				var b bytes.Buffer
				if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
					t.Fatal(err)
				}
				input["attachments"] = []any{map[string]any{"kind": "image", "mediaType": "image/png", "name": "chart.png", "data": base64.StdEncoding.EncodeToString(b.Bytes())}}
			}
			evs := c.stream(input)
			results := 0
			for _, e := range evs {
				if e.name == "card" {
					t.Fatal("read emitted action card")
				}
				if e.name == "tool_call" && slices.Contains(names, str(t, e.data, "name")) {
					if str(t, e.data, "label") == str(t, e.data, "name") {
						t.Fatal("raw tool-name label")
					}
				}
				if e.name == "tool_result" && slices.Contains(names, str(t, e.data, "name")) {
					if at(t, e.data, "ok") != true {
						t.Fatalf("failed read: %v", e.data)
					}
					results++
				}
			}
			cost := 2
			if withImage {
				cost = 3
			}
			if calls != 2 || results != 5 || evs[len(evs)-1].name != "done" || num(t, evs[len(evs)-1].data, "credits.used") != float64(cost) {
				t.Fatalf("turns/results/credits: %d %d %v", calls, results, evs)
			}
			if n := s.count(t, "select count(*) from trades where user_id=$1", c.id); n != 1 {
				t.Fatal("reads placed fills")
			}
			if n := s.count(t, "select count(*) from alerts where user_id=$1", c.id); n != 0 {
				t.Fatal("reads created automations")
			}
			if n := s.count(t, "select count(*) from autopilots where user_id=$1", c.id); n != 0 {
				t.Fatal("reads deployed pilots")
			}
		})
	}
}
