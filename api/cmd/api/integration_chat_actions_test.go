package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// The chat drafts a forecast (1 credit on top of the message's 2) and stores
// its forecast_draft card; Run posts that draft (5 more), and the stored chat
// then links the card to the run, through to its completed headline.
func TestIntegrationChatActions(t *testing.T) {
	const idea = "Forecast whether BTC tops $120k by 2099"
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "You are the question editor"):
			return llmtest.Reply{Text: draftReply}
		case r.Stream && r.Prompt == idea:
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "d1", Name: "draft_forecast", Input: `{"idea":"BTC above $120k by 2099"}`}}}
		case r.Stream && r.AfterTools: // the draft came back
			return llmtest.Reply{Text: "Drafted: tap Run to send it to the desk. **View:** neutral."}
		case strings.HasPrefix(r.System, queryWriter): // the Polymarket query writer: no queries, so the question's keywords are searched
			return noMarketQueries
		// The run the user starts from the card.
		case strings.HasPrefix(r.System, "You are the research assistant"):
			return llmtest.Reply{Text: `{"queries":["bitcoin ETF flows"]}`}
		case strings.HasPrefix(r.System, "You are the Base-rate statistician"), strings.HasPrefix(r.System, "You are the Narrative & macro skeptic"),
			strings.HasPrefix(r.System, "You are the Market & quant analyst"):
			return llmtest.Reply{Text: opinion(0.3)}
		case strings.HasPrefix(r.System, "You are the aggregator"):
			return llmtest.Reply{Text: aggregatorReply}
		}
		t.Errorf("unexpected model call: %.60q / %q", r.System, r.Prompt)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)

	ev := c.stream(map[string]any{"message": idea})
	var card any
	for _, e := range ev {
		if e.name == "card" && str(t, e.data, "type") == "forecast_draft" {
			card = at(t, e.data, "data")
		}
	}
	last := ev[len(ev)-1]
	if card == nil || last.name != "done" || num(t, last.data, "credits.used") != 3 || !isNull(t, card, "forecast") {
		t.Fatalf("stream: card %v, last %v", card, last)
	}
	shape(t, card, "", "draft forecast")
	shape(t, card, "draft", draftKeys)
	chatID := str(t, ev[0].data, "chatId")
	stored := c.call("GET", "/api/chats/"+chatID, nil, 200)
	if str(t, stored, "chat.messages.1.cards.0.type") != "forecast_draft" || !isNull(t, stored, "chat.messages.1.cards.0.data.forecast") ||
		str(t, stored, "chat.messages.1.cards.0.data.draft.question") != str(t, card, "draft.question") {
		t.Fatalf("stored card: %v", stored)
	}

	created := c.call("POST", "/api/forecasts", at(t, card, "draft"), 201)
	id := str(t, created, "forecast.id")
	if num(t, created, "credits.used") != 8 {
		t.Fatalf("credits after the run: %v", at(t, created, "credits"))
	}
	linked := "chat.messages.1.cards.0.data.forecast."
	eventually(t, 30*time.Second, "the run from the card", func() bool {
		stored = c.call("GET", "/api/chats/"+chatID, nil, 200)
		return str(t, stored, linked+"id") == id && str(t, stored, linked+"status") == "completed"
	})
	shape(t, stored, linked[:len(linked)-1], "id status headline")
	if str(t, stored, linked+"headline.outcome") != "Yes" || num(t, stored, linked+"headline.p") != 0.27 {
		t.Fatalf("linked run: %v", at(t, stored, linked[:len(linked)-1]))
	}
}
