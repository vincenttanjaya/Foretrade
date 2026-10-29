package chat

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

func TestParseQuickOrder(t *testing.T) {
	for in, want := range map[string]quickOrder{
		"buy $500 of SOL":               {side: "buy", symbol: "SOL", notional: 500},
		"  Buy $1.5k in eth. ":          {side: "buy", symbol: "ETH", notional: 1500},
		"short 2 sol":                   {side: "sell", symbol: "SOL", qty: 2},
		"SELL $2,000 worth of BTC now!": {side: "sell", symbol: "BTC", notional: 2000},
		"buy $ 250.50 doge":             {side: "buy", symbol: "DOGE", notional: 250.5},
		"sell 0.25 btc":                 {side: "sell", symbol: "BTC", qty: 0.25},
	} {
		if got, ok := parseQuickOrder(in); !ok || got != want {
			t.Errorf("%q: %+v %v, want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"buy nvda", "buy $5 sol", "buy SOL if it dips", "buy $500 of SPY", "buy $2000k of sol", "buy 0 sol",
		"should I buy $500 of SOL?", "buy $500 of SOL and ETH", "buy $500 of", "hold $500 of sol"} {
		if got, ok := parseQuickOrder(in); ok {
			t.Errorf("%q parsed as %+v", in, got)
		}
	}
}

// Levels: an order that opens a long or a short gets a stop 1.5× the 4h ATR
// away and a 2R target on its side; one that trims or closes a position gets
// neither and says so.
func TestQuickTradeLevels(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	h := handler{e.Deps}
	an, err := e.Market.Analyze(ctx, "SOL", "4h")
	if err != nil || an.Atr14 <= 0 {
		t.Fatalf("analysis %+v %v", an, err)
	}
	card := func(q quickOrder) (tradeProposal, string) {
		t.Helper()
		r := h.quickTrade(ctx, e.User, q)
		if r == nil {
			t.Fatalf("%+v: no card", q)
		}
		return r.run.card.Data.(tradeProposal), r.answer
	}

	long, answer := card(quickOrder{side: "buy", symbol: "SOL", notional: 500})
	if long.Notional != 500 || long.Price != 200 || *long.StopLoss != tick(200-1.5*an.Atr14) || *long.TakeProfit != tick(200+3*an.Atr14) ||
		!strings.HasPrefix(long.Rationale, "Quick order: stop 1.5× the 4h ATR, target 2R.") {
		t.Fatalf("long %+v", long)
	}
	if want := "**Paper BUY $500 of SOL** at about $200.00 · stop " + usd(*long.StopLoss) + " (1.5× 4h ATR) · target " + usd(*long.TakeProfit) +
		" (2R). Nothing executes until you tap Confirm.\n\n**View:** 4h trend is " + trendWord[an.Trend] + ", RSI "; !strings.HasPrefix(answer, want) ||
		!strings.HasSuffix(answer, "; a move through "+usd(*long.StopLoss)+" invalidates the setup.") {
		t.Fatalf("answer %q", answer)
	}
	short, _ := card(quickOrder{side: "sell", symbol: "SOL", qty: 2})
	if short.Notional != 400 || *short.StopLoss <= 200 || *short.TakeProfit >= 200 || 200-*short.TakeProfit < 1.99*(*short.StopLoss-200) {
		t.Fatalf("short %+v", short)
	}

	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "SOL", Side: "buy", Notional: new(2000.0)}); err != nil {
		t.Fatal(err)
	}
	trim, answer := card(quickOrder{side: "sell", symbol: "SOL", notional: 500})
	if trim.StopLoss != nil || trim.TakeProfit != nil || answer != "**Paper SELL $500 of SOL** at about $200.00, reducing your long. "+
		"Nothing executes until you tap Confirm.\n\n**View:** Trimming locks in part of the move; ask me for a read on what comes next." {
		t.Fatalf("trim %+v %q", trim, answer)
	}
	if closing, answer := card(quickOrder{side: "sell", symbol: "SOL", notional: 2000}); closing.StopLoss != nil || !strings.Contains(answer, "closing your long") {
		t.Fatalf("close %+v %q", closing, answer)
	}
	add, _ := card(quickOrder{side: "buy", symbol: "SOL", notional: 500}) // adding to the long: levels again
	if add.StopLoss == nil || *add.StopLoss >= 200 {
		t.Fatalf("add %+v", add)
	}

	// The market down: no card, so the message goes to the model.
	e.Binance.Down("", true)
	if r := (handler{e.Deps}).quickTrade(ctx, e.User, quickOrder{side: "buy", symbol: "BTC", notional: 500}); r != nil {
		t.Fatalf("market down: %+v", r)
	}
	if got := tick(0.123456) + tick(123.456) + tick(12345.6); got != 0.1235+123.46+12346 {
		t.Errorf("tick %v", got)
	}
}

// "buy $500 of SOL" streams a trade card with no model call and no credits,
// and both messages are stored; an asset outside the universe, or a chat
// opened from a forecast, goes to the model.
func TestStreamQuickOrder(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Text: "SPY is outside the configured paper instruments, so this is research only. **View:** neutral."},
		llmtest.Reply{Text: "**View:** analyze first."})})
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"buy $500 of SOL"}`).Body.String())
	if n := names(evs); !slices.Equal(n, []string{"chat", "tool_call", "tool_result", "delta", "card", "card", "done"}) {
		t.Fatalf("events %v", n)
	}
	var call struct{ ID, Name, Label string }
	var result struct {
		Name, Summary string
		OK            bool
	}
	json.Unmarshal(evs[1].Data, &call)
	json.Unmarshal(evs[2].Data, &result)
	cards := cardsOf(evs)
	var done struct {
		MessageID string
		Credits   struct{ Used, Limit int }
	}
	json.Unmarshal(evs[6].Data, &done)
	if call.Name != "propose_trade" || call.Label != "Drafting BUY $500 of SOL" || !result.OK || result.Summary != "BUY $500.00 of SOL at $200.00" ||
		cards[0].Type != "trade_proposal" || cards[0].Data["notional"] != 500.0 || cards[0].Data["status"] != "open" ||
		cards[1].Type != "followups" || done.Credits.Used != 0 || done.Credits.Limit != 500 || !isUUID(done.MessageID) || spent(t, e) != 0 {
		t.Fatalf("call %+v result %+v cards %+v done %+v", call, result, cards, done)
	}
	if prompts := cards[1].Data["prompts"].([]any); len(prompts) != 3 || prompts[0] != "Analyze SOL before I confirm" {
		t.Fatalf("follow-ups %v", prompts)
	}
	var c struct{ ChatID string }
	json.Unmarshal(evs[0].Data, &c)
	m := apptest.JSON[storedChat](t, e.Do("GET", "/api/chats/"+c.ChatID, nil)).Chat.Messages
	if len(m) != 2 || m[0].Content != "buy $500 of SOL" || !strings.HasPrefix(m[1].Content, "**Paper BUY $500 of SOL** at about $200.00 · stop $") ||
		len(m[1].Cards) != 2 || m[1].Cards[0].Type != "trade_proposal" || len(e.Bedrock.Requests()) != 0 {
		t.Fatalf("stored %+v, %d model calls", m, len(e.Bedrock.Requests()))
	}

	if n := names(parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"buy $500 of SPY"}`).Body.String())); !slices.Equal(n, []string{"chat", "delta", "done"}) ||
		len(e.Bedrock.Requests()) != 1 || spent(t, e) != 2 {
		t.Fatalf("SPY: %v, %d model calls", n, len(e.Bedrock.Requests()))
	}
	id := addForecast(t, e, e.User, "completed", "Will SOL close above $250 on Dec 31?")
	evs = parseSSE(t, e.Do("POST", "/api/chat/stream", map[string]any{"message": "buy $500 of SOL", "context": map[string]string{"forecastId": id}}).Body.String())
	if len(e.Bedrock.Requests()) != 2 || slices.Contains(names(evs), "tool_call") {
		t.Fatalf("from a forecast: %v", names(evs))
	}
}
