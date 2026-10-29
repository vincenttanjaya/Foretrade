package alerts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/researchrun"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

type schedulerRunner func(context.Context, researchrun.Request) (researchrun.Result, error)

func (f schedulerRunner) Run(ctx context.Context, req researchrun.Request) (researchrun.Result, error) {
	return f(ctx, req)
}

// This double isolates orchestration. Durable cross-instance/accounting
// guarantees are exercised separately against the concrete PostgreSQL store.
type schedulerStorage struct {
	due       func(context.Context) ([]ResearchDue, error)
	admit     func(context.Context) (*ResearchLease, error)
	claim     func(context.Context, ResearchLease, ResearchDue) (*ResearchClaim, error)
	authorize func(context.Context, ResearchLease) error
	mark      func(context.Context, ResearchLease) error
	finalize  func(context.Context, ResearchLease, researchrun.Result, error) error
	recover   func(context.Context) error
	release   func(context.Context, ResearchLease) error
}

func (s schedulerStorage) Due(ctx context.Context) ([]ResearchDue, error) {
	if s.due != nil {
		return s.due(ctx)
	}
	return []ResearchDue{{AlertID: "alert", UserID: "owner"}}, nil
}
func (s schedulerStorage) Admit(ctx context.Context) (*ResearchLease, error) {
	if s.admit != nil {
		return s.admit(ctx)
	}
	d, _ := ctx.Deadline()
	a := d.Add(-researchrun.Deadline)
	return &ResearchLease{Slot: 1, Fence: 1, RunID: "run", AdmittedAt: a, Deadline: d, LeaseUntil: a.Add(researchrun.Lease)}, nil
}
func (s schedulerStorage) Claim(ctx context.Context, lease ResearchLease, due ResearchDue) (*ResearchClaim, error) {
	if s.claim != nil {
		return s.claim(ctx, lease, due)
	}
	return &ResearchClaim{Lease: lease, Request: researchrun.Request{UserID: due.UserID, AlertID: due.AlertID, RunID: lease.RunID}}, nil
}
func (s schedulerStorage) Authorize(ctx context.Context, lease ResearchLease) error {
	if s.authorize != nil {
		return s.authorize(ctx, lease)
	}
	return ctx.Err()
}
func (s schedulerStorage) MarkAttempt(ctx context.Context, lease ResearchLease) error {
	if s.mark != nil {
		return s.mark(ctx, lease)
	}
	return ctx.Err()
}
func (s schedulerStorage) Finalize(ctx context.Context, lease ResearchLease, result researchrun.Result, err error) error {
	if s.finalize != nil {
		return s.finalize(ctx, lease, result, err)
	}
	return nil
}
func (s schedulerStorage) Recover(ctx context.Context) error {
	if s.recover != nil {
		return s.recover(ctx)
	}
	return ctx.Err()
}
func (s schedulerStorage) Release(ctx context.Context, lease ResearchLease) error {
	if s.release != nil {
		return s.release(ctx, lease)
	}
	return nil
}

func TestResearchSchedulerBudgetIncludesAdmissionAndClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var called atomic.Int32
		store := schedulerStorage{
			admit: func(ctx context.Context) (*ResearchLease, error) {
				d, ok := ctx.Deadline()
				if !ok || !d.Equal(start.Add(120*time.Second)) {
					t.Fatalf("admission has renewed/missing budget: %v", d)
				}
				time.Sleep(30 * time.Second)
				return &ResearchLease{Slot: 1, Fence: 4, RunID: "run", AdmittedAt: start, Deadline: d, LeaseUntil: start.Add(130 * time.Second)}, nil
			},
			claim: func(ctx context.Context, l ResearchLease, d ResearchDue) (*ResearchClaim, error) {
				time.Sleep(20 * time.Second)
				return &ResearchClaim{Lease: l, Request: researchrun.Request{UserID: d.UserID, AlertID: d.AlertID}}, nil
			},
		}
		s := NewResearchScheduler(nil, schedulerRunner(func(ctx context.Context, r researchrun.Request) (researchrun.Result, error) {
			called.Add(1)
			d, _ := ctx.Deadline()
			if !d.Equal(start.Add(120*time.Second)) || !r.Deadline.Equal(d) || !r.AdmittedAt.Equal(start) || r.RunID != "run" {
				t.Errorf("worker renewed or lost admitted identity: %+v ctx=%v", r, d)
			}
			return researchrun.Result{Answer: "report", Settled: true}, nil
		}), store)
		if err := s.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		s.Close()
		if called.Load() != 1 {
			t.Fatalf("runner calls=%d", called.Load())
		}
	})
}

func TestResearchSchedulerDatabaseClockDoesNotChangeSDKDeadline(t *testing.T) {
	for _, skew := range []time.Duration{-time.Hour, time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			open := sync.OnceFunc(func() { close(release) })
			client, fake := llmtest.New(t, func(llmtest.Request) llmtest.Reply {
				return llmtest.Reply{Text: "Held raw report", AfterFirst: func() {
					close(entered)
					defer close(exited)
					<-release
				}}
			})
			t.Cleanup(open)
			ctx, cancel := context.WithTimeout(t.Context(), 350*time.Millisecond)
			defer cancel()
			original, _ := ctx.Deadline()
			finished := make(chan error, 1)
			store := schedulerStorage{
				admit: func(context.Context) (*ResearchLease, error) {
					admitted := time.Now().Add(skew)
					return &ResearchLease{Slot: 1, Fence: 1, RunID: "run", AdmittedAt: admitted, Deadline: admitted.Add(120 * time.Second), LeaseUntil: admitted.Add(130 * time.Second)}, nil
				},
				claim: func(_ context.Context, lease ResearchLease, due ResearchDue) (*ResearchClaim, error) {
					return &ResearchClaim{Lease: lease, Request: researchrun.Request{UserID: due.UserID, AlertID: due.AlertID, Prompt: "Review portfolio risks"}}, nil
				},
				finalize: func(clean context.Context, _ ResearchLease, _ researchrun.Result, err error) error {
					if clean.Err() != nil {
						t.Error("terminal cleanup inherited expired operation context")
					}
					finished <- err
					return nil
				},
			}
			s := NewResearchScheduler(nil, schedulerRunner(func(work context.Context, req researchrun.Request) (researchrun.Result, error) {
				d, _ := work.Deadline()
				if !d.Equal(original) || !req.Deadline.Equal(original) || !req.AdmittedAt.Equal(original.Add(-120*time.Second)) {
					t.Errorf("DB wall-clock timestamps changed local SDK budget: req=%+v ctx=%v", req, d)
				}
				return schedulerSDKRunner(client).Run(work, req)
			}), store)
			defer s.Close()
			if err := s.Check(ctx); err != nil {
				t.Fatalf("PG-only absolute deadline rejected a live local worker: %v", err)
			}
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("actual local SDK did not enter")
			}
			select {
			case err := <-finished:
				if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() == nil {
					t.Fatalf("SDK did not end at original local budget: %v, local %v", err, ctx.Err())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("DB clock skew renewed original SDK budget")
			}
			open()
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
				t.Fatal("fixture driver did not join")
			}
			if len(fake.Requests()) != 1 {
				t.Fatalf("bounded SDK retried: %d", len(fake.Requests()))
			}
		})
	}
}

func TestResearchSchedulerExpiredAdmissionNeverClaims(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var claims, releases, runs atomic.Int32
		store := schedulerStorage{admit: func(ctx context.Context) (*ResearchLease, error) {
			d, _ := ctx.Deadline()
			time.Sleep(121 * time.Second)
			return &ResearchLease{Slot: 1, Fence: 1, RunID: "expired", AdmittedAt: d.Add(-120 * time.Second), Deadline: d, LeaseUntil: d.Add(10 * time.Second)}, nil
		}, claim: func(context.Context, ResearchLease, ResearchDue) (*ResearchClaim, error) {
			claims.Add(1)
			return nil, nil
		}, release: func(ctx context.Context, l ResearchLease) error {
			releases.Add(1)
			if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
				t.Errorf("release lacks fresh finite cleanup: %v", ctx.Err())
			}
			return nil
		}}
		s := NewResearchScheduler(nil, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
			runs.Add(1)
			return researchrun.Result{}, nil
		}), store)
		if err := s.Check(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired admission: %v", err)
		}
		s.Close()
		if claims.Load() != 0 || runs.Load() != 0 || releases.Load() != 1 {
			t.Fatalf("claims=%d runs=%d releases=%d", claims.Load(), runs.Load(), releases.Load())
		}
	})
}

func TestResearchSchedulerMarkerFailurePreventsDispatch(t *testing.T) {
	for _, markerErr := range []error{errors.New("marker rollback"), errors.New("commit acknowledgement uncertain")} {
		t.Run(markerErr.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var sends, tools, marks, finalized atomic.Int32
				store := schedulerStorage{mark: func(context.Context, ResearchLease) error { marks.Add(1); return markerErr }, finalize: func(_ context.Context, _ ResearchLease, r researchrun.Result, err error) error {
					finalized.Add(1)
					if !errors.Is(err, markerErr) || r.Produced {
						t.Errorf("marker outcome: %+v,%v", r, err)
					}
					return nil
				}}
				s := NewResearchScheduler(nil, schedulerRunner(func(ctx context.Context, r researchrun.Request) (researchrun.Result, error) {
					if err := r.BeforeTool(ctx); err != nil {
						return researchrun.Result{}, err
					}
					tools.Add(1)
					if err := r.BeforeModel(ctx); err != nil {
						return researchrun.Result{Settled: true}, err
					}
					sends.Add(1)
					return researchrun.Result{Produced: true, Settled: true}, nil
				}), store)
				if err := s.Check(t.Context()); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				s.Close()
				if sends.Load() != 0 || marks.Load() != 1 || tools.Load() != 1 || finalized.Load() != 1 {
					t.Fatalf("sends=%d marks=%d tools=%d finalized=%d", sends.Load(), marks.Load(), tools.Load(), finalized.Load())
				}
			})
		})
	}
}

func TestResearchSchedulerCloseCancelsAndJoinsFreshCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		cleanup := make(chan struct{})
		finish := make(chan struct{})
		closed := make(chan struct{})
		store := schedulerStorage{finalize: func(ctx context.Context, _ ResearchLease, r researchrun.Result, err error) error {
			if ctx.Err() != nil {
				t.Errorf("cleanup inherited cancellation: %v", ctx.Err())
			}
			if d, ok := ctx.Deadline(); !ok || !d.After(time.Now()) || d.Sub(time.Now()) > 15*time.Second {
				t.Errorf("cleanup is not fresh and finite: %v", d)
			}
			if !errors.Is(err, context.Canceled) || r.Settled {
				t.Errorf("cancel outcome: %+v,%v", r, err)
			}
			close(cleanup)
			<-finish
			return nil
		}}
		s := NewResearchScheduler(nil, schedulerRunner(func(ctx context.Context, _ researchrun.Request) (researchrun.Result, error) {
			close(started)
			<-ctx.Done()
			return researchrun.Result{}, ctx.Err()
		}), store)
		if err := s.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("Check did not start accepted work")
		}
		go func() { s.Close(); close(closed) }()
		<-cleanup
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close returned while owned finalization still uses storage")
		default:
		}
		close(finish)
		<-closed
		if err := s.Check(t.Context()); !errors.Is(err, ErrResearchAuthority) {
			t.Fatalf("closed scheduler admitted work: %v", err)
		}
		s.Close()
	})
}

func TestResearchSchedulerPanicFinalizesOnceAsUncertain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var finalized atomic.Int32
		store := schedulerStorage{finalize: func(ctx context.Context, _ ResearchLease, r researchrun.Result, err error) error {
			finalized.Add(1)
			if err == nil || r.Settled || ctx.Err() != nil {
				t.Errorf("panic outcome: %+v,%v ctx=%v", r, err, ctx.Err())
			}
			return nil
		}}
		s := NewResearchScheduler(nil, schedulerRunner(func(ctx context.Context, r researchrun.Request) (researchrun.Result, error) {
			if err := r.BeforeModel(ctx); err != nil {
				return researchrun.Result{}, err
			}
			panic("worker failed")
		}), store)
		if err := s.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		s.Close()
		if finalized.Load() != 1 {
			t.Fatalf("finalizations=%d", finalized.Load())
		}
	})
}

func TestResearchSchedulerClaimErrorNeverRuns(t *testing.T) {
	claimErr := errors.New("claim commit unknown")
	var runs, releases atomic.Int32
	store := schedulerStorage{claim: func(_ context.Context, l ResearchLease, _ ResearchDue) (*ResearchClaim, error) {
		return &ResearchClaim{Lease: l}, claimErr
	}, release: func(context.Context, ResearchLease) error { releases.Add(1); return nil }}
	s := NewResearchScheduler(nil, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
		runs.Add(1)
		return researchrun.Result{}, nil
	}), store)
	if err := s.Check(t.Context()); !errors.Is(err, claimErr) {
		t.Fatalf("claim ambiguity: %v", err)
	}
	s.Close()
	if runs.Load() != 0 || releases.Load() != 1 {
		t.Fatalf("runs=%d releases=%d", runs.Load(), releases.Load())
	}
}

func TestResearchSchedulerSaturationLeavesDueUnclaimed(t *testing.T) {
	var admits, claims, runs atomic.Int32
	store := schedulerStorage{due: func(context.Context) ([]ResearchDue, error) {
		return []ResearchDue{{AlertID: "one"}, {AlertID: "two"}, {AlertID: "three"}}, nil
	}, admit: func(context.Context) (*ResearchLease, error) { admits.Add(1); return nil, nil }, claim: func(context.Context, ResearchLease, ResearchDue) (*ResearchClaim, error) {
		claims.Add(1)
		return nil, nil
	}}
	s := NewResearchScheduler(nil, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
		runs.Add(1)
		return researchrun.Result{}, nil
	}), store)
	if err := s.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if admits.Load() != 1 || claims.Load() != 0 || runs.Load() != 0 {
		t.Fatalf("admit=%d claim=%d run=%d", admits.Load(), claims.Load(), runs.Load())
	}
}

func TestResearchSchedulerRechecksAuthorityAtEveryBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var authorized atomic.Int32
		var marks, sends, tools atomic.Int32
		store := schedulerStorage{authorize: func(context.Context, ResearchLease) error {
			if authorized.Add(1) == 4 {
				return ErrResearchAuthority
			}
			return nil
		}, mark: func(context.Context, ResearchLease) error { marks.Add(1); return nil }}
		s := NewResearchScheduler(nil, schedulerRunner(func(ctx context.Context, r researchrun.Request) (researchrun.Result, error) {
			if err := r.BeforeModel(ctx); err != nil {
				return researchrun.Result{}, err
			}
			sends.Add(1)
			if err := r.BeforeTool(ctx); err != nil {
				return researchrun.Result{}, err
			}
			tools.Add(1)
			if err := r.BeforeModel(ctx); err != nil {
				return researchrun.Result{Produced: true, Settled: true}, err
			}
			sends.Add(1)
			return researchrun.Result{Produced: true, Settled: true}, nil
		}), store)
		if err := s.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		s.Close()
		if sends.Load() != 1 || tools.Load() != 1 || marks.Load() != 1 {
			t.Fatalf("revoked sends=%d tools=%d marks=%d", sends.Load(), tools.Load(), marks.Load())
		}
	})
}

func TestResearchSchedulerRunOwnsAcceptedWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		started := make(chan struct{})
		finalized := make(chan struct{})
		exited := make(chan struct{})
		var once sync.Once
		store := schedulerStorage{finalize: func(context.Context, ResearchLease, researchrun.Result, error) error { close(finalized); return nil }}
		s := NewResearchScheduler(nil, schedulerRunner(func(ctx context.Context, _ researchrun.Request) (researchrun.Result, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return researchrun.Result{}, ctx.Err()
		}), store)
		go func() { s.Run(ctx); close(exited) }()
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("Run did not start accepted work")
		}
		cancel()
		<-exited
		select {
		case <-finalized:
		default:
			t.Fatal("Run returned before its owned worker finalized")
		}
	})
}

func TestResearchSchedulerRecoveryHasFiniteServiceContext(t *testing.T) {
	store := schedulerStorage{
		recover: func(ctx context.Context) error {
			if d, ok := ctx.Deadline(); !ok || !d.After(time.Now()) || d.Sub(time.Now()) > 15*time.Second {
				t.Errorf("expired-slot recovery has no finite cleanup budget: %v", d)
			}
			return errors.New("recovery incomplete")
		},
		due: func(context.Context) ([]ResearchDue, error) {
			t.Error("recovery failure proceeded to due work")
			return nil, nil
		},
	}
	s := NewResearchScheduler(nil, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
		t.Error("recovery failure dispatched inference")
		return researchrun.Result{}, nil
	}), store)
	if err := s.Check(t.Context()); err == nil {
		t.Fatal("recovery error was lost")
	}
	s.Close()
}

// With no AI provider, due runs wait: recovery still runs, nothing is
// listed, claimed or charged.
func TestResearchSchedulerWaitsWithoutAIProvider(t *testing.T) {
	off, err := llm.NewFromSettings(llm.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	var recovered atomic.Bool
	store := schedulerStorage{
		recover: func(context.Context) error { recovered.Store(true); return nil },
		due: func(context.Context) ([]ResearchDue, error) {
			t.Error("listed due runs with AI off")
			return nil, nil
		},
	}
	s := NewResearchScheduler(&app.Deps{LLM: off}, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
		t.Error("ran research with AI off")
		return researchrun.Result{}, nil
	}), store)
	defer s.Close()
	if err := s.Check(t.Context()); err != nil || !recovered.Load() {
		t.Fatalf("check: %v, recovered %v", err, recovered.Load())
	}
}

func TestResearchSchedulerCloseJoinsBlockedAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		admitting := make(chan struct{})
		checked := make(chan error, 1)
		store := schedulerStorage{admit: func(ctx context.Context) (*ResearchLease, error) {
			close(admitting)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		s := NewResearchScheduler(nil, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
			t.Error("shutdown dispatched inference")
			return researchrun.Result{}, nil
		}), store)
		go func() { checked <- s.Check(t.Context()) }()
		<-admitting
		s.Close()
		synctest.Wait()
		select {
		case err := <-checked:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("admission shutdown error: %v", err)
			}
		default:
			t.Fatal("Close returned before the blocked admission exited")
		}
	})
}

func TestResearchSchedulerCancelledWorkerRejectsBackgroundBoundaries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		var authorizations, marks atomic.Int32
		store := schedulerStorage{
			authorize: func(context.Context, ResearchLease) error { authorizations.Add(1); return nil },
			mark:      func(context.Context, ResearchLease) error { marks.Add(1); return nil },
		}
		s := NewResearchScheduler(nil, schedulerRunner(func(ctx context.Context, r researchrun.Request) (researchrun.Result, error) {
			close(started)
			<-ctx.Done()
			if err := r.BeforeModel(context.Background()); !errors.Is(err, context.Canceled) {
				t.Errorf("model boundary escaped lifetime: %v", err)
			}
			if err := r.BeforeTool(context.Background()); !errors.Is(err, context.Canceled) {
				t.Errorf("tool boundary escaped lifetime: %v", err)
			}
			return researchrun.Result{}, ctx.Err()
		}), store)
		if err := s.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		<-started
		s.Close()
		if authorizations.Load() != 1 || marks.Load() != 0 {
			t.Fatalf("cancelled callbacks touched storage: authorizations=%d markers=%d", authorizations.Load(), marks.Load())
		}
	})
}

func schedulerDBFixture(t *testing.T, count int) (*app.Deps, string, []ResearchDue) {
	t.Helper()
	db := storetest.Schema(t)
	user := storetest.User(t, db)
	d := &app.Deps{DB: db, Usage: usage.NewMeter(db, 32)}
	list := make([]ResearchDue, 0, count)
	for i := range count {
		a, err := Create(t.Context(), d, user, Input{Action: "research", Trigger: "schedule", Every: "day", At: "00:00", Prompt: "Review portfolio risks"})
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(time.Duration(i) * time.Second)
		if _, err := db.Exec(t.Context(), "update alerts set next_run_at=$2 where id=$1", a.ID, at); err != nil {
			t.Fatal(err)
		}
		list = append(list, ResearchDue{AlertID: a.ID, UserID: user, SlotAt: at})
	}
	return d, user, list
}

func schedulerPeer(t *testing.T, d *app.Deps) *app.Deps {
	t.Helper()
	config := d.DB.Config().Copy()
	config.ConnConfig.RuntimeParams["application_name"] = "research_scheduler_peer"
	db, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	peer := *d
	peer.DB, peer.Usage = db, usage.NewMeter(db, d.Usage.DefaultLimit())
	return &peer
}

type observedResearchStore struct {
	ResearchStorage
	finished chan error
}

func (s observedResearchStore) Finalize(ctx context.Context, lease ResearchLease, r researchrun.Result, err error) error {
	result := s.ResearchStorage.Finalize(ctx, lease, r, err)
	s.finished <- result
	return result
}

func schedulerSDKRunner(client *llm.Client) schedulerRunner {
	return func(ctx context.Context, req researchrun.Request) (researchrun.Result, error) {
		if err := req.BeforeModel(ctx); err != nil {
			return researchrun.Result{}, err
		}
		var produced bool
		answer, err := client.Agent(ctx, llm.AgentRequest{
			System: "Read-only research fixture", Messages: []llm.Message{{Role: "user", Text: req.Prompt}},
			Effort: llm.Low, MaxTokens: 4096, NoRetries: true,
			OnProviderEvent: func() { produced = true },
		}, nil)
		return researchrun.Result{Answer: answer, Produced: produced, Settled: err == nil}, err
	}
}

func awaitResearchCompletion(t *testing.T, finished <-chan error, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for range count {
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("research completion did not join")
		}
	}
}

func TestResearchSchedulerDBCrossInstanceAdmissionAndSameSlot(t *testing.T) {
	for _, count := range []int{3, 1} {
		t.Run(fmt.Sprintf("%d_due", count), func(t *testing.T) {
			d, user, due := schedulerDBFixture(t, count)
			peer := schedulerPeer(t, d)
			started := make(chan struct{}, 4)
			release := make(chan struct{})
			var releaseOnce sync.Once
			client, bedrock := llmtest.New(t, func(llmtest.Request) llmtest.Reply {
				started <- struct{}{}
				return llmtest.Reply{Text: "Research evidence is stable.", AfterFirst: func() { <-release }}
			})
			finished := make(chan error, 4)
			oneStore, twoStore := NewResearchStore(d), NewResearchStore(peer)
			one := NewResearchScheduler(d, schedulerSDKRunner(client), observedResearchStore{oneStore, finished})
			two := NewResearchScheduler(peer, schedulerSDKRunner(client), observedResearchStore{twoStore, finished})
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); one.Close(); two.Close() })
			checked := make(chan error, 2)
			go func() { checked <- one.Check(t.Context()) }()
			go func() { checked <- two.Check(t.Context()) }()
			for range 2 {
				if err := <-checked; err != nil {
					t.Fatal(err)
				}
			}
			want := min(count, 2)
			var runs, paid, slots, remaining, streak int
			if err := d.DB.QueryRow(t.Context(), `select
				(select count(*) from alert_runs where user_id=$1 and status='running'),
				(select coalesce(sum(credits),0) from ai_usage where user_id=$1),
				(select count(*) from research_worker_slots where run_id is not null),
				(select count(*) from alerts where user_id=$1 and next_run_at<=clock_timestamp()),
				(select sum(quota_streak) from alerts where user_id=$1)`, user).Scan(&runs, &paid, &slots, &remaining, &streak); err != nil {
				t.Fatal(err)
			}
			if runs != want || paid != want*2 || slots != want || remaining != count-want || streak != 0 {
				t.Fatalf("admission runs=%d paid=%d slots=%d due=%d streak=%d", runs, paid, slots, remaining, streak)
			}
			if count == 3 {
				var history int
				var at time.Time
				if err := d.DB.QueryRow(t.Context(), `select next_run_at,(select count(*) from alert_runs where alert_id=$1) from alerts where id=$1`, due[2].AlertID).Scan(&at, &history); err != nil {
					t.Fatal(err)
				}
				if history != 0 || !at.Equal(due[2].SlotAt) {
					t.Fatalf("third due slot changed despite saturation: %v/%d", at, history)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			for range want {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("admitted SDK call did not reach fake Bedrock")
				}
			}
			// The other instance cannot recover this owner's live lease.
			if err := twoStore.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := d.DB.QueryRow(t.Context(), "select count(*) from alert_runs where status='running'").Scan(&runs); err != nil || runs != want {
				t.Fatalf("live lease recovered: %d,%v", runs, err)
			}
			releaseOnce.Do(func() { close(release) })
			awaitResearchCompletion(t, finished, want)
			one.Close()
			two.Close()
			if len(bedrock.Requests()) != want {
				t.Fatalf("same slot inferred more than once or global slots exceeded: %d requests", len(bedrock.Requests()))
			}
			var reports, distinctSlots int
			if err := d.DB.QueryRow(t.Context(), `select count(*),count(distinct (alert_id,slot_at)) from alert_runs where user_id=$1 and status='ok'`, user).Scan(&reports, &distinctSlots); err != nil {
				t.Fatal(err)
			}
			if reports != want || distinctSlots != want {
				t.Fatalf("once-per-slot reports=%d distinct=%d", reports, distinctSlots)
			}
		})
	}
}

func TestResearchSchedulerDBSaturationDoesNotCountAsQuota(t *testing.T) {
	d, user, _ := schedulerDBFixture(t, 1)
	if _, err := d.DB.Exec(t.Context(), "update users set credit_limit=0 where id=$1", user); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(t.Context(), "update alerts set quota_streak=2 where user_id=$1", user); err != nil {
		t.Fatal(err)
	}
	store := NewResearchStore(d)
	for range 2 {
		if lease, err := store.Admit(t.Context()); err != nil || lease == nil {
			t.Fatalf("occupy global slot: %v %v", lease, err)
		}
	}
	var calls atomic.Int32
	s := NewResearchScheduler(d, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
		calls.Add(1)
		return researchrun.Result{}, nil
	}), store)
	if err := s.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var status string
	var streak, runs, paid int
	if err := d.DB.QueryRow(t.Context(), `select status,quota_streak,(select count(*) from alert_runs where user_id=$1),(select coalesce(sum(credits),0) from ai_usage where user_id=$1) from alerts where user_id=$1`, user).Scan(&status, &streak, &runs, &paid); err != nil {
		t.Fatal(err)
	}
	if status != "active" || streak != 2 || runs != 0 || paid != 0 || calls.Load() != 0 {
		t.Fatalf("saturation treated as quota: %s/%d runs=%d paid=%d model=%d", status, streak, runs, paid, calls.Load())
	}
}

func TestResearchSchedulerDBThreeQuotaRefusalsPauseWithoutAutoResume(t *testing.T) {
	d, user, due := schedulerDBFixture(t, 1)
	if _, err := d.DB.Exec(t.Context(), "update users set credit_limit=0 where id=$1", user); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := NewResearchScheduler(d, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
		calls.Add(1)
		return researchrun.Result{}, nil
	}), NewResearchStore(d))
	defer s.Close()
	for i := 1; i <= 3; i++ {
		at := due[0].SlotAt.Add(time.Duration(i) * time.Second)
		if _, err := d.DB.Exec(t.Context(), "update alerts set next_run_at=$2 where id=$1", due[0].AlertID, at); err != nil {
			t.Fatal(err)
		}
		if err := s.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		var status string
		var streak, runs, cost int
		var next time.Time
		if err := d.DB.QueryRow(t.Context(), `select status,quota_streak,next_run_at,(select count(*) from alert_runs where alert_id=$1),(select coalesce(sum(cost),0) from alert_runs where alert_id=$1) from alerts where id=$1`, due[0].AlertID).Scan(&status, &streak, &next, &runs, &cost); err != nil {
			t.Fatal(err)
		}
		wantStatus := "active"
		if i == 3 {
			wantStatus = "paused"
		}
		if status != wantStatus || streak != i || runs != i || cost != 0 || !next.After(time.Now()) {
			t.Fatalf("quota refusal %d: %s/%d history=%d cost=%d next=%v", i, status, streak, runs, cost, next)
		}
	}
	if _, err := d.DB.Exec(t.Context(), "update users set credit_limit=32 where id=$1", user); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(t.Context(), "update alerts set next_run_at=clock_timestamp()-interval '1 second' where id=$1", due[0].AlertID); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	var runs, paid int
	if err := d.DB.QueryRow(t.Context(), `select status,(select count(*) from alert_runs where user_id=$1),(select coalesce(sum(credits),0) from ai_usage where user_id=$1) from alerts where id=$2`, user, due[0].AlertID).Scan(&status, &runs, &paid); err != nil {
		t.Fatal(err)
	}
	if status != "paused" || runs != 3 || paid != 0 || calls.Load() != 0 {
		t.Fatalf("quota recovery auto-resumed/inferred: %s runs=%d paid=%d calls=%d", status, runs, paid, calls.Load())
	}
}

// Owner-only mode is not a quota: a non-admin's slot is recorded with that
// reason, uncharged, and never counts toward pausing the automation.
func TestResearchSchedulerDBOwnerOnlyNeverPauses(t *testing.T) {
	d, user, due := schedulerDBFixture(t, 1)
	d.Usage.OwnerOnly = func() bool { return true }
	if _, err := d.DB.Exec(t.Context(), "update alerts set quota_streak=2 where user_id=$1", user); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := NewResearchScheduler(d, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
		calls.Add(1)
		return researchrun.Result{}, nil
	}), NewResearchStore(d))
	defer s.Close()
	if err := s.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, msg string
	var streak, cost int
	var next time.Time
	if err := d.DB.QueryRow(t.Context(), `select a.status,a.quota_streak,a.next_run_at,r.error,r.cost from alerts a join alert_runs r on r.alert_id=a.id where a.id=$1`,
		due[0].AlertID).Scan(&status, &streak, &next, &msg, &cost); err != nil {
		t.Fatal(err)
	}
	if status != "active" || streak != 2 || msg != "AI is in owner-only mode on this server" || cost != 0 || !next.After(time.Now()) || calls.Load() != 0 {
		t.Fatalf("owner-only counted as quota: %s/%d %q cost=%d next=%v calls=%d", status, streak, msg, cost, next, calls.Load())
	}
}

func TestResearchSchedulerDBClaimFailureRollsBackWithoutQuota(t *testing.T) {
	d, user, due := schedulerDBFixture(t, 1)
	if _, err := d.DB.Exec(t.Context(), `create function reject_test_research_run() returns trigger language plpgsql as $$ begin raise exception 'injected run insert failure'; end $$;
		create trigger reject_test_research_run before insert on alert_runs for each row execute function reject_test_research_run()`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(t.Context(), "update alerts set quota_streak=2 where user_id=$1", user); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := NewResearchScheduler(d, schedulerRunner(func(context.Context, researchrun.Request) (researchrun.Result, error) {
		calls.Add(1)
		return researchrun.Result{}, nil
	}), NewResearchStore(d))
	if err := s.Check(t.Context()); err == nil {
		t.Fatal("injected claim storage failure was lost")
	}
	s.Close()
	var status string
	var next time.Time
	var streak, runs, paid, slots int
	if err := d.DB.QueryRow(t.Context(), `select status,quota_streak,next_run_at,(select count(*) from alert_runs),(select coalesce(sum(credits),0) from ai_usage where user_id=$1),(select count(*) from research_worker_slots where run_id is not null) from alerts where user_id=$1`, user).Scan(&status, &streak, &next, &runs, &paid, &slots); err != nil {
		t.Fatal(err)
	}
	if status != "active" || streak != 2 || !next.Equal(due[0].SlotAt) || runs != 0 || paid != 0 || slots != 0 || calls.Load() != 0 {
		t.Fatalf("partial/false-quota claim: %s/%d next=%v runs=%d paid=%d slots=%d calls=%d", status, streak, next, runs, paid, slots, calls.Load())
	}
}

func claimSchedulerDB(t *testing.T, d *app.Deps, due ResearchDue, store ResearchStorage) *ResearchClaim {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	lease, err := store.Admit(ctx)
	if err != nil || lease == nil {
		t.Fatalf("admission: %v %v", lease, err)
	}
	claim, err := store.Claim(ctx, *lease, due)
	if err != nil || claim == nil {
		t.Fatalf("paid claim: %v %v", claim, err)
	}
	return claim
}

func expireSchedulerLease(t *testing.T, d *app.Deps, lease ResearchLease) {
	t.Helper()
	if _, err := d.DB.Exec(t.Context(), `update research_worker_slots set admitted_at=x.at,deadline=x.at+interval '120 seconds',lease_until=x.at+interval '130 seconds'
		from (select clock_timestamp()-interval '140 seconds' as at) x where slot=$1 and fence=$2 and run_id=$3`, lease.Slot, lease.Fence, lease.RunID); err != nil {
		t.Fatal(err)
	}
}

func moveSchedulerChargeToPriorDay(t *testing.T, d *app.Deps, user, runID string) time.Time {
	t.Helper()
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	if _, err := d.DB.Exec(t.Context(), "update ai_usage set day=$2 where user_id=$1", user, day); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(t.Context(), "update alert_runs set charged_day=$2 where id=$1", runID, day); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(t.Context(), "insert into ai_usage(user_id,day,credits) values($1,$2,6)", user, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	return day
}

func TestResearchSchedulerDBExpiredRecoveryAndStaleFence(t *testing.T) {
	for _, attempted := range []bool{false, true} {
		t.Run(fmt.Sprintf("attempted_%t", attempted), func(t *testing.T) {
			d, user, due := schedulerDBFixture(t, 1)
			store, peer := NewResearchStore(d), NewResearchStore(schedulerPeer(t, d))
			old := claimSchedulerDB(t, d, due[0], store)
			if attempted {
				if err := store.MarkAttempt(t.Context(), old.Lease); err != nil {
					t.Fatal(err)
				}
			}
			day := moveSchedulerChargeToPriorDay(t, d, user, old.Lease.RunID)
			if err := peer.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := d.DB.QueryRow(t.Context(), "select status from alert_runs where id=$1", old.Lease.RunID).Scan(&status); err != nil || status != "running" {
				t.Fatalf("live foreign lease recovered: %s %v", status, err)
			}
			expireSchedulerLease(t, d, old.Lease)
			if lease, err := peer.Admit(t.Context()); err != nil || lease == nil {
				t.Fatalf("second slot: %v %v", lease, err)
			} else {
				if lease.Slot == old.Lease.Slot {
					t.Fatal("expired paid slot reassigned before accounting")
				}
				if err := peer.Release(t.Context(), *lease); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			for range 6 {
				wg.Go(func() {
					if err := peer.Recover(t.Context()); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			var refunded bool
			var oldPaid, newPaid int
			if err := d.DB.QueryRow(t.Context(), `select status,refunded,(select credits from ai_usage where user_id=$2 and day=$3),(select credits from ai_usage where user_id=$2 and day=$4) from alert_runs where id=$1`, old.Lease.RunID, user, day, day.AddDate(0, 0, 1)).Scan(&status, &refunded, &oldPaid, &newPaid); err != nil {
				t.Fatal(err)
			}
			wantPaid := 0
			if attempted {
				wantPaid = 2
			}
			if status != "interrupted" || refunded == attempted || oldPaid != wantPaid || newPaid != 6 {
				t.Fatalf("recovery status=%s refunded=%t original=%d today=%d", status, refunded, oldPaid, newPaid)
			}
			var at time.Time
			if err := d.DB.QueryRow(t.Context(), `update alerts set next_run_at=clock_timestamp()-interval '1 second' where id=$1 returning next_run_at`, due[0].AlertID).Scan(&at); err != nil {
				t.Fatal(err)
			}
			fresh := claimSchedulerDB(t, d, ResearchDue{AlertID: due[0].AlertID, UserID: user, SlotAt: at}, peer)
			if fresh.Lease.Slot != old.Lease.Slot || fresh.Lease.Fence <= old.Lease.Fence {
				t.Fatalf("replacement failed fence ordering: old=%+v new=%+v", old.Lease, fresh.Lease)
			}
			if err := store.Finalize(t.Context(), old.Lease, researchrun.Result{Settled: true}, errors.New("stale refund")); err != nil {
				t.Fatal(err)
			}
			if err := store.Finalize(t.Context(), old.Lease, researchrun.Result{Answer: "stale report", Produced: true, Settled: true}, nil); err != nil {
				t.Fatal(err)
			}
			if err := store.Release(t.Context(), old.Lease); err != nil {
				t.Fatal(err)
			}
			if err := store.Authorize(t.Context(), old.Lease); !errors.Is(err, ErrResearchAuthority) {
				t.Fatalf("stale authority accepted: %v", err)
			}
			if err := store.MarkAttempt(t.Context(), old.Lease); !errors.Is(err, ErrResearchAuthority) {
				t.Fatalf("stale attempt accepted: %v", err)
			}
			var current string
			var reports, paid int
			if err := d.DB.QueryRow(t.Context(), `select run_id::text,(select count(*) from chat_messages),(select credits from ai_usage where user_id=$2 and day=$3) from research_worker_slots where slot=$1`, fresh.Lease.Slot, user, day.AddDate(0, 0, 1)).Scan(&current, &reports, &paid); err != nil {
				t.Fatal(err)
			}
			if current != fresh.Lease.RunID || reports != 0 || paid != 8 {
				t.Fatalf("stale owner changed replacement: run=%s reports=%d paid=%d", current, reports, paid)
			}
			if err := peer.Finalize(t.Context(), fresh.Lease, researchrun.Result{}, errors.New("pre-attempt cleanup")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResearchSchedulerDBKnownSettledRefundOnly(t *testing.T) {
	cases := []struct {
		name                                 string
		attempted, produced, settled, refund bool
	}{
		{"before_first_model", false, false, false, true},
		{"settled_no_raw", true, false, true, true},
		{"private_or_malformed_raw", true, true, true, false},
		{"uncertain_after_attempt", true, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, user, due := schedulerDBFixture(t, 1)
			store := NewResearchStore(d)
			claim := claimSchedulerDB(t, d, due[0], store)
			if tc.attempted {
				if err := store.MarkAttempt(t.Context(), claim.Lease); err != nil {
					t.Fatal(err)
				}
			}
			day := moveSchedulerChargeToPriorDay(t, d, user, claim.Lease.RunID)
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					if err := store.Finalize(t.Context(), claim.Lease, researchrun.Result{Produced: tc.produced, Settled: tc.settled}, errors.New("provider outcome")); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			var refunded bool
			var oldPaid, newPaid, terminal int
			if err := d.DB.QueryRow(t.Context(), `select refunded,(select credits from ai_usage where user_id=$2 and day=$3),(select credits from ai_usage where user_id=$2 and day=$4),(select count(*) from alert_runs where id=$1 and status='failed') from alert_runs where id=$1`, claim.Lease.RunID, user, day, day.AddDate(0, 0, 1)).Scan(&refunded, &oldPaid, &newPaid, &terminal); err != nil {
				t.Fatal(err)
			}
			want := 2
			if tc.refund {
				want = 0
			}
			if refunded != tc.refund || oldPaid != want || newPaid != 6 || terminal != 1 {
				t.Fatalf("refund=%t original=%d today=%d terminals=%d", refunded, oldPaid, newPaid, terminal)
			}
		})
	}
}

type lostMarkerAcknowledgement struct{ ResearchStorage }

func (s lostMarkerAcknowledgement) MarkAttempt(ctx context.Context, lease ResearchLease) error {
	if err := s.ResearchStorage.MarkAttempt(ctx, lease); err != nil {
		return err
	}
	// Simulate loss of the acknowledgement at the storage boundary after the
	// actual PostgreSQL marker committed. This is not a wire-level PG proxy.
	return errors.New("attempt marker acknowledgement lost")
}

func TestResearchSchedulerDBAttemptFailureMakesZeroSDKRequests(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("marker_committed_%t", committed), func(t *testing.T) {
			d, user, _ := schedulerDBFixture(t, 1)
			var storage ResearchStorage = NewResearchStore(d)
			if committed {
				storage = lostMarkerAcknowledgement{storage}
			} else {
				if _, err := d.DB.Exec(t.Context(), `create function reject_test_attempt() returns trigger language plpgsql as $$ begin raise exception 'injected marker write failure'; end $$;
					create trigger reject_test_attempt before update of attempted on alert_runs for each row execute function reject_test_attempt()`); err != nil {
					t.Fatal(err)
				}
			}
			client, bedrock := llmtest.New(t, llmtest.Queue(t)) // any SDK request is a test failure
			finished := make(chan error, 1)
			s := NewResearchScheduler(d, schedulerSDKRunner(client), observedResearchStore{storage, finished})
			defer s.Close()
			if err := s.Check(t.Context()); err != nil {
				t.Fatal(err)
			}
			awaitResearchCompletion(t, finished, 1)
			s.Close()
			var attempted, refunded bool
			var paid int
			if err := d.DB.QueryRow(t.Context(), `select attempted,refunded,(select credits from ai_usage where user_id=$1) from alert_runs where user_id=$1`, user).Scan(&attempted, &refunded, &paid); err != nil {
				t.Fatal(err)
			}
			wantPaid := 0
			if committed {
				wantPaid = 2
			}
			if len(bedrock.Requests()) != 0 || attempted != committed || refunded == committed || paid != wantPaid {
				t.Fatalf("marker attempted=%t refunded=%t paid=%d sdk=%d", attempted, refunded, paid, len(bedrock.Requests()))
			}
		})
	}
}

func TestResearchSchedulerDBLateSDKWorkerCannotTouchReplacement(t *testing.T) {
	d, user, due := schedulerDBFixture(t, 1)
	store, peer := NewResearchStore(d), NewResearchStore(schedulerPeer(t, d))
	streaming := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	client, bedrock := llmtest.New(t, func(llmtest.Request) llmtest.Reply {
		return llmtest.Reply{Text: "Late old report.", AfterFirst: func() { close(streaming); <-release }}
	})
	finished := make(chan error, 1)
	s := NewResearchScheduler(d, schedulerSDKRunner(client), observedResearchStore{store, finished})
	t.Cleanup(func() { once.Do(func() { close(release) }); s.Close() })
	if err := s.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case <-streaming:
	case <-ctx.Done():
		t.Fatal("old worker did not stream raw SDK data")
	}
	var old ResearchLease
	if err := d.DB.QueryRow(t.Context(), `select slot,fence,run_id::text,admitted_at,deadline,lease_until from research_worker_slots where run_id is not null`).Scan(&old.Slot, &old.Fence, &old.RunID, &old.AdmittedAt, &old.Deadline, &old.LeaseUntil); err != nil {
		t.Fatal(err)
	}
	// A suspended/partitioned former worker is still in flight when authority
	// expires at the DB. Recovery and replacement happen before it returns.
	expireSchedulerLease(t, d, old)
	if err := peer.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	var at time.Time
	if err := d.DB.QueryRow(t.Context(), `update alerts set next_run_at=clock_timestamp()-interval '1 second' where id=$1 returning next_run_at`, due[0].AlertID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	fresh := claimSchedulerDB(t, d, ResearchDue{AlertID: due[0].AlertID, UserID: user, SlotAt: at}, peer)
	once.Do(func() { close(release) })
	awaitResearchCompletion(t, finished, 1)
	s.Close()
	var current, status string
	var reports, paid int
	if err := d.DB.QueryRow(t.Context(), `select run_id::text,(select status from alert_runs where id=$2),(select count(*) from chat_messages),(select credits from ai_usage where user_id=$3) from research_worker_slots where slot=$1`, fresh.Lease.Slot, fresh.Lease.RunID, user).Scan(&current, &status, &reports, &paid); err != nil {
		t.Fatal(err)
	}
	if current != fresh.Lease.RunID || status != "running" || reports != 0 || paid != 4 || len(bedrock.Requests()) != 1 {
		t.Fatalf("late worker changed replacement: run=%s status=%s reports=%d paid=%d SDK=%d", current, status, reports, paid, len(bedrock.Requests()))
	}
	if err := peer.MarkAttempt(t.Context(), fresh.Lease); err != nil {
		t.Fatal(err)
	}
	if err := peer.Finalize(t.Context(), fresh.Lease, researchrun.Result{Answer: "Current owned report.", Produced: true, Settled: true}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestResearchSchedulerDBRevocationStopsNextBoundaryAndPublication(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled_%t", disabled), func(t *testing.T) {
			d, user, due := schedulerDBFixture(t, 1)
			streaming := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			client, bedrock := llmtest.New(t, func(llmtest.Request) llmtest.Reply {
				return llmtest.Reply{Text: "Evidence report.", AfterFirst: func() { close(streaming); <-release }}
			})
			base := schedulerSDKRunner(client)
			var effects atomic.Int32
			runner := schedulerRunner(func(ctx context.Context, r researchrun.Request) (researchrun.Result, error) {
				result, err := base(ctx, r)
				if err == nil {
					err = r.BeforeTool(context.Background())
					if err == nil {
						effects.Add(1)
					}
				}
				return result, err
			})
			finished := make(chan error, 1)
			s := NewResearchScheduler(d, runner, observedResearchStore{NewResearchStore(d), finished})
			t.Cleanup(func() { once.Do(func() { close(release) }); s.Close() })
			if err := s.Check(t.Context()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			select {
			case <-streaming:
			case <-ctx.Done():
				t.Fatal("model did not produce its first raw event")
			}
			if disabled {
				if _, err := d.DB.Exec(t.Context(), "update users set disabled_at=clock_timestamp() where id=$1", user); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := d.DB.Exec(t.Context(), "update alerts set status='cancelled' where id=$1", due[0].AlertID); err != nil {
					t.Fatal(err)
				}
			}
			once.Do(func() { close(release) })
			awaitResearchCompletion(t, finished, 1)
			s.Close()
			var status string
			var paid, reports, eligible int
			if err := d.DB.QueryRow(t.Context(), `select status,(select credits from ai_usage where user_id=$1),(select count(*) from chat_messages),(select count(*) from alert_runs where notification_eligible) from alert_runs where user_id=$1`, user).Scan(&status, &paid, &reports, &eligible); err != nil {
				t.Fatal(err)
			}
			if status != "cancelled" || paid != 2 || reports != 0 || eligible != 0 || effects.Load() != 0 || len(bedrock.Requests()) != 1 {
				t.Fatalf("revoked work leaked: %s paid=%d reports=%d eligible=%d effects=%d SDK=%d", status, paid, reports, eligible, effects.Load(), len(bedrock.Requests()))
			}
		})
	}
}

func TestResearchSchedulerDBOwnerLockConsumesAdmissionDeadline(t *testing.T) {
	d, user, due := schedulerDBFixture(t, 1)
	holder, err := d.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	if _, err := holder.Exec(t.Context(), "select id from users where id=$1 for no key update", user); err != nil {
		t.Fatal(err)
	}
	client, bedrock := llmtest.New(t, llmtest.Queue(t))
	s := NewResearchScheduler(d, schedulerSDKRunner(client), NewResearchStore(d))
	defer s.Close()
	service, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	checked := make(chan error, 1)
	go func() { checked <- s.Check(service) }()
	// Observe an actual PostgreSQL owner-row wait, then keep it held until the
	// original caller budget expires. It must not open a fresh model window.
	for {
		var waiting bool
		if err := d.DB.QueryRow(t.Context(), `select exists(select 1 from pg_stat_activity where datname=current_database() and wait_event_type='Lock' and query like '%select disabled_at is null from users where id=%for no key update%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-checked:
			t.Fatalf("claim never waited for owner: %v", err)
		case <-service.Done():
			t.Fatal("owner lock wait was not observed")
		case <-time.After(time.Millisecond):
		}
	}
	var storedDeadline, admitted time.Time
	if err := d.DB.QueryRow(t.Context(), "select deadline,admitted_at from research_worker_slots where run_id is not null").Scan(&storedDeadline, &admitted); err != nil {
		t.Fatal(err)
	}
	var databaseNow time.Time
	if err := d.DB.QueryRow(t.Context(), "select clock_timestamp()").Scan(&databaseNow); err != nil {
		t.Fatal(err)
	}
	// Database lease authority is independent of the shorter local operation
	// budget. The held owner wait below must still expire at service's deadline.
	if admitted.After(databaseNow) || databaseNow.Sub(admitted) > time.Second || storedDeadline.Sub(admitted) != 120*time.Second {
		t.Fatalf("lease is not database-clock acquisition authority: persisted=%v database=%v admitted=%v", storedDeadline, databaseNow, admitted)
	}
	<-service.Done()
	select {
	case err := <-checked:
		if err == nil {
			t.Fatal("expired lock wait succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expired admission did not join")
	}
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var slots, runs, paid int
	var next time.Time
	if err := d.DB.QueryRow(t.Context(), `select next_run_at,(select count(*) from research_worker_slots where run_id is not null),(select count(*) from alert_runs),(select coalesce(sum(credits),0) from ai_usage where user_id=$2) from alerts where id=$1`, due[0].AlertID, user).Scan(&next, &slots, &runs, &paid); err != nil {
		t.Fatal(err)
	}
	if !next.Equal(due[0].SlotAt) || slots != 0 || runs != 0 || paid != 0 || len(bedrock.Requests()) != 0 {
		t.Fatalf("expired wait claimed/paid/inferred: next=%v slots=%d runs=%d paid=%d SDK=%d", next, slots, runs, paid, len(bedrock.Requests()))
	}
}
