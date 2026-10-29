package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/chat"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"strings"
	"testing"
	"time"
)

func TestIntegrationResearch(t *testing.T) {
	entered := make(chan llmtest.Request, 1)
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		entered <- r
		return llmtest.Reply{Text: "Current portfolio risks remain stable."}
	}})
	c := s.signIn(t)
	foreign := s.signIn(t)
	created := c.call("POST", "/api/alerts", map[string]any{"action": "research", "trigger": "schedule", "every": "weekdays", "at": "08:00", "prompt": "Review my portfolio risks", "notifyWhen": "changed"}, 201)
	shape(t, created, "alert", alertKeys)
	id := str(t, created, "alert.id")
	c.call("GET", "/api/alerts/"+id+"/runs", nil, 200)
	foreign.call("GET", "/api/alerts/"+id+"/runs", nil, 404)
	foreign.call("POST", "/api/alerts/"+id+"/resume", map[string]any{}, 404)
	c.call("GET", "/api/alerts/"+id+"/runs?limit=51", nil, 400)
	if _, err := s.db.Exec(t.Context(), `update alerts set next_run_at=clock_timestamp()-interval '1 second' where id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var req llmtest.Request
	select {
	case req = <-entered:
	case <-time.After(19 * time.Second):
		t.Fatal("booted API did not dispatch due Research through the actual scheduler")
	}
	if req.Effort != "low" || len(req.Tools) != 15 {
		t.Fatalf("headless wire effort/tools %s/%v", req.Effort, req.Tools)
	}
	for _, name := range req.Tools {
		if name == "remember" || name == "get_market_odds" || name == "draft_forecast" {
			t.Fatalf("forbidden tool %s", name)
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(req.Body, &raw); err != nil {
		t.Fatal(err)
	}
	if config, ok := raw["inferenceConfig"].(map[string]any); !ok || config["maxTokens"] != float64(4096) {
		t.Fatalf("token bound %v", raw["inferenceConfig"])
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var status string
		err := s.db.QueryRow(t.Context(), `select status from alert_runs where alert_id=$1 order by ran_at desc,id desc limit 1`, id).Scan(&status)
		if err == nil && status == "ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Research did not publish status=%q err=%v", status, err)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("test cancelled")
		case <-time.After(10 * time.Millisecond):
		}
	}
	history := c.call("GET", "/api/alerts/"+id+"/runs", nil, 200)
	shape(t, history, "", "runs nextCursor")
	if len(list(t, history, "runs")) != 1 {
		t.Fatalf("history %v", history)
	}
	shape(t, history, "runs.0", "id at status body error reportChatId excerpt verdict")
	chatID := str(t, history, "runs.0.reportChatId")
	chat := c.call("GET", "/api/chats/"+chatID, nil, 200)
	if !strings.Contains(fmt.Sprint(chat), "Current portfolio risks remain stable.") {
		t.Fatalf("private report missing %v", chat)
	}
	foreign.call("GET", "/api/chats/"+chatID, nil, 404)
	bell := c.call("GET", "/api/notifications", nil, 200)
	if len(list(t, bell, "items")) != 1 || str(t, bell, "items.0.kind") != "workflow_research" || str(t, bell, "items.0.link") != "/chat/"+chatID {
		t.Fatalf("owned Research Bell %v", bell)
	}
	if n := s.count(t, `select credits from ai_usage where user_id=$1 and day=current_date`, c.id); n != 2 {
		t.Fatalf("Research cost %d", n)
	}
	c.call("DELETE", "/api/chats/"+chatID, nil, 204)
	history = c.call("GET", "/api/alerts/"+id+"/runs", nil, 200)
	if !isNull(t, history, "runs.0.reportChatId") {
		t.Fatalf("deleted report link %v", history)
	}
	c.call("DELETE", "/api/alerts/"+id, nil, 204)
	var slots int
	if err := s.db.QueryRow(context.Background(), `select count(*) from research_worker_slots where run_id is not null`).Scan(&slots); err != nil || slots != 0 {
		t.Fatalf("completed Research retained lease %d %v", slots, err)
	}
}

func TestResearchActualRunnerMarkerAcknowledgementAccounting(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("durable=%t", committed), func(t *testing.T) {
			e := apptest.New(t, alerts.Register, apptest.Options{})
			store := alerts.NewResearchStore(e.Deps)
			a, err := alerts.Create(t.Context(), e.Deps, e.User, alerts.Input{Action: "research", Trigger: "schedule", Every: "day", At: "08:00", Prompt: "Review portfolio risks"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.DB.Exec(t.Context(), `update alerts set next_run_at=clock_timestamp()-interval '1 second' where id=$1`, a.ID); err != nil {
				t.Fatal(err)
			}
			due, err := store.Due(t.Context())
			if err != nil || len(due) != 1 {
				t.Fatalf("due %v %v", due, err)
			}
			lease, err := store.Admit(t.Context())
			if err != nil || lease == nil {
				t.Fatalf("lease %v %v", lease, err)
			}
			claim, err := store.Claim(t.Context(), *lease, due[0])
			if err != nil || claim == nil {
				t.Fatalf("claim %v %v", claim, err)
			}
			ackError := errors.New("marker acknowledgement lost")
			claim.Request.BeforeModel = func(ctx context.Context) error {
				if committed {
					if err := store.MarkAttempt(ctx, *lease); err != nil {
						return err
					}
				}
				return ackError
			}
			claim.Request.BeforeTool = func(ctx context.Context) error { return store.Authorize(ctx, *lease) }
			result, runErr := chat.NewResearchRunner(e.Deps).Run(t.Context(), claim.Request)
			if !errors.Is(runErr, ackError) || result.Produced || len(e.Bedrock.Requests()) != 0 {
				t.Fatalf("marker error dispatched SDK: %+v/%v requests%d", result, runErr, len(e.Bedrock.Requests()))
			}
			if err = store.Finalize(t.Context(), *lease, result, runErr); err != nil {
				t.Fatal(err)
			}
			if err = store.Finalize(t.Context(), *lease, result, runErr); err != nil {
				t.Fatal(err)
			}
			var credits int
			var attempted, refunded bool
			if err = e.DB.QueryRow(t.Context(), `select u.credits,r.attempted,r.refunded from ai_usage u join alert_runs r on r.user_id=u.user_id and r.charged_day=u.day where r.id=$1`, lease.RunID).Scan(&credits, &attempted, &refunded); err != nil {
				t.Fatal(err)
			}
			want := 0
			if committed {
				want = 2
			}
			if credits != want || attempted != committed || refunded == committed {
				t.Fatalf("marker uncertainty accounting: credits%d attempted%t refunded%t want%d", credits, attempted, refunded, want)
			}
			remaining, err := store.Due(t.Context())
			if err != nil || len(remaining) != 0 {
				t.Fatalf("interrupted slot retried %v %v", remaining, err)
			}
		})
	}
}
