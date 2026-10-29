package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// Forecast-driven AI portfolios: the build reads the caller's live forecast
// desk into the prompt as F1..Fn, and each holding comes back with the driver
// the model named: a desk forecast (linked by id), or daily-history stats for an id
// that is not on the desk. The drivers are stored with the basket.
func TestIntegrationPortfolioDrivers(t *testing.T) {
	const deskLine = "F1 · Will SOL close above $130 on 2026-10-17? · Yes 39% (leans No) · bullish probability for SOL: 39%\n"
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, "You build mixed crypto and stock/index perp portfolios") || !strings.Contains(r.System, "Never invent a forecast id.") {
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		}
		if !strings.Contains(r.Prompt, deskLine+"\n<idea>") {
			t.Errorf("desk not in the prompt:\n%s", r.Prompt)
		}
		return llmtest.Reply{Text: `{"name":"Desk-led core","thesis":"Majors, light on SOL.","holdings":[
			{"symbol":"BTC","weight":0.6,"rationale":"The anchor.","driver":"stats"},
			{"symbol":"ETH","weight":0.3,"rationale":"Smart-contract beta.","driver":"F7"},
			{"symbol":"SOL","weight":0.1,"rationale":"Underweight: the desk leans No.","driver":"F1"}]}`}
	}})
	c := s.signIn(t)
	var sol string
	if err := s.db.QueryRow(t.Context(), `insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, price_rule, completed_at)
		values ($1, 'binary', 'Will SOL close above $130 on 2026-10-17?', '["Yes","No"]', (now() at time zone 'utc')::date + 14, 'completed',
			'{"probabilities":[{"outcome":"Yes","p":0.39},{"outcome":"No","p":0.61}]}', '{"symbol":"SOL","op":"above","threshold":130,"mode":"on_date"}', now())
		returning id`, c.id).Scan(&sol); err != nil {
		t.Fatal(err)
	}

	b := c.call("POST", "/api/ai-portfolios/build", map[string]string{"prompt": "Majors, sized by my forecast desk"}, 201)
	shapeAll(t, b, "portfolio.holdings", "symbol weight rationale driver")
	id := str(t, b, "portfolio.id")
	for _, p := range []any{b, c.call("GET", "/api/ai-portfolios/"+id, nil, 200)} {
		drivers := map[string]any{}
		for i, h := range list(t, p, "portfolio.holdings") {
			shape(t, p, "portfolio.holdings."+strconv.Itoa(i)+".driver", "kind id label")
			drivers[str(t, h, "symbol")] = at(t, h, "driver")
		}
		if sd := drivers["SOL"]; str(t, sd, "kind") != "forecast" || str(t, sd, "id") != sol || str(t, sd, "label") != "Forecast · Yes 39% (leans No)" {
			t.Fatalf("SOL driver: %v", sd)
		}
		for _, sym := range []string{"BTC", "ETH"} { // F7 is not on the desk
			if d := drivers[sym]; str(t, d, "kind") != "stats" || !isNull(t, d, "id") || str(t, d, "label") != "Daily history stats" {
				t.Fatalf("%s driver: %v", sym, d)
			}
		}
	}
}
