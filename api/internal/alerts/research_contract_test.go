package alerts

import (
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

func TestResearchContractAndSchema(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	body := map[string]any{"trigger": "schedule", "action": "research", "every": "weekdays", "at": "08:00", "prompt": "Review my portfolio"}
	w := e.Do("POST", "/api/alerts", body)
	if w.Code != 201 {
		t.Fatalf("research create: %d %s", w.Code, w.Body)
	}
	var a struct {
		Alert struct {
			ID, Symbol, Action, Prompt, NotifyWhen string
			Thesis                                 *struct{ Statement, Invalidation string }
		}
	}
	a = apptest.JSON[struct {
		Alert struct {
			ID, Symbol, Action, Prompt, NotifyWhen string
			Thesis                                 *struct{ Statement, Invalidation string }
		}
	}](t, w)
	if a.Alert.Symbol != "" || a.Alert.Action != "research" || a.Alert.Prompt != "Review my portfolio" || a.Alert.NotifyWhen != "always" {
		t.Fatalf("roundtrip %+v", a)
	}
	for _, override := range []map[string]any{{"prompt": strings.Repeat("界", 9)}, {"prompt": strings.Repeat("界", 501)}, {"notifyWhen": "sometimes"}, {"notional": 10}, {"stopLoss": 5}, {"price": 10}, {"symbol": "UNKNOWN"}, {"thesis": map[string]any{"statement": strings.Repeat("界", 301), "invalidation": "risk"}}} {
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		for k, v := range override {
			b[k] = v
		}
		if got := e.Do("POST", "/api/alerts", b); got.Code != 400 {
			t.Fatalf("invalid %+v: %d %s", override, got.Code, got.Body)
		}
	}
	var slots int
	if err := e.DB.QueryRow(t.Context(), "select count(*) from research_worker_slots").Scan(&slots); err != nil || slots != 2 {
		t.Fatalf("global slots %d: %v", slots, err)
	}
	if _, err := e.DB.Exec(t.Context(), "insert into research_worker_slots(slot) values(3)"); err == nil {
		t.Fatal("third global slot admitted")
	}
	for _, suffix := range []string{"null,null,null", "'valid prompt text',null,null", "'valid prompt text','{\"statement\":\"valid\"}'::jsonb,'always'"} {
		_, err := e.DB.Exec(t.Context(), `insert into alerts(user_id,symbol,trigger,every,at_minute,next_run_at,action,prompt,thesis,notify_when) values($1,'','schedule','day',480,now(),'research',`+suffix+`)`, e.User)
		if err == nil {
			t.Fatalf("database accepted incomplete research shape: %s", suffix)
		}
	}
	for _, missing := range []string{"deadline", "lease_until", "deadline,lease_until"} {
		tx, err := e.DB.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(t.Context(), `update research_worker_slots set run_id=gen_random_uuid(),admitted_at=now(),deadline=now()+interval '120 seconds',lease_until=now()+interval '130 seconds' where slot=1`)
		if err != nil {
			tx.Rollback(t.Context())
			t.Fatalf("valid fenced lease setup: %v", err)
		}
		assignments := strings.ReplaceAll(missing, ",", "=null,") + "=null"
		_, err = tx.Exec(t.Context(), `update research_worker_slots set `+assignments+` where slot=1`)
		tx.Rollback(t.Context())
		if err == nil {
			t.Errorf("database accepted incomplete fenced lease: %s", missing)
		}
	}
	if _, err := e.DB.Exec(t.Context(), `insert into alert_runs(alert_id,user_id,status,slot_at,charged_day,cost,fence) values($1,$2,'failed',now(),current_date,2,1)`, a.Alert.ID, e.User); err == nil {
		t.Error("database accepted paid run without a global worker slot")
	}
}

func TestResearchFieldsRequireResearchForEveryTrigger(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	for _, fields := range []map[string]any{{"prompt": "Review portfolio risks"}, {"thesis": map[string]any{"statement": "Growth", "invalidation": "Decline"}}, {"notifyWhen": "changed"}} {
		body := map[string]any{"symbol": "BTC", "trigger": "price", "action": "notify", "op": "above", "price": 110000}
		for k, v := range fields {
			body[k] = v
		}
		if got := e.Do("POST", "/api/alerts", body); got.Code != 400 || !strings.Contains(got.Body.String(), "Research fields require action research") {
			t.Errorf("ordinary price alert accepted Research fields %+v: %d %s", fields, got.Code, got.Body)
		}
	}
	var count int
	if err := e.DB.QueryRow(t.Context(), "select count(*) from alerts where user_id=$1", e.User).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected Research fields created alerts: %d %v", count, err)
	}
}
