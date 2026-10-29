package alerts

import (
	"context"
	"errors"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/researchrun"
	"strings"
	"testing"
	"time"
)

func claimResearch(t *testing.T, e *apptest.Env, s ResearchStorage, id string, ctx context.Context) *ResearchClaim {
	t.Helper()
	if _, err := e.DB.Exec(ctx, `update alerts set next_run_at=clock_timestamp()-interval '1 second' where id=$1`, id); err != nil {
		t.Fatal(err)
	}
	due, err := s.Due(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var d *ResearchDue
	for i := range due {
		if due[i].AlertID == id {
			d = &due[i]
		}
	}
	if d == nil {
		t.Fatal("not due")
	}
	l, err := s.Admit(ctx)
	if err != nil || l == nil {
		t.Fatalf("admission %v %v", l, err)
	}
	c, err := s.Claim(ctx, *l, *d)
	if err != nil || c == nil {
		t.Fatalf("claim %v %v", c, err)
	}
	return c
}

func TestResearchLeaseUsesDatabaseAcquisitionClock(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps).(*dbResearchStore)
	// Only this store's instance-clock seam is skewed. PostgreSQL and the
	// process clock remain unchanged; an independent query bounds acquisition.
	s.now = func() time.Time { return time.Now().Add(-20 * time.Second) }
	var before, after time.Time
	if err := e.DB.QueryRow(t.Context(), `select clock_timestamp()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	l, err := s.Admit(t.Context())
	if err != nil || l == nil {
		t.Fatalf("admission %v %v", l, err)
	}
	defer s.Release(t.Context(), *l)
	if err := e.DB.QueryRow(t.Context(), `select clock_timestamp()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if l.AdmittedAt.Before(before) || l.AdmittedAt.After(after) || !l.Deadline.Equal(l.AdmittedAt.Add(120*time.Second)) || !l.LeaseUntil.Equal(l.AdmittedAt.Add(130*time.Second)) {
		t.Fatalf("lease persisted instance-clock timestamps, PG acquisition [%v,%v], lease %+v", before, after, l)
	}
}

func TestResearchGeneralPreviousAndChangedBaseline(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks","notifyWhen":"changed"}`)
	one := claimResearch(t, e, s, a.ID, t.Context())
	if err := s.MarkAttempt(t.Context(), one.Lease); err != nil {
		t.Fatal(err)
	}
	report := "Portfolio risks remain stable."
	if err := s.Finalize(t.Context(), one.Lease, researchrun.Result{Answer: report, Produced: true, Settled: true}, nil); err != nil {
		t.Fatal(err)
	}
	two := claimResearch(t, e, s, a.ID, t.Context())
	if two.Request.Previous != report {
		t.Fatalf("general successful report missing from previous context: %q", two.Request.Previous)
	}
	if err := s.MarkAttempt(t.Context(), two.Lease); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(t.Context(), two.Lease, researchrun.Result{Answer: "  Portfolio  risks remain stable.  ", Produced: true, Settled: true}, nil); err != nil {
		t.Fatal(err)
	}
	var eligible int
	if err := e.DB.QueryRow(t.Context(), `select count(*) from alert_runs where alert_id=$1 and notification_eligible`, a.ID).Scan(&eligible); err != nil || eligible != 1 {
		t.Fatalf("repeated general report notified %d times: %v", eligible, err)
	}
}

func TestResearchUnknownThesisDoesNotReplaceBaseline(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Assess investment thesis","notifyWhen":"changed","thesis":{"statement":"Network grows","invalidation":"Users decline"}}`)
	baseline := "Evidence remains sound.\nVerdict: holds — Users grew"
	for _, answer := range []string{baseline, "Unclear evidence without a final verdict"} {
		c := claimResearch(t, e, s, a.ID, t.Context())
		if err := s.MarkAttempt(t.Context(), c.Lease); err != nil {
			t.Fatal(err)
		}
		if err := s.Finalize(t.Context(), c.Lease, researchrun.Result{Answer: answer, Produced: true, Settled: true}, nil); err != nil {
			t.Fatal(err)
		}
	}
	c := claimResearch(t, e, s, a.ID, t.Context())
	if c.Request.Previous != baseline {
		t.Fatalf("unknown replaced baseline: %q", c.Request.Previous)
	}
	if err := s.Finalize(t.Context(), c.Lease, researchrun.Result{}, errors.New("before first model")); err != nil {
		t.Fatal(err)
	}
}

func TestResearchUnknownThesisIsNotRepeatedFirstSuccess(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Assess investment thesis","notifyWhen":"changed","thesis":{"statement":"Network grows","invalidation":"Users decline"}}`)
	for range 2 {
		c := claimResearch(t, e, s, a.ID, t.Context())
		if c.Request.Previous != "" {
			t.Fatalf("unknown used as baseline %q", c.Request.Previous)
		}
		if err := s.MarkAttempt(t.Context(), c.Lease); err != nil {
			t.Fatal(err)
		}
		if err := s.Finalize(t.Context(), c.Lease, researchrun.Result{Answer: "Evidence is unavailable, verdict unknown.", Produced: true, Settled: true}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var notifications int
	if err := e.DB.QueryRow(t.Context(), `select count(*) from alert_runs where alert_id=$1 and notification_eligible`, a.ID).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("unknown became repeated first success %d %v", notifications, err)
	}
	c := claimResearch(t, e, s, a.ID, t.Context())
	if err := s.MarkAttempt(t.Context(), c.Lease); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(t.Context(), c.Lease, researchrun.Result{Answer: "Evidence improved.\nVerdict: holds — Users grew", Produced: true, Settled: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.QueryRow(t.Context(), `select count(*) from alert_runs where alert_id=$1 and notification_eligible`, a.ID).Scan(&notifications); err != nil || notifications != 2 {
		t.Fatalf("first known comparison baseline did not notify %d %v", notifications, err)
	}
}

func TestResearchAnchoredVerdictAndInvalidationTransitions(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Assess investment thesis","notifyWhen":"invalidated","thesis":{"statement":"Network grows","invalidation":"Users decline"}}`)
	for _, tc := range []struct {
		answer, verdict string
		eligible        bool
	}{
		{"Sound evidence.\nVerdict: holds — Users grew", "holds", false},
		{"Verdict: invalidated — Users fell\nThis line prevents a terminal verdict.", "unknown", false},
		{"Adverse evidence.\nVerdict: invalidated — Users declined", "invalidated", true},
		{"Adverse evidence.\nVerdict: invalidated — ", "unknown", false},
		{"More adverse evidence.\nVerdict: invalidated — Users declined again", "invalidated", false},
		{"Recovered evidence.\nVerdict: holds — Users returned", "holds", false},
		{"Renewed adverse evidence.\nVerdict: invalidated — Users declined", "invalidated", true},
	} {
		c := claimResearch(t, e, s, a.ID, t.Context())
		if err := s.MarkAttempt(t.Context(), c.Lease); err != nil {
			t.Fatal(err)
		}
		if err := s.Finalize(t.Context(), c.Lease, researchrun.Result{Answer: tc.answer, Produced: true, Settled: true}, nil); err != nil {
			t.Fatal(err)
		}
		var verdict string
		var eligible bool
		if err := e.DB.QueryRow(t.Context(), `select verdict,notification_eligible from alert_runs where id=$1`, c.Lease.RunID).Scan(&verdict, &eligible); err != nil || verdict != tc.verdict || eligible != tc.eligible {
			t.Fatalf("terminal verdict/transition %q: %s/%t, expected %s/%t: %v", tc.answer, verdict, eligible, tc.verdict, tc.eligible, err)
		}
	}
	items, _, err := feed(t.Context(), e.DB, e.User)
	if err != nil || len(items) != 2 {
		t.Fatalf("invalidation Bell transitions %d %v", len(items), err)
	}
}

func TestResearchDeletedChatAndRetentionPreserveOwnershipAndUnsettledAccounting(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`)
	publish := func(answer string) string {
		t.Helper()
		c := claimResearch(t, e, s, a.ID, t.Context())
		if err := s.MarkAttempt(t.Context(), c.Lease); err != nil {
			t.Fatal(err)
		}
		if err := s.Finalize(t.Context(), c.Lease, researchrun.Result{Answer: answer, Produced: true, Settled: true}, nil); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := e.DB.QueryRow(t.Context(), `select report_chat_id::text from alert_runs where id=$1`, c.Lease.RunID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := publish("First private report")
	if _, err := e.DB.Exec(t.Context(), `delete from chats where id=$1 and user_id=$2`, old, e.User); err != nil {
		t.Fatal(err)
	}
	var links int
	if err := e.DB.QueryRow(t.Context(), `select count(*) from alert_runs where alert_id=$1 and report_chat_id is not null`, a.ID).Scan(&links); err != nil || links != 0 {
		t.Fatalf("deleted chat retained a report link: %d %v", links, err)
	}
	current := publish("Second private report")
	if current == old {
		t.Fatal("deleted report chat was reused")
	}
	active := claimResearch(t, e, s, a.ID, t.Context())
	if err := s.MarkAttempt(t.Context(), active.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(t.Context(), `update alert_runs set ran_at=clock_timestamp()-interval '31 days' where alert_id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	oddsMu.Lock()
	previousPrune := pruned
	pruned = time.Time{}
	oddsMu.Unlock()
	defer func() { oddsMu.Lock(); pruned = previousPrune; oddsMu.Unlock() }()
	if err := prune(t.Context(), e.Deps); err != nil {
		t.Fatal(err)
	}
	var runs, ownedMessages, liveSlots int
	if err := e.DB.QueryRow(t.Context(), `select count(*) from alert_runs where alert_id=$1 and id=$2 and status='running' and attempted and not refunded`, a.ID, active.Lease.RunID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.QueryRow(t.Context(), `select count(*) from chat_messages m join chats c on c.id=m.chat_id where c.id=$1 and c.user_id=$2 and m.content='Second private report'`, current, e.User).Scan(&ownedMessages); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.QueryRow(t.Context(), `select count(*) from research_worker_slots where run_id=$1 and fence=$2`, active.Lease.RunID, active.Lease.Fence).Scan(&liveSlots); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || ownedMessages != 1 || liveSlots != 1 {
		t.Fatalf("retention lost unresolved accounting or owned report: %d/%d/%d", runs, ownedMessages, liveSlots)
	}
	if err := s.Finalize(t.Context(), active.Lease, researchrun.Result{}, errors.New("uncertain interrupted attempt")); err != nil {
		t.Fatal(err)
	}
}

func TestResearchPublicationWaitCannotRenewDeadline(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`)
	one := claimResearch(t, e, s, a.ID, t.Context())
	if err := s.MarkAttempt(t.Context(), one.Lease); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(t.Context(), one.Lease, researchrun.Result{Answer: "First report", Produced: true, Settled: true}, nil); err != nil {
		t.Fatal(err)
	}
	var chatID string
	if err := e.DB.QueryRow(t.Context(), `select report_chat_id::text from alerts where id=$1`, a.ID).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	tx, err := e.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), `select id from chats where id=$1 for update`, chatID); err != nil {
		t.Fatal(err)
	}
	// The setup below (a claim, an attempt, a goroutine reaching the lock) fits 3 s under load (gap round G24: 700 ms
	// failed once on a loaded runner); the assertions hold for any window.
	admission, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	two := claimResearch(t, e, s, a.ID, admission)
	if err = s.MarkAttempt(admission, two.Lease); err != nil {
		t.Fatal(err)
	}
	clean, stop := context.WithTimeout(t.Context(), 8*time.Second)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- s.Finalize(clean, two.Lease, researchrun.Result{Answer: "Late second report", Produced: true, Settled: true}, nil)
	}()
	// Observe the actual chat-row lock wait, rather than a timing assumption.
	for {
		var waiting bool
		if err = e.DB.QueryRow(t.Context(), `select exists(select 1 from pg_stat_activity where datname=current_database() and wait_event_type='Lock' and query like '%select c.id::text from chats c join alerts%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err = <-done:
			t.Fatalf("did not wait for chat lock: %v", err)
		case <-admission.Done():
			t.Fatal("chat lock wait not observed")
		case <-time.After(time.Millisecond):
		}
	}
	<-admission.Done()
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-clean.Done():
		t.Fatal("publication did not join")
	}
	if err == nil {
		t.Fatal("success publication committed after admission deadline")
	}
	var messages, eligible int
	if er := e.DB.QueryRow(t.Context(), `select count(*) from chat_messages where chat_id=$1`, chatID).Scan(&messages); er != nil {
		t.Fatal(er)
	}
	if er := e.DB.QueryRow(t.Context(), `select count(*) from alert_runs where alert_id=$1 and notification_eligible`, a.ID).Scan(&eligible); er != nil {
		t.Fatal(er)
	}
	if messages != 1 || eligible != 1 {
		t.Fatalf("late publication leaked message/Bell %d/%d", messages, eligible)
	}
	if err = s.Finalize(clean, two.Lease, researchrun.Result{}, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
}

func TestResearchSlotsRecoveryAndChargedDayRefund(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	l1, err := s.Admit(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	l2, err := s.Admit(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.Admit(t.Context())
	if err != nil || l1 == nil || l2 == nil || third != nil {
		t.Fatalf("slots %+v %+v %+v %v", l1, l2, third, err)
	}
	if err = s.Release(t.Context(), *l1); err != nil {
		t.Fatal(err)
	}
	if err = s.Release(t.Context(), *l2); err != nil {
		t.Fatal(err)
	}
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`)
	c := claimResearch(t, e, s, a.ID, t.Context())
	var charged time.Time
	if err = e.DB.QueryRow(t.Context(), `select charged_day from alert_runs where id=$1`, c.Lease.RunID).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	old := charged.AddDate(0, 0, -1)
	if _, err = e.DB.Exec(t.Context(), `update ai_usage set day=$2 where user_id=$1`, e.User, old); err != nil {
		t.Fatal(err)
	}
	if _, err = e.DB.Exec(t.Context(), `update alert_runs set charged_day=$2 where user_id=$1`, e.User, old); err != nil {
		t.Fatal(err)
	}
	if err = s.Finalize(t.Context(), c.Lease, researchrun.Result{}, errors.New("pre-attempt failure")); err != nil {
		t.Fatal(err)
	}
	if err = s.Finalize(t.Context(), c.Lease, researchrun.Result{}, errors.New("duplicate cleanup")); err != nil {
		t.Fatal(err)
	}
	var credit int
	if err = e.DB.QueryRow(t.Context(), `select credits from ai_usage where user_id=$1 and day=$2`, e.User, old).Scan(&credit); err != nil || credit != 0 {
		t.Fatalf("charged-day refund %d %v", credit, err)
	}
	c = claimResearch(t, e, s, a.ID, t.Context())
	if err = s.MarkAttempt(t.Context(), c.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err = e.DB.Exec(t.Context(), `update research_worker_slots set admitted_at=t.admitted,deadline=t.admitted+interval '121 seconds',lease_until=t.admitted+interval '130 seconds' from (select clock_timestamp()-interval '140 seconds' admitted) t where run_id=$1`, c.Lease.RunID); err == nil {
		t.Fatal("inconsistent lease timestamps accepted")
	}
	if _, err = e.DB.Exec(t.Context(), `update research_worker_slots set admitted_at=t.admitted,deadline=t.admitted+interval '120 seconds',lease_until=t.admitted+interval '130 seconds' from (select clock_timestamp()-interval '140 seconds' admitted) t where run_id=$1`, c.Lease.RunID); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	var refunded bool
	if err = e.DB.QueryRow(t.Context(), `select status,refunded from alert_runs where id=$1`, c.Lease.RunID).Scan(&status, &refunded); err != nil || status != "interrupted" || refunded {
		t.Fatalf("unknown attempt recovery %s/%t %v", status, refunded, err)
	}
	replacement, err := s.Admit(t.Context())
	if err != nil || replacement == nil {
		t.Fatalf("replacement %v %v", replacement, err)
	}
	if err = s.Finalize(t.Context(), c.Lease, researchrun.Result{Answer: strings.Repeat("stale", 10), Produced: true, Settled: true}, nil); err != nil {
		t.Fatal(err)
	}
	var current string
	if err = e.DB.QueryRow(t.Context(), `select run_id::text from research_worker_slots where slot=$1`, replacement.Slot).Scan(&current); err != nil || current != replacement.RunID {
		t.Fatalf("stale callback cleared replacement %s %v", current, err)
	}
	if err = s.Release(t.Context(), *replacement); err != nil {
		t.Fatal(err)
	}
}

func TestResearchPublicationRollbackAndDisabledSuppression(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	s := NewResearchStore(e.Deps)
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`)
	c := claimResearch(t, e, s, a.ID, t.Context())
	if err := s.MarkAttempt(t.Context(), c.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(t.Context(), `create function fail_research_publication() returns trigger language plpgsql as $$begin if new.status='ok' then raise exception 'injected final write failure'; end if;return new;end$$;
	create trigger fail_research_publication before update on alert_runs for each row execute function fail_research_publication()`); err != nil {
		t.Fatal(err)
	}
	result := researchrun.Result{Answer: "Owned full report", Produced: true, Settled: true, Sources: []researchrun.Source{{N: 1, Title: "Evidence", URL: "https://example.test/evidence", Kind: "web"}}}
	if err := s.Finalize(t.Context(), c.Lease, result, nil); err == nil {
		t.Fatal("injected publication succeeded")
	}
	var chats int
	if err := e.DB.QueryRow(t.Context(), `select count(*) from chats where user_id=$1`, e.User).Scan(&chats); err != nil || chats != 0 {
		t.Fatalf("rollback leaked chats %d %v", chats, err)
	}
	if _, err := e.DB.Exec(t.Context(), `drop trigger fail_research_publication on alert_runs`); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(t.Context(), c.Lease, result, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(t.Context(), c.Lease, result, nil); err != nil {
		t.Fatal(err)
	}
	var messages int
	if err := e.DB.QueryRow(t.Context(), `select count(*) from chat_messages m join chats c on c.id=m.chat_id where c.user_id=$1 and m.cards->0->'data'->'items'->0->>'url'='https://example.test/evidence'`, e.User).Scan(&messages); err != nil || messages != 1 {
		t.Fatalf("atomic sources/publication %d %v", messages, err)
	}
	c = claimResearch(t, e, s, a.ID, t.Context())
	if err := s.MarkAttempt(t.Context(), c.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(t.Context(), `update users set disabled_at=clock_timestamp() where id=$1`, e.User); err != nil {
		t.Fatal(err)
	}
	if err := s.Authorize(t.Context(), c.Lease); !errors.Is(err, ErrResearchAuthority) {
		t.Fatalf("disabled authority %v", err)
	}
	if err := s.Finalize(t.Context(), c.Lease, result, nil); err != nil {
		t.Fatal(err)
	}
	var status string
	var notify bool
	if err := e.DB.QueryRow(t.Context(), `select status,notification_eligible from alert_runs where id=$1`, c.Lease.RunID).Scan(&status, &notify); err != nil || status != "cancelled" || notify {
		t.Fatalf("disabled delivery %s/%t %v", status, notify, err)
	}
}
