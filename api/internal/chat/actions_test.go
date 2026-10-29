package chat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func TestStance(t *testing.T) {
	str := func(s *string) string {
		if s == nil {
			return "null"
		}
		return *s
	}
	// P(Yes) 0.40 / 0.46 / 0.48 / 0.50 / 0.60 for each rule op and trade side: within 3 points of 50% is split.
	for _, tc := range []struct {
		op, side string
		want     [5]string
	}{
		{"above", "buy", [5]string{"opposes", "opposes", "split", "split", "supports"}},
		{"above", "sell", [5]string{"supports", "supports", "split", "split", "opposes"}},
		{"below", "buy", [5]string{"supports", "supports", "split", "split", "opposes"}},
		{"below", "sell", [5]string{"opposes", "opposes", "split", "split", "supports"}},
	} {
		for i, p := range []float64{0.40, 0.46, 0.48, 0.50, 0.60} {
			f := forecastRef{Outcome: "Yes", P: p, shape: "binary", rule: &priceRule{Symbol: "BTC", Op: tc.op, Threshold: 1e5, Mode: "on_date"}}
			if got := str(f.stance("BTC", tc.side)); got != tc.want[i] {
				t.Errorf("%s rule, %s at P(Yes) %.2f: %s, want %s", tc.op, tc.side, p, got, tc.want[i])
			}
			if got := f.stance("ETH", tc.side); got != nil {
				t.Errorf("another symbol: %s, want null", *got)
			}
		}
	}
	if got := (forecastRef{Outcome: "Yes", P: 0.9, shape: "binary"}).stance("BTC", "buy"); got != nil {
		t.Errorf("no price rule: %s, want null", *got)
	}
}

const draftReply = `{"shape":"binary","question":"Will the Binance SOL/USDT daily close on 2099-11-28 be above $150?","outcomes":["Yes","No"],
	"resolutionCriteria":"Resolves Yes if the Binance SOLUSDT 1d candle of 2099-11-28 closes above $150.","resolutionDate":"2099-11-28",
	"category":"crypto","priceRule":{"symbol":"SOL","op":"above","threshold":150,"mode":"on_date"}}`

// draft_forecast spends the draft's 1 credit, calls the desk's drafter once
// and shows a forecast_draft card; a second call in the message is refused
// without a model call, and done reports the credits after the draft.
func TestDraftForecast(t *testing.T) {
	draft := tool("a", "draft_forecast", `{"idea":"SOL closes above $150 on Nov 28"}`)
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{draft, tool("b", "draft_forecast", `{"idea":"again"}`), tool("c", "draft_forecast", `{"idea":"x"}`)}},
		llmtest.Reply{Text: draftReply}, // the drafter's own model call
		llmtest.Reply{Text: "Drafted. Tap Run. **View:** neutral."},
	)})
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Forecast whether SOL closes above $150 on Nov 28"}`).Body.String())
	var chips, labels []string
	for _, ev := range find(evs, "tool_result") {
		var r struct {
			OK      bool
			Summary string
		}
		json.Unmarshal(ev.Data, &r)
		chips = append(chips, fmt.Sprint(r.OK, " ", r.Summary))
	}
	for _, ev := range find(evs, "tool_call") {
		var c struct{ Label string }
		json.Unmarshal(ev.Data, &c)
		labels = append(labels, c.Label)
	}
	if !slices.Equal(chips, []string{"true Drafted: Will the Binance SOL/USDT daily close on 2099-11-28 be abov…",
		"false one forecast draft per message; the card is already shown", "false idea must be 3..400 characters"}) || labels[0] != "Drafting a forecast question" {
		t.Errorf("chips %q labels %q", chips, labels)
	}
	cards := cardsOf(evs)
	if len(cards) != 1 || cards[0].Type != "forecast_draft" || cards[0].Data["forecast"] != nil {
		t.Fatalf("cards %+v", cards)
	}
	d := cards[0].Data["draft"].(map[string]any)
	if d["question"] != "Will the Binance SOL/USDT daily close on 2099-11-28 be above $150?" || d["shape"] != "binary" ||
		d["priceRule"].(map[string]any)["symbol"] != "SOL" {
		t.Errorf("draft %v", d)
	}
	var done struct{ Credits struct{ Used int } }
	json.Unmarshal(evs[len(evs)-1].Data, &done)
	if spent(t, e) != 3 || done.Credits.Used != 3 {
		t.Errorf("credits: spent %d, done %d; want 2 + 1", spent(t, e), done.Credits.Used)
	}
	reqs := e.Bedrock.Requests()
	if len(reqs) != 3 || reqs[1].Stream || !strings.Contains(reqs[1].Prompt, "SOL closes above $150 on Nov 28") ||
		!strings.Contains(string(reqs[2].Body), "The desk runs only when the user taps Run (5 credits, usually four to eight minutes)") {
		t.Fatalf("model calls: %d", len(reqs))
	}

	// Out of credits after the message's own charge: refused before the drafter runs.
	poor := apptest.New(t, Register, apptest.Options{Credits: 2, Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{draft}},
		llmtest.Reply{Text: "No credits left. **View:** neutral."},
	)})
	evs = parseSSE(t, poor.Do("POST", "/api/chat/stream", `{"message":"Forecast SOL"}`).Body.String())
	var r struct {
		OK      bool
		Summary string
	}
	json.Unmarshal(find(evs, "tool_result")[0].Data, &r)
	if r.OK || r.Summary != "Out of AI credits for today: the forecast desk needs 1 to draft and 5 to run." || len(cardsOf(evs)) != 0 ||
		spent(t, poor) != 2 || len(poor.Bedrock.Requests()) != 2 {
		t.Fatalf("quota: %+v, cards %d, spent %d", r, len(cardsOf(evs)), spent(t, poor))
	}
}

// Owner-only mode turning on mid-message refuses the draft with its own
// reason, not as spent credits.
func TestDraftForecastOwnerOnly(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{tool("a", "draft_forecast", `{"idea":"SOL closes above $150 on Nov 28"}`)}},
		llmtest.Reply{Text: "Not now. **View:** neutral."},
	)})
	var spends atomic.Int32
	e.Usage.OwnerOnly = func() bool { return spends.Add(1) > 1 } // the message's own charge passes
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Forecast SOL"}`).Body.String())
	var r struct {
		OK      bool
		Summary string
	}
	json.Unmarshal(find(evs, "tool_result")[0].Data, &r)
	if r.OK || r.Summary != "AI is in owner-only mode on this server: only admins can draft forecasts." || len(cardsOf(evs)) != 0 {
		t.Fatalf("owner-only: %+v", r)
	}
}

// build_portfolio and backtest_strategy only show a card: no model call, no
// credits beyond the message's.
func TestBuildActions(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{
			tool("a", "build_portfolio", `{"prompt":"  a defensive crypto basket "}`),
			tool("b", "backtest_strategy", `{"prompt":"Buy SOL when RSI < 30","symbol":"sol","interval":"1h"}`),
			tool("c", "backtest_strategy", `{"prompt":"Buy SOL when RSI < 30","interval":"5m"}`),
			tool("d", "build_portfolio", `{"prompt":"x"}`)}},
		llmtest.Reply{Text: "Tap the cards. **View:** neutral."},
	)})
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"Build me a defensive crypto basket and backtest RSI on SOL"}`).Body.String())
	var chips []string
	for _, ev := range find(evs, "tool_result") {
		var r struct {
			OK      bool
			Summary string
		}
		json.Unmarshal(ev.Data, &r)
		chips = append(chips, fmt.Sprint(r.OK, " ", r.Summary))
	}
	if !slices.Equal(chips, []string{"true Portfolio card ready", "true Strategy card ready", "false interval must be one of 15m, 1h, 4h, 1d",
		"false prompt must be 3..500 characters"}) {
		t.Errorf("chips %q", chips)
	}
	var got []string
	for _, c := range cardsOf(evs) {
		b, _ := json.Marshal(c.Data)
		got = append(got, c.Type+" "+string(b))
	}
	if !slices.Equal(got, []string{
		`build_action {"cost":2,"interval":null,"kind":"portfolio","prompt":"a defensive crypto basket","result":null,"symbol":null}`,
		`build_action {"cost":2,"interval":"1h","kind":"strategy","prompt":"Buy SOL when RSI ` + "\\u003c" + ` 30","result":null,"symbol":"SOL"}`}) {
		t.Errorf("cards\n%s", strings.Join(got, "\n"))
	}
	if spent(t, e) != 2 || len(e.Bedrock.Requests()) != 2 {
		t.Errorf("spent %d, model calls %d", spent(t, e), len(e.Bedrock.Requests()))
	}
	if !strings.Contains(string(e.Bedrock.Requests()[1].Body), "Do not invent holdings or returns.") {
		t.Error("tool result text")
	}
}
