package signals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// The refresh state and the read cache (gap round). A refresh that keeps
// failing retries on a ceiling, the rationale model is called at most
// modelCallsPerDay times a UTC day, and both survive a restart in the one
// signals_refresh row. GET /api/signals computes the shown board, its exits
// and the track record once a minute per process instead of on every read.

// modelCallsPerDay caps the board's rationale model calls per UTC day, across
// processes; past it a board keeps the Go rationales.
const modelCallsPerDay = 12

// viewTTL is how long a computed read is served: a board that stops
// validating shows for at most this long.
var viewTTL = time.Minute

// validations counts validBoard calls (tests read it).
var validations atomic.Int64

// memoTTL is how long an older board's validation is reused (gap round G22):
// the legacy history may lag an older board's invalidation by up to this
// long. The shown board is validated on every recompute; Forget and a stored
// refresh clear it.
var memoTTL = 15 * time.Minute

// memoValid is one older board's remembered validation.
type memoValid struct {
	valid bool
	at    time.Time
}

// refreshState is the signals_refresh row, mirrored in the flight for GET.
type refreshState struct {
	Failures    int        // failed refreshes in a row
	LastError   string     // the last failure's reason, first 200 characters
	LastErrorAt *time.Time //
	RetryAt     *time.Time // when Run retries after the last failure
	ModelDay    string     // the UTC day ModelCalls counts, YYYY-MM-DD
	ModelCalls  int
}

// Refresh is GET /api/signals' refresh object: running (a refresh is
// computing), retrying (the last one failed; the next starts at retryAt),
// due (the shown board needs its successor) or idle.
type Refresh struct {
	State       string     `json:"state"`
	Failures    int        `json:"failures"`
	LastErrorAt *time.Time `json:"lastErrorAt"`
	RetryAt     *time.Time `json:"retryAt"` // null unless retrying
}

func loadState(ctx context.Context, db *pgxpool.Pool) (refreshState, error) {
	var st refreshState
	var lastErr *string
	var day *time.Time
	err := db.QueryRow(ctx, `select failures, last_error, last_error_at, retry_at, model_day, model_calls from signals_refresh`).
		Scan(&st.Failures, &lastErr, &st.LastErrorAt, &st.RetryAt, &day, &st.ModelCalls)
	if errors.Is(err, pgx.ErrNoRows) {
		return refreshState{}, nil
	}
	if err != nil {
		return refreshState{}, fmt.Errorf("signals: refresh state: %w", err)
	}
	if lastErr != nil {
		st.LastError = *lastErr
	}
	if day != nil {
		st.ModelDay = day.Format(time.DateOnly)
	}
	return st, nil
}

// failed records the failures in a row and the retry time, in the row and the flight.
func (f *flight) failed(ctx context.Context, db *pgxpool.Pool, failures int, cause error, retryAt time.Time) {
	now := time.Now().UTC()
	msg := []rune(cause.Error())
	if len(msg) > 200 {
		msg = msg[:200]
	}
	f.mu.Lock()
	f.state.Failures, f.state.LastError, f.state.LastErrorAt, f.state.RetryAt = failures, string(msg), &now, &retryAt
	f.mu.Unlock()
	if _, err := db.Exec(ctx, `insert into signals_refresh (failures, last_error, last_error_at, retry_at) values ($1, $2, $3, $4)
		on conflict (id) do update set failures = $1, last_error = $2, last_error_at = $3, retry_at = $4, updated_at = now()`,
		failures, string(msg), now, retryAt); err != nil && ctx.Err() == nil {
		slog.Warn("signals: refresh state not saved", "err", err)
	}
}

// recovered clears the failures after a refresh that stored a board; the last
// error and its time stay for the admin overview.
func (f *flight) recovered(ctx context.Context, db *pgxpool.Pool) {
	f.mu.Lock()
	f.state.Failures, f.state.RetryAt = 0, nil
	f.mu.Unlock()
	if _, err := db.Exec(ctx, `update signals_refresh set failures = 0, retry_at = null, updated_at = now()`); err != nil && ctx.Err() == nil {
		slog.Warn("signals: refresh state not saved", "err", err)
	}
}

// spendModelCall takes one of today's (UTC) rationale model calls: false once
// modelCallsPerDay are spent. One conditional upsert, so processes share the cap.
func spendModelCall(ctx context.Context, d *app.Deps, now time.Time) (bool, error) {
	day := now.UTC().Format(time.DateOnly)
	var n int
	err := d.DB.QueryRow(ctx, `insert into signals_refresh (model_day, model_calls) values ($1::date, 1)
		on conflict (id) do update set model_day = excluded.model_day, updated_at = now(),
			model_calls = case when signals_refresh.model_day = excluded.model_day then signals_refresh.model_calls + 1 else 1 end
		where signals_refresh.model_day is distinct from excluded.model_day or signals_refresh.model_calls < $2
		returning model_calls`, day, modelCallsPerDay).Scan(&n)
	spent := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !spent {
		return false, err
	}
	if spent {
		n = modelCallsPerDay
	}
	f := flightOf(d)
	f.mu.Lock()
	f.state.ModelDay, f.state.ModelCalls = day, n
	f.mu.Unlock()
	return !spent, nil
}

// status is the refresh object for a read whose shown board is pending (due).
func (f *flight) status(pending bool, now time.Time) Refresh {
	f.mu.Lock()
	st := f.state
	f.mu.Unlock()
	r := Refresh{State: "idle", Failures: st.Failures, LastErrorAt: st.LastErrorAt}
	switch {
	case f.running.Load():
		r.State = "running"
	case st.Failures > 0 && st.RetryAt != nil && st.RetryAt.After(now):
		r.State, r.RetryAt = "retrying", st.RetryAt
	case pending:
		r.State = "due"
	}
	return r
}

// view is one computed read of GET /api/signals.
type view struct {
	board   *Board
	asOf    *time.Time
	history []RemovedPick
	// historyUnavailable: the legacy picks' outcomes could not be read (1h
	// candles failed); history then holds the ledger's exits only.
	historyUnavailable bool
	record             Record
	firstBoard         bool
	at                 time.Time
}

// current is the cached read, computed at most once per viewTTL: concurrent
// reads on a cold cache wait for one computation (single flight), which runs
// on its own context so one client leaving does not fail the others.
func (f *flight) current(ctx context.Context, d *app.Deps) (v *view, err error) {
	f.mu.Lock()
	for {
		if cached := f.view; cached != nil && time.Since(cached.at) < viewTTL {
			f.mu.Unlock()
			return cached, nil
		}
		if f.computing == nil {
			break
		}
		ch := f.computing
		f.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		f.mu.Lock()
	}
	ch, gen := make(chan struct{}), f.gen
	f.computing = ch
	f.mu.Unlock()
	defer func() { // a panic too: the waiters must never hang on a computation that is gone
		f.mu.Lock()
		f.computing = nil
		close(ch)
		if err == nil && v != nil && gen == f.gen { // a board stored meanwhile: the next read recomputes
			f.view = v
		}
		f.mu.Unlock()
	}()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return computeView(cctx, d)
}

// forget drops the cached read and the remembered validations.
func (f *flight) forget() {
	f.mu.Lock()
	f.view, f.memo = nil, nil
	f.gen++
	f.mu.Unlock()
}

// validOlder is validBoard for a board older than the shown one, remembered
// per board id for memoTTL (gap round G22): before, a cold read re-validated
// every board of the week, each re-reading its briefs (42 boards took 3 s).
func (f *flight) validOlder(ctx context.Context, db *pgxpool.Pool, b *Board) (bool, error) {
	f.mu.Lock()
	m, ok := f.memo[b.ID]
	f.mu.Unlock()
	if ok && time.Since(m.at) < memoTTL {
		return m.valid, nil
	}
	valid, err := validBoard(ctx, db, b)
	if err != nil {
		return false, err
	}
	f.mu.Lock()
	if f.memo == nil {
		f.memo = map[string]memoValid{}
	}
	f.memo[b.ID] = memoValid{valid, time.Now()}
	f.mu.Unlock()
	return valid, nil
}

// historyBars is how many 1h bars legacyHistory reads: enough to reach back
// 2 h before the oldest pick of boards, in hundreds (a few fixed sizes keep the
// market cache warm), at least 200 and at most 1,000.
func historyBars(boards []Board, now time.Time) int {
	oldest := now
	for _, b := range boards {
		for _, p := range slices.Concat(b.Bullish, b.Bearish) {
			if p.PickedAt.Before(oldest) {
				oldest = p.PickedAt
			}
		}
	}
	hours := int(math.Ceil(now.Sub(oldest).Hours())) + 2
	return min(max((hours+99)/100*100, 200), 1000)
}

// check drops the cached read when Run's check finds another newest valid
// board than the one it shows (one stored, or the shown one no longer valid).
func (f *flight) check(b *Board) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v := f.view; v != nil && (v.board == nil) != (b == nil) || v != nil && v.board != nil && b != nil && v.board.ID != b.ID {
		f.view = nil
		f.gen++
	}
}

// Forget drops GET /api/signals' cached read for d, so the next read
// recomputes it: for code that changes stored boards outside Run (tests).
func Forget(d *app.Deps) { flightOf(d).forget() }

// computeView reads the newest board that validates (Latest), the ledger's
// newest exits and the track record; steady-v1 outcomes were resolved and
// stored when the boards were made, so these read no candles. While a board
// stored before steady-v1 is retained, the picks that left legacy boards are
// added (legacyRemovals), newest removal first, at most 12 in all.
func computeView(ctx context.Context, d *app.Deps) (*view, error) {
	b, err := Latest(ctx, d.DB)
	if err != nil {
		return nil, err
	}
	v := &view{board: b}
	if b != nil {
		v.asOf = &b.CreatedAt
	} else if err := d.DB.QueryRow(ctx, "select not exists (select 1 from signal_boards)").Scan(&v.firstBoard); err != nil {
		return nil, fmt.Errorf("signals: boards: %w", err)
	}
	if v.history, err = exits(ctx, d.DB); err != nil {
		return nil, err
	}
	if b != nil {
		old, unavailable, err := flightOf(d).legacyRemovals(ctx, d, b)
		if err != nil {
			return nil, err
		}
		if len(old) > 0 {
			v.history = slices.Concat(v.history, old)
			slices.SortStableFunc(v.history, func(x, y RemovedPick) int { return y.RemovedAt.Compare(x.RemovedAt) })
			v.history = v.history[:min(len(v.history), maxHistory)]
		}
		v.historyUnavailable = unavailable
	}
	if v.record, err = LoadRecord(ctx, d.DB); err != nil {
		return nil, err
	}
	v.at = time.Now()
	return v, nil
}

// legacyRemovals is how the picks of the retained boards stored before
// steady-v1 left (legacyHistory, gap round G22's reasons): never part of the
// ledger or the record. It reads nothing more while no legacy board with a
// pick is retained older than the shown one (none a week after the switch).
// The chain is the week's boards up to the shown one that validate: an older
// board that does not restarts it after it, so no number from an unvalidated
// board is shown; older boards' validations are remembered for memoTTL
// (validOlder), so a cold read with a warm memo validates only the shown
// board. Any 1h candle failure makes the legacy part unavailable, never
// partial: the ledger's exits still show.
func (f *flight) legacyRemovals(ctx context.Context, d *app.Deps, shown *Board) (rows []RemovedPick, unavailable bool, err error) {
	since := time.Now().Add(-keep)
	var found bool
	if err := d.DB.QueryRow(ctx, `select exists (select 1 from signal_boards where created_at >= $1 and (created_at, id) < ($2, $3)
		and board -> '_ledger' is null and (board -> 'bullish' <> '[]'::jsonb or board -> 'bearish' <> '[]'::jsonb))`, since, shown.CreatedAt, shown.ID).Scan(&found); err != nil {
		return nil, false, fmt.Errorf("signals: legacy boards: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	all, err := stored(ctx, d.DB)
	if err != nil {
		return nil, false, err
	}
	at := slices.IndexFunc(all, func(b Board) bool { return b.ID == shown.ID })
	if at < 0 {
		return nil, false, nil // replaced meanwhile: the next read has it
	}
	var boards, legacy []Board // the chain, oldest first, and its legacy boards
	for i := len(all) - 1; i >= at; i-- {
		if all[i].CreatedAt.Before(since) {
			continue
		}
		if i > at {
			valid, err := f.validOlder(ctx, d.DB, &all[i])
			if err != nil {
				return nil, false, err
			}
			if !valid {
				boards, legacy = nil, nil
				continue
			}
		}
		boards = append(boards, all[i])
		if all[i].legacy() {
			legacy = append(legacy, all[i])
		}
	}
	var down error // after one candle failure the rest are skipped: market data is down, the board still answers
	n := historyBars(legacy, time.Now())
	hourly := func(symbol string) ([]market.Candle, error) {
		if down != nil {
			return nil, down
		}
		bars, err := d.Market.Candles(ctx, symbol, "1h", n) // cached by the market client
		down = err
		return bars, err
	}
	rows = legacyHistory(boards, hourly)
	if down != nil {
		return nil, true, nil
	}
	return rows, false, nil
}
