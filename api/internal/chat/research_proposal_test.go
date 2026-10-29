package chat

import (
	"encoding/json"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"testing"
)

func TestResearchProposalsRequireConfirmation(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	a := &agentRun{d: e.Deps, userID: e.User}
	tools := a.alertTools()
	var schedule, track func(json.RawMessage) (string, error)
	for _, tool := range tools {
		tool := tool
		switch tool.Name {
		case "set_schedule":
			schedule = func(b json.RawMessage) (string, error) { return tool.Run(t.Context(), b) }
		case "track_thesis":
			track = func(b json.RawMessage) (string, error) { return tool.Run(t.Context(), b) }
		}
	}
	if schedule == nil {
		t.Fatal("missing schedule tool")
	}
	if _, err := schedule(json.RawMessage(`{"every":"weekdays","at_utc":"08:00","action":"research","prompt":"Review portfolio risks","notifyWhen":"changed"}`)); err != nil {
		t.Fatalf("general research proposal: %v", err)
	}
	if a.card == nil || a.card.Type != "price_alert" {
		t.Fatalf("proposal card %+v", a.card)
	}
	cardJSON, _ := json.Marshal(a.card.Data)
	var card map[string]any
	_ = json.Unmarshal(cardJSON, &card)
	if card["action"] != "research" || card["prompt"] != "Review portfolio risks" || card["notifyWhen"] != "changed" {
		t.Fatalf("card fields %s", cardJSON)
	}
	if track == nil {
		t.Fatal("missing track_thesis tool")
	}
	if _, err := track(json.RawMessage(`{"symbol":"SOL","thesis":"Users grow","invalidation":"Users decline","every":"day","at_utc":"08:00"}`)); err == nil {
		t.Fatal("second alert card admitted")
	}
	var count int
	if err := e.DB.QueryRow(t.Context(), `select count(*) from alerts where user_id=$1`, e.User).Scan(&count); err != nil || count != 0 {
		t.Fatalf("proposal auto-created %d %v", count, err)
	}
	b := &agentRun{d: e.Deps, userID: e.User}
	var tracker func(json.RawMessage) (string, error)
	for _, tool := range b.alertTools() {
		if tool.Name == "track_thesis" {
			tracker = func(raw json.RawMessage) (string, error) { return tool.Run(t.Context(), raw) }
		}
	}
	if _, err := tracker(json.RawMessage(`{"thesis":"Users grow","invalidation":"Users decline","every":"day","at_utc":"08:00"}`)); err == nil {
		t.Fatal("symbolless thesis tracker admitted")
	}
	if _, err := tracker(json.RawMessage(`{"symbol":"SOL","thesis":"Users grow","invalidation":"Users decline","every":"day","at_utc":"08:00"}`)); err != nil {
		t.Fatal(err)
	}
}
