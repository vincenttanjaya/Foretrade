package alerts

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/researchrun"
)

const researchCleanupTimeout = 10 * time.Second

// ResearchScheduler owns each admission operation from before its first DB
// wait through worker finalization. Close prevents additions, cancels those
// operations and joins them before its caller may close the database.
type ResearchScheduler struct {
	d                *app.Deps // AIOff: due runs wait
	runner           researchrun.Runner
	store            ResearchStorage
	mu               sync.Mutex // protects closed, checking and the operation cancellation set
	closed, checking bool
	stopped          chan struct{}
	next             uint64
	operations       map[uint64]context.CancelFunc
	workers          sync.WaitGroup
}

func NewResearchScheduler(d *app.Deps, runner researchrun.Runner, store ResearchStorage) *ResearchScheduler {
	return &ResearchScheduler{d: d, runner: runner, store: store, stopped: make(chan struct{}), operations: make(map[uint64]context.CancelFunc)}
}

// Run is process-scoped: ctx is the service lifetime, never an HTTP request.
// Returning also joins every admitted worker and its bounded cleanup.
func (s *ResearchScheduler) Run(ctx context.Context) {
	defer s.Close()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.Check(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, ErrResearchAuthority) {
			slog.Error("research schedule check", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.stopped:
			return
		case <-ticker.C:
		}
	}
}

// Check recovers expired leases before considering due work. Admission and
// claim are synchronous; only already admitted, paid work starts a goroutine.
// The supplied context must remain alive for the accepted worker lifetime.
func (s *ResearchScheduler) Check(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrResearchAuthority
	}
	if s.checking {
		s.mu.Unlock()
		return nil
	}
	s.checking = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.checking = false; s.mu.Unlock() }()
	if s.store == nil || s.runner == nil {
		return errors.New("research scheduler dependencies unavailable")
	}
	checkCtx, done, err := s.own(ctx, time.Time{})
	if err != nil {
		return err
	}
	defer done()
	recoveryCtx, cancelRecovery := context.WithTimeout(checkCtx, researchCleanupTimeout)
	err = s.store.Recover(recoveryCtx)
	cancelRecovery()
	if err != nil || s.d.AIOff() { // no AI provider: due runs wait, uncharged
		return err
	}
	list, err := s.store.Due(checkCtx)
	if err != nil {
		return err
	}
	for _, due := range list {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		// Start the total budget before Admit: pool and lock waits consume it.
		available, err := s.admit(ctx, due)
		if err != nil {
			return err
		}
		if !available {
			break
		}
	}
	return nil
}

func (s *ResearchScheduler) admit(ctx context.Context, due ResearchDue) (bool, error) {
	workCtx, done, err := s.own(ctx, time.Now().Add(researchrun.Deadline))
	if err != nil {
		return false, err
	}
	lease, err := s.store.Admit(workCtx)
	if err != nil {
		if lease != nil {
			err = errors.Join(err, s.release(workCtx, *lease))
		}
		done()
		return false, err
	}
	if lease == nil {
		done()
		return false, nil
	}
	// Database lease timestamps belong only to PostgreSQL's clock. Preserve
	// the original prospective local budget through admission and cleanup.
	localDeadline, _ := workCtx.Deadline()
	lease.LocalDeadline = localDeadline
	lease.LocalAdmittedAt = localDeadline.Add(-researchrun.Deadline)
	leaseCtx, cancel := context.WithCancel(workCtx)
	finish := func() { cancel(); done() }
	if err := leaseCtx.Err(); err != nil {
		err = errors.Join(err, s.release(leaseCtx, *lease))
		finish()
		return false, err
	}
	claim, err := s.store.Claim(leaseCtx, *lease, due)
	if err != nil {
		err = errors.Join(err, s.release(leaseCtx, *lease))
		finish()
		return false, err
	}
	if claim == nil {
		err = s.release(leaseCtx, *lease)
		finish()
		return true, err
	}
	if claim.Lease.Slot != lease.Slot || claim.Lease.Fence != lease.Fence || claim.Lease.RunID != lease.RunID {
		err = errors.Join(ErrResearchAuthority, s.release(leaseCtx, *lease))
		finish()
		return false, err
	}
	// Claim's request is trusted storage data. Bind callbacks and deadlines here
	// rather than accepting callbacks or a renewed budget from its contents.
	deadline, _ := leaseCtx.Deadline()
	claim.Request.RunID = lease.RunID
	claim.Request.AdmittedAt = lease.LocalAdmittedAt
	claim.Request.Deadline = deadline
	claim.Request.BeforeModel = func(ctx context.Context) error { return s.boundary(ctx, leaseCtx, *lease, true) }
	claim.Request.BeforeTool = func(ctx context.Context) error { return s.boundary(ctx, leaseCtx, *lease, false) }
	go s.runPaid(leaseCtx, *lease, claim.Request, finish)
	return true, nil
}

func (s *ResearchScheduler) boundary(ctx, workCtx context.Context, lease ResearchLease, attempt bool) error {
	if err := workCtx.Err(); err != nil {
		return err
	}
	deadline, _ := workCtx.Deadline()
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	stop := context.AfterFunc(workCtx, cancel)
	defer stop()
	defer cancel()
	if err := callCtx.Err(); err != nil {
		return err
	}
	if err := s.store.Authorize(callCtx, lease); err != nil {
		return err
	}
	if err := workCtx.Err(); err != nil {
		return err
	}
	if err := callCtx.Err(); err != nil {
		return err
	}
	if attempt {
		if err := s.store.MarkAttempt(callCtx, lease); err != nil {
			return err
		}
	}
	if err := workCtx.Err(); err != nil {
		return err
	}
	return callCtx.Err()
}

func (s *ResearchScheduler) runPaid(ctx context.Context, lease ResearchLease, request researchrun.Request, done func()) {
	var result researchrun.Result
	var runErr error
	defer done()
	defer func() {
		if p := recover(); p != nil {
			slog.Error("research runner panic", "run", lease.RunID, "panic", p)
			runErr = errors.New("Research could not complete.")
			result.Settled = false // production after a recorded attempt is unknown
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), researchCleanupTimeout)
		defer cancel()
		if err := s.store.Finalize(cleanup, lease, result, runErr); err != nil {
			// The durable identity remains recoverable; never repeat inference.
			slog.Error("research finalization", "run", lease.RunID, "err", err)
		}
	}()
	if runErr = ctx.Err(); runErr != nil {
		return
	}
	if runErr = s.store.Authorize(ctx, lease); runErr != nil {
		return
	}
	if runErr = ctx.Err(); runErr != nil {
		return
	}
	result, runErr = s.runner.Run(ctx, request)
}

func (s *ResearchScheduler) release(ctx context.Context, lease ResearchLease) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), researchCleanupTimeout)
	defer cancel()
	return s.store.Release(cleanup, lease)
}

// own registers before any storage call, so Close also joins an admission
// blocked in pool/row-lock acquisition. Add and the close transition share mu.
func (s *ResearchScheduler) own(ctx context.Context, deadline time.Time) (context.Context, func(), error) {
	var cancel context.CancelFunc
	if deadline.IsZero() {
		ctx, cancel = context.WithCancel(ctx)
	} else {
		ctx, cancel = context.WithDeadline(ctx, deadline)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return nil, nil, ErrResearchAuthority
	}
	s.next++
	id := s.next
	s.operations[id] = cancel
	s.workers.Add(1)
	s.mu.Unlock()
	return ctx, func() { cancel(); s.mu.Lock(); delete(s.operations, id); s.mu.Unlock(); s.workers.Done() }, nil
}

func (s *ResearchScheduler) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stopped)
		for _, cancel := range s.operations {
			cancel()
		}
	}
	s.mu.Unlock()
	s.workers.Wait()
}
