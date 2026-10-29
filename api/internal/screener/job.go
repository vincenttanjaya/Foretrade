package screener

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

const (
	// Runs start every weekday (Monday to Friday) at 10:45 UTC, after the last
	// of the nine venues' closes has reached Yahoo: SET's closing auction ends
	// about 09:40 UTC and Yahoo shows it 30 minutes later (TSE 06:30, KRX
	// 06:30, TWSE 05:30, HKEX 08:10, SGX about 09:06, IDX 09:15, Bursa 09:00,
	// HOSE 08:00 UTC, each 10 to 20 minutes later on Yahoo). No venue here
	// keeps daylight saving.
	slotHour, slotMinute = 10, 45
	// slotLead: a run started this long before a slot (from 10:30 UTC, the
	// quote feeds' settle point for SET) already saw the day's closes, so the
	// slot is skipped.
	slotLead   = 15 * time.Minute
	bootFresh  = 24 * time.Hour   // at boot, a completed run this recent (and since the latest slot) waits for the next slot
	runLimit   = 45 * time.Minute // one run: ~785 tickers at 1 a second, retries included
	perTicker  = 45 * time.Second // one chart, the client's retries and pacing included
	failRetry  = 3 * time.Hour    // after a failed run
	busyRetry  = 15 * time.Minute // another process holds the run, or the run log was unreadable
	staleRun   = 10 * time.Minute // a running row without a heartbeat this long was interrupted
	beatEvery  = 10               // tickers between run-log updates
	maxStrikes = 20               // upstream failures in a row that end a run as failed
	keepRuns   = 400 * 24 * time.Hour
)

// Options configure Run.
type Options struct {
	// MaxTickers is SCREENER_MAX_TICKERS: 0 screens every constituent, N > 0
	// the first N (tests, ops), and a negative value turns the job off.
	MaxTickers int
	// Pace is the least time between two tickers' fetches; 0 → 1 s.
	Pace time.Duration
}

// errBusy: another process holds the running row.
var errBusy = errors.New("screener: a run is in progress")

// Run keeps the screener fresh for the server's lifetime: once at boot unless
// a run completed in the last 24 hours and since the latest slot (bootCutoff),
// then every weekday at 10:45 UTC. A failed run is retried after 3 h (never
// past the next slot); prior rows are never deleted.
func Run(ctx context.Context, d *app.Deps, o Options) {
	if o.MaxTickers < 0 {
		slog.Info("screener: off (SCREENER_MAX_TICKERS)")
		return
	}
	now := time.Now().UTC()
	cutoff, next := bootCutoff(now), now
	for {
		if !sleep(ctx, next) {
			return
		}
		now = time.Now().UTC()
		slot := nextSlot(now)
		done, err := completedSince(ctx, d.DB, cutoff)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			slog.Warn("screener: run log unreadable", "err", err)
			next = now.Add(busyRetry)
			continue
		case done:
			cutoff, next = slot.Add(-slotLead), slot
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, runLimit)
		s, err := refresh(rctx, d, o)
		cancel()
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, errBusy):
			next = now.Add(busyRetry) // then: did that run complete?
		case err != nil:
			slog.Warn("screener run failed", "err", err, "done", s.Done, "of", s.Tickers, "seconds", int(s.Took.Seconds()), "yahooCalls", s.Calls)
			next = time.Now().Add(failRetry)
			if slot.Before(next) {
				next = slot
			}
		default:
			slog.Info("screener run", "tickers", s.Tickers, "ok", s.OK, "insufficient", s.Insufficient,
				"unavailable", s.Unavailable, "failed", s.Failed, "seconds", int(s.Took.Seconds()), "yahooCalls", s.Calls)
			cutoff, next = slot.Add(-slotLead), slot
		}
	}
}

func sleep(ctx context.Context, until time.Time) bool {
	t := time.NewTimer(max(0, time.Until(until)))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nextSlot is the first weekday 10:45 UTC after now.
func nextSlot(now time.Time) time.Time {
	now = now.UTC()
	d := time.Date(now.Year(), now.Month(), now.Day(), slotHour, slotMinute, 0, 0, time.UTC)
	for !d.After(now) || d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// lastSlot is the latest weekday 10:45 UTC at or before now.
func lastSlot(now time.Time) time.Time {
	now = now.UTC()
	d := time.Date(now.Year(), now.Month(), now.Day(), slotHour, slotMinute, 0, 0, time.UTC)
	for d.After(now) || d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// bootCutoff is the oldest start of a completed run that spares a boot its
// run: one in the last 24 hours that also counts for the latest slot (started
// from slotLead before it). A boot after a slot whose run did not complete (a
// deploy stopped it, or it failed and its retry was pending) runs at once,
// even when an off-slot run (a retry, a boot's) completed in the last 24
// hours; otherwise that weekday would go without a recompute.
func bootCutoff(now time.Time) time.Time {
	c, s := now.Add(-bootFresh), lastSlot(now).Add(-slotLead)
	if s.After(c) {
		return s
	}
	return c
}

func completedSince(ctx context.Context, db *pgxpool.Pool, cutoff time.Time) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, "select exists(select 1 from screener_runs where status = 'completed' and started_at >= $1)", cutoff).Scan(&ok)
	return ok, err
}

// summary is one run's progress, as its run-log row records it, with how
// long it took and the Yahoo requests it sent so far.
type summary struct {
	ID                                                   string
	Tickers, Done, OK, Insufficient, Unavailable, Failed int
	Took                                                 time.Duration
	Calls                                                int64
	calls                                                *atomic.Int64
}

// count reads the run's Yahoo requests so far into Calls.
func (s *summary) count() {
	if s.calls != nil {
		s.Calls = s.calls.Load()
	}
}

// refresh is one run over the (capped) constituents: each ticker's lifetime
// weekly chart, paced, its row upserted at once, so an interrupted run keeps
// what it read. It ends failed after maxStrikes upstream failures in a row,
// on its deadline or on a storage error.
func refresh(ctx context.Context, d *app.Deps, o Options) (summary, error) {
	todo := Stocks()
	if o.MaxTickers > 0 && o.MaxTickers < len(todo) {
		todo = todo[:o.MaxTickers]
	}
	pace := o.Pace
	if pace == 0 {
		pace = time.Second
	}
	s := summary{Tickers: len(todo), calls: &atomic.Int64{}}
	id, err := claim(ctx, d.DB, len(todo))
	if err != nil {
		return s, err
	}
	s.ID = id
	start := time.Now()
	err = s.work(yahoo.WithCallCount(ctx, s.calls), d, todo, pace)
	s.Took = time.Since(start)
	s.count()
	finish(ctx, d.DB, s, err)
	return s, err
}

// claim starts a run: a running row left by a process that stopped beating is
// failed first; errBusy when another run is live.
func claim(ctx context.Context, db *pgxpool.Pool, tickers int) (string, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer app.Rollback(ctx, tx)
	if _, err := tx.Exec(ctx, `update screener_runs set status = 'failed', finished_at = now(), error = 'interrupted'
		where status = 'running' and heartbeat_at < now() - make_interval(secs => $1)`, staleRun.Seconds()); err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, "insert into screener_runs (tickers) values ($1) on conflict do nothing returning id", tickers).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errBusy
	}
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// runError is a failed run's fixed message for the run log; the cause is logged.
type runError struct{ msg string }

func (e *runError) Error() string { return e.msg }

func (s *summary) work(ctx context.Context, d *app.Deps, todo []Stock, pace time.Duration) error {
	y := d.YahooSource()
	f := fxBook{y: y, rates: map[string]*yahoo.Rate{}, tries: map[string]int{}}
	strikes := 0
	for i, st := range todo {
		start := time.Now()
		tctx, cancel := context.WithTimeout(yahoo.NoCache(ctx), perTicker)
		r := read(tctx, y, &f, st)
		cancel()
		if ctx.Err() != nil {
			return s.halted(ctx)
		}
		if err := upsert(ctx, d.DB, s.ID, st.Ticker, r); err != nil {
			if ctx.Err() != nil { // stopped mid-write, not a storage fault
				return s.halted(ctx)
			}
			slog.Warn("screener: storing a row", "ticker", st.Ticker, "err", err)
			return &runError{"storage error"}
		}
		s.Done++
		switch r.status {
		case "ok":
			s.OK++
		case "insufficient_history":
			s.Insufficient++
		case "unavailable":
			s.Unavailable++
		default:
			s.Failed++
		}
		if r.status == "failed" {
			if strikes++; strikes >= maxStrikes {
				return &runError{fmt.Sprintf("Yahoo unavailable: %d tickers failed in a row (%s)", strikes, r.reason)}
			}
		} else {
			strikes = 0
		}
		if (i+1)%beatEvery == 0 {
			beat(ctx, d.DB, *s)
		}
		if i+1 < len(todo) && !sleep(ctx, start.Add(pace)) {
			return s.halted(ctx)
		}
	}
	return nil
}

// halted is the run log's message for a run ctx stopped (shutdown or the run time limit).
func (s *summary) halted(ctx context.Context) error {
	return &runError{fmt.Sprintf("stopped after %d of %d tickers (%s)", s.Done, s.Tickers, stopped(ctx))}
}

func stopped(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "run time limit"
	}
	return "shutdown"
}

// result is one ticker's outcome: status "ok", "insufficient_history"
// (computed, but under 200 weeks), "unavailable" (Yahoo serves no data for it)
// or "failed" (Yahoo could not be read this time; earlier values are kept).
type result struct {
	status, reason string
	m              *metrics
	fx             *yahoo.Rate // the currency's USD rate; nil when FX failed
}

func read(ctx context.Context, y yahoo.Source, f *fxBook, st Stock) result {
	ch, err := y.Chart(ctx, st.Ticker, "max", "1wk")
	var u *yahoo.UnavailableError
	switch {
	case yahoo.IsNotFound(err):
		return result{status: "unavailable", reason: "Yahoo serves no data for this ticker"}
	case errors.As(err, &u):
		return result{status: "failed", reason: u.Reason}
	case errors.Is(err, context.DeadlineExceeded):
		return result{status: "failed", reason: "Yahoo timed out"}
	case err != nil:
		return result{status: "failed", reason: "unexpected error"}
	}
	m, ok := compute(ch)
	if !ok {
		return result{status: "unavailable", reason: "Yahoo sent no weekly bars"}
	}
	r := result{status: "ok", m: &m}
	if m.SMA == nil {
		r.status, r.reason = "insufficient_history", fmt.Sprintf("%d weeks of history; the 200-week average needs %d", m.Weeks, smaWeeks)
	}
	r.fx = f.rate(ctx, m.Currency)
	return r
}

// fxBook holds each currency's USD rate for one run: read once, a failure
// retried on the next ticker at most twice.
type fxBook struct {
	y     yahoo.Source
	rates map[string]*yahoo.Rate
	tries map[string]int
}

func (f *fxBook) rate(ctx context.Context, ccy string) *yahoo.Rate {
	if r, ok := f.rates[ccy]; ok || ccy == "" || f.tries[ccy] >= 3 {
		return r
	}
	f.tries[ccy]++
	r, err := f.y.FX(ctx, ccy)
	if err != nil {
		slog.Warn("screener: FX unavailable", "ccy", ccy, "err", err)
		return nil
	}
	f.rates[ccy] = &r
	return &r
}

// upsert writes a ticker's row. A run that computed values replaces them all;
// one that could not read the ticker only records the attempt, keeping
// whatever an earlier run computed.
func upsert(ctx context.Context, db *pgxpool.Pool, runID, ticker string, r result) error {
	var reason *string
	if r.reason != "" {
		reason = &r.reason
	}
	if r.m == nil {
		_, err := db.Exec(ctx, `insert into screener_rows (ticker, status, reason, attempted_at, run_id) values ($1, $2, $3, now(), $4)
			on conflict (ticker) do update set status = excluded.status, reason = excluded.reason,
				attempted_at = excluded.attempted_at, run_id = excluded.run_id`, ticker, r.status, reason, runID)
		return err
	}
	m := r.m
	var fxUSD, priceUSD *float64
	var fxPair *string
	var fxAt *time.Time
	if r.fx != nil {
		fxUSD, priceUSD = &r.fx.USD, ptr(m.Price*r.fx.USD)
		if r.fx.Symbol != "" {
			fxPair, fxAt = &r.fx.Symbol, &r.fx.Time
		}
	}
	_, err := db.Exec(ctx, `insert into screener_rows (ticker, status, reason, attempted_at, run_id, computed_at, yahoo_name,
			currency, price, price_at, fx_usd, fx_pair, fx_at, price_usd, weeks, first_week, sma200w, pct_vs_sma,
			prev_pct_vs_sma, rsi14w, ath, ath_at, drawdown_pct)
		values ($1, $2, $3, now(), $4, now(), $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		on conflict (ticker) do update set status = excluded.status, reason = excluded.reason, attempted_at = excluded.attempted_at,
			run_id = excluded.run_id, computed_at = excluded.computed_at, yahoo_name = excluded.yahoo_name,
			currency = excluded.currency, price = excluded.price, price_at = excluded.price_at, fx_usd = excluded.fx_usd,
			fx_pair = excluded.fx_pair, fx_at = excluded.fx_at, price_usd = excluded.price_usd, weeks = excluded.weeks,
			first_week = excluded.first_week, sma200w = excluded.sma200w, pct_vs_sma = excluded.pct_vs_sma,
			prev_pct_vs_sma = excluded.prev_pct_vs_sma, rsi14w = excluded.rsi14w, ath = excluded.ath,
			ath_at = excluded.ath_at, drawdown_pct = excluded.drawdown_pct`,
		ticker, r.status, reason, runID, m.Name, m.Currency, m.Price, m.PriceAt, fxUSD, fxPair, fxAt, priceUSD,
		m.Weeks, m.FirstWeek, m.SMA, m.Pct, m.PrevPct, m.RSI, m.ATH, m.ATHAt, m.Drawdown)
	return err
}

// beat records progress and proves the run alive; a failure is only logged.
func beat(ctx context.Context, db *pgxpool.Pool, s summary) {
	s.count()
	if _, err := db.Exec(ctx, `update screener_runs set heartbeat_at = now(), done = $2, ok = $3, insufficient = $4,
		unavailable = $5, failed = $6, yahoo_calls = $7 where id = $1`, s.ID, s.Done, s.OK, s.Insufficient, s.Unavailable, s.Failed,
		s.Calls); err != nil {
		slog.Warn("screener: run heartbeat", "err", err)
	}
}

// finish closes the run's row (also after cancellation) and prunes old runs.
func finish(ctx context.Context, db *pgxpool.Pool, s summary, err error) {
	ctx, cancel := app.Cleanup(ctx)
	defer cancel()
	status, msg := "completed", (*string)(nil)
	if err != nil {
		status = "failed"
		var re *runError
		if !errors.As(err, &re) {
			re = &runError{"unexpected error"}
		}
		msg = &re.msg
	}
	if _, err := db.Exec(ctx, `update screener_runs set status = $2, finished_at = now(), heartbeat_at = now(), done = $3,
		ok = $4, insufficient = $5, unavailable = $6, failed = $7, error = $8, yahoo_calls = $9 where id = $1`,
		s.ID, status, s.Done, s.OK, s.Insufficient, s.Unavailable, s.Failed, msg, s.Calls); err != nil {
		slog.Warn("screener: closing the run", "err", err)
	}
	if _, err := db.Exec(ctx, "delete from screener_runs where started_at < $1 and status <> 'running'", time.Now().Add(-keepRuns)); err != nil {
		slog.Warn("screener: pruning runs", "err", err)
	}
}
