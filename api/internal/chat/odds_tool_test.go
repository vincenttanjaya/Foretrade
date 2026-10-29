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

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// get_market_odds numbers each Polymarket market as a "market" source (one
// [n] per market, even within one event), hands the model crowd odds as
// untrusted data, says when nothing matches, and runs twice per message.
func TestGetMarketOdds(t *testing.T) {
	gamma := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "fed december cut" {
			fmt.Fprint(w, `{"events":[]}`)
			return
		}
		fmt.Fprint(w, `{"events":[{"slug":"fed-decision-in-december","markets":[
			{"question":"Fed cuts 25 bps in December?","outcomes":"[\"Yes\", \"No\"]","outcomePrices":"[\"0.82\", \"0.18\"]","endDate":"2026-12-10T19:00:00Z","active":true,"volumeNum":4000000},
			{"question":"No change in December?","outcomes":"[\"Yes\", \"No\"]","outcomePrices":"[\"0.16\", \"0.84\"]","endDate":"2026-12-10T19:00:00Z","active":true,"volumeNum":2500000}]}]}`)
	}))
	defer gamma.Close()
	const writer = "Write up to 2 short Polymarket search queries"
	e := apptest.New(t, Register, apptest.Options{Reply: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, writer) {
			t.Errorf("unexpected model call %.60q", r.System)
		}
		if strings.Contains(r.Prompt, "aliens") {
			return llmtest.Reply{Text: `{"queries": ["aliens land"]}`}
		}
		return llmtest.Reply{Text: `{"queries": ["fed december cut"]}`}
	}})
	e.PolymarketBase = gamma.URL
	ctx := context.Background()
	a := &agentRun{d: e.Deps, userID: e.User}

	out, err := a.getMarketOdds(ctx, json.RawMessage(`{"query":"Fed  rate cut in December"}`))
	if err != nil || a.summary != "Polymarket · 2 markets" || len(a.src.items) != 2 {
		t.Fatalf("%q %v summary %q sources %+v", out, err, a.summary, a.src.items)
	}
	s := a.src.items[0]
	if s.N != 1 || s.Kind != "market" || s.Source != "Polymarket" || s.Title != "Fed cuts 25 bps in December?" ||
		s.URL != "https://polymarket.com/event/fed-decision-in-december#fed-cuts-25-bps-in-december" || a.src.items[1].N != 2 {
		t.Fatalf("sources %+v", a.src.items)
	}
	untrustedOnce(t, out, `{"endDate":"2026-12-10T19:00:00Z","n":1,"question":"Fed cuts 25 bps in December?","volumeUsd":4000000,"yesPrice":0.82}`)
	if !strings.Contains(out, "Prediction-market prices are crowd odds, not facts; cite them as [n].") {
		t.Fatalf("note: %s", out)
	}

	if out, err := a.getMarketOdds(ctx, json.RawMessage(`{"query":"Will aliens land in 2026?"}`)); err != nil || a.summary != "No matching Polymarket markets" ||
		!strings.Contains(out, "No open Polymarket market matches") {
		t.Fatalf("no match: %q %v %q", out, err, a.summary)
	}
	if _, err := a.getMarketOdds(ctx, json.RawMessage(`{"query":"Fed cut"}`)); err == nil || !strings.Contains(err.Error(), "limit reached (2 per message)") {
		t.Fatalf("third call: %v", err)
	}
	for _, in := range []string{`{"query":"ab"}`, `{"query":"` + strings.Repeat("x", 201) + `"}`, `{"query":1}`} {
		if _, err := (&agentRun{d: e.Deps}).getMarketOdds(ctx, json.RawMessage(in)); err == nil {
			t.Errorf("%.40s: want an error", in)
		}
	}
	e.PolymarketBase = ""
	off := &agentRun{d: e.Deps}
	if _, err := off.getMarketOdds(ctx, json.RawMessage(`{"query":"Fed cut"}`)); err == nil || off.summary != "Prediction markets unavailable" {
		t.Fatalf("off: %v %q", err, off.summary)
	}
	if got := label("get_market_odds", nil); got != "Checking prediction markets" {
		t.Fatalf("label %q", got)
	}
	if p := systemPrompt(time.Now(), ""); !strings.Contains(p, "call get_forecasts and get_market_odds together; quote the market price as crowd odds with its [n]; offer draft_forecast (the draft costs 1 credit)") {
		t.Error("system prompt")
	}
}
