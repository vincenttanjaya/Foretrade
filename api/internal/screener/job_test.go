package screener

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// fast is a run paced for tests.
func fast(n int) Options { return Options{MaxTickers: n, Pace: time.Nanosecond} }

func deps(t *testing.T) (*app.Deps, *yahootest.Fake) {
	f := yahootest.New()
	return &app.Deps{DB: storetest.Schema(t), Yahoo: f}, f
}

type stored struct {
	status, reason           string
	computed                 *time.Time
	attempted                time.Time
	price, priceUSD, fx, pct *float64
	pair, currency, name     *string
	weeks                    *int
}

func row(t *testing.T, d *app.Deps, ticker string) stored {
	t.Helper()
	var s stored
	var reason *string
	if err := d.DB.QueryRow(context.Background(), `select status, reason, computed_at, attempted_at, price, price_usd, fx_usd,
		pct_vs_sma, fx_pair, currency, yahoo_name, weeks from screener_rows where ticker = $1`, ticker).Scan(&s.status, &reason,
		&s.computed, &s.attempted, &s.price, &s.priceUSD, &s.fx, &s.pct, &s.pair, &s.currency, &s.name, &s.weeks); err != nil {
		t.Fatalf("%s: %v", ticker, err)
	}
	if reason != nil {
		s.reason = *reason
	}
	return s
}

type runRow struct {
	status                                             string
	tickers, done, ok, insufficient, unavailable, fail int
	err                                                *string
	finished                                           *time.Time
}

func lastRun(t *testing.T, d *app.Deps) runRow {
	t.Helper()
	var r runRow
	if err := d.DB.QueryRow(context.Background(), `select status, tickers, done, ok, insufficient, unavailable, failed, error, finished_at
		from screener_runs order by started_at desc limit 1`).Scan(&r.status, &r.tickers, &r.done, &r.ok, &r.insufficient,
		&r.unavailable, &r.fail, &r.err, &r.finished); err != nil {
		t.Fatal(err)
	}
	return r
}

// One run over the first four Nikkei rows: a full history, a short one, a
// ticker Yahoo does not serve and one it fails on, each stored with its
// status; the run log counts them and the USD price uses Yahoo's JPY rate.
func TestRefreshStoresEachTickerAndTheRun(t *testing.T) {
	d, y := deps(t)
	y.Set(weekly("1332.T", series(260, func(i int) float64 { return 1000 - float64(i) })...))
	y.Set(weekly("1605.T", series(120, func(i int) float64 { return 2000 })...))
	y.SetErr("1801.T", yahootest.Down)
	y.SetFX("JPY", 0.0063)
	s, err := refresh(context.Background(), d, fast(4))
	if err != nil || s.Tickers != 4 || s.Done != 4 || s.OK != 1 || s.Insufficient != 1 || s.Unavailable != 1 || s.Failed != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	r := lastRun(t, d)
	if r.status != "completed" || r.tickers != 4 || r.done != 4 || r.ok != 1 || r.insufficient != 1 || r.unavailable != 1 || r.fail != 1 ||
		r.err != nil || r.finished == nil {
		t.Fatalf("run %+v", r)
	}
	ok := row(t, d, "1332.T")
	if ok.status != "ok" || ok.reason != "" || ok.computed == nil || *ok.price != 741 || *ok.currency != "JPY" || *ok.weeks != 260 ||
		*ok.fx != 0.0063 || *ok.pair != "JPYUSD=X" || math.Abs(*ok.priceUSD-741*0.0063) > 1e-9 || *ok.pct >= 0 || *ok.name != "Test 1332.T" {
		t.Fatalf("ok row %+v", ok)
	}
	if short := row(t, d, "1605.T"); short.status != "insufficient_history" || short.pct != nil || *short.weeks != 120 ||
		short.reason != "120 weeks of history; the 200-week average needs 200" || short.price == nil {
		t.Fatalf("short row %+v", short)
	}
	if none := row(t, d, "1721.T"); none.status != "unavailable" || none.reason != "Yahoo serves no data for this ticker" || none.computed != nil {
		t.Fatalf("not served %+v", none)
	}
	if down := row(t, d, "1801.T"); down.status != "failed" || down.reason != "Yahoo answered HTTP 503" || down.computed != nil {
		t.Fatalf("failed %+v", down)
	}
	if n := y.Calls("JPYUSD=X"); n != 1 {
		t.Fatalf("FX read %d times in one run", n)
	}
	if y.Calls("1802.T") != 0 {
		t.Fatal("read past the cap")
	}
	// The run counts its Yahoo calls (four charts and one FX read) and its time.
	var calls *int
	if err := d.DB.QueryRow(context.Background(), "select yahoo_calls from screener_runs where id = $1", s.ID).Scan(&calls); err != nil ||
		calls == nil || *calls != 5 || s.Calls != 5 || s.Took <= 0 {
		t.Fatalf("calls %v (%d), took %v: %v", calls, s.Calls, s.Took, err)
	}
	h := &handler{d: d}
	v, _, err := h.runs(context.Background(), time.Now())
	if err != nil || v.YahooCalls == nil || *v.YahooCalls != 5 || v.DurationSeconds < 0 || v.DurationSeconds > 60 {
		t.Fatalf("run view %+v %v", v, err)
	}
	// While running, the duration is the time so far; an old run has no count.
	if _, err := d.DB.Exec(context.Background(), `update screener_runs set started_at = started_at - interval '2 hours';
		insert into screener_runs (tickers, started_at) values (785, now() - interval '90 seconds')`); err != nil {
		t.Fatal(err)
	}
	if v, _, err := h.runs(context.Background(), time.Now()); err != nil || v.Status != "running" || v.YahooCalls != nil ||
		v.DurationSeconds < 89 || v.DurationSeconds > 120 {
		t.Fatalf("running %+v %v", v, err)
	}
}

// A high over 100× the price is stored as null, with the rest of the row.
func TestRefreshStoresNoImplausibleHigh(t *testing.T) {
	d, y := deps(t)
	y.Set(weekly("1332.T", series(230, func(i int) float64 { return max(500, 1e6*float64(1-i)) })...))
	y.SetFX("JPY", 0.0063)
	if _, err := refresh(context.Background(), d, fast(1)); err != nil {
		t.Fatal(err)
	}
	var ath, drawdown, pct *float64
	var athAt *time.Time
	var status string
	if err := d.DB.QueryRow(context.Background(), "select status, ath, ath_at, drawdown_pct, pct_vs_sma from screener_rows where ticker = '1332.T'").
		Scan(&status, &ath, &athAt, &drawdown, &pct); err != nil || status != "ok" || ath != nil || athAt != nil || drawdown != nil || pct == nil {
		t.Fatalf("%v: %s ath %v at %v drawdown %v pct %v", err, status, ath, athAt, drawdown, pct)
	}
}

// A ticker that fails on a later run keeps the values an earlier run
// computed; only its status, reason and attempt time change. Nothing is
// deleted.
func TestRefreshKeepsPriorValuesOnFailure(t *testing.T) {
	d, y := deps(t)
	y.Set(weekly("1332.T", series(230, func(i int) float64 { return 500 + float64(i%7) })...))
	y.SetFX("JPY", 0.0063)
	if _, err := refresh(context.Background(), d, fast(1)); err != nil {
		t.Fatal(err)
	}
	before := row(t, d, "1332.T")
	y.SetErr("1332.T", yahootest.Down)
	if _, err := refresh(context.Background(), d, fast(1)); err != nil {
		t.Fatal(err)
	}
	after := row(t, d, "1332.T")
	if after.status != "failed" || after.reason != "Yahoo answered HTTP 503" || !after.computed.Equal(*before.computed) ||
		*after.price != *before.price || *after.pct != *before.pct || !after.attempted.After(before.attempted) {
		t.Fatalf("before %+v after %+v", before, after)
	}
	var runs int
	d.DB.QueryRow(context.Background(), "select count(*) from screener_runs where status = 'completed'").Scan(&runs)
	if runs != 2 {
		t.Fatalf("%d completed runs", runs)
	}
}

// Twenty upstream failures in a row end the run as failed, and every row read
// so far is kept.
func TestRefreshStopsAfterRepeatedFailures(t *testing.T) {
	d, y := deps(t)
	for _, s := range Stocks()[:25] {
		y.SetErr(s.Ticker, yahootest.Down)
	}
	s, err := refresh(context.Background(), d, fast(25))
	if err == nil || s.Done != maxStrikes || s.Failed != maxStrikes {
		t.Fatalf("%+v %v", s, err)
	}
	r := lastRun(t, d)
	if r.status != "failed" || r.done != 20 || r.err == nil || *r.err != "Yahoo unavailable: 20 tickers failed in a row (Yahoo answered HTTP 503)" {
		t.Fatalf("run %+v %v", r, *r.err)
	}
	var rows int
	d.DB.QueryRow(context.Background(), "select count(*) from screener_rows").Scan(&rows)
	if rows != 20 {
		t.Fatalf("%d rows", rows)
	}
}

// cancelOn is a Yahoo source that cancels the run on its nth chart read, so
// the stop lands mid-run however long the run's database round trips take.
type cancelOn struct {
	yahoo.Source
	n      int32
	calls  atomic.Int32
	cancel context.CancelFunc
}

func (c *cancelOn) Chart(ctx context.Context, symbol, rng, interval string) (yahoo.Chart, error) {
	if c.calls.Add(1) == c.n {
		c.cancel()
	}
	return c.Source.Chart(ctx, symbol, rng, interval)
}

// A run cancelled mid-way (shutdown) is closed as failed with how far it got.
func TestRefreshCancelled(t *testing.T) {
	d, f := deps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Yahoo = &cancelOn{Source: f, n: 2, cancel: cancel}
	s, err := refresh(ctx, d, Options{MaxTickers: 50, Pace: 20 * time.Millisecond})
	if err == nil || s.Done == 0 || s.Done == 50 {
		t.Fatalf("%+v %v", s, err)
	}
	if r := lastRun(t, d); r.status != "failed" || r.err == nil || !strings.HasSuffix(*r.err, "tickers (shutdown)") || r.done != s.Done {
		t.Fatalf("run %+v", r)
	}
}

// One run at a time: a live running row makes another process back off; one
// whose heartbeat stopped was interrupted and gives way.
func TestClaimIsExclusive(t *testing.T) {
	d, _ := deps(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(ctx, "insert into screener_runs (tickers) values (785)"); err != nil {
		t.Fatal(err)
	}
	if _, err := refresh(ctx, d, fast(1)); err != errBusy {
		t.Fatalf("live run: %v", err)
	}
	d.DB.Exec(ctx, "update screener_runs set heartbeat_at = now() - interval '11 minutes'")
	if _, err := refresh(ctx, d, fast(1)); err != nil {
		t.Fatal(err)
	}
	var interrupted, completed int
	d.DB.QueryRow(ctx, `select count(*) filter (where status = 'failed' and error = 'interrupted'),
		count(*) filter (where status = 'completed') from screener_runs`).Scan(&interrupted, &completed)
	if interrupted != 1 || completed != 1 {
		t.Fatalf("interrupted %d completed %d", interrupted, completed)
	}
}

// Boot: no completed run in the last 24 hours runs at once; a recent one
// waits for the next weekday slot. A negative cap turns the job off.
func TestRunAtBoot(t *testing.T) {
	d, y := deps(t)
	y.Set(weekly("1332.T", series(10, func(int) float64 { return 1 })...))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, d, fast(1)); close(done) }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if ok, _ := completedSince(ctx, d.DB, time.Now().Add(-time.Hour)); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no run at boot")
		}
	}
	cancel()
	<-done

	// A second boot finds the fresh run and only waits.
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { Run(ctx, d, fast(1)); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	var runs int
	d.DB.QueryRow(context.Background(), "select count(*) from screener_runs").Scan(&runs)
	if runs != 1 || y.Calls("1332.T") != 1 {
		t.Fatalf("%d runs, %d reads", runs, y.Calls("1332.T"))
	}

	off := make(chan struct{})
	go func() { Run(context.Background(), d, Options{MaxTickers: -1}); close(off) }()
	select {
	case <-off:
	case <-time.After(5 * time.Second):
		t.Fatal("an off job did not return")
	}
}

// A boot spares its run only for a run that completed in the last 24 hours
// and counts for the latest slot: a deploy that stops a weekday's run (or
// lands while its failed run waits for the retry) runs it at boot, even when
// the day before ran off its slot (a retry, a boot's run).
func TestBootCutoff(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for in, want := range map[string][2]string{ // boot: latest slot, cutoff
		"2026-10-14T09:00:00Z": {"2026-10-13T10:45:00Z", "2026-10-13T10:30:00Z"}, // before Wednesday's slot: Tuesday's run
		"2026-10-14T10:40:00Z": {"2026-10-13T10:45:00Z", "2026-10-13T10:40:00Z"}, // the last 24 hours bind
		"2026-10-14T10:45:00Z": {"2026-10-14T10:45:00Z", "2026-10-14T10:30:00Z"}, // at the slot: that day's run
		"2026-10-14T11:00:00Z": {"2026-10-14T10:45:00Z", "2026-10-14T10:30:00Z"}, // after it: that day's run
		"2026-10-17T12:00:00Z": {"2026-10-16T10:45:00Z", "2026-10-16T12:00:00Z"}, // Saturday: none in the last day runs
		"2026-10-19T09:00:00Z": {"2026-10-16T10:45:00Z", "2026-10-18T09:00:00Z"}, // Monday before the slot
		"2027-01-01T11:00:00Z": {"2027-01-01T10:45:00Z", "2027-01-01T10:30:00Z"}, // a holiday is a weekday
	} {
		now := at(in)
		if got := lastSlot(now); !got.Equal(at(want[0])) {
			t.Fatalf("%s: latest slot %v, want %s", in, got, want[0])
		}
		if got := bootCutoff(now); !got.Equal(at(want[1])) {
			t.Fatalf("%s: cutoff %v, want %s", in, got, want[1])
		}
	}

	// Tuesday's run failed at its slot and its retry completed at 13:45;
	// Wednesday's 10:45 run was stopped by a deploy, which boots at 11:00.
	d, _ := deps(t)
	ctx := context.Background()
	for _, r := range []struct{ start, status string }{
		{"2026-10-13T10:45:00Z", "failed"}, {"2026-10-13T13:45:00Z", "completed"}, {"2026-10-14T10:45:00Z", "failed"},
	} {
		if _, err := d.DB.Exec(ctx, `insert into screener_runs (status, started_at, finished_at, heartbeat_at, tickers)
			values ($1, $2, $2::timestamptz + interval '15 minutes', $2::timestamptz + interval '15 minutes', 785)`,
			r.status, at(r.start)); err != nil {
			t.Fatal(err)
		}
	}
	boot := at("2026-10-14T11:00:00Z")
	if done, err := completedSince(ctx, d.DB, bootCutoff(boot)); err != nil || done {
		t.Fatalf("Wednesday's stopped run is spared at boot: %v %v", done, err)
	}
	if done, _ := completedSince(ctx, d.DB, boot.Add(-bootFresh)); !done {
		t.Fatal("the fixture: Tuesday's retry completed in the last 24 hours")
	}
	// The same boot on Thursday before its slot waits for it: Tuesday's
	// retry does not count, Wednesday's run (rerun at that boot) does.
	if _, err := d.DB.Exec(ctx, `insert into screener_runs (status, started_at, finished_at, heartbeat_at, tickers)
		values ('completed', $1, $1::timestamptz + interval '15 minutes', $1::timestamptz + interval '15 minutes', 785)`, boot); err != nil {
		t.Fatal(err)
	}
	if done, err := completedSince(ctx, d.DB, bootCutoff(at("2026-10-15T09:00:00Z"))); err != nil || !done {
		t.Fatalf("Thursday 09:00: %v %v", done, err)
	}
}

// Runs are every weekday at 10:45 UTC, after the last Asian close.
func TestNextSlot(t *testing.T) {
	for in, want := range map[string]string{
		"2026-10-14T04:00:00Z": "2026-10-14T10:45:00Z", // Wednesday morning
		"2026-10-14T10:44:59Z": "2026-10-14T10:45:00Z",
		"2026-10-14T10:45:00Z": "2026-10-15T10:45:00Z", // the slot itself: the next one
		"2026-10-16T11:00:00Z": "2026-10-19T10:45:00Z", // Friday after the slot: Monday
		"2026-10-17T09:00:00Z": "2026-10-19T10:45:00Z", // Saturday
		"2026-10-18T23:59:00Z": "2026-10-19T10:45:00Z", // Sunday
		"2026-12-31T23:00:00Z": "2027-01-01T10:45:00Z", // across a year (a holiday run reads unchanged closes)
	} {
		now, _ := time.Parse(time.RFC3339, in)
		if got := nextSlot(now).Format(time.RFC3339); got != want {
			t.Fatalf("%s: %s, want %s", in, got, want)
		}
	}
}
