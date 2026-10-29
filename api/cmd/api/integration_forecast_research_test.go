package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// Round 4 research provenance: with Brave off, a SOL price-rule forecast runs
// on the stored SOL headlines (known outlets first, junk hidden), says so in
// result.research and the knowledge note, and the panel's prompt calls the
// pack a stored wire, not a live search.
func TestIntegrationForecastResearchFallback(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "You are the aggregator"):
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.4},{"outcome":"No","p":0.6}],"summary":"We lean No.","confidence":"medium",
				"evidence":[{"claim":"Validators upgraded","sourceId":"S1","source":"CoinDesk","stance":"supports","asOf":"2026-10-02"}]}`}
		case strings.HasPrefix(r.System, "You are the"):
			if !strings.Contains(r.Prompt, "Stored news wire (headlines the app collected in the last 72 h; not a live search, 2 sources") {
				t.Errorf("%.40s did not see the stored wire: %.300s", r.System, r.Prompt)
			}
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.4},{"outcome":"No","p":0.6}],"reasoning":"r","keyFactors":["k"],"confidence":"medium"}`}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	if _, err := s.db.Exec(t.Context(), `insert into news_items (url, title, source, description, published_at, symbols) values
		('https://blog.example/sol-dex', 'Solana DEX volumes hit a record', 'Blog', 'Traders rotate into SOL', now() - interval '1 hour', '{SOL}'),
		('https://www.coindesk.com/sol-firedancer', 'Solana validators ship Firedancer', 'CoinDesk', 'Client diversity improves', now() - interval '5 hours', '{SOL}'),
		('https://www.techbullion.com/remittix', 'Best Crypto to Buy in October 2026: Remittix, Solana', 'TechBullion', '', now(), '{SOL}'),
		('https://blog.example/btc', 'Bitcoin miners sell', 'Blog', '', now() - interval '2 hours', '{BTC}')`); err != nil {
		t.Fatal(err)
	}
	c := s.signIn(t)
	id := str(t, c.call("POST", "/api/forecasts", map[string]any{"shape": "binary", "question": "Will the Binance SOL/USDT daily close on 2099-12-31 be above $200?",
		"outcomes": []string{"Yes", "No"}, "resolutionDate": "2099-12-31", "category": "crypto",
		"priceRule": map[string]any{"symbol": "SOL", "op": "above", "threshold": 200, "mode": "on_date"}}, 201), "forecast.id")
	var f any
	eventually(t, 30*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	r := "forecast.result."
	shape(t, f, r+"research", "queries status note")
	sources := list(t, f, r+"sources")
	if str(t, f, "forecast.status") != "completed" || str(t, f, r+"research.status") != "wire" ||
		str(t, f, r+"research.note") != "Live search found no usable sources for this run; the panel used the stored news wire (last 72 h)." ||
		len(list(t, f, r+"research.queries")) != 0 || len(sources) != 2 ||
		str(t, sources[0], "url") != "https://www.coindesk.com/sol-firedancer" || at(t, sources[0], "read") != false ||
		str(t, f, r+"evidence.0.url") != "https://www.coindesk.com/sol-firedancer" ||
		!strings.HasPrefix(str(t, f, r+"knowledgeNote"), "Stored news wire (2 headlines) + live Binance spot data + model knowledge") {
		t.Fatalf("wire fallback: status %v research %v sources %v evidence %v note %v", at(t, f, "forecast.status"), at(t, f, r+"research"),
			sources, at(t, f, r+"evidence"), at(t, f, r+"knowledgeNote"))
	}
}
