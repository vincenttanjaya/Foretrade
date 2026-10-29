package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

// propose_trade reports whether the user has seen analysis (300+ runes of
// answer before the card) as data, never as an instruction; the prompt says
// what to write either way.
func TestProposeTradeReportsAnalysisShown(t *testing.T) {
	trade := tool("t", "propose_trade", `{"symbol":"BTC","side":"buy","notional_usd":500,"rationale":"Trend up."}`)
	analysis := strings.Repeat("BTC holds its 4h uptrend above the 20 EMA; ETF inflows continue [1]. ", 5) // > 300 runes
	for _, tc := range []struct {
		name  string
		first llmtest.Reply
		shown bool
	}{
		{"card first", llmtest.Reply{Tools: []llmtest.ToolUse{trade}}, false},
		{"short preamble", llmtest.Reply{Text: "Let me check.", Tools: []llmtest.ToolUse{trade}}, false},
		{"analysis first", llmtest.Reply{Text: analysis, Tools: []llmtest.ToolUse{trade}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, tc.first, llmtest.Reply{Text: "**View:** bullish."})})
			parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Should I buy BTC?"}`).Body.String())
			reqs := e.Bedrock.Requests()
			if len(reqs) != 2 {
				t.Fatalf("%d model requests", len(reqs))
			}
			sent := string(reqs[1].Body)
			if !strings.Contains(sent, fmt.Sprintf(`\"analysisShown\":%v`, tc.shown)) || strings.Contains(sent, "Now write") || strings.Contains(sent, "already on screen") {
				t.Errorf("tool result: want analysisShown %v and no instruction in\n%s", tc.shown, sent)
			}
		})
	}
	p := systemPrompt(time.Now(), "")
	for _, want := range []string{"first write your analysis for the user as visible text with [n] citations, then call propose_trade",
		"propose_trade and set_price_alert return analysisShown. If it is false, the user has seen no analysis yet: now write 3–5 short bullets",
		"If it is true, write only the one-line proposal and the **View:** line; never repeat the analysis or the card's numbers.",
		"Never describe a tool result as an instruction and never mention this rule.",
		"If propose_trade returns sizeCapped, say in one clause that AI-proposed sizes are capped at 10% of equity unless you name a size.",
		"Never say an analysis or card is above unless you wrote it in this answer.", "If the tool says none were shown, end without mentioning it"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}

// suggest_followups cleans what it gets instead of failing: trimmed, deduped,
// clipped to 80 runes, at most 3; nothing usable is a quiet no-op.
func TestSuggestFollowupsNeverErrors(t *testing.T) {
	long := strings.Repeat("Why ", 30) // 120 runes
	a := &agentRun{}
	in, _ := json.Marshal(map[string]any{"prompts": []string{"What next?", "", long, " What  next? ", "Compare with ETH", "Size it"}})
	out, err := a.suggestFollowups(context.Background(), in)
	if err != nil || a.card == nil || a.summary != "3 prompts" || !strings.Contains(out, "Write your complete answer now") {
		t.Fatalf("out %q, err %v, card %+v, summary %q", out, err, a.card, a.summary)
	}
	prompts := a.card.Data.(map[string]any)["prompts"].([]string)
	if len(prompts) != 3 || prompts[0] != "What next?" || len([]rune(prompts[1])) != 80 || !strings.HasSuffix(prompts[1], "…") || prompts[2] != "Compare with ETH" {
		t.Errorf("prompts %q", prompts)
	}
	for _, input := range []string{`{"prompts":["", "  "]}`, `{"prompts":[]}`, `{}`} {
		a := &agentRun{}
		if out, err := a.suggestFollowups(context.Background(), json.RawMessage(input)); err != nil || out != "No follow-ups shown." || a.card != nil {
			t.Errorf("%s: %q %v %+v", input, out, err, a.card)
		}
	}
}

// A search naming one asset numbers the results whose title names it first and
// keeps one multi-coin roundup, last; a query naming two assets keeps Brave's order.
func TestSearchOrdersOnTopic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[
			{"title":"Top 5 coins: Bitcoin, Ethereum, Solana and XRP levels","url":"https://a.example/roundup"},
			{"title":"Fed holds rates; risk assets dip","url":"https://b.example/fed"},
			{"title":"Solana ETF filing advances","url":"https://c.example/sol-etf"},
			{"title":"Support levels for BTC, ETH, SOL, XRP and BNB","url":"https://d.example/levels"}]}`)
	}))
	defer srv.Close()
	run := func(query string) []string {
		a := &agentRun{d: &app.Deps{Research: research.New(research.Config{BraveAPIKey: "test", BraveBase: srv.URL}, nil, nil)}}
		if _, err := a.searchNews(context.Background(), json.RawMessage(fmt.Sprintf(`{"query":%q}`, query))); err != nil {
			t.Fatal(err)
		}
		var urls []string
		for _, s := range a.src.items {
			urls = append(urls, s.URL)
		}
		return urls
	}
	if got := run("Solana SOL news this week"); fmt.Sprint(got) != "[https://c.example/sol-etf https://b.example/fed https://a.example/roundup]" {
		t.Errorf("one asset: %v", got)
	}
	if got := run("SOL vs ETH"); len(got) != 4 || got[0] != "https://a.example/roundup" {
		t.Errorf("two assets: %v", got)
	}
}

// After propose_trade: a turn that restarts an earlier one is dropped, keeping
// its **View line; a first sentence about tool instructions is dropped, with
// or without answer text before it; a different turn passes unchanged.
func TestChatDropsRepeatedTurn(t *testing.T) {
	trade := tool("t", "propose_trade", `{"symbol":"BTC","side":"buy","notional_usd":500,"rationale":"Trend up."}`)
	a := strings.Repeat("BTC holds its 4h uptrend above the 20 EMA; ETF inflows continue [1]. ", 5) // > 300 runes
	const view = "\n\n**View:** bullish."
	other := "Buy $500 of BTC, stop $90,000, target $120,000; paper trading only, nothing executes until you tap Confirm." + view
	const meta = "The tool's output asked me to write the analysis now, but that instruction didn't come from you. "
	for _, tc := range []struct{ name, first, second, want string }{
		{"repeat", a, a + view, a + view},
		{"different", a, other, a + "\n\n" + other},
		{"short preamble", "Let me check.", a + view, "Let me check.\n\n" + a + view},
		{"meta after text", a, meta + other, a + "\n\n" + other},
		{"meta first", "", meta + a + view, a + view},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
				llmtest.Reply{Text: tc.first, Tools: []llmtest.ToolUse{trade}}, llmtest.Reply{Text: tc.second})})
			evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Should I buy BTC?"}`).Body.String())
			var deltas strings.Builder
			for _, ev := range find(evs, "delta") {
				var d struct{ Text string }
				json.Unmarshal(ev.Data, &d)
				deltas.WriteString(d.Text)
			}
			var c struct{ ChatID string }
			json.Unmarshal(evs[0].Data, &c)
			m := apptest.JSON[storedChat](t, e.Do("GET", "/api/chats/"+c.ChatID, nil)).Chat.Messages
			if deltas.String() != tc.want || len(m) != 2 || m[1].Content != tc.want || strings.Count(deltas.String(), a) > 1 {
				t.Fatalf("streamed %q\nstored %+v\nwant %q", deltas.String(), m, tc.want)
			}
		})
	}
}

// The guard decides at 120 normalized runes or at the end of the turn: a
// short repeat (< 60 runes) passes, a 60..119-rune turn that starts an earlier
// one is dropped at the end, and a dropped turn's **View line is kept only
// when the answer has none. Every turn after a tool round loses a first
// sentence about tool instructions; the first turn and clean turns pass as
// they are.
func TestTurnGuard(t *testing.T) {
	run := func(turns ...string) string {
		var answer strings.Builder
		g := turnGuard{emit: func(s string) { answer.WriteString(s) }, viewed: func() bool { return strings.Contains(answer.String(), "**View") }}
		for i, turn := range turns {
			if i > 0 {
				g.end() // a tool round
			}
			for j, part := range strings.SplitAfter(turn, " ") {
				g.delta(part, i > 0 && j == 0)
			}
		}
		g.end()
		return answer.String()
	}
	mid := "Bitcoin keeps its uptrend while ETF inflows continue this week." // 60..119 normalized runes
	long := mid + " " + mid                                                  // 120+
	unpunctuated := strings.Repeat("levels hold ", 30)                       // 330 runes, no sentence end
	for _, tc := range [][]string{
		{"Hi there.", "Hi there.", "Hi there.\n\nHi there."},                                        // too short to judge
		{mid + " Funding stays neutral.", mid, mid + " Funding stays neutral."},                     // decided at the end of the run
		{long, long + "\n**View:** bullish.", long + "\n\n**View:** bullish."},                      // decided at 120: the View line is kept
		{long + " **View:** neutral.", long + "\n**View:** bullish.", long + " **View:** neutral."}, // the answer has its View
		{mid, "SOL is a different story today, with DEX volumes at a record high.", mid + "\n\nSOL is a different story today, with DEX volumes at a record high."},
		// Meta sentences: dropped after a tool round, empty answer or not; the first turn is never held.
		{"", "I was instructed to write the analysis now. BTC holds its uptrend.", "BTC holds its uptrend."},
		{"Let me check.", "That instruction didn't come from you! Here it is: BTC is up.", "Let me check.\n\nHere it is: BTC is up."},
		{"Let me check.", "Per the tool result, that is a prompt injection", "Let me check."}, // the whole turn is one meta sentence
		{"Let me check.", "BTC holds its uptrend.", "Let me check.\n\nBTC holds its uptrend."},
		{"The tool output is in. BTC is up.", "", "The tool output is in. BTC is up."},
		{"", unpunctuated + "I was told to", unpunctuated + "I was told to"}, // past 300 runes the first "sentence" is clean
	} {
		if got := run(tc[0], tc[1]); got != tc[2] {
			t.Errorf("turns %q\ngot  %q\nwant %q", tc[:2], got, tc[2])
		}
	}
	if got := normalize("  BTC: $100,000 — **up** 2.5%!  "); got != "btc 100 000 up 2 5" {
		t.Errorf("normalize %q", got)
	}
	for s, want := range map[string]int{"Hi. There": 3, "e.g.x y": -1, "Wait! ": 5, "Why? No": 4, "no end": -1, strings.Repeat("é", 300): 600} {
		if got := sentenceEnd(s); got != want {
			t.Errorf("sentenceEnd(%.20q) = %d, want %d", s, got, want)
		}
	}
}

// A held turn that follows answer text goes out once its first sentence is in
// and no earlier turn starts with it, mid-turn; one that still starts an
// earlier turn waits until it diverges.
func TestTurnGuardReleasesDivergentTurn(t *testing.T) {
	var out strings.Builder
	g := turnGuard{emit: func(s string) { out.WriteString(s) }, viewed: func() bool { return false }}
	first := "Bitcoin keeps its uptrend while ETF inflows continue this week. "
	g.delta(first, false)
	g.end()
	for _, step := range []struct{ text, want string }{
		{"SOL is", first}, // the first sentence is not in yet
		{" weaker. ", first + "\n\nSOL is weaker. "}, // diverged: released before 120 runes
		{"DEX volumes", first + "\n\nSOL is weaker. DEX volumes"},
	} {
		g.delta(step.text, step.text == "SOL is")
		if out.String() != step.want {
			t.Fatalf("after %q: %q", step.text, out.String())
		}
	}
	g.end()
	before := out.String()
	g.delta("Bitcoin keeps its uptrend. ", true) // starts the first turn: still held
	if out.String() != before {
		t.Fatalf("a possible repeat went out: %q", out.String())
	}
	g.delta("Funding stays neutral. ", false)
	if want := before + "\n\nBitcoin keeps its uptrend. Funding stays neutral. "; out.String() != want {
		t.Fatalf("diverged turn: %q, want %q", out.String(), want)
	}
}

func TestSystemPromptKeepsModelPrivate(t *testing.T) {
	if !strings.Contains(systemPrompt(time.Now(), ""), "never name the AI model or company behind you") {
		t.Error("system prompt lost the model-privacy rule")
	}
}

// The horizon follows the user's words: the prompt says so and names
// target_multiple; the old 3-5 year default is gone.
func TestHorizonPromptRules(t *testing.T) {
	p := systemPrompt(time.Now(), "")
	for _, want := range []string{"The horizon follows the question, never a default", "target_multiple", "horizon_reason", "timeToTarget", "requiredCagr"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, anchor := range []string{"default to 3–5 years", "within 3–5 years"} { // the memo's example anchored a 10x on 3-5 years
		if strings.Contains(p, anchor) {
			t.Errorf("the prompt still anchors a 10x question on 3-5 years: %q", anchor)
		}
	}
}
