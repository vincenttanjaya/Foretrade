package alerts

import (
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/researchrun"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestResearchOrdinaryCheckerExcludesPaidSchedules(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`)
	if _, err := e.DB.Exec(t.Context(), `update alerts set next_run_at=clock_timestamp()-interval '1 second' where id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("ordinary runner dispatched Research: %v", p)
		}
	}()
	if err := runSchedules(t.Context(), e.Deps); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := e.DB.QueryRow(t.Context(), `select count(*) from alert_runs where alert_id=$1`, a.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ordinary runner claimed Research %d %v", count, err)
	}
}

func TestResearchBellUsesPersistedEligibilityAndOwnedChat(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`)
	c := claimResearch(t, e, s, a.ID, t.Context())
	if err := s.MarkAttempt(t.Context(), c.Lease); err != nil {
		t.Fatal(err)
	}
	report := strings.Repeat("界", 400)
	if err := s.Finalize(t.Context(), c.Lease, researchrun.Result{Answer: report, Produced: true, Settled: true}, nil); err != nil {
		t.Fatal(err)
	}
	listed, err := List(t.Context(), e.Deps, e.User, "all", 100)
	if err != nil || len(listed) != 1 || listed[0].LastRun == nil || utf8.RuneCountInString(listed[0].LastRun.Body) != 300 {
		t.Fatalf("Research latest body bound: %+v %v", listed, err)
	}
	var stored string
	if err := e.DB.QueryRow(t.Context(), `select m.content from chat_messages m join chats ch on ch.id=m.chat_id join alert_runs r on r.report_chat_id=ch.id where r.id=$1 and ch.user_id=$2`, c.Lease.RunID, e.User).Scan(&stored); err != nil || stored != report {
		t.Fatalf("latest body cap truncated owned full report: %d %v", utf8.RuneCountInString(stored), err)
	}
	items, _, err := feed(t.Context(), e.DB, e.User)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != "workflow_research" {
		t.Fatalf("research Bell %+v", items)
	}
	var owned string
	if err := e.DB.QueryRow(t.Context(), `select report_chat_id::text from alert_runs where id=$1`, c.Lease.RunID).Scan(&owned); err != nil {
		t.Fatal(err)
	}
	if items[0].Title != "Scheduled research · Review portfolio risks" {
		t.Fatalf("blank Research title: %q", items[0].Title)
	}
	if items[0].Link != "/chat/"+owned {
		t.Fatalf("owned report link %q", items[0].Link)
	}
	if _, err := e.DB.Exec(t.Context(), `update alert_runs set notification_eligible=false where id=$1`, c.Lease.RunID); err != nil {
		t.Fatal(err)
	}
	items, _, err = feed(t.Context(), e.DB, e.User)
	if err != nil || len(items) != 0 {
		t.Fatalf("ineligible Bell %+v %v", items, err)
	}
}
