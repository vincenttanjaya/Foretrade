package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// The real boot job runs against local fakes. Shared decisions use no user
// credits; routes retain owner/session isolation and stop/bell semantics.
func TestIntegrationLiveArena(t *testing.T) {
	s := boot(t, options{live: true, model: func(r llmtest.Request) llmtest.Reply {
		if !strings.Contains(r.System, "live paper-trading desk") {
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		}
		return llmtest.Reply{Text: "Holding cash on the shared paper desk."}
	}})
	c := s.signIn(t)
	var b any
	eventually(t, 45*time.Second, "the first shared live step", func() bool {
		b = c.call("GET", "/api/arena/live", nil, 200)
		// A polling read may begin before the first commit and finish after it.
		return num(t, b, "desk.steps") == 1 && str(t, b, "desk.status") == "live"
	})
	shape(t, b, "desk", "enabled status startedAt lastStepAt nextStepAt cadence startingCash universe steps method lastError stale historyStartAt historyLimited chartWindowBars")
	agents := list(t, b, "agents")
	if len(agents) != 3 || len(list(t, b, "benchmarks")) != 2 || len(list(t, b, "myPlans")) != 0 || str(t, b, "desk.status") != "live" {
		t.Fatalf("live board: %v", b)
	}
	aid := str(t, agents[0], "id")
	if n := s.count(t, "select count(*) from live_attempts where completed_at is not null"); n != 3 {
		t.Fatalf("durable slots: %d", n)
	}
	p := c.call("POST", "/api/arena/live/agents/"+aid+"/copy", map[string]any{"ratio": .1, "maxUsd": 100}, 201)
	pid := str(t, p, "plan.id")
	if len(list(t, p, "plan.positions")) != 0 {
		t.Fatal("copy caught up at activation")
	}
	c.fail("POST", "/api/arena/live/agents/"+aid+"/copy", map[string]any{"ratio": .1, "maxUsd": 100}, 409, "conflict")
	other := s.signIn(t)
	if len(list(t, other.call("GET", "/api/copy-plans", nil, 200), "plans")) != 0 {
		t.Fatal("owner plans leaked")
	}
	other.fail("POST", "/api/copy-plans/"+pid+"/stop", map[string]bool{"closePositions": true}, 404, "not_found")
	steps := c.call("GET", "/api/arena/live/agents/"+aid+"/steps?limit=1", nil, 200)
	if len(list(t, steps, "steps")) != 1 || str(t, steps, "steps.0.reasoning") == "" {
		t.Fatalf("live reasoning: %v", steps)
	}
	stopped := c.call("POST", "/api/copy-plans/"+pid+"/stop", map[string]bool{"closePositions": false}, 200)
	if str(t, stopped, "plan.status") != "stopped" || len(list(t, stopped, "trades")) != 0 {
		t.Fatalf("stop: %v", stopped)
	}
	bell := list(t, c.call("GET", "/api/notifications", nil, 200), "items")
	if len(bell) != 1 || str(t, bell[0], "kind") != "copy_stopped" {
		t.Fatalf("copy stop bell: %v", bell)
	}
	if n := s.count(t, "select coalesce(sum(credits),0) from ai_usage"); n != 0 {
		t.Fatalf("shared desk spent user credits: %d", n)
	}
	if n := len(s.bedrock.Requests()); n != 3 {
		t.Fatalf("same bar retried: %d", n)
	}
}
