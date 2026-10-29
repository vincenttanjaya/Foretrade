package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

// set_protection checks the levels against the live price (fake BTC
// $100,000) and the user's position, shows a protection card with the
// numbers, and never changes the position.
func TestSetProtection(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	a := &agentRun{d: e.Deps, userID: e.User}
	run := func(input string) (string, error) {
		a.summary, a.card = "", nil
		return a.setProtection(ctx, json.RawMessage(input))
	}

	if _, err := run(`{"symbol":"BTC","stop_loss":95000,"rationale":"Under support."}`); err == nil ||
		err.Error() != "No open position in BTC; use propose_trade to open one." || a.card != nil {
		t.Fatalf("no position: %v", err)
	}
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(2000.0), TakeProfit: new(120_000.0)}); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Paper.Portfolio(ctx, e.User)
	for input, want := range map[string]string{
		`{"symbol":"BTC","rationale":"x"}`:                                                  "give stop_loss, take_profit or both",
		`{"symbol":"BTC","stop_loss":95000,"rationale":" "}`:                                "rationale is required",
		`{"symbol":"BTC","stop_loss":101000,"rationale":"x"}`:                               "For a long, the stop must be below the price ($100,000).",
		`{"symbol":"BTC","take_profit":90000,"rationale":"x"}`:                              "For a long, the target must be above the price ($100,000).",
		`{"symbol":"BTC","stop_loss":-1,"rationale":"x"}`:                                   "must be positive prices",
		`{"symbol":"SPY","stop_loss":1,"rationale":"x"}`:                                    "no live data",
		`{"symbol":"BTC","stop_loss":95000,"rationale":"` + strings.Repeat("x", 301) + `"}`: "at most 300 characters",
	} {
		if _, err := run(input); err == nil || !strings.Contains(err.Error(), want) || a.card != nil {
			t.Errorf("%.60s: %v, want %q", input, err, want)
		}
	}

	out, err := run(`{"symbol":"btc","stop_loss":95000,"rationale":"Under the 4h support."}`)
	if err != nil {
		t.Fatal(err)
	}
	pos := before.Positions[0]
	var got struct {
		Status                                                      string
		Card                                                        protection
		AfterConfirm                                                levels
		StopDistancePct, UsdAtRisk, TargetDistancePct, RewardToRisk float64
	}
	json.Unmarshal([]byte(out), &got)
	c := got.Card
	if a.card == nil || a.card.Type != "protection" || c.Symbol != "BTC" || c.Side != "long" || c.Qty != pos.Qty || c.MarkPrice != 100_000 ||
		c.Current.StopLoss != nil || *c.Current.TakeProfit != 120_000 || *c.Proposed.StopLoss != 95_000 || c.Proposed.TakeProfit != nil ||
		c.Rationale != "Under the 4h support." || !strings.Contains(got.Status, "NOT applied") ||
		*got.AfterConfirm.StopLoss != 95_000 || *got.AfterConfirm.TakeProfit != 120_000 { // the kept target is spelled out
		t.Fatalf("card: %s", out)
	}
	// The kept target counts: −5% to the stop, +20% to the target, 4:1.
	if got.StopDistancePct != -5 || got.TargetDistancePct != 20 || got.RewardToRisk != 4 || got.UsdAtRisk != round2(pos.Qty*5000) {
		t.Fatalf("numbers: %s", out)
	}
	if a.summary != "Protect BTC long · stop $95,000 · target $120,000" {
		t.Fatalf("summary %q", a.summary)
	}
	if after, _ := e.Paper.Portfolio(ctx, e.User); after.Revision != before.Revision || after.Positions[0].StopLoss != nil {
		t.Fatalf("the tool changed the position: %+v", after.Positions[0])
	}

	// A short: the reverse sides.
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "SOL", Side: "sell", Notional: new(500.0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(`{"symbol":"SOL","stop_loss":190,"rationale":"x"}`); err == nil || !strings.Contains(err.Error(), "For a short, the stop must be above the price ($200.00).") {
		t.Fatalf("short stop below: %v", err)
	}
	if _, err := run(`{"symbol":"SOL","stop_loss":215,"take_profit":170,"rationale":"x"}`); err != nil || a.card.Data.(protection).Side != "short" {
		t.Fatalf("short: %v", err)
	}
}

// get_derivatives returns the perp context or says there is none.
func TestGetDerivatives(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	e.Market.HyperliquidBase = e.Binance.URL // markettest answers Hyperliquid's /info too
	a := &agentRun{d: e.Deps, userID: e.User}
	ctx := context.Background()

	out, err := a.getDerivatives(ctx, json.RawMessage(`{"symbol":"btc"}`))
	var d struct {
		Symbol, Venue                   string
		FundingRatePct, OpenInterestUsd float64
	}
	json.Unmarshal([]byte(out), &d)
	if err != nil || d.Symbol != "BTC" || d.Venue != "Hyperliquid" || d.OpenInterestUsd != 1.2e9 || a.summary != "BTC funding +0.0013%/h · OI $1.2B" {
		t.Fatalf("BTC: %s %v, summary %q", out, err, a.summary)
	}
	if out, err := a.getDerivatives(ctx, json.RawMessage(`{"symbol":"PAXG"}`)); err != nil || out != "No perp data for PAXG right now." {
		t.Fatalf("PAXG: %q %v", out, err)
	}
	if _, err := a.getDerivatives(ctx, json.RawMessage(`{"symbol":"PEPE"}`)); err == nil {
		t.Fatal("unknown symbol: want an error")
	}
	e.Binance.Down("", true) // Hyperliquid down too: not an error, just no data
	mkt := testMarketClient(e.Binance.URL)
	mkt.HyperliquidBase = e.Binance.URL
	if out, err := (&agentRun{d: &app.Deps{Market: mkt}}).getDerivatives(ctx, json.RawMessage(`{"symbol":"BTC"}`)); err != nil || out != "No perp data for BTC right now." {
		t.Fatalf("upstream down: %q %v", out, err)
	}
	if got := compactUSD(340.5e6) + " " + compactUSD(12_300) + " " + compactUSD(950); got != "$340.5M $12.3K $950.00" {
		t.Fatalf("compactUSD: %s", got)
	}
}

// "Set stops on my BTC": the agent answers with a protection card, stored with
// the message; Confirm (the PATCH) is the user's.
func TestProtectionCardInChat(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{tool("a", "set_protection", `{"symbol":"BTC","stop_loss":96000,"take_profit":110000,"rationale":"Below the swing low."}`)}},
		llmtest.Reply{Text: "Stop under the swing low. **View:** neutral."},
	)})
	ctx := context.Background()
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(1000.0)}); err != nil {
		t.Fatal(err)
	}
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Set stops on my BTC"}`).Body.String())
	cards := cardsOf(evs)
	if len(cards) != 1 || cards[0].Type != "protection" || cards[0].Data["side"] != "long" ||
		cards[0].Data["proposed"].(map[string]any)["stopLoss"] != 96000.0 {
		t.Fatalf("cards %+v", cards)
	}
	if r := find(evs, "tool_result"); len(r) != 1 || !strings.Contains(string(r[0].Data), "Protect BTC long · stop $96,000 · target $110,000") {
		t.Fatalf("tool result %+v", r)
	}
	tools := e.Bedrock.Requests()[0].Tools
	if tools[0] != "set_protection" || tools[1] != "get_derivatives" {
		t.Fatalf("tools %v", tools)
	}
	var c struct{ ChatID string }
	json.Unmarshal(evs[0].Data, &c)
	if body := e.Do("GET", "/api/chats/"+c.ChatID, nil).Body.String(); !strings.Contains(body, `"type":"protection"`) {
		t.Fatalf("stored chat: %s", body)
	}
	if pf, _ := e.Paper.Portfolio(ctx, e.User); pf.Positions[0].StopLoss != nil {
		t.Fatal("the agent changed the stop")
	}
}

// An AI-picked size over 10% of equity is capped and the result says so; the
// user's own size, and a trim of a bigger position, are not.
func TestProposeTradeCapsAISize(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	a := &agentRun{d: e.Deps, userID: e.User, forecasts: map[string]forecastRef{}}
	propose := func(input string) (tradeProposal, map[string]any) {
		t.Helper()
		out, err := a.proposeTrade(ctx, json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		var res map[string]any
		json.Unmarshal([]byte(out), &res)
		return a.card.Data.(tradeProposal), res
	}
	c, res := propose(`{"symbol":"BTC","side":"buy","notional_usd":50000,"stop_loss":95000,"rationale":"Breakout."}`)
	if capped, _ := res["sizeCapped"].(map[string]any); c.Notional != 10000 || capped["from"] != 50000.0 || capped["to"] != 10000.0 ||
		res["usdAtRisk"] != 500.0 || res["analysisShown"] != false {
		t.Fatalf("capped: %+v %v", c, res)
	}
	if c, res := propose(`{"symbol":"BTC","side":"buy","notional_usd":50000,"size_from_user":true,"rationale":"Your size."}`); c.Notional != 50000 || res["sizeCapped"] != nil {
		t.Fatalf("user size: %+v %v", c, res)
	}
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "SOL", Side: "buy", Notional: new(40000.0)}); err != nil {
		t.Fatal(err)
	}
	if c, res := propose(`{"symbol":"SOL","side":"sell","notional_usd":30000,"rationale":"Trim."}`); c.Notional != 30000 || res["sizeCapped"] != nil {
		t.Fatalf("trim: %+v %v", c, res)
	}
}

// A halted pair is left out of the market overview, and propose_trade shows
// no card that opens or adds to a position in it; a trim still gets one.
func TestHaltedPairInChat(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "TON", Side: "buy", Notional: new(1000.0)}); err != nil {
		t.Fatal(err)
	}
	e.Binance.Halt("TONUSDT", true)
	e.Market = testMarketClient(e.Binance.URL) // past the ticker the order cached
	e.Paper = paper.NewEngine(e.DB, e.Market)
	a := &agentRun{d: e.Deps, userID: e.User, forecasts: map[string]forecastRef{}}

	if out, err := a.marketOverview(ctx, nil); err != nil || strings.Contains(out, `"TON"`) || !strings.HasPrefix(a.summary, "106 assets ·") {
		t.Fatalf("overview: %v %s %.200s", err, a.summary, out)
	}
	var halted *paper.HaltedError
	for _, in := range []string{`{"symbol":"TON","side":"buy","notional_usd":100,"rationale":"Dip."}`,
		`{"symbol":"ton","side":"sell","notional_usd":3000,"rationale":"Flip short."}`} {
		a.card = nil
		if _, err := a.proposeTrade(ctx, json.RawMessage(in)); !errors.As(err, &halted) || a.card != nil {
			t.Fatalf("%s: %v %+v", in, err, a.card)
		}
	}
	if _, err := a.proposeTrade(ctx, json.RawMessage(`{"symbol":"TON","side":"sell","notional_usd":500,"rationale":"Trim."}`)); err != nil || a.card == nil {
		t.Fatalf("trim: %v", err)
	}
	// About $4.60 left: the minimum $10 sell would open a bigger short than the
	// position, which the engine refuses, so no card offers it either.
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "TON", Side: "sell", Notional: new(995.0)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "TON", Side: "sell", Notional: new(10.0)}); !errors.As(err, &halted) {
		t.Fatalf("engine dust flip: %v", err)
	}
	a.card = nil
	if _, err := a.proposeTrade(ctx, json.RawMessage(`{"symbol":"TON","side":"sell","notional_usd":10,"rationale":"Close."}`)); !errors.As(err, &halted) || a.card != nil {
		t.Fatalf("dust flip: %v %+v", err, a.card)
	}

	e.Binance.Halt("", true)
	perps := &agentRun{d: &app.Deps{Market: testMarketClient(e.Binance.URL)}}
	if out, err := perps.marketOverview(ctx, nil); err != nil || !strings.HasPrefix(perps.summary, "38 assets ·") || strings.Contains(out, `"TON"`) {
		t.Fatalf("Binance halt must preserve healthy perps: %v %s", err, out)
	}
	e.Binance.Down("xyz", true)
	if _, err := (&agentRun{d: &app.Deps{Market: testMarketClient(e.Binance.URL)}}).marketOverview(ctx, nil); err == nil ||
		!strings.Contains(err.Error(), "No configured instrument has an available live price") {
		t.Fatalf("all halted: %v", err)
	}
}
