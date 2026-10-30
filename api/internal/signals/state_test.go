package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
)

// A full week of boards (42 × 100 cited wire rows) is read once per minute,
// not on every read: ten concurrent cold reads validate the
// newest board once (the exits and the record come from the ledger, and none
// of these boards stored before steady-v1 has a pick that could have left
// it), later reads none and come back fast.
func TestReadComputedOncePerMinute(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	var inputs []research.NewsItem
	for i := range 100 {
		var n research.NewsItem
		if err := e.DB.QueryRow(ctx, `insert into news_items (title,url,source,description,symbols,topic) values ($1,$2,'Reuters',$3,'{BTC}','asset')
			returning id,title,url,source,description,published_at,symbols,topic`,
			fmt.Sprintf("Bitcoin BTC price climbs as ETF flows grow, day %d", i), fmt.Sprintf("https://www.reuters.com/btc-%d", i),
			"Bitcoin rose as spot ETF inflows continued.").
			Scan(&n.ID, &n.Title, &n.URL, &n.Source, &n.Description, &n.PublishedAt, &n.Symbols, &n.Topic); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, n)
	}
	q := quality{Version: 1, Inputs: inputs, InputCount: len(inputs), Briefs: []research.MarketBrief{}}
	raw, _ := json.Marshal(struct {
		Bullish []Pick   `json:"bullish"`
		Bearish []Pick   `json:"bearish"`
		Quality *quality `json:"_quality"`
	}{[]Pick{}, []Pick{}, &q})
	for i := range 42 { // every 4 h for a week, the newest due for its successor
		if _, err := e.DB.Exec(ctx, `insert into signal_boards (board, created_at) values ($1, now() - make_interval(mins => $2))`, raw, 240*i+235); err != nil {
			t.Fatal(err)
		}
	}

	before := validations.Load()
	var wg sync.WaitGroup
	got := make([]response, 10)
	for i := range got {
		wg.Go(func() {
			w := e.Do("GET", "/api/signals", nil)
			if w.Code == 200 {
				got[i] = apptest.JSON[response](t, w)
			}
		})
	}
	wg.Wait()
	if n := validations.Load() - before; n != 1 {
		t.Errorf("10 cold reads validated %d boards, want the newest once", n)
	}
	for _, r := range got {
		if r.Board == nil || r.Board.ID != got[0].Board.ID || !r.Refreshing || r.Refresh.State != "due" {
			t.Fatalf("cold read %+v", r)
		}
	}
	before = validations.Load()
	for i := range 4 {
		start := time.Now()
		r := get(t, e)
		if took := time.Since(start); took > 50*time.Millisecond || r.Board == nil || r.Board.ID != got[0].Board.ID {
			t.Errorf("read %d took %v: %+v", i+2, took, r.Board)
		}
	}
	if n := validations.Load() - before; n != 0 {
		t.Errorf("cached reads validated %d boards", n)
	}
}

// A refresh that stores a board drops the cached read: the next read shows
// it, current, not the due board cached before it.
func TestReadAfterRefresh(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	old := storeBoard(t, e, emptyBoard, 5*time.Hour)
	if r := get(t, e); r.Board == nil || r.Board.ID != old || r.Refresh.State != "due" {
		t.Fatalf("before: %+v %+v", r.Board, r.Refresh)
	}
	if err := flightOf(e.Deps).refresh(context.Background(), e.Deps, "test"); err != nil {
		t.Fatal(err)
	}
	r := get(t, e)
	if r.Board == nil || r.Board.ID == old || time.Since(r.Board.CreatedAt) > time.Minute || r.Refreshing || r.Refresh.State != "idle" {
		t.Fatalf("after a refresh: %+v %+v", r.Board, r.Refresh)
	}
	// The stored board entered the ledger under the same lock.
	var synced time.Time
	if err := e.DB.QueryRow(context.Background(), "select last_board_at from signal_ledger_sync").Scan(&synced); err != nil || !synced.Equal(r.Board.CreatedAt) {
		t.Fatalf("ledger synced to %v %v", synced, err)
	}
}

// A refresh that keeps failing retries on a ceiling: 2, 5,
// then 10 minutes four times, then hourly; GET says retrying with the retry
// time, running while a refresh computes and idle after one succeeds; a
// restart keeps the failures.
func TestRefreshRetryCeiling(t *testing.T) {
	strip := func(t *testing.T, e *apptest.Env) {
		if _, err := e.DB.Exec(context.Background(), `create function strip_quality() returns trigger language plpgsql as
			$$ begin new.board := new.board - '_quality'; return new; end $$;
			create trigger strip_quality before insert on signal_boards for each row execute function strip_quality()`); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("ceiling", func(t *testing.T) {
		e := apptest.New(t, Register, apptest.Options{})
		logs := captureLogs(t)
		var scaled []time.Duration // the real backoff, a minute to a millisecond
		for _, d := range backoff {
			scaled = append(scaled, d/time.Minute*time.Millisecond)
		}
		pace(t, time.Hour, scaled...)
		strip(t, e)
		start(t, e)
		eventually(t, "nine failures", func() bool { return strings.Contains(logs.String(), "failures=9 ") })
		for i, want := range []string{"2ms", "5ms", "10ms", "10ms", "10ms", "10ms", "60ms", "60ms", "60ms"} {
			if line := fmt.Sprintf("failures=%d retry_in=%s", i+1, want); !strings.Contains(logs.String(), line) {
				t.Errorf("no %q in\n%s", line, logs)
			}
		}
	})

	t.Run("state", func(t *testing.T) {
		e := apptest.New(t, Register, apptest.Options{})
		uncached(t)
		logs := captureLogs(t)
		pace(t, time.Hour, time.Hour)
		strip(t, e)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { Run(ctx, e.Deps); close(done) }()
		eventually(t, "the failure", func() bool { return strings.Contains(logs.String(), "failures=1 ") })
		r := get(t, e)
		if r.Board != nil || !r.Refreshing || r.Refresh.State != "retrying" || r.Refresh.Failures != 1 || r.Refresh.RetryAt == nil ||
			time.Until(*r.Refresh.RetryAt) < 59*time.Minute || r.Refresh.LastErrorAt == nil || time.Since(*r.Refresh.LastErrorAt) > time.Minute {
			t.Fatalf("during the backoff: %+v", r.Refresh)
		}
		var lastErr string
		if err := e.DB.QueryRow(context.Background(), `select last_error from signals_refresh`).Scan(&lastErr); err != nil || lastErr != "signals: the new board does not validate" {
			t.Fatalf("stored error %q %v", lastErr, err)
		}

		// A restart keeps the failures and waits out the stored retry time.
		f := flightOf(e.Deps)
		restart := func(retryIn string) {
			cancel()
			<-done
			f.mu.Lock()
			f.state = refreshState{} // a new process: nothing in memory
			f.mu.Unlock()
			if _, err := e.DB.Exec(context.Background(), `update signals_refresh set retry_at = now() + $1::interval`, retryIn); err != nil {
				t.Fatal(err)
			}
			ctx, cancel = context.WithCancel(context.Background())
			done = make(chan struct{})
			go func() { Run(ctx, e.Deps); close(done) }()
		}
		t.Cleanup(func() { cancel(); <-done })
		restart("200 milliseconds")
		eventually(t, "the second failure", func() bool { return strings.Contains(logs.String(), "failures=2 ") })
		if !strings.Contains(logs.String(), "waiting out the retry from before the restart") {
			t.Errorf("no resumed wait in\n%s", logs)
		}

		// Running while a refresh computes; idle after one succeeds.
		f.running.Store(true)
		if r := get(t, e); r.Refresh.State != "running" || r.Refresh.RetryAt != nil || r.Refresh.Failures != 2 {
			t.Fatalf("while running: %+v", r.Refresh)
		}
		f.running.Store(false)
		if _, err := e.DB.Exec(context.Background(), `drop trigger strip_quality on signal_boards`); err != nil {
			t.Fatal(err)
		}
		restart("0 seconds")
		eventually(t, "the board", func() bool { return latestOrFatal(t, e) != nil })
		eventually(t, "idle", func() bool { r := get(t, e); return r.Refresh.State == "idle" && r.Refresh.Failures == 0 })
		var failures int
		if err := e.DB.QueryRow(context.Background(), `select failures from signals_refresh`).Scan(&failures); err != nil || failures != 0 {
			t.Fatalf("stored failures %d %v", failures, err)
		}
	})
}

// At most 12 rationale model calls a UTC day: the 13th board keeps its Go
// rationales with no model call, and the count starts over the next day.
func TestModelCallCap(t *testing.T) {
	var replies []llmtest.Reply
	for range modelCallsPerDay + 1 {
		replies = append(replies, llmtest.Reply{Text: `{"picks":[{"symbol":"BTC","rationale":"BTC is up 40% in a month.","target":null}]}`})
	}
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, replies...)})
	board := func() Board { return build([]asset{mk("BTC", 40, "uptrend")}, nil, nil, nil, now) }
	for i := range modelCallsPerDay {
		b := board()
		annotate(context.Background(), e.Deps, &b)
		if b.Bullish[0].Rationale != "BTC is up 40% in a month." {
			t.Fatalf("call %d: %q", i+1, b.Bullish[0].Rationale)
		}
	}
	b := board()
	annotate(context.Background(), e.Deps, &b)
	if n := len(e.Bedrock.Requests()); n != modelCallsPerDay || b.Bullish[0].Rationale != "30-day +40.0%, RSI 50, uptrend." {
		t.Fatalf("past the cap: %d model calls, rationale %q", n, b.Bullish[0].Rationale)
	}
	f := flightOf(e.Deps)
	f.mu.Lock()
	st := f.state
	f.mu.Unlock()
	if st.ModelCalls != modelCallsPerDay || st.ModelDay != time.Now().UTC().Format(time.DateOnly) {
		t.Errorf("mirrored state %+v", st)
	}
	// The next UTC day starts over.
	if _, err := e.DB.Exec(context.Background(), `update signals_refresh set model_day = model_day - 1`); err != nil {
		t.Fatal(err)
	}
	b = board()
	annotate(context.Background(), e.Deps, &b)
	if n := len(e.Bedrock.Requests()); n != modelCallsPerDay+1 || b.Bullish[0].Rationale != "BTC is up 40% in a month." {
		t.Fatalf("the next day: %d model calls, rationale %q", n, b.Bullish[0].Rationale)
	}
	var calls int
	if err := e.DB.QueryRow(context.Background(), `select model_calls from signals_refresh`).Scan(&calls); err != nil || calls != 1 {
		t.Fatalf("stored calls %d %v", calls, err)
	}
}

// Run's check drops the cached read when the newest valid board is another
// one (stored elsewhere, or the shown one no longer valid), and keeps it
// otherwise.
func TestCheckDropsCachedRead(t *testing.T) {
	f := &flight{}
	a, b := &Board{ID: "a"}, &Board{ID: "b"}
	for _, c := range []struct {
		shown, latest *Board
		kept          bool
	}{{a, a, true}, {a, b, false}, {a, nil, false}, {nil, a, false}, {nil, nil, true}} {
		f.view, f.gen = &view{board: c.shown, at: time.Now()}, 0
		f.check(c.latest)
		if kept := f.view != nil; kept != c.kept || kept == (f.gen == 1) {
			t.Errorf("shown %v, latest %v: kept %v gen %d", c.shown, c.latest, kept, f.gen)
		}
	}
}

// An older board stored before steady-v1 that no longer validates restarts
// the legacy chain after it (the legacy rule, kept for those boards):
// no removal is read from it or across it (SOL left the oldest board at the
// invalid one), so no number from an unvalidated board is shown, and the
// boards after it still give theirs. The same holds with a steady board shown
// on top, where the shown legacy board's pick left at the switch.
func TestLegacyHistorySurvivesOneInvalidBoard(t *testing.T) {
	for _, steadyTop := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy shown", true: "steady shown"}[steadyTop], func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{})
			board := func(age time.Duration, sym, title, url string) []byte {
				at := time.Now().Add(-age).UTC().Truncate(time.Second)
				p := Pick{Symbol: sym, Side: "bullish", Score: 30, PriceAtPick: 18, PickedAt: at, Target: 1000,
					ValidUntil: at.Add(72 * time.Hour), VolTier: "high", Headlines: []Headline{{title, url, "CoinDesk"}}}
				raw, _ := json.Marshal(Board{Bullish: []Pick{p}, Bearish: []Pick{}})
				return researchtest.Board(t, e.DB, raw)
			}
			if _, err := e.DB.Exec(context.Background(), `insert into signal_boards (board, created_at) values
				($1, now() - interval '12 hours'), ($2, now() - interval '8 hours'), ($3, now() - interval '4 hours'), ($4, now() - interval '10 minutes')`,
				board(12*time.Hour, "SOL", "Solana SOL gains on DEX volumes", "https://www.coindesk.com/sol-1"),
				board(8*time.Hour, "LINK", "Chainlink LINK rallies on oracle deal", "https://www.coindesk.com/link-1"),
				board(4*time.Hour, "BTC", "Bitcoin ETF inflows top $1bn", "https://www.coindesk.com/btc-etf"),
				board(10*time.Minute, "ETH", "Ether ETH climbs as staking grows", "https://www.coindesk.com/eth-1")); err != nil {
				t.Fatal(err)
			}
			shown, want := "ETH", []string{"BTC", "LINK", "SOL"}
			if steadyTop {
				storeSteady(t, e, time.Now().Add(-5*time.Minute).UTC().Truncate(time.Microsecond), []Pick{}, []Pick{}, []ledgerAsset{}, nil, true)
				shown, want = "", []string{"ETH", "BTC", "LINK", "SOL"}
			}
			check := func(when string, want []string) {
				t.Helper()
				r := get(t, e)
				var got []string
				for _, h := range r.History {
					if !h.Legacy || h.Reason != "left" {
						t.Errorf("%s: row %+v", when, h)
					}
					got = append(got, h.Symbol)
				}
				if r.Board == nil || shown != "" && (len(r.Board.Bullish) != 1 || r.Board.Bullish[0].Symbol != shown) || shown == "" && len(r.Board.Bullish) != 0 ||
					strings.Join(got, ",") != strings.Join(want, ",") || r.HistoryUnavailable {
					t.Fatalf("%s: board %+v, history %v (want %v), unavailable %v", when, r.Board, got, want, r.HistoryUnavailable)
				}
			}
			check("before", want)
			// The LINK board's cited row changed: that board no longer validates.
			if _, err := e.DB.Exec(context.Background(), `update news_items set description = 'updated snippet' where url = 'https://www.coindesk.com/link-1'`); err != nil {
				t.Fatal(err)
			}
			Forget(e.Deps)
			check("after", want[:len(want)-2])
		})
	}
}

// A 1h candle failure leaves the legacy rows out whole, never a partial list:
// BTC's candles answer and LINK's, read after them, fail.
func TestLegacyHistoryNeverPartial(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	at := time.Now().Add(-4 * time.Hour).UTC().Truncate(time.Second)
	pick := func(sym string) Pick {
		return Pick{Symbol: sym, Side: "bullish", Score: 30, PriceAtPick: 18, PickedAt: at, Target: 1000,
			ValidUntil: at.Add(72 * time.Hour), VolTier: "high", Headlines: []Headline{}}
	}
	older, _ := json.Marshal(Board{Bullish: []Pick{pick("BTC"), pick("LINK")}, Bearish: []Pick{}})
	if _, err := e.DB.Exec(context.Background(), `insert into signal_boards (board, created_at) values ($1, now() - interval '4 hours')`,
		researchtest.Board(t, e.DB, older)); err != nil {
		t.Fatal(err)
	}
	storeSteady(t, e, time.Now().Add(-10*time.Minute).UTC().Truncate(time.Microsecond), []Pick{}, []Pick{}, []ledgerAsset{}, nil, true)
	e.Binance.Down("LINKUSDT", true)
	if r := get(t, e); r.Board == nil || len(r.History) != 0 || !r.HistoryUnavailable {
		t.Fatalf("LINK's candles down: %+v", r)
	}
	e.Binance.Down("LINKUSDT", false)
	e.Market = testMarketClient(e.Binance.URL) // past the candle cache
	Forget(e.Deps)                             // past the read cache
	if r := get(t, e); len(r.History) != 2 || r.History[0].Symbol != "BTC" || r.History[1].Symbol != "LINK" || r.HistoryUnavailable {
		t.Fatalf("market up: %+v", r)
	}
}
