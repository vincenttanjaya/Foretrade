package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Quick orders (RockFlow's "Talk, Tap, Done"): a bare order sentence such as
// "buy $500 of SOL" gets its trade card at once, priced off the live market,
// with no model call and no credits.

type quickOrder struct {
	side, symbol  string  // buy | sell; a universe ticker
	notional, qty float64 // one is set: USD, or units priced at the live price
}

var (
	quickUSD = regexp.MustCompile(`^(buy|sell|short)\s+\$\s?(\d[\d,]*(?:\.\d+)?)\s*(k)?\s+(?:(?:of|worth of|in)\s+)?([a-z][a-z0-9]{1,9})(?:\s+now)?$`)
	quickQty = regexp.MustCompile(`^(buy|sell|short)\s+(\d+(?:\.\d+)?)\s+([a-z][a-z0-9]{1,9})(?:\s+now)?$`)
)

// parseQuickOrder reads "buy $500 of SOL", "sell $1.5k in eth" or "short 2 sol"
// (any case, an optional trailing '.' or '!'); short is sell. ok is false for
// anything else, a ticker outside the universe or a notional outside 10..1,000,000.
// ponytail: regex intent parse; move to a tool-use pre-pass if phrasing variety grows
func parseQuickOrder(text string) (q quickOrder, ok bool) {
	s := strings.TrimSpace(strings.TrimRight(strings.ToLower(strings.TrimSpace(text)), ".!"))
	if m := quickUSD.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseFloat(strings.ReplaceAll(m[2], ",", ""), 64)
		if err != nil {
			return q, false
		}
		if m[3] != "" {
			n *= 1000
		}
		q = quickOrder{side: m[1], symbol: m[4], notional: n}
	} else if m := quickQty.FindStringSubmatch(s); m != nil {
		n, _ := strconv.ParseFloat(m[2], 64)
		q = quickOrder{side: m[1], symbol: m[3], qty: n}
	} else {
		return q, false
	}
	info, known := market.Lookup(q.symbol)
	if !known {
		return q, false
	}
	q.symbol = info.Symbol
	if q.side == "short" {
		q.side = "sell"
	}
	return q, q.qty > 0 || q.notional >= 10 && q.notional <= 1_000_000
}

// quickResult is a drafted quick order: the propose_trade run that built its
// card, the tool input and the answer text.
type quickResult struct {
	run    *agentRun
	input  json.RawMessage
	answer string
}

// quickTrade drafts q's card through propose_trade: for an order that opens,
// adds to or flips a position, a stop 1.5× the 4h ATR away and a 2R target
// on the side of the position it leaves; none for a trim or a close. nil when
// the market, the analysis or the portfolio is unavailable or the card is
// refused: the message then goes to the model.
// ponytail: quick orders skip the forecast desk; the user can ask for analysis
func (h handler) quickTrade(ctx context.Context, user string, q quickOrder) *quickResult {
	price, err := h.d.Market.Price(ctx, q.symbol)
	if err != nil {
		return nil
	}
	an, err := h.d.Market.Analyze(ctx, q.symbol, "4h")
	if err != nil {
		return nil
	}
	pf, err := h.d.Paper.Portfolio(ctx, user)
	if err != nil {
		return nil
	}
	notional := q.notional
	if q.qty > 0 {
		notional = round2(q.qty * price)
	}
	if notional < 10 || notional > 1_000_000 {
		return nil
	}
	var held float64
	for _, p := range pf.Positions {
		if p.Symbol == q.symbol {
			held = p.Qty
		}
	}
	in := map[string]any{"symbol": q.symbol, "side": q.side, "notional_usd": notional, "size_from_user": true,
		"rationale": "Quick order: stop 1.5× the 4h ATR, target 2R. Ask me for analysis before you confirm if you want a second look."}
	next := after(held, q.side, notional, price)
	trim := shrinks(held, next, price)
	if !trim {
		dir := math.Copysign(1, next)
		in["stop_loss"], in["take_profit"] = tick(price-dir*1.5*an.Atr14), tick(price+dir*3*an.Atr14)
	}
	input, _ := json.Marshal(in)
	run := &agentRun{d: h.d, userID: user, forecasts: map[string]forecastRef{}}
	if _, err := run.proposeTrade(ctx, input); err != nil {
		slog.Info("chat: quick order went to the model", "err", err)
		return nil
	}
	c := run.card.Data.(tradeProposal)
	head := fmt.Sprintf("**Paper %s %s of %s** at about %s", strings.ToUpper(c.Side), dollars(c.Notional), c.Symbol, usd(c.Price))
	answer := ""
	switch side := map[bool]string{true: "long", false: "short"}[held > 0]; {
	case trim && math.Abs(next)*price < 10:
		answer = head + ", closing your " + side + ". Nothing executes until you tap Confirm.\n\n" +
			"**View:** Closing takes you flat; ask me for a read on what comes next."
	case trim:
		answer = head + ", reducing your " + side + ". Nothing executes until you tap Confirm.\n\n" +
			"**View:** Trimming locks in part of the move; ask me for a read on what comes next."
	default:
		answer = fmt.Sprintf("%s · stop %s (1.5× 4h ATR) · target %s (2R). Nothing executes until you tap Confirm.\n\n"+
			"**View:** 4h trend is %s, RSI %.0f; a move through %s invalidates the setup.",
			head, usd(*c.StopLoss), usd(*c.TakeProfit), trendWord[an.Trend], an.Rsi14, usd(*c.StopLoss))
	}
	return &quickResult{run: run, input: input, answer: answer}
}

var trendWord = map[string]string{"uptrend": "up", "downtrend": "down", "range": "sideways"}

// streamQuick answers a quick order as a one-tool run without the model:
// chat → tool_call → tool_result → delta → trade card → follow-up chips →
// done. It spends nothing; both messages are stored as on the model path.
func (h handler) streamQuick(ctx context.Context, w http.ResponseWriter, user, chatID, title, text string, q *quickResult) {
	credits, err := h.d.Usage.Get(ctx, user)
	if err == nil {
		if chatID == "" {
			chatID, err = h.create(ctx, user, title, text, nil)
		} else {
			_, err = h.add(ctx, user, chatID, "user", text, []Card{}, nil)
		}
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	sse, err := httpx.NewSSE(w)
	if err != nil {
		slog.Error("chat stream", "err", err)
		return
	}
	inflight.Store(chatID, struct{}{})
	defer inflight.Delete(chatID)
	c := q.run.card.Data.(tradeProposal)
	cards := []Card{*q.run.card, {Type: "followups", Data: map[string]any{"prompts": []string{
		"Analyze " + c.Symbol + " before I confirm", "What would invalidate this?", "Size this for 1% account risk"}}}}
	s := &stream{sse: sse}
	s.send("chat", map[string]string{"chatId": chatID, "title": title})
	s.send("tool_call", map[string]any{"id": "quick", "name": "propose_trade", "input": q.input,
		"label": fmt.Sprintf("Drafting %s %s of %s", strings.ToUpper(c.Side), dollars(c.Notional), c.Symbol)})
	s.send("tool_result", map[string]any{"id": "quick", "name": "propose_trade", "ok": true, "summary": q.run.summary})
	s.send("delta", map[string]string{"text": q.answer})
	for _, card := range cards {
		s.send("card", card)
	}
	saveCtx, cancel := app.Cleanup(ctx)
	defer cancel()
	msgID, err := h.add(saveCtx, user, chatID, "assistant", q.answer, cards, nil)
	inflight.Delete(chatID)
	if err != nil {
		slog.Error("chat: save quick order", "chat", chatID, "err", err)
		s.send("error", map[string]string{"code": "internal", "message": "The answer could not be saved."})
		return
	}
	s.send("done", map[string]any{"messageId": msgID, "credits": credits})
}

// tick rounds a level the way usd prints it: 4 significant digits under $1,
// cents under $1,000, whole dollars above.
func tick(x float64) float64 {
	switch {
	case x < 1:
		v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'g', 4, 64), 64)
		return v
	case x < 1000:
		return round2(x)
	}
	return math.Round(x)
}

// dollars is an order size without zero cents: $500 · $1,500 · $250.50.
func dollars(x float64) string { return strings.TrimSuffix(usd(x), ".00") }
