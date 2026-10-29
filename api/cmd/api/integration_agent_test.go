package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// The agent loop: a streamed trade card carries a proposalId; Confirm fills
// it once (200, then 409 conflict); the stored chat reads the card filled with
// its trade, and open again after a reset. suggest_followups ends the answer:
// its chips arrive as a card and later text is dropped.
func TestIntegrationAgent(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		body := string(r.Body)
		switch {
		case r.Prompt == "Buy BTC?":
			return llmtest.Reply{Text: "Trend is up.", Tools: []llmtest.ToolUse{{ID: "t1", Name: "propose_trade",
				Input: `{"symbol":"BTC","side":"buy","notional_usd":1500,"stop_loss":95000,"rationale":"Trend intact."}`}}}
		case strings.Contains(body, "Shown to the user. Write nothing after this."):
			return llmtest.Reply{Text: "Trailing text the user never sees."}
		case r.AfterTools: // the trade card's result came back
			return llmtest.Reply{Text: "**View:** bullish above $95k.", Tools: []llmtest.ToolUse{{ID: "t2", Name: "suggest_followups",
				Input: `{"prompts":["Size this for 1% account risk","What would invalidate this?","Compare with ETH"]}`}}}
		}
		t.Errorf("unexpected chat turn %q", r.Prompt)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)

	ev := c.stream(map[string]any{"message": "Buy BTC?"})
	var names []string
	var answer strings.Builder
	cards := map[string]any{}
	for _, e := range ev {
		if e.name != "delta" || names[len(names)-1] != "delta" {
			names = append(names, e.name)
		}
		switch e.name {
		case "delta":
			answer.WriteString(str(t, e.data, "text"))
		case "card":
			cards[str(t, e.data, "type")] = at(t, e.data, "data")
		}
	}
	if want := []string{"chat", "delta", "tool_call", "tool_result", "card", "delta", "tool_call", "tool_result", "card", "done"}; !slices.Equal(names, want) {
		t.Fatalf("events %v, want %v", names, want)
	}
	if answer.String() != "Trend is up.\n\n**View:** bullish above $95k." {
		t.Fatalf("answer %q", answer.String())
	}
	trade, chips := cards["trade_proposal"], cards["followups"]
	shape(t, trade, "", "proposalId symbol side notional price stopLoss takeProfit rationale forecast status trade")
	shape(t, chips, "", "prompts")
	pid := str(t, trade, "proposalId")
	if len(pid) != 36 || str(t, trade, "status") != "open" || !isNull(t, trade, "trade") || !isNull(t, trade, "forecast") ||
		len(list(t, chips, "prompts")) != 3 {
		t.Fatalf("cards: %v", cards)
	}
	chatID := str(t, ev[0].data, "chatId")

	order := map[string]any{"symbol": "BTC", "side": "buy", "notional": 1500, "stopLoss": 95000, "source": "agent", "note": "Trend intact.", "proposalId": pid}
	fill := c.call("POST", "/api/orders", order, 200)
	shape(t, fill, "trade", tradeKeys)
	c.fail("POST", "/api/orders", order, 409, "conflict")
	order["proposalId"] = "not-a-uuid"
	c.fail("POST", "/api/orders", order, 400, "bad_request")
	if n := s.count(t, "select count(*) from trades where user_id = $1 and proposal_id = $2", c.id, pid); n != 1 {
		t.Fatalf("trades for the proposal: %d", n)
	}

	got := c.call("GET", "/api/chats/"+chatID, nil, 200)
	stored := "chat.messages.1."
	card := at(t, got, stored+"cards.0.data")
	shape(t, card, "trade", tradeKeys)
	if str(t, got, stored+"content") != answer.String() || str(t, got, stored+"cards.0.type") != "trade_proposal" || str(t, card, "status") != "filled" ||
		str(t, card, "trade.id") != str(t, fill, "trade.id") || str(t, got, stored+"cards.1.type") != "followups" {
		t.Fatalf("stored chat: %v", got)
	}

	c.call("POST", "/api/portfolio/reset", map[string]any{}, 200)
	got = c.call("GET", "/api/chats/"+chatID, nil, 200)
	if str(t, got, stored+"cards.0.data.status") != "open" || !isNull(t, got, stored+"cards.0.data.trade") {
		t.Fatalf("after a reset: %v", got)
	}
}
