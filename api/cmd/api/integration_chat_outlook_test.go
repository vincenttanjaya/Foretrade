package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

const (
	chatOutlookKeys = "symbol spot horizonDays volAnnPct buckets p5 p50 p95 volBasis longHorizon horizon history view scenarios timeToTarget warnings"
	outlookViewKeys = "kind label muAnnPct median p5 p25 p75 p95 pAbove pAboveByWeights changePct annualPct buckets multiples listingSpot volAnnPct volAssumed"
)

// TestIntegrationChatOutlookTSM: "can TSMC 10x, if yes by when" end to end.
// The fake model reads the TSM quote, finds consensus EPS on the web, then
// passes three EPS × P/E scenarios with target_multiple 10. The card leads
// with the scenario view (median above today, 2× likelier than 0.5×) and
// answers by when; σ comes from the listing's Yahoo closes, not the perp's
// venue bars; a P/E the cited source does not state is labelled an
// assumption; the zero-drift baseline stays as the secondary reference.
func TestIntegrationChatOutlookTSM(t *testing.T) {
	const ask = "can TSMC 10x, if yes by when"
	const query = "TSMC consensus EPS $16.56 FY2026 $21.33 FY2027" // the fake Brave snippet repeats the query
	// The years follow the wall clock (the server validates eps_year against
	// today), five years of growth as in §6.2, so the levels never drift.
	years := fmt.Sprintf(`"eps_year":%d,"exit_year":%d`, time.Now().UTC().Year()+1, time.Now().UTC().Year()+6)
	scenarios := `[
		{"name":"AI capex digestion","kind":"bear","p":0.3,"eps":21.33,` + years + `,"eps_basis":"[1]","growth_pct":3,"growth_basis":"assumption","pe":14,"pe_basis":"assumption"},
		{"name":"Compounding foundry leader","kind":"base","p":0.5,"eps":21.33,` + years + `,"eps_basis":"[1]","growth_pct":12,"growth_basis":"assumption","pe":20,"pe_basis":"[1]"},
		{"name":"AI supercycle","kind":"bull","p":0.2,"eps":21.33,` + years + `,"eps_basis":"[1]","growth_pct":20,"growth_basis":"model knowledge, as of 2025-06","pe":25,"pe_basis":"assumption"}]`
	const tenYears, hold, compare = "NVDA 10 years outlook", "is TSM a good long-term hold?", "Compare TSM 5 years vs 10 years"
	var rounds atomic.Int32
	s := boot(t, options{brave: true, model: func(r llmtest.Request) llmtest.Reply {
		q := firstUserText(r)
		switch {
		case !r.Stream || !strings.HasPrefix(r.System, "You are the trading desk analyst"):
		// The owner's phrasing, with a model that passes 5 years and no reason: the question's 10 years win.
		case q == tenYears && !r.AfterTools:
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "o2", Name: "price_outlook", Input: `{"symbol":"NVDA","horizon_days":1825}`}}}
		// No timeframe and no reason: accepted, labelled the agent's choice.
		case q == hold && !r.AfterTools:
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "o3", Name: "price_outlook", Input: `{"symbol":"TSM","horizon_days":1095}`}}}
		// Two timeframes in one question: one card each.
		case q == compare && !r.AfterTools:
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "o4", Name: "price_outlook", Input: `{"symbol":"TSM","horizon_days":1826}`},
				{ID: "o5", Name: "price_outlook", Input: `{"symbol":"TSM","horizon_days":3653}`}}}
		case (q == tenYears || q == hold || q == compare) && r.AfterTools:
			return llmtest.Reply{Text: "Here is the outlook card."}
		case r.Prompt == ask:
			return llmtest.Reply{Text: "Checking TSM's listing and consensus.", Tools: []llmtest.ToolUse{
				{ID: "q1", Name: "get_stock_quote", Input: `{"symbol":"TSM"}`},
				{ID: "w1", Name: "web_search", Input: `{"query":"` + query + `"}`}}}
		case r.AfterTools && rounds.Add(1) == 1:
			return llmtest.Reply{Tools: []llmtest.ToolUse{{ID: "o1", Name: "price_outlook",
				Input: `{"symbol":"TSM","horizon_days":1825,"target_multiple":10,"scenarios":` + scenarios + `}`}}}
		case r.AfterTools:
			return llmtest.Reply{Text: "Yes, it's possible, but only if the AI-supercycle case plays out [1]. **View:** bullish, a 10x is a decade-plus call."}
		}
		t.Errorf("unexpected model call: %.60q / %q", r.System, r.Prompt)
		return llmtest.Reply{Status: http.StatusBadRequest}
	}})
	s.yahoo.Set(yahootest.Daily("TSM", "USD", listingCloses()...))
	c := s.signIn(t)

	var card any
	var outlookSummary string
	for _, e := range c.stream(map[string]any{"message": ask}) {
		switch {
		case e.name == "tool_result" && at(t, e.data, "ok") != true:
			t.Fatalf("tool failed: %v", e.data)
		case e.name == "tool_result" && str(t, e.data, "name") == "price_outlook":
			outlookSummary = str(t, e.data, "summary")
		case e.name == "card" && str(t, e.data, "type") == "price_outlook":
			card = at(t, e.data, "data")
		}
	}
	if card == nil || !strings.HasPrefix(outlookSummary, "TSM in 5y: median $") || !strings.Contains(outlookSummary, "(view)") {
		t.Fatalf("no outlook card (summary %q)", outlookSummary)
	}
	shape(t, card, "", chatOutlookKeys)
	shape(t, card, "view", outlookViewKeys)
	shape(t, card, "horizon", "days basis reason")
	shape(t, card, "history", "source from to years volAnnPct cagrPct maxDrawdownPct maxDrawdownPeak maxDrawdownTrough recovered driftHalvedPct")
	shape(t, card, "timeToTarget", "multiple target rows medianYears medianExtrapolated byScenario requiredCagr ownCagrPct ownSince")
	shapeAll(t, card, "timeToTarget.rows", "years pEnd pTouch baselinePEnd baselinePTouch extrapolated")
	shapeAll(t, card, "view.buckets", "label lo hi p pBaseline")
	shapeAll(t, card, "view.multiples", "x pEnd pTouch baselinePEnd baselinePTouch")
	shapeAll(t, card, "scenarios", "name kind p level changePct annualPct inputs")
	shape(t, card, "scenarios.1.inputs.0", "field value year basis kind verified") // eps: eps_year
	shape(t, card, "scenarios.1.inputs.1", "field value year basis kind verified") // growth: to exit_year
	shape(t, card, "scenarios.1.inputs.2", "field value basis kind verified")      // pe: no year

	// The view carries the direction; the baseline mirrors.
	spot := num(t, card, "spot")
	if spot != 485 || num(t, card, "p50") != spot || !(num(t, card, "view.median") > spot) || !(num(t, card, "view.pAbove") > 0.5) ||
		str(t, card, "view.kind") != "scenarios" || len(list(t, card, "view.buckets")) != 7 || len(list(t, card, "buckets")) != 7 {
		t.Fatalf("view %v", at(t, card, "view"))
	}
	if half, two := num(t, card, "view.multiples.0.pEnd"), num(t, card, "view.multiples.1.pEnd"); !(two > half) ||
		math.Abs(num(t, card, "view.multiples.0.baselinePEnd")-num(t, card, "view.multiples.1.baselinePEnd")) > 1e-12 {
		t.Errorf("multiples %v", at(t, card, "view.multiples"))
	}
	// Horizon from the question; by when answered; σ from decades of listing closes.
	if str(t, card, "horizon.basis") != "by_when" || num(t, card, "horizon.days") != 1825 || num(t, card, "timeToTarget.multiple") != 10 ||
		num(t, card, "timeToTarget.target") != 4850 || len(list(t, card, "timeToTarget.requiredCagr")) != 5 ||
		math.Abs(num(t, card, "timeToTarget.requiredCagr.2.pct")-25.89) > 0.01 || str(t, card, "timeToTarget.ownSince") != str(t, card, "history.from") {
		t.Errorf("time to target %v", at(t, card, "timeToTarget"))
	}
	if str(t, card, "volBasis.source") != "Yahoo Finance daily closes (NYSE: TSM)" || str(t, card, "volBasis.window") != "long-run" ||
		num(t, card, "volBasis.days") < 7000 || num(t, card, "history.years") < 28 || s.yahoo.Calls("TSM") == 0 {
		t.Errorf("history %v / %v", at(t, card, "volBasis"), at(t, card, "history"))
	}
	// Sourced inputs are checked against the cited text.
	if at(t, card, "scenarios.1.inputs.0.verified") != true || str(t, card, "scenarios.1.inputs.0.basis") != "[1]" ||
		str(t, card, "scenarios.1.inputs.2.basis") != "assumption" || at(t, card, "scenarios.1.inputs.2.verified") != false ||
		str(t, card, "scenarios.2.inputs.1.kind") != "model_knowledge" || math.Abs(num(t, card, "scenarios.1.level")-751.8) > 0.1 {
		t.Errorf("scenarios %v", at(t, card, "scenarios"))
	}
	warned := false
	for _, w := range list(t, card, "warnings") {
		warned = warned || w == "P/E 20× is not stated in source [1]: shown as an assumption"
	}
	b, _ := json.Marshal(card)
	if !warned || strings.Contains(string(b), "Bear ·") || strings.Contains(string(b), "Bull ·") {
		t.Errorf("warnings %v", at(t, card, "warnings"))
	}

	// The prompt the model saw carries the horizon rule.
	reqs := s.bedrock.Requests()
	if len(reqs) == 0 || !strings.Contains(reqs[len(reqs)-1].System, "The horizon follows the question, never a default") {
		t.Error("system prompt lacks the horizon rule")
	}
	// The stored message keeps the card.
	chats := c.call("GET", "/api/chats", nil, 200)
	got := c.call("GET", "/api/chats/"+str(t, chats, "chats.0.id"), nil, 200)
	if str(t, got, "chat.messages.1.cards.0.type") != "price_outlook" || str(t, got, "chat.messages.1.cards.0.data.view.kind") != "scenarios" {
		t.Errorf("stored chat %v", got)
	}

	// "NVDA 10 years outlook": 10 years from the question, not the model's 5; without scenarios
	// the drift headline is capped at the S&P 500's long-run return + 2 points.
	s.yahoo.Set(yahootest.Daily("NVDA", "USD", gbm(3, 27, 0.30, 0.5, 239.24)...))
	s.yahoo.Set(yahootest.Daily("^GSPC", "USD", gbm(4, 60, 0.063, 0.15, 6700)...))
	nv := outlookCard(t, c, tenYears)
	if num(t, nv, "horizon.days") != 3653 || str(t, nv, "horizon.basis") != "explicit" || str(t, nv, "horizon.reason") != `From your question: "10 years outlook"` ||
		str(t, nv, "view.kind") != "historical_drift" || at(t, nv, "view.driftCap.applied") != true || str(t, nv, "view.driftCap.benchmark") != "SPX" ||
		!strings.Contains(str(t, nv, "view.label"), "capped at the market's long-run return + 2 pts") ||
		!(num(t, nv, "history.driftHalvedPct") > num(t, nv, "view.muAnnPct")) || num(t, nv, "view.driftCap.capPct") > 10 || len(list(t, nv, "view.buckets")) > 8 {
		t.Errorf("NVDA 10 years: %v / %v", at(t, nv, "horizon"), at(t, nv, "view"))
	}
	shape(t, nv, "view.driftCap", "benchmark source since benchmarkCagrPct capPct uncappedPct applied")
	// No timeframe, no reason: no round trip, the agent's choice on the card.
	hc := outlookCard(t, c, hold)
	if str(t, hc, "horizon.basis") != "agent" || str(t, hc, "horizon.reason") != "Horizon chosen by the agent: 1,095 days (3 years)" {
		t.Errorf("no reason: %v", at(t, hc, "horizon"))
	}
	// "5 years vs 10 years": both timeframes are the user's, each its own card, no override.
	var cards []any
	for _, e := range c.stream(map[string]any{"message": compare}) {
		switch {
		case e.name == "tool_result" && at(t, e.data, "ok") != true:
			t.Fatalf("%q: tool failed: %v", compare, e.data)
		case e.name == "card" && str(t, e.data, "type") == "price_outlook":
			cards = append(cards, at(t, e.data, "data"))
		}
	}
	if len(cards) != 2 || num(t, cards[0], "horizon.days") != 1826 || num(t, cards[1], "horizon.days") != 3653 ||
		str(t, cards[0], "horizon.reason") != `From your question: "5 years"` || str(t, cards[1], "horizon.reason") != `From your question: "10 years"` ||
		overridden(t, cards[0]) || overridden(t, cards[1]) || num(t, cards[0], "view.median") == num(t, cards[1], "view.median") {
		t.Errorf("compare: %d cards %v", len(cards), cards)
	}
}

// overridden reports whether a card says the question's timeframe overrode horizon_days.
func overridden(t *testing.T, card any) bool {
	w, _ := at(t, card, "warnings").([]any)
	for _, x := range w {
		if s, _ := x.(string); strings.HasPrefix(s, "Horizon set from the question") {
			return true
		}
	}
	return false
}

// outlookCard asks ask in a new chat and returns its price_outlook card; the tool must succeed.
func outlookCard(t *testing.T, c *client, ask string) any {
	t.Helper()
	var card any
	for _, e := range c.stream(map[string]any{"message": ask}) {
		switch {
		case e.name == "tool_result" && at(t, e.data, "ok") != true:
			t.Fatalf("%q: tool failed: %v", ask, e.data)
		case e.name == "card" && str(t, e.data, "type") == "price_outlook":
			card = at(t, e.data, "data")
		}
	}
	if card == nil {
		t.Fatalf("%q: no outlook card", ask)
	}
	return card
}

// firstUserText is the conversation's first user text block.
func firstUserText(r llmtest.Request) string {
	for _, m := range r.Messages {
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if txt, ok := b["text"].(string); ok {
				return txt
			}
		}
	}
	return ""
}

// gbm is n years of weekday closes from a seeded GBM ending at last: synthetic, never a real price.
func gbm(seed uint64, years, mu, sigma, last float64) []float64 {
	r := rand.New(rand.NewPCG(seed, 7))
	n := int(years * 261)
	logs := make([]float64, n)
	for i := 1; i < n; i++ {
		logs[i] = logs[i-1] + mu/252 + sigma/math.Sqrt(252)*r.NormFloat64()
	}
	closes := make([]float64, n)
	for i, x := range logs {
		closes[i] = math.Round(last*math.Exp(x-logs[n-1])*100) / 100
	}
	return closes
}

// listingCloses is 29 years of weekday closes from a seeded GBM (σ 38%, 10%/yr
// log drift) ending at $487.10: a synthetic NYSE TSM, never a real price.
func listingCloses() []float64 {
	r := rand.New(rand.NewPCG(1, 2))
	n := 29 * 261
	logs := make([]float64, n)
	for i := 1; i < n; i++ {
		logs[i] = logs[i-1] + 0.10/252 + 0.38/math.Sqrt(252)*r.NormFloat64()
	}
	closes := make([]float64, n)
	for i, x := range logs {
		closes[i] = math.Round(487.10*math.Exp(x-logs[n-1])*100) / 100
	}
	return closes
}
