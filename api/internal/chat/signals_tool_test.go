package chat

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/signals"
)

// signalsResult runs get_signals and decodes the board inside its untrusted block.
func signalsResult(t *testing.T, e *apptest.Env) (string, map[string]json.RawMessage, []map[string]any) {
	t.Helper()
	a := &agentRun{d: e.Deps}
	out, err := a.getSignals(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	open := strings.Index(out, "<untrusted_web_content>\n")
	if open < 0 {
		return out, nil, nil
	}
	body := strings.TrimSuffix(out[open+len("<untrusted_web_content>\n"):], "\n</untrusted_web_content>")
	var board map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &board); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var picks []map[string]any
	for _, side := range []string{"bullish", "bearish"} {
		var ps []map[string]any
		json.Unmarshal(board[side], &ps)
		picks = append(picks, ps...)
	}
	return a.summary, board, picks
}

func insertSignalBoard(t *testing.T, e *apptest.Env) {
	t.Helper()
	picked := time.Now().Add(-6 * time.Hour).UTC().Truncate(time.Second)
	f := signals.Factors{Return7dPct: 4.2, Return30dPct: 12.5, Rsi14: 61, Trend: "uptrend", VolAnnPct: 52, DrawdownPct: 3.1}
	btc := markettest.Price("BTCUSDT")
	board, _ := json.Marshal(signals.Board{
		Bullish: []signals.Pick{{Symbol: "BTC", Side: "bullish", Score: 44, PriceAtPick: btc / 1.05, PickedAt: picked, Target: btc * 1.1, Invalidation: btc * 0.9,
			ValidUntil: picked.Add(72 * time.Hour), VolTier: "medium", Factors: f, Rationale: "Momentum and trend agree.",
			Headlines: []signals.Headline{{Title: "Bitcoin ETF inflows top $1bn", URL: "https://www.coindesk.com/btc-etf", Source: "CoinDesk"}}}},
		Bearish: []signals.Pick{{Symbol: "ETH", Side: "bearish", Score: -31, PriceAtPick: markettest.Price("ETHUSDT") * 1.1, PickedAt: picked,
			Target: markettest.Price("ETHUSDT") * 0.9, ValidUntil: picked.Add(72 * time.Hour), VolTier: "high", Factors: f, Rationale: "Below its 50-day."}},
	})
	if _, err := e.DB.Exec(context.Background(), "insert into signal_boards (board, created_at) values ($1, now() - interval '1 hour')", researchtest.Board(t, e.DB, board)); err != nil {
		t.Fatal(err)
	}
}

// get_signals hands the agent the board's own numbers plus the move since each
// pick at the live price (signed in the pick's direction), without headlines,
// and the board's track record with its verdict next to the replay's.
func TestGetSignals(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	a := &agentRun{d: e.Deps}
	if out, err := a.getSignals(context.Background(), nil); err != nil || out != "No valid signals board right now; a refresh is due." || a.summary != "No valid board" {
		t.Fatalf("no board: %q %v", out, err)
	}

	insertSignalBoard(t, e)
	summary, board, picks := signalsResult(t, e)
	if summary != "1 bullish · 1 bearish" || len(picks) != 2 || string(board["method"]) == "" || board["boardAt"] == nil || board["nextAt"] == nil || board["boardAgeHours"] != nil {
		t.Fatalf("summary %q board %v", summary, board)
	}
	for _, tool := range a.tools() {
		if tool.Name == "get_signals" && (!strings.Contains(tool.Description, "a momentum screen") || !strings.Contains(tool.Description, "not a forecast") ||
			!strings.Contains(tool.Description, "Up to 4 bullish (strongest momentum) and 4 bearish (weakest) picks") || !strings.Contains(tool.Description, "verdict")) {
			t.Errorf("description %q", tool.Description)
		}
	}
	keys := "factors invalidation livePrice pickedAt priceAtPick rationale score side sincePickPct symbol target targetDistancePct validUntil volTier"
	for _, p := range picks {
		var got []string
		for k := range p {
			got = append(got, k)
		}
		if len(got) != 14 || !containsAll(keys, got) {
			t.Errorf("pick keys %v", got)
		}
	}
	if picks[0]["invalidation"] != markettest.Price("BTCUSDT")*0.9 || picks[1]["invalidation"] != 0.0 {
		t.Errorf("invalidation %v %v", picks[0]["invalidation"], picks[1]["invalidation"])
	}
	var rec struct {
		Rule, Since  any
		HorizonHours int
		All, Bullish struct {
			Verdict   string
			Scored    int
			ExcessPct *float64
		}
		Replay struct{ ExcessPct, ExcessLo95, ExcessHi95 float64 }
	}
	if err := json.Unmarshal(board["record"], &rec); err != nil || rec.Rule != "steady-v1" || rec.Since != nil || rec.HorizonHours != 72 ||
		rec.All.Verdict != "insufficient" || rec.All.Scored != 0 || rec.All.ExcessPct != nil || rec.Replay.ExcessPct != -0.03 ||
		rec.Replay.ExcessLo95 != -0.97 || rec.Replay.ExcessHi95 != 0.72 {
		t.Errorf("record %s %v", board["record"], err)
	}
	btc, eth := picks[0], picks[1]
	if btc["livePrice"] != markettest.Price("BTCUSDT") || btc["sincePickPct"] != 5.0 || btc["targetDistancePct"] != 10.0 || btc["score"] != 44.0 {
		t.Errorf("bullish pick %v", btc)
	}
	// ETH fell from 1.1× to 1×: a bearish pick that worked reads +9.09%.
	if want := math.Round((1-1/1.1)*10000) / 100; eth["sincePickPct"] != want || eth["targetDistancePct"] != -10.0 {
		t.Errorf("bearish pick %v, want sincePickPct %v", eth, want)
	}

	// A board over 8 h old carries its age for the agent to flag.
	if _, err := e.DB.Exec(context.Background(), "update signal_boards set created_at = now() - interval '9 hours'"); err != nil {
		t.Fatal(err)
	}
	if _, board, _ := signalsResult(t, e); string(board["boardAgeHours"]) != "9" {
		t.Errorf("an old board: boardAgeHours %s", board["boardAgeHours"])
	}

	down := apptest.New(t, Register, apptest.Options{}) // a fresh market cache
	insertSignalBoard(t, down)
	down.Binance.Down("", true)
	if _, _, picks := signalsResult(t, down); picks[0]["livePrice"] != nil || picks[0]["sincePickPct"] != nil || picks[0]["targetDistancePct"] != nil || picks[0]["target"] == nil {
		t.Errorf("market down: %v", picks[0])
	}
	if l := label("get_signals", nil); l != "Reading the signals board" {
		t.Errorf("label %q", l)
	}
	if p := systemPrompt(time.Now(), ""); !strings.Contains(p, "- get_signals for questions about the Signals board") ||
		!strings.Contains(p, "The board is a momentum screen, not a prediction: never present a pick as a forecast or a recommendation. When asked whether to follow it, quote the record's verdict and that the replay found no measurable edge over random picks.") {
		t.Error("system prompt")
	}
}

func containsAll(keys string, got []string) bool {
	for _, k := range got {
		if !strings.Contains(" "+keys+" ", " "+k+" ") {
			return false
		}
	}
	return true
}

func TestGetSignalsSuppressesLegacyDerivedProse(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	insertSignalBoard(t, e)
	if _, err := e.DB.Exec(context.Background(), `update signal_boards set board=board-'_quality'`); err != nil {
		t.Fatal(err)
	}
	a := &agentRun{d: e.Deps}
	for range 2 {
		out, err := a.getSignals(context.Background(), nil)
		if err != nil || out != "No valid signals board right now; a refresh is due." {
			t.Fatalf("legacy tool input: %q %v", out, err)
		}
	}
	if len(e.Bedrock.Requests()) != 0 {
		t.Fatal("tool invoked repair model")
	}
}
