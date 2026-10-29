package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

const deskKeys = "forecastId question resolutionDate outcome p lean priceRule stance"

// The forecast desk where the user trades: GET /api/copilot/desk reads the
// user's newest live price forecast on the symbol, and a Copilot plan carries
// it (stance for the plan's side) and tells the model about it.
func TestIntegrationDesk(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, "You are the Copilot") {
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		}
		return llmtest.Reply{Text: `{"direction":"long","entry":200,"stopLoss":190,"takeProfit":230,"confidence":45,"horizon":"1-3 days",
			"rationale":["The desk leans No above $210, so confidence is lower."],"invalidation":"A close below 190.","catalysts":[]}`}
	}})
	c := s.signIn(t)
	if got := c.call("GET", "/api/copilot/desk?symbol=SOL", nil, 200); !isNull(t, got, "desk") {
		t.Fatalf("empty desk: %v", got)
	}
	c.fail("GET", "/api/copilot/desk?symbol=SHIB", nil, 400, "bad_request")

	var id string
	if err := s.db.QueryRow(t.Context(), `insert into forecasts (user_id, shape, question, outcomes, resolution_criteria, resolution_date, category,
		status, result, price_rule, completed_at)
		values ($1, 'binary', 'Will SOL close above $210 next week?', '["Yes","No"]', 'Binance SOLUSDT daily close.', (now() at time zone 'utc')::date + 7,
			'crypto', 'completed', '{"probabilities":[{"outcome":"Yes","p":0.28},{"outcome":"No","p":0.72}]}',
			'{"symbol":"SOL","op":"above","threshold":210,"mode":"on_date"}', now())
		returning id`, c.id).Scan(&id); err != nil {
		t.Fatal(err)
	}
	date := time.Now().UTC().AddDate(0, 0, 7).Format(time.DateOnly)
	got := c.call("GET", "/api/copilot/desk?symbol=sol", nil, 200)
	shape(t, got, "desk", deskKeys)
	shape(t, got, "desk.priceRule", "symbol op threshold mode")
	if str(t, got, "desk.forecastId") != id || str(t, got, "desk.outcome") != "Yes" || num(t, got, "desk.p") != 0.28 || str(t, got, "desk.lean") != "No 72%" ||
		str(t, got, "desk.stance") != "opposes" || str(t, got, "desk.resolutionDate") != date || num(t, got, "desk.priceRule.threshold") != 210 {
		t.Fatalf("desk: %v", got)
	}
	if s.count(t, "select coalesce(sum(credits), 0) from ai_usage where user_id = $1", c.id) != 0 {
		t.Fatal("the desk read costs nothing")
	}
	// Another user never sees it.
	if got := s.signIn(t).call("GET", "/api/copilot/desk?symbol=SOL", nil, 200); !isNull(t, got, "desk") {
		t.Fatalf("another user's desk: %v", got)
	}

	plan := c.call("POST", "/api/copilot/plan", map[string]string{"symbol": "SOL", "style": "day", "bias": "long"}, 200)
	shape(t, plan, "plan.desk", deskKeys)
	if str(t, plan, "plan.desk.forecastId") != id || str(t, plan, "plan.desk.stance") != "opposes" || num(t, plan, "credits.used") != 1 {
		t.Fatalf("plan desk: %v", plan)
	}
	if r := s.bedrock.Requests(); len(r) != 1 || !strings.Contains(r[0].Prompt,
		"Forecast desk (the user's own AI forecasting panel; may be stale): 'Will SOL close above $210 next week?' → No 72%, resolves "+date+".") {
		t.Fatalf("plan prompt: %d requests", len(r))
	}
}
