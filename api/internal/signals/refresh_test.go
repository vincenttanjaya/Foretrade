package signals

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

// response is GET /api/signals.
type response struct {
	Board              *Board
	History            []RemovedPick
	HistoryUnavailable bool
	Record             *Record
	Refreshing         bool
	Refresh            Refresh
	AsOf               *time.Time
	FirstBoard         bool
}

func get(t *testing.T, e *apptest.Env) response {
	t.Helper()
	w := e.Do("GET", "/api/signals", nil)
	if w.Code != 200 {
		t.Fatalf("GET /api/signals: %d %s", w.Code, w.Body)
	}
	return apptest.JSON[response](t, w)
}

// legacyBoard has no provenance: it never validates.
const legacyBoard = `{"bullish":[],"bearish":[]}`

// storeBoard stores raw as a board age old and returns its id.
func storeBoard(t *testing.T, e *apptest.Env, raw string, age time.Duration) string {
	t.Helper()
	var id string
	if err := e.DB.QueryRow(context.Background(), `insert into signal_boards (board, created_at) values ($1, $2) returning id`, raw,
		time.Now().Add(-age)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// eventually polls cond; the deadline is generous because a refresh under
// the race detector on a loaded machine takes tens of seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(120 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// start runs Run until the test ends.
func start(t *testing.T, e *apptest.Env) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, e.Deps); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// uncached makes every GET /api/signals compute its read, for tests that
// change stored boards between reads.
func uncached(t *testing.T) {
	t.Helper()
	old := viewTTL
	viewTTL = 0
	t.Cleanup(func() { viewTTL = old })
}

// pace shortens Run's watchdog and backoff for the test; call it before start.
func pace(t *testing.T, tick time.Duration, retries ...time.Duration) {
	t.Helper()
	oldTick, oldRetries := watchdog, backoff
	watchdog, backoff = tick, retries
	t.Cleanup(func() { watchdog, backoff = oldTick, oldRetries })
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// captureLogs collects the default logger's lines; call it before start.
func captureLogs(t *testing.T) *logBuffer {
	t.Helper()
	l := &logBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(l, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return l
}

func latestOrFatal(t *testing.T, e *apptest.Env) *Board {
	t.Helper()
	b, err := Latest(context.Background(), e.DB)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The next board is due every − lead after the current one, so it is stored
// before the current one turns 4 h old; nextAt says when.
func TestRefreshLead(t *testing.T) {
	at := time.Now()
	for _, tc := range []struct {
		age time.Duration
		due bool
	}{{time.Hour, false}, {every - lead - time.Second, false}, {every - lead, true}, {every, true}} {
		if got := due(&Board{CreatedAt: at.Add(-tc.age)}, at); got != tc.due {
			t.Errorf("a board %s old: due %v", tc.age, got)
		}
	}
	if !due(nil, at) {
		t.Error("no board is not due")
	}

	e := apptest.New(t, Register, apptest.Options{})
	storeBoard(t, e, emptyBoard, every-lead-500*time.Millisecond) // due in half a second; the watchdog is 5 min
	old := latestOrFatal(t, e)
	start(t, e)
	eventually(t, "the next board", func() bool { return boards(t, e) == 2 })
	b := latestOrFatal(t, e)
	if b == nil || b.CreatedAt.Before(old.NextAt) || !b.CreatedAt.Before(old.CreatedAt.Add(every)) || !old.NextAt.Equal(old.CreatedAt.Add(every-lead)) {
		t.Fatalf("next board %+v after %s (next at %s)", b, old.CreatedAt, old.NextAt)
	}
}

// Run decides from the newest valid board: one that does not validate is
// replaced at boot even when it is fresh.
func TestRunSelfHealsAtBoot(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	storeBoard(t, e, legacyBoard, time.Hour)
	start(t, e)
	eventually(t, "a valid board", func() bool { b := latestOrFatal(t, e); return b != nil && time.Since(b.CreatedAt) < time.Minute })
	if n := boards(t, e); n != 2 {
		t.Fatalf("boards: %d", n)
	}
}

// A board that stops validating while Run waits is replaced on the next
// watchdog tick, not when it turns 4 h old.
func TestRunSelfHealsOnWatchdog(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	pace(t, 50*time.Millisecond, time.Hour)
	insertBoard(t, e, time.Hour)
	start(t, e)
	time.Sleep(300 * time.Millisecond) // six ticks: a valid fresh board is kept
	if n := boards(t, e); n != 1 {
		t.Fatalf("a fresh valid board was recomputed: %d boards", n)
	}
	if _, err := e.DB.Exec(context.Background(), `update signal_boards set board = board - '_quality'`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the replacement", func() bool { return latestOrFatal(t, e) != nil })
	if n := boards(t, e); n != 2 {
		t.Fatalf("boards: %d", n)
	}
}

// Failures retry after 2, 5, then 10 minutes four times, then hourly, logged
// with the reason; a nudge waits out the backoff.
func TestRunBackoff(t *testing.T) {
	m := time.Minute
	for i, want := range []time.Duration{2 * m, 5 * m, 10 * m, 10 * m, 10 * m, 10 * m, time.Hour, time.Hour, time.Hour} {
		if got := retryIn(i); got != want {
			t.Errorf("after %d failures: %s", i, got)
		}
	}

	t.Run("retries", func(t *testing.T) {
		e := apptest.New(t, Register, apptest.Options{})
		logs := captureLogs(t)
		pace(t, time.Hour, 30*time.Millisecond, 60*time.Millisecond, 120*time.Millisecond)
		e.Binance.Down("", true)
		start(t, e)
		eventually(t, "four failures", func() bool { return strings.Contains(logs.String(), "failures=4") })
		for _, want := range []string{"failures=1 retry_in=30ms", "failures=2 retry_in=60ms", "failures=3 retry_in=120ms", "failures=4 retry_in=120ms"} {
			if !strings.Contains(logs.String(), want) {
				t.Errorf("no %q in\n%s", want, logs)
			}
		}
		if !strings.Contains(logs.String(), "no asset analyzed") || !strings.Contains(logs.String(), `reason="no valid board"`) {
			t.Errorf("reasons not logged:\n%s", logs)
		}
		e.Binance.Down("", false)
		eventually(t, "the board after the outage", func() bool { return latestOrFatal(t, e) != nil })
	})

	t.Run("nudge waits", func(t *testing.T) {
		e := apptest.New(t, Register, apptest.Options{})
		logs := captureLogs(t)
		pace(t, time.Hour, time.Hour)
		e.Binance.Down("", true)
		start(t, e)
		eventually(t, "the failure", func() bool { return strings.Contains(logs.String(), "failures=1") })
		e.Binance.Down("", false)
		if got := get(t, e); got.Board != nil || !got.Refreshing {
			t.Fatalf("during the backoff: %+v", got)
		}
		time.Sleep(300 * time.Millisecond)
		if n := boards(t, e); n != 0 {
			t.Fatalf("a nudge cut the backoff short: %d boards", n)
		}
	})

	t.Run("new board invalid", func(t *testing.T) {
		// A refresh whose stored board does not validate counts as a failure instead of looping.
		e := apptest.New(t, Register, apptest.Options{})
		logs := captureLogs(t)
		pace(t, time.Hour, time.Hour)
		if _, err := e.DB.Exec(context.Background(), `create function strip_quality() returns trigger language plpgsql as
			$$ begin new.board := new.board - '_quality'; return new; end $$;
			create trigger strip_quality before insert on signal_boards for each row execute function strip_quality()`); err != nil {
			t.Fatal(err)
		}
		start(t, e)
		eventually(t, "the failure", func() bool { return strings.Contains(logs.String(), "failures=1") })
		time.Sleep(300 * time.Millisecond)
		// The board is dropped too: kept, one would pile up per retry for a week.
		if n := boards(t, e); n != 0 || !strings.Contains(logs.String(), "the new board does not validate") {
			t.Fatalf("%d boards; logs:\n%s", n, logs)
		}
	})
}

// A row this build cannot read (a field's type changed in a deploy) is a
// board that does not validate, not an error: the page shows the older valid
// board and Run, whose check would otherwise fail until the row aged out,
// sees a due board to replace.
func TestUnreadableBoardIsInvalid(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	older := storeBoard(t, e, emptyBoard, 5*time.Hour)
	storeBoard(t, e, `{"bullish":"three picks","bearish":[]}`, time.Hour)
	storeBoard(t, e, `{"bullish":[],"bearish":[],"_quality":{"version":"1","inputCount":0,"inputs":[],"briefs":[]}}`, 30*time.Minute)
	if got := get(t, e); got.Board == nil || got.Board.ID != older || !got.Refreshing || got.FirstBoard {
		t.Fatalf("unreadable newer rows: %+v", got)
	}
	if b := latestOrFatal(t, e); b == nil || b.ID != older || !due(b, time.Now()) {
		t.Fatalf("latest: %+v", b)
	}
}

// Latest walks back one row at a time by (createdAt, id), so boards stored at
// the same instant are neither skipped nor read twice.
func TestLatestWalksBackThroughTies(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	at := time.Now().Add(-time.Hour)
	var ids []string
	for _, raw := range []string{emptyBoard, legacyBoard, legacyBoard} {
		var id string
		if err := e.DB.QueryRow(context.Background(), `insert into signal_boards (board, created_at) values ($1, $2) returning id`, raw, at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if b := latestOrFatal(t, e); b == nil || b.ID != ids[0] {
		t.Fatalf("latest of %v: %+v", ids, b)
	}
}

// GET /api/signals shows the newest board that still validates, with
// refreshing while its successor is due, and null only when none does.
func TestHandlerServesNewestValidBoard(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	uncached(t)
	f := flightOf(e.Deps)
	older := storeBoard(t, e, emptyBoard, 5*time.Hour)
	storeBoard(t, e, legacyBoard, time.Hour)
	got := get(t, e)
	if got.Board == nil || got.Board.ID != older || !got.Refreshing || got.AsOf == nil || !got.AsOf.Equal(got.Board.CreatedAt) || got.FirstBoard {
		t.Fatalf("an invalid newest board: %+v", got)
	}
	if len(f.wake) != 1 {
		t.Fatal("a due board did not nudge Run")
	}
	<-f.wake

	// A valid board that is not due yet: shown, nothing pending.
	e.DB.Exec(context.Background(), `update signal_boards set created_at = now() - interval '2 hours' where id = $1`, older)
	if got := get(t, e); got.Board == nil || got.Board.ID != older || got.Refreshing || len(f.wake) != 0 {
		t.Fatalf("a current older board: %+v", got)
	}
	// While Run computes, the board says so.
	f.running.Store(true)
	if got := get(t, e); got.Board == nil || !got.Refreshing || len(f.wake) != 0 {
		t.Fatalf("while refreshing: %+v", got)
	}
	f.running.Store(false)

	// None validates: null and refreshing, never "first board".
	e.DB.Exec(context.Background(), `update signal_boards set board = $1`, legacyBoard)
	if got := get(t, e); got.Board != nil || !got.Refreshing || got.AsOf != nil || got.FirstBoard || got.History == nil || len(got.History) != 0 {
		t.Fatalf("no valid board: %+v", got)
	}
}

// Many requests that find no valid board make one nudge, and Run computes one board.
func TestNullBoardRefreshesOnce(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	pace(t, time.Hour, time.Hour)
	f := flightOf(e.Deps)
	storeBoard(t, e, legacyBoard, time.Hour)
	var wg sync.WaitGroup
	recorders := make([]*httptest.ResponseRecorder, 20)
	for i := range recorders {
		wg.Go(func() { recorders[i] = e.Do("GET", "/api/signals", nil) })
	}
	wg.Wait()
	for _, w := range recorders {
		if r := apptest.JSON[response](t, w); w.Code != 200 || r.Board != nil || !r.Refreshing || r.FirstBoard {
			t.Fatalf("no valid board: %d %s", w.Code, w.Body)
		}
	}
	if len(f.wake) != 1 {
		t.Fatalf("%d nudges", len(f.wake))
	}
	start(t, e)
	eventually(t, "the board", func() bool { return latestOrFatal(t, e) != nil })
	for range 5 {
		get(t, e)
	}
	time.Sleep(300 * time.Millisecond)
	if n := boards(t, e); n != 2 {
		t.Fatalf("boards: %d", n)
	}
	if got := get(t, e); got.Board == nil || got.Refreshing {
		t.Fatalf("after the refresh: %+v", got)
	}
}

// A refresh holds a Postgres advisory lock: while another process holds it,
// nothing is computed; two processes refreshing at once store one board.
func TestRefreshAdvisoryLock(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock($1, hashtext(current_schema()))", lockKey); err != nil {
		t.Fatal(err)
	}
	if err := flightOf(e.Deps).refresh(ctx, e.Deps, "test"); !errors.Is(err, errBusy) || boards(t, e) != 0 {
		t.Fatalf("refresh under another process's lock: %v, %d boards", err, boards(t, e))
	}

	t.Run("run", func(t *testing.T) {
		logs := captureLogs(t)
		pace(t, time.Hour, 50*time.Millisecond)
		start(t, e)
		eventually(t, "the busy log", func() bool { return strings.Contains(logs.String(), "another process is computing") })
		if n := boards(t, e); n != 0 || strings.Contains(logs.String(), "signals board failed") {
			t.Fatalf("%d boards; logs:\n%s", n, logs)
		}
		tx.Rollback(ctx)
		eventually(t, "the board once the lock is free", func() bool { return boards(t, e) == 1 })
	})

	e.DB.Exec(ctx, "delete from signal_boards")
	other := *e.Deps // a second process on the same database
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, d := range []*app.Deps{e.Deps, &other} {
		wg.Go(func() { errs[i] = flightOf(d).refresh(ctx, d, "test") })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && !errors.Is(err, errBusy) {
			t.Fatal(err)
		}
	}
	if n := boards(t, e); n != 1 {
		t.Fatalf("two processes stored %d boards (%v)", n, errs)
	}
}
