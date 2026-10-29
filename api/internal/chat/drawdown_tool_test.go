package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"uuid"

	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// set_drawdown_alert checks the trigger as POST /api/alerts does, resolves the
// high (fake NVDA: Yahoo lifetime high $252.50, perp mark $235), shows a
// price_alert card with trigger "drawdown" and a fresh proposalId, warns when
// the price is already past the threshold and stores nothing.
func TestSetDrawdownAlert(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	e.Yahoo.Set(yahootest.Daily("NVDA", "USD", 100, 250, 180))
	e.Yahoo.Set(yahootest.Daily("HSDT", "USD", 27_000_000, 2.66)) // a reverse-split-adjusted high
	a := &agentRun{d: e.Deps, userID: e.User}
	run := func(input string) (string, error) {
		a.summary, a.card = "", nil
		return a.setDrawdownAlert(ctx, json.RawMessage(input))
	}
	for input, want := range map[string]string{
		`{"symbol":"NVDA","drawdown_pct":0,"note":"x"}`:                                 "drawdown_pct must be 0.01–99.99 (% below the all-time high).",
		`{"symbol":"NVDA","drawdown_pct":20,"action":"buy","note":"x"}`:                 "action must be notify or sell.",
		`{"symbol":"NVDA","drawdown_pct":20,"sell_pct":50,"note":"x"}`:                  "sell_pct is for action sell only.",
		`{"symbol":"NVDA","drawdown_pct":20,"action":"sell","sell_pct":150,"note":"x"}`: "sell_pct must be a whole number from 1 to 100",
		`{"symbol":"SPY","drawdown_pct":20,"note":"x"}`:                                 "no live data",
		`{"symbol":"TSLA","drawdown_pct":20,"note":"x"}`:                                "live market data is temporarily unavailable", // no Yahoo history for TSLA here
		`{"symbol":"HSDT","drawdown_pct":40,"note":"x"}`:                                "HSDT has no usable all-time high: Yahoo's split-adjusted lifetime high for HSDT is over 100× the price",
	} {
		if _, err := run(input); err == nil || !strings.Contains(err.Error(), want) || a.card != nil {
			t.Errorf("%.60s: %v, want %q", input, err, want)
		}
	}

	// "Sell half my NVDA if it falls 20% from its all-time high."
	out, err := run(`{"symbol":"nvda","drawdown_pct":20,"action":"sell","sell_pct":50,"note":" Protect gains. "}`)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := a.card.Data.(priceAlert)
	if _, err := uuid.Parse(c.ProposalID); err != nil || a.card.Type != "price_alert" || c.Symbol != "NVDA" || c.Trigger != "drawdown" ||
		c.Op != nil || c.Price != nil || *c.MarkPrice != 235 || *c.DrawdownPct != 20 || *c.SellPct != 50 || c.Action != "sell" ||
		c.Notional != nil || c.Note != "Protect gains." || c.ATH == nil || c.ATH.High != 252.5 || c.ATH.Source != "yahoo" || c.ATH.Ticker != "NVDA" ||
		*c.Threshold != 202 || *c.FromATHPct != -6.93 || *c.DistancePct != -14.04 {
		t.Fatalf("card %+v ath %+v", c, c.ATH)
	}
	if !strings.Contains(out, "NOT set") || !strings.Contains(out, "All-time high $252.50 on ") ||
		!strings.Contains(out, "(Yahoo Finance NVDA lifetime daily highs since ") || !strings.Contains(out, "Threshold $202.00, 20% below it. Now $235.00, -6.93% from the high.") ||
		strings.Contains(out, "already") || !strings.HasSuffix(out, " analysisShown: false.") {
		t.Fatalf("result %q", out)
	}
	if a.summary != "SELL 50% of the NVDA long if NVDA 20% below ATH $252.50" {
		t.Fatalf("summary %q", a.summary)
	}
	// The card's fields are what POST /api/alerts takes.
	if _, _, err := alerts.ValidateDrawdown(ctx, e.Deps, alerts.Input{Symbol: c.Symbol, Trigger: c.Trigger, DrawdownPct: *c.DrawdownPct,
		Action: c.Action, SellPct: c.SellPct, Note: c.Note, Source: "agent", ProposalID: &c.ProposalID}); err != nil {
		t.Fatal(err)
	}

	// Already past the threshold: the card still shows, and the result says it fires on the next check.
	first := c.ProposalID
	out, err = run(`{"symbol":"NVDA","drawdown_pct":5,"note":"x"}`)
	if c := a.card.Data.(priceAlert); err != nil || c.ProposalID == first || c.SellPct != nil || c.Action != "notify" || *c.MarkPrice > *c.Threshold ||
		!strings.Contains(out, "already at or below the threshold, so it fires on the next check") || a.summary != "NVDA 5% below ATH $252.50" {
		t.Fatalf("past card: %v %+v %q %q", err, c, out, a.summary)
	}
	var n int
	if e.DB.QueryRow(ctx, "select count(*) from alerts").Scan(&n); n != 0 {
		t.Fatalf("the tool stored %d alerts", n)
	}
	if got := label("set_drawdown_alert", json.RawMessage(`{"symbol":"nvda","drawdown_pct":20}`)); got != "Drafting drawdown alert: NVDA 20% below its high" {
		t.Fatalf("label %q", got)
	}
}

// "Tell me if SOL falls 30% from its high": the agent answers with a
// price_alert card of trigger "drawdown", stored with the message; Set alert
// (POST /api/alerts) is the user's.
func TestDrawdownCardInChat(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{tool("a", "set_drawdown_alert", `{"symbol":"SOL","drawdown_pct":30,"note":"Deep value zone."}`)}},
		llmtest.Reply{Text: "Set it on the card. **View:** neutral."},
	)})
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Tell me if SOL falls 30% from its high"}`).Body.String())
	cards := cardsOf(evs)
	if len(cards) != 1 || cards[0].Type != "price_alert" || cards[0].Data["trigger"] != "drawdown" || cards[0].Data["drawdownPct"] != 30.0 ||
		cards[0].Data["proposalId"] == "" || cards[0].Data["ath"].(map[string]any)["source"] != "binance" {
		t.Fatalf("cards %+v", cards)
	}
	if c := find(evs, "tool_call"); len(c) != 1 || !strings.Contains(string(c[0].Data), `"label":"Drafting drawdown alert: SOL 30% below its high"`) {
		t.Fatalf("tool call %+v", c)
	}
	var n int
	if e.DB.QueryRow(context.Background(), "select count(*) from alerts").Scan(&n); n != 0 {
		t.Fatal("the agent set the alert")
	}
}
