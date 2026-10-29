package main

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// A quick order ("buy $500 of SOL") streams its trade card with no model call
// and no credits, and both messages are stored; a ticker outside the universe
// goes to the model (2 credits).
func TestIntegrationChatQuickOrder(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		if r.Stream && r.Prompt == "buy $500 of SPY" {
			return llmtest.Reply{Text: "Paper trading covers 69 crypto and nine stock/index perps today, so this is research only. **View:** neutral."}
		}
		t.Errorf("unexpected model call: %.60q / %q", r.System, r.Prompt)
		return llmtest.Reply{Status: http.StatusBadRequest}
	}})
	c := s.signIn(t)

	ev := c.stream(map[string]any{"message": "buy $500 of SOL"})
	var names []string
	for _, e := range ev {
		names = append(names, e.name)
	}
	if !slices.Equal(names, []string{"chat", "tool_call", "tool_result", "delta", "card", "card", "done"}) {
		t.Fatalf("events %v", names)
	}
	card := at(t, ev[4].data, "data")
	price := num(t, card, "price")
	if str(t, ev[1].data, "name") != "propose_trade" || str(t, ev[1].data, "label") != "Drafting BUY $500 of SOL" ||
		str(t, ev[4].data, "type") != "trade_proposal" || num(t, card, "notional") != 500 || str(t, card, "side") != "buy" ||
		!(num(t, card, "stopLoss") < price && price < num(t, card, "takeProfit")) || str(t, ev[5].data, "type") != "followups" ||
		num(t, ev[6].data, "credits.used") != 0 || num(t, c.call("GET", "/api/me", nil, 200), "credits.used") != 0 {
		t.Fatalf("quick order: %v", ev)
	}
	stored := c.call("GET", "/api/chats/"+str(t, ev[0].data, "chatId"), nil, 200)
	if str(t, stored, "chat.messages.0.content") != "buy $500 of SOL" || str(t, stored, "chat.messages.1.id") != str(t, ev[6].data, "messageId") ||
		!strings.HasPrefix(str(t, stored, "chat.messages.1.content"), "**Paper BUY $500 of SOL** at about $") ||
		str(t, stored, "chat.messages.1.cards.0.type") != "trade_proposal" || str(t, stored, "chat.messages.1.cards.1.type") != "followups" {
		t.Fatalf("stored %v", stored)
	}

	// A selected stock perp also uses the quick path without model credits.
	ev = c.stream(map[string]any{"message": "buy $500 of NVDA"})
	if len(ev) != 7 || str(t, ev[4].data, "type") != "trade_proposal" || str(t, ev[4].data, "data.symbol") != "NVDA" || num(t, ev[6].data, "credits.used") != 0 {
		t.Fatalf("stock quick order: %v", ev)
	}

	ev = c.stream(map[string]any{"message": "buy $500 of SPY"})
	if last := ev[len(ev)-1]; last.name != "done" || num(t, last.data, "credits.used") != 2 {
		t.Fatalf("SPY: %v", ev)
	}
}

// "How likely …?": the model calls get_market_odds, the query writer's terms
// find the Gamma event, and the sources card lists its markets as kind market.
func TestIntegrationChatMarketOdds(t *testing.T) {
	const ask = "How likely is the Fed to cut in December?"
	s := boot(t, options{
		model: func(r llmtest.Request) llmtest.Reply {
			switch {
			case strings.HasPrefix(r.System, queryWriter):
				return llmtest.Reply{Text: `{"queries": ["fed december cut"]}`}
			case r.Stream && r.Prompt == ask:
				return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "o1", Name: "get_market_odds", Input: `{"query":"Fed rate cut in December"}`}}}
			case r.Stream && r.AfterTools: // the odds came back
				return llmtest.Reply{Text: "Polymarket prices a 25 bp December cut at 82% [1]. **View:** a cut is the base case."}
			}
			t.Errorf("unexpected model call: %.60q / %q", r.System, r.Prompt)
			return llmtest.Reply{Status: http.StatusBadRequest}
		},
		gamma: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("q") != "fed december cut" {
				io.WriteString(w, `{"events":[]}`)
				return
			}
			io.WriteString(w, `{"events":[{"slug":"fed-decision-in-december","markets":[
				{"question":"Fed decreases rates by 25 bps after December meeting?","outcomes":"[\"Yes\", \"No\"]","outcomePrices":"[\"0.82\", \"0.18\"]","endDate":"2026-12-10T19:00:00Z","active":true,"volumeNum":4000000},
				{"question":"No change in Fed rates after December meeting?","outcomes":"[\"Yes\", \"No\"]","outcomePrices":"[\"0.16\", \"0.84\"]","endDate":"2026-12-10T19:00:00Z","active":true,"volumeNum":2500000}]}]}`)
		},
	})
	c := s.signIn(t)

	ev := c.stream(map[string]any{"message": ask})
	var sources any
	for _, e := range ev {
		switch {
		case e.name == "tool_result" && (str(t, e.data, "name") != "get_market_odds" || str(t, e.data, "summary") != "Polymarket · 2 markets"):
			t.Fatalf("tool result %v", e.data)
		case e.name == "card" && str(t, e.data, "type") == "sources":
			sources = at(t, e.data, "data")
		}
	}
	if sources == nil || len(list(t, sources, "items")) != 2 || str(t, sources, "items.0.kind") != "market" || str(t, sources, "items.0.source") != "Polymarket" ||
		str(t, sources, "items.0.title") != "Fed decreases rates by 25 bps after December meeting?" ||
		!strings.HasPrefix(str(t, sources, "items.0.url"), "https://polymarket.com/event/fed-decision-in-december#") {
		t.Fatalf("sources %v", sources)
	}
	if last := ev[len(ev)-1]; last.name != "done" || num(t, last.data, "credits.used") != 2 {
		t.Fatalf("last %v", last)
	}
}
