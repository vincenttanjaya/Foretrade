package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
	"github.com/vincenttanjaya/Foretrade/api/internal/researchrun"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
	"strings"
	"time"
	"uuid"
)

var ErrResearchAuthority = errors.New("research authority expired or revoked")

// ResearchLease identifies one of the two durable slots. All writes match
// Slot, Fence and RunID; a stale callback cannot affect its replacement.
type ResearchLease struct {
	Slot                             int
	Fence                            int64
	RunID                            string
	AdmittedAt, Deadline, LeaseUntil time.Time // PostgreSQL authority clock only
	LocalAdmittedAt, LocalDeadline   time.Time // original process budget, preserving monotonic time
}

type dbResearchStore struct {
	d   *app.Deps
	now func() time.Time // process clock; persistence must use the database clock
}

func NewResearchStore(d *app.Deps) ResearchStorage { return &dbResearchStore{d: d, now: time.Now} }

func (s *dbResearchStore) Due(ctx context.Context) ([]ResearchDue, error) {
	rows, err := s.d.DB.Query(ctx, `select a.id::text,a.user_id::text,a.next_run_at from alerts a join users u on u.id=a.user_id
	where a.action='research' and a.status='active' and u.disabled_at is null and a.next_run_at<=clock_timestamp()
	and not exists(select 1 from alert_runs r where r.alert_id=a.id and r.status='running') order by a.next_run_at,a.id limit 100`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ResearchDue, error) {
		var d ResearchDue
		err := r.Scan(&d.AlertID, &d.UserID, &d.SlotAt)
		return d, err
	})
}

func (s *dbResearchStore) Admit(ctx context.Context) (*ResearchLease, error) {
	var l ResearchLease
	l.RunID = uuid.NewV4().String()
	l.LocalAdmittedAt = s.now()
	l.LocalDeadline = l.LocalAdmittedAt.Add(researchrun.Deadline)
	if parent, ok := ctx.Deadline(); ok && parent.Before(l.LocalDeadline) {
		l.LocalDeadline = parent
		l.LocalAdmittedAt = l.LocalDeadline.Add(-researchrun.Deadline)
	}
	ctx, cancel := context.WithDeadline(ctx, l.LocalDeadline)
	defer cancel()
	err := s.d.DB.QueryRow(ctx, `with available as (select slot from research_worker_slots where run_id is null order by slot for update skip locked limit 1),
	t as (select clock_timestamp() admitted from available)
	update research_worker_slots w set fence=w.fence+1,run_id=$1,admitted_at=t.admitted,deadline=t.admitted+interval '120 seconds',lease_until=t.admitted+interval '130 seconds'
	from available,t where w.slot=available.slot returning w.slot,w.fence,w.admitted_at,w.deadline,w.lease_until`, l.RunID).Scan(&l.Slot, &l.Fence, &l.AdmittedAt, &l.Deadline, &l.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func lockResearchLease(ctx context.Context, tx pgx.Tx, l ResearchLease, live bool) error {
	var ok bool
	err := tx.QueryRow(ctx, `select ($4=false or lease_until>clock_timestamp()) from research_worker_slots where slot=$1 and fence=$2 and run_id=$3 for update`, l.Slot, l.Fence, l.RunID, live).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !ok {
		return ErrResearchAuthority
	}
	return err
}

func clearResearchLease(ctx context.Context, tx pgx.Tx, l ResearchLease) error {
	_, err := tx.Exec(ctx, `update research_worker_slots set run_id=null,admitted_at=null,deadline=null,lease_until=null where slot=$1 and fence=$2 and run_id=$3`, l.Slot, l.Fence, l.RunID)
	return err
}

func (s *dbResearchStore) Release(ctx context.Context, l ResearchLease) error {
	return pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		if err := lockResearchLease(ctx, tx, l, false); errors.Is(err, ErrResearchAuthority) {
			return nil
		} else if err != nil {
			return err
		}
		var running bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from alert_runs where id=$1 and status='running')`, l.RunID).Scan(&running); err != nil {
			return err
		}
		if running {
			return ErrResearchAuthority
		}
		return clearResearchLease(ctx, tx, l)
	})
}

func (s *dbResearchStore) Claim(ctx context.Context, l ResearchLease, due ResearchDue) (*ResearchClaim, error) {
	if l.LocalDeadline.IsZero() {
		return nil, ErrResearchAuthority
	}
	ctx, cancel := context.WithDeadline(ctx, l.LocalDeadline)
	defer cancel()
	var claim *ResearchClaim
	err := pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, `select disabled_at is null from users where id=$1 for no key update`, due.UserID).Scan(&enabled); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if !enabled {
			return nil
		}
		a, err := scan(tx.QueryRow(ctx, `select `+columns+` from alerts where id=$1 and user_id=$2 for update`, due.AlertID, due.UserID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.Action != "research" || a.Status != "active" || a.NextRunAt == nil || !a.NextRunAt.Equal(due.SlotAt) {
			return nil
		}
		if err = lockResearchLease(ctx, tx, l, true); err != nil {
			return err
		}
		var running bool
		if err = tx.QueryRow(ctx, `select exists(select 1 from alert_runs where alert_id=$1 and (status='running' or slot_at=$2))`, a.ID, due.SlotAt).Scan(&running); err != nil {
			return err
		}
		if running {
			return nil
		}
		var now time.Time
		if err = tx.QueryRow(ctx, `select clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		if !now.Before(l.Deadline) {
			return context.DeadlineExceeded
		}
		if a.NextRunAt.After(now) {
			return nil
		}
		day := now.UTC().Truncate(24 * time.Hour)
		_, err = s.d.Usage.SpendTx(ctx, tx, due.UserID, researchrun.Cost, day)
		quota := errors.Is(err, usage.ErrQuota)
		if err != nil && !quota {
			return err
		}
		status, cost, msg := "running", researchrun.Cost, (*string)(nil)
		streak := 0
		switch {
		case errors.Is(err, usage.ErrOwnerOnly): // not spent credits: no streak toward the pause
			status, cost, msg, streak = "failed", 0, new("AI is in owner-only mode on this server"), a.QuotaStreak
		case quota:
			status, cost, msg = "failed", 0, new("Out of AI credits today")
			streak = min(a.QuotaStreak+1, 3)
		}
		if _, err = tx.Exec(ctx, `insert into alert_runs(id,alert_id,user_id,ran_at,status,error,slot_at,charged_day,cost,slot,fence) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, l.RunID, a.ID, due.UserID, now, status, msg, due.SlotAt, day, cost, l.Slot, l.Fence); err != nil {
			return err
		}
		wd := 0
		if a.Weekday != nil {
			wd = *a.Weekday
		}
		state := "active"
		if streak == 3 {
			state = "paused"
		}
		if _, err = tx.Exec(ctx, `update alerts set next_run_at=$2,fired_at=$3,quota_streak=$4,status=$5 where id=$1`, a.ID, nextRun(*a.Every, atMinute(*a.At), wd, now), now, streak, state); err != nil {
			return err
		}
		if quota {
			return clearResearchLease(ctx, tx, l)
		}
		p, err := profile.EnsureTx(ctx, tx, due.UserID, true)
		if err != nil {
			return err
		}
		req := researchrun.Request{UserID: due.UserID, AlertID: a.ID, RunID: l.RunID, Symbol: a.Symbol, Prompt: *a.Prompt, Thesis: a.Thesis, Profile: p, AdmittedAt: l.LocalAdmittedAt, Deadline: l.LocalDeadline}
		var prior *string
		err = tx.QueryRow(ctx, `select report,ran_at from alert_runs where alert_id=$1 and user_id=$2 and status='ok' and (verdict is null or verdict in ('holds','weakened','invalidated')) and report is not null order by ran_at desc,id desc limit 1`, a.ID, due.UserID).Scan(&prior, &req.PreviousAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if prior != nil {
			req.Previous = clip(*prior, 4000)
		}
		claim = &ResearchClaim{Lease: l, Request: req}
		return nil
	})
	// Lost acknowledgement never authorizes inference, even if the claim committed.
	if err != nil {
		return nil, err
	}
	return claim, nil
}

func (s *dbResearchStore) Authorize(ctx context.Context, l ResearchLease) error {
	if l.LocalDeadline.IsZero() {
		return ErrResearchAuthority
	}
	ctx, cancel := context.WithDeadline(ctx, l.LocalDeadline)
	defer cancel()
	var allowed bool
	err := s.d.DB.QueryRow(ctx, `select exists(select 1 from research_worker_slots w join alert_runs r on r.id=w.run_id
	join alerts a on a.id=r.alert_id and a.user_id=r.user_id join users u on u.id=r.user_id
	where w.slot=$1 and w.fence=$2 and w.run_id=$3 and r.slot=w.slot and r.fence=w.fence and r.status='running'
	and a.action='research' and a.status='active' and u.disabled_at is null and w.deadline>clock_timestamp() and w.lease_until>clock_timestamp())`, l.Slot, l.Fence, l.RunID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrResearchAuthority
	}
	return nil
}

func (s *dbResearchStore) MarkAttempt(ctx context.Context, l ResearchLease) error {
	if l.LocalDeadline.IsZero() {
		return ErrResearchAuthority
	}
	ctx, cancel := context.WithDeadline(ctx, l.LocalDeadline)
	defer cancel()
	return pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		var uid, aid string
		if err := tx.QueryRow(ctx, `select user_id::text,alert_id::text from alert_runs where id=$1`, l.RunID).Scan(&uid, &aid); errors.Is(err, pgx.ErrNoRows) {
			return ErrResearchAuthority
		} else if err != nil {
			return err
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `select disabled_at is null from users where id=$1 for no key update`, uid).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return ErrResearchAuthority
		}
		var active bool
		if err := tx.QueryRow(ctx, `select status='active' and action='research' from alerts where id=$1 and user_id=$2 for update`, aid, uid).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrResearchAuthority
		}
		if err := lockResearchLease(ctx, tx, l, true); err != nil {
			return err
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `select clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		if !now.Before(l.Deadline) {
			return context.DeadlineExceeded
		}
		tag, err := tx.Exec(ctx, `update alert_runs set attempted=true where id=$1 and status='running' and slot=$2 and fence=$3`, l.RunID, l.Slot, l.Fence)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrResearchAuthority
		}
		return nil
	})
}

// Finalize has a separate caller cleanup budget. Publication still obeys the
// admission deadline; accounting can settle during the remaining live lease.
func (s *dbResearchStore) Finalize(ctx context.Context, l ResearchLease, result researchrun.Result, runErr error) error {
	return s.finish(ctx, l, result, runErr, false)
}

func (s *dbResearchStore) finish(ctx context.Context, l ResearchLease, result researchrun.Result, runErr error, recovery bool) error {
	if runErr == nil && !recovery && strings.TrimSpace(result.Answer) != "" {
		if l.LocalDeadline.IsZero() {
			return ErrResearchAuthority
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, l.LocalDeadline)
		defer cancel()
	}
	return pgx.BeginFunc(ctx, s.d.DB, func(tx pgx.Tx) error {
		var uid, aid string
		err := tx.QueryRow(ctx, `select user_id::text,alert_id::text from alert_runs where id=$1`, l.RunID).Scan(&uid, &aid)
		if errors.Is(err, pgx.ErrNoRows) {
			if err = lockResearchLease(ctx, tx, l, !recovery); errors.Is(err, ErrResearchAuthority) {
				return nil
			} else if err != nil {
				return err
			}
			return clearResearchLease(ctx, tx, l)
		}
		if err != nil {
			return err
		}
		var enabled bool
		if err = tx.QueryRow(ctx, `select disabled_at is null from users where id=$1 for no key update`, uid).Scan(&enabled); err != nil {
			return err
		}
		a, err := scan(tx.QueryRow(ctx, `select `+columns+` from alerts where id=$1 and user_id=$2 for update`, aid, uid))
		if err != nil {
			return err
		}
		if err = lockResearchLease(ctx, tx, l, !recovery); errors.Is(err, ErrResearchAuthority) {
			return nil
		} else if err != nil {
			return err
		}
		var now, until time.Time
		if err = tx.QueryRow(ctx, `select clock_timestamp(),lease_until from research_worker_slots where slot=$1`, l.Slot).Scan(&now, &until); err != nil {
			return err
		}
		if recovery && now.Before(until) {
			return nil
		}
		if !recovery && !now.Before(until) {
			return ErrResearchAuthority
		}
		var status string
		var attempted, refunded bool
		var day time.Time
		var cost int
		if err = tx.QueryRow(ctx, `select status,attempted,refunded,charged_day,cost from alert_runs where id=$1 and slot=$2 and fence=$3 for update`, l.RunID, l.Slot, l.Fence).Scan(&status, &attempted, &refunded, &day, &cost); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if status != "running" {
			return clearResearchLease(ctx, tx, l)
		}
		cancelled := !enabled || a.Status != "active"
		if !now.Before(l.Deadline) && runErr == nil {
			runErr = context.DeadlineExceeded
		}
		if recovery {
			runErr = errors.New("Research interrupted")
		}
		answer := clip(result.Answer, 16000)
		if strings.TrimSpace(answer) == "" && runErr == nil {
			runErr = errors.New("Research returned no report")
		}
		status = "ok"
		var msg *string
		if runErr != nil {
			status = "failed"
			msg = new("Research could not complete")
		}
		if recovery || errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
			status = "interrupted"
			msg = new("Research interrupted")
		}
		if cancelled {
			status = "cancelled"
			msg = new("Research cancelled")
		}
		refund := status != "ok" && !refunded && cost > 0 && (!attempted || !recovery && runErr != nil && result.Settled && !result.Produced)
		if refund {
			if err = s.d.Usage.RefundTx(ctx, tx, uid, day, cost); err != nil {
				return err
			}
			refunded = true
		}
		var chatID, verdict, excerpt *string
		eligible := false
		var savedReport *string
		var sources any
		if status == "ok" {
			v := ""
			if a.Thesis != nil {
				v = researchVerdict(answer)
				verdict = &v
			}
			excerpt = new(clip(answer, 160))
			savedReport = &answer
			var priorReport, priorVerdict string
			err = tx.QueryRow(ctx, `select report,coalesce(verdict,'') from alert_runs where alert_id=$1 and user_id=$2 and status='ok' and (verdict is null or verdict in ('holds','weakened','invalidated')) and report is not null order by ran_at desc,id desc limit 1`, aid, uid).Scan(&priorReport, &priorVerdict)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			first := errors.Is(err, pgx.ErrNoRows)
			var hadSuccess bool
			if err = tx.QueryRow(ctx, `select exists(select 1 from alert_runs where alert_id=$1 and user_id=$2 and status='ok')`, aid, uid).Scan(&hadSuccess); err != nil {
				return err
			}
			switch *a.NotifyWhen {
			case "always":
				eligible = true
			case "changed":
				eligible = !hadSuccess || (!first && (v != priorVerdict || researchPrefix(answer) != researchPrefix(priorReport))) || (first && v != "unknown")
			case "invalidated":
				eligible = v == "invalidated" && (first || priorVerdict != "invalidated")
			}
			var owned string
			err = tx.QueryRow(ctx, `select c.id::text from chats c join alerts a on a.report_chat_id=c.id where a.id=$1 and c.user_id=$2 for update of c`, aid, uid).Scan(&owned)
			if errors.Is(err, pgx.ErrNoRows) {
				err = tx.QueryRow(ctx, `insert into chats(user_id,title) values($1,$2) returning id::text`, uid, "Workflow · "+clip(cmpResearchNote(a), 80)).Scan(&owned)
			}
			if err != nil {
				return err
			}
			chatID = &owned
			cards := []any{}
			if len(result.Sources) > 0 {
				cards = append(cards, map[string]any{"type": "sources", "data": map[string]any{"items": result.Sources}})
			}
			cardJSON, err := json.Marshal(cards)
			if err != nil {
				return err
			}
			sourceJSON, err := json.Marshal(result.Sources)
			if err != nil {
				return err
			}
			sources = sourceJSON
			if _, err = tx.Exec(ctx, `insert into chat_messages(chat_id,role,content,cards) values($1,'assistant',$2,$3)`, owned, answer, cardJSON); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `update chats set updated_at=clock_timestamp() where id=$1 and user_id=$2`, owned, uid); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `update alerts set report_chat_id=$2 where id=$1`, aid, owned); err != nil {
				return err
			}
		}
		if status == "ok" {
			var allowed bool
			if err = tx.QueryRow(ctx, `select deadline>clock_timestamp() and lease_until>clock_timestamp() from research_worker_slots where slot=$1 and fence=$2 and run_id=$3`, l.Slot, l.Fence, l.RunID).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				return ErrResearchAuthority
			}
		}
		body := ""
		if savedReport != nil {
			body = clip(*savedReport, 300)
		}
		if _, err = tx.Exec(ctx, `update alert_runs set status=$2,body=$3,error=$4,refunded=$5,report_chat_id=$6,report=$7,sources=$8,verdict=$9,excerpt=$10,notification_eligible=$11 where id=$1 and status='running' and slot=$12 and fence=$13`, l.RunID, status, body, msg, refunded, chatID, savedReport, sources, verdict, excerpt, eligible, l.Slot, l.Fence); err != nil {
			return err
		}
		return clearResearchLease(ctx, tx, l)
	})
}

func cmpResearchNote(a Alert) string {
	if a.Note != "" {
		return a.Note
	}
	if a.Prompt != nil {
		return *a.Prompt
	}
	return "Research"
}

func researchVerdict(answer string) string {
	lines := strings.Split(strings.TrimSpace(answer), "\n")
	last := lines[len(lines)-1]
	for _, v := range []string{"holds", "weakened", "invalidated"} {
		prefix := "Verdict: " + v + " — "
		if strings.HasPrefix(last, prefix) && strings.TrimSpace(strings.TrimPrefix(last, prefix)) != "" {
			return v
		}
	}
	return "unknown"
}
func researchPrefix(answer string) string {
	return clip(strings.ToLower(strings.Join(strings.Fields(answer), " ")), 200)
}

func (s *dbResearchStore) Recover(ctx context.Context) error {
	rows, err := s.d.DB.Query(ctx, `select slot,fence,run_id::text,admitted_at,deadline,lease_until from research_worker_slots where run_id is not null and lease_until<=clock_timestamp() order by slot`)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ResearchLease, error) {
		var l ResearchLease
		err := r.Scan(&l.Slot, &l.Fence, &l.RunID, &l.AdmittedAt, &l.Deadline, &l.LeaseUntil)
		return l, err
	})
	if err != nil {
		return err
	}
	for _, l := range list {
		if err = s.finish(ctx, l, researchrun.Result{}, fmt.Errorf("Research interrupted"), true); err != nil {
			return err
		}
	}
	return nil
}

type ResearchDue struct {
	AlertID, UserID string
	SlotAt          time.Time
}
type ResearchClaim struct {
	Lease   ResearchLease
	Request researchrun.Request
}

// ResearchStorage is frozen for the scheduler contributor. The assembler owns
// its SQL implementation. Nil Admit means saturation; nil Claim means no paid
// work (stale/disabled/quota), never permission to call the runner.
type ResearchStorage interface {
	Due(context.Context) ([]ResearchDue, error)
	Admit(context.Context) (*ResearchLease, error)
	Claim(context.Context, ResearchLease, ResearchDue) (*ResearchClaim, error)
	Authorize(context.Context, ResearchLease) error
	MarkAttempt(context.Context, ResearchLease) error
	Finalize(context.Context, ResearchLease, researchrun.Result, error) error
	Recover(context.Context) error                // expired only; settles once before releasing
	Release(context.Context, ResearchLease) error // only an unclaimed matching slot
}
