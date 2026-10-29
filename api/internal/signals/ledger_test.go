package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
)

// storeSteady stores a steady board at at with its picks, ledger universe and
// exits; valid false leaves its provenance out, so it never validates.
func storeSteady(t *testing.T, e *apptest.Env, at time.Time, bullish, bearish []Pick, universe []ledgerAsset, exits []ledgerExit, valid bool) {
	t.Helper()
	var q *quality
	if valid {
		q = &quality{Version: 1, Inputs: []research.NewsItem{}, Briefs: []research.MarketBrief{}}
		for _, p := range append(append([]Pick{}, bullish...), bearish...) {
			q.Origins = append(q.Origins, origin{PickedAt: p.PickedAt, Inputs: []string{}, Briefs: []string{}})
		}
	}
	if exits == nil {
		exits = []ledgerExit{}
	}
	raw, err := json.Marshal(struct {
		Bullish []Pick   `json:"bullish"`
		Bearish []Pick   `json:"bearish"`
		Quality *quality `json:"_quality,omitempty"`
		Ledger  *ledger  `json:"_ledger"`
	}{bullish, bearish, q, &ledger{Version: 1, Rule: rule, Universe: universe, Exits: exits}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(context.Background(), "insert into signal_boards (board, created_at) values ($1, $2)", raw, at); err != nil {
		t.Fatal(err)
	}
}

func syncOrFatal(t *testing.T, e *apptest.Env) {
	t.Helper()
	if err := syncLedger(context.Background(), e.DB); err != nil {
		t.Fatal(err)
	}
}

// ledgerRows is every signal_picks row as text, for comparing whole states.
func ledgerRows(t *testing.T, e *apptest.Env) string {
	t.Helper()
	var out string
	if err := e.DB.QueryRow(context.Background(), `select coalesce(string_agg(row_to_json(p)::text, E'\n' order by symbol, side, picked_at), '') from signal_picks p`).
		Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

type pickRow struct {
	Reason                         *string
	ExitPrice, ReturnPct           *float64
	ExitAt, RemovedAt, ScoredAt    *time.Time
	Result, Random, UpShare, Hours *float64
	N                              *int
	Universe                       string
}

func rowOf(t *testing.T, e *apptest.Env, symbol, side string) (pickRow, bool) {
	t.Helper()
	var r pickRow
	err := e.DB.QueryRow(context.Background(), `select exit_reason, exit_price, return_pct, exit_at, removed_at, scored_at, result_pct, random_pct,
		random_up_share, horizon_h, universe_n, universe::text from signal_picks where symbol = $1 and side = $2`, symbol, side).
		Scan(&r.Reason, &r.ExitPrice, &r.ReturnPct, &r.ExitAt, &r.RemovedAt, &r.ScoredAt, &r.Result, &r.Random, &r.UpShare, &r.Hours, &r.N, &r.Universe)
	if err != nil {
		return r, false
	}
	return r, true
}

func near9(a *float64, b float64) bool { return a != nil && math.Abs(*a-b) < 1e-9 }

// Acceptance 11: syncLedger records each shown steady board once: the pick
// board's new picks with its eligible universe, the exits the next boards
// resolved, and at the first board 72 h or more after the pick, the move to
// its prices against the universe's mean move (a random pick from the same
// list). Twice changes nothing; a board that does not validate is not
// recorded; a symbol missing at the scoring board defers its score.
func TestSyncLedger(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	pk := func(sym, side string, price float64, at time.Time) Pick {
		d := dir(side)
		return Pick{Symbol: sym, Side: side, Score: int(30 * d), PriceAtPick: price, PickedAt: at, Target: price * (1 + 0.06*d),
			Invalidation: price * (1 - 0.06*d), ValidUntil: at.Add(valid), VolTier: "high", Headlines: []Headline{}}
	}
	a, b := pk("A", "bullish", 100, t0), pk("B", "bearish", 50, t0)
	storeSteady(t, e, t0, []Pick{a}, []Pick{b}, []ledgerAsset{{"A", 100, true}, {"B", 50, true}, {"C", 10, true}, {"X", 7, false}}, nil, true)
	syncOrFatal(t, e)
	if r, ok := rowOf(t, e, "A", "bullish"); !ok || r.Reason != nil || r.ScoredAt != nil || r.Universe != `[{"p": 100, "s": "A"}, {"p": 50, "s": "B"}, {"p": 10, "s": "C"}]` {
		t.Fatalf("A at pick time: %+v %v", r, ok)
	}
	// Kept picks on later boards are not new rows.
	storeSteady(t, e, t0.Add(4*time.Hour), []Pick{a}, []Pick{b}, []ledgerAsset{{"A", 101, true}, {"B", 50.5, true}, {"C", 10, true}}, nil, true)
	// The board 74 h on: both expired at validUntil, and both are scored at its prices.
	s := t0.Add(74 * time.Hour)
	storeSteady(t, e, s, []Pick{}, []Pick{}, []ledgerAsset{{"A", 103, true}, {"B", 49, true}, {"C", 10.5, true}},
		[]ledgerExit{{"A", "bullish", t0, "expired", t0.Add(valid), 102.5}, {"B", "bearish", t0, "expired", t0.Add(valid), 49.5}}, true)
	syncOrFatal(t, e)
	ra, _ := rowOf(t, e, "A", "bullish")
	rb, _ := rowOf(t, e, "B", "bearish")
	if !near9(ra.Result, 3) || !near9(ra.Random, 2) || !near9(ra.UpShare, 2.0/3) || !near9(ra.Hours, 74) || *ra.N != 3 || !ra.ScoredAt.Equal(s) ||
		*ra.Reason != "expired" || *ra.ExitPrice != 102.5 || *ra.ReturnPct != 2.5 || !ra.ExitAt.Equal(t0.Add(valid)) || !ra.RemovedAt.Equal(s) {
		t.Fatalf("A %+v", ra)
	}
	if !near9(rb.Result, 2) || !near9(rb.Random, -2) || !near9(rb.UpShare, 1.0/3) || *rb.ReturnPct != 1 || *rb.N != 3 {
		t.Fatalf("B %+v", rb)
	}
	// A later board listing an exit again (its refresh fell back to an older board) never rewrites the first one.
	storeSteady(t, e, s.Add(2*time.Hour), []Pick{}, []Pick{}, []ledgerAsset{{"A", 90, true}},
		[]ledgerExit{{"A", "bullish", t0, "invalidated", t0.Add(valid), 94}}, true)
	before := ledgerRows(t, e)
	syncOrFatal(t, e)
	if ra2, _ := rowOf(t, e, "A", "bullish"); *ra2.Reason != "expired" || *ra2.ExitPrice != 102.5 || !ra2.RemovedAt.Equal(s) || ledgerRows(t, e) != before {
		t.Fatalf("A after a repeated exit %+v", ra2)
	}
	if _, err := e.DB.Exec(context.Background(), "delete from signal_ledger_sync"); err != nil { // as after a crash before the step
		t.Fatal(err)
	}
	syncOrFatal(t, e)
	syncOrFatal(t, e)
	if after := ledgerRows(t, e); after != before {
		t.Fatalf("a rerun changed the ledger:\n%s\n%s", before, after)
	}

	// A board that does not validate is not recorded; a missing price defers the score to the next board.
	storeSteady(t, e, t0.Add(78*time.Hour), []Pick{pk("D", "bullish", 5, t0.Add(78*time.Hour))}, []Pick{}, []ledgerAsset{{"D", 5, true}}, nil, false)
	p1 := t0.Add(80 * time.Hour)
	storeSteady(t, e, p1, []Pick{pk("E", "bullish", 20, p1)}, []Pick{}, []ledgerAsset{{"E", 20, true}, {"C", 10, true}}, nil, true)
	storeSteady(t, e, p1.Add(72*time.Hour), []Pick{}, []Pick{}, []ledgerAsset{{"C", 11, true}}, nil, true)
	storeSteady(t, e, p1.Add(76*time.Hour), []Pick{}, []Pick{}, []ledgerAsset{{"E", 21, true}, {"C", 12, true}}, nil, true)
	syncOrFatal(t, e)
	if _, ok := rowOf(t, e, "D", "bullish"); ok {
		t.Fatal("a board that does not validate entered the ledger")
	}
	if re, _ := rowOf(t, e, "E", "bullish"); !near9(re.Hours, 76) || !near9(re.Result, 5) || !near9(re.Random, 12.5) || *re.N != 2 {
		t.Fatalf("E, scored at the board after the one without its price: %+v", re)
	}
	var last time.Time
	if err := e.DB.QueryRow(context.Background(), "select last_board_at from signal_ledger_sync").Scan(&last); err != nil || !last.Equal(p1.Add(76*time.Hour)) {
		t.Fatalf("last board %v %v", last, err)
	}

	// A pick no board scores within 7 days stays unscored.
	p2 := t0.Add(200 * time.Hour)
	storeSteady(t, e, p2, []Pick{pk("F", "bullish", 3, p2)}, []Pick{}, []ledgerAsset{{"F", 3, true}}, nil, true)
	storeSteady(t, e, p2.Add(keep), []Pick{}, []Pick{}, []ledgerAsset{{"F", 4, true}}, nil, true)
	syncOrFatal(t, e)
	if rf, _ := rowOf(t, e, "F", "bullish"); rf.ScoredAt != nil {
		t.Fatalf("F scored a week on: %+v", rf)
	}
}

func f64(x float64) *float64 { return &x }
func str(s string) *string   { return &s }

// Acceptance 12: the record's excess CI is a t interval over non-overlapping
// 3-day block sums of (result − random), weighted by count, at 99%; the
// verdict needs 200 scored picks over 30 blocks.
func TestRecordCI(t *testing.T) {
	since := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	var rows []ledgerRow
	for _, x := range [][2]float64{{0, 1.0}, {0, -0.5}, {1, 2.0}, {2, -1.0}, {3, 0.5}, {5, 3.0}, {5, 1.0}, {6, -2.0}, {9, 0.0}, {10, 1.5}, {13, -0.5}} {
		at := since.Add(time.Duration(x[0]*24) * time.Hour)
		if x[0] == 0 {
			at = since.Add(-12 * time.Hour) // the same UTC day as since
		}
		rows = append(rows, ledgerRow{side: "bullish", pickedAt: at, reason: str("expired"), result: f64(x[1] + 0.25), random: f64(0.25), upShare: f64(0.5)})
	}
	first := rows[0].pickedAt
	s := sideStats(rows, &first, "")
	lo, hi := s.excess-recordZ*s.se, s.excess+recordZ*s.se
	if math.Abs(s.excess-0.4545454545) > 1e-9 || math.Abs(s.se-0.4218115754) > 1e-9 || math.Abs(lo+0.6320411636) > 1e-9 || math.Abs(hi-1.5411320727) > 1e-9 || s.blocks != 5 {
		t.Fatalf("stats %+v (lo %v hi %v)", s, lo, hi)
	}
	r := record(rows, since.Add(30*24*time.Hour))
	all := r.All
	if all.Verdict != "insufficient" || all.Scored != 11 || all.Blocks != 5 || *all.ExcessPct != 0.45 || *all.ExcessLo99 != -0.63 || *all.ExcessHi99 != 1.54 ||
		*all.MovedItsWayPct != 63.64 || *all.RandomMovedItsWayPct != 50 || *all.AvgResultPct != 0.7 || *all.AvgResultNetPct != 0.6 || *all.AvgRandomPct != 0.25 ||
		all.Expired != 11 || r.Bearish.Scored != 0 || r.Bearish.ExcessPct != nil || r.Bearish.Verdict != "insufficient" || !r.Since.Equal(first) {
		t.Fatalf("record %+v", r)
	}

	// Verdicts: 240 picks over 30 blocks with block means 0.8 / 1.0 / 1.2 is better, negated worse,
	// blocks alternating ±2 no difference, and 199 picks with a positive CI insufficient.
	verdict := func(blocks, perBlock int, mean func(b int) float64) RecordSide {
		var rows []ledgerRow
		for b := range blocks {
			for k := range perBlock {
				rows = append(rows, ledgerRow{side: "bearish", pickedAt: since.Add(time.Duration(b*blockDays*24+k) * time.Hour), reason: str("target_hit"),
					result: f64(mean(b)), random: f64(0), upShare: f64(0.4)})
			}
		}
		return record(rows, since).Bearish
	}
	cycle := func(b int) float64 { return []float64{0.8, 1.0, 1.2}[b%3] }
	for name, c := range map[string]struct {
		got  RecordSide
		want string
	}{
		"better":        {verdict(30, 8, cycle), "better"},
		"worse":         {verdict(30, 8, func(b int) float64 { return -cycle(b) }), "worse"},
		"no_difference": {verdict(30, 8, func(b int) float64 { return float64(2 - 4*(b%2)) }), "no_difference"},
	} {
		if c.got.Verdict != c.want {
			t.Errorf("%s: %+v", name, c.got)
		}
	}
	var rows199 []ledgerRow
	for i := range 199 {
		rows199 = append(rows199, ledgerRow{side: "bullish", pickedAt: since.Add(time.Duration(i) * 11 * time.Hour), result: f64(1 + float64(i%5)/10), random: f64(0)})
	}
	if r := record(rows199, since).Bullish; r.Verdict != "insufficient" || *r.ExcessLo99 <= 0 || r.Blocks < recordMinBlocks {
		t.Errorf("199 picks: %+v", r)
	}
	if one := record(rows199[:5], since).Bullish; one.Blocks != 1 || one.ExcessPct != nil || one.AvgResultPct == nil || one.Verdict != "insufficient" {
		t.Errorf("one block: %+v", one)
	}
	if b := verdict(30, 7, cycle); b.Scored != 210 || b.Verdict != "better" {
		t.Errorf("210 picks over 30 blocks: %+v", b)
	}
	// Enough picks but too short a span: 261 picks over 29 blocks with a CI above 0 are still insufficient.
	if b := verdict(29, 9, cycle); b.Scored != 261 || b.Blocks != 29 || *b.ExcessLo99 <= 0 || b.Verdict != "insufficient" {
		t.Errorf("261 picks over 29 blocks: %+v", b)
	}

	// Open and unscored: unscored rows within the last 7 days, and older.
	r = record([]ledgerRow{{side: "bullish", pickedAt: since}, {side: "bearish", pickedAt: since.Add(-8 * 24 * time.Hour)}, rows[1]}, since.Add(time.Hour))
	if r.Open != 1 || r.Unscored != 1 || r.All.Scored != 1 || !r.Since.Equal(since.Add(-8*24*time.Hour)) {
		t.Errorf("open/unscored %+v", r)
	}
	if r := record(nil, since); r.Since != nil || r.All.Verdict != "insufficient" || r.Replay != replayRef || r.Rule != "steady-v1" || r.HorizonHours != 72 ||
		r.CostPct != 0.1 || r.Confidence != 0.99 || r.MinPicks != 200 || r.MinBlocks != 30 {
		t.Errorf("empty record %+v", r)
	}
}

// countingMarket points the deps' market client at a proxy of the fake that
// counts every request, and returns the count.
func countingMarket(t *testing.T, e *apptest.Env) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	target, _ := url.Parse(e.Binance.URL)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, target.String()+r.URL.RequestURI(), r.Body)
		req.Header = r.Header
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
	}))
	t.Cleanup(proxy.Close)
	e.Market = testMarketClient(proxy.URL)
	return &n
}

// Acceptance 13, end to end: a refresh resolves the previous board's held
// pick from the fake venue's 1h candles, stores the exit in its ledger part,
// the sync records it, and GET /api/signals serves it from the ledger (the
// newest exit first, with the new fields) with the record, reading no
// candle at all.
func TestRefreshLedgerAndRead(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	at := markettest.End.Add(6 * time.Hour) // the fake's daily bars are current: every asset is eligible
	t0 := markettest.End.Add(-20 * time.Hour)
	btc := Pick{Symbol: "BTC", Side: "bullish", Score: 30, PriceAtPick: 100_000, PickedAt: t0, Target: 100, Invalidation: 1, // touched at its first bar
		ValidUntil: t0.Add(valid), VolTier: "medium", Rationale: "Kept.", Headlines: []Headline{}}
	eth := Pick{Symbol: "ETH", Side: "bearish", Score: -30, PriceAtPick: 4000, PickedAt: t0, Target: 1, Invalidation: 1e9, // never touched
		ValidUntil: t0.Add(valid), VolTier: "medium", Rationale: "Kept too.", Headlines: []Headline{}}
	storeSteady(t, e, t0, []Pick{btc}, []Pick{eth}, []ledgerAsset{{"BTC", 100_000, true}, {"ETH", 4000, true}}, nil, true)
	if err := refresh(ctx, e.Deps, at); err != nil {
		t.Fatal(err)
	}
	b := latestOrFatal(t, e)
	if b == nil || !b.CreatedAt.Equal(at) || len(b.Bullish) != 0 || len(b.Bearish) != 1 || b.Bearish[0].Rationale != "Kept too." || !b.Bearish[0].PickedAt.Equal(t0) ||
		b.Bearish[0].Score != 9 || b.ledger == nil || len(b.ledger.Exits) != 1 {
		t.Fatalf("the refreshed board %+v", b)
	}
	if x := b.ledger.Exits[0]; x.Symbol != "BTC" || x.Reason != "target_hit" || x.ExitPrice != 100 || !x.ExitAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("exit %+v", x)
	}
	if len(e.Bedrock.Requests()) != 0 {
		t.Fatal("a board without a new pick called the model")
	}
	syncOrFatal(t, e) // as Run's refresh does once the board validates
	if r, _ := rowOf(t, e, "BTC", "bullish"); r.Reason == nil || *r.Reason != "target_hit" || *r.ReturnPct != -99.9 || !r.RemovedAt.Equal(at) {
		t.Fatalf("ledger row %+v", r)
	}

	// More exits than the history shows: the 12 newest, newest exit first.
	for i := range 13 {
		if _, err := e.DB.Exec(ctx, `insert into signal_picks (symbol, side, picked_at, price_at_pick, target, invalidation, valid_until, score_at_pick, universe,
			exit_reason, exit_at, exit_price, return_pct, removed_at, scored_at, result_pct, random_pct, random_up_share, universe_n, horizon_h)
			values ($1, 'bearish', $2, 10, 9, 11, $2::timestamptz + interval '72 hours', -30, '[]', 'invalidated', $3, 11, -10, $3, $4, -12.345, 1.004, 0.5, 40, 72)`,
			fmt.Sprintf("S%02d", i), at.Add(-time.Duration(100+i)*time.Hour), at.Add(-time.Duration(i+1)*time.Hour), at.Add(-time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	calls := countingMarket(t, e)
	Forget(e.Deps)
	w := e.Do("GET", "/api/signals", nil)
	got := apptest.JSON[response](t, w)
	var raw struct {
		History []map[string]json.RawMessage `json:"history"`
	}
	json.Unmarshal(w.Body.Bytes(), &raw)
	if w.Code != 200 || len(got.History) != 12 || got.History[0].Symbol != "BTC" || got.History[1].Symbol != "S00" || got.History[11].Symbol != "S10" ||
		got.HistoryUnavailable || got.Record == nil || got.Record.Unscored != 2 || got.Record.Open != 0 {
		t.Fatalf("read %d: %s", w.Code, w.Body)
	}
	if h := got.History[0]; h.Reason != "target_hit" || h.Invalidation != 1 || !h.ExitAt.Equal(t0.Add(time.Hour)) || h.ExitPrice != 100 || h.Result72Pct != nil ||
		h.ScoredAt != nil || !h.RemovedAt.Equal(at) || !h.PickedAt.Equal(t0) {
		t.Fatalf("the newest exit %+v", h)
	}
	if h := got.History[1]; *h.Result72Pct != -12.35 && *h.Result72Pct != -12.34 || *h.Random72Pct != 1 || h.ScoredAt == nil || h.Reason != "invalidated" {
		t.Fatalf("a scored exit %+v", h)
	}
	keys := []string{}
	for k := range raw.History[0] {
		keys = append(keys, k)
	}
	if want := "symbol side pickedAt removedAt priceAtPick target invalidation exitAt exitPrice returnPct result72Pct random72Pct scoredAt reason"; len(keys) != 14 ||
		!containsAllKeys(want, keys) {
		t.Fatalf("history keys %v", keys)
	}
	if r := got.Record; r.All.Scored != 13 || r.Bearish.Scored != 13 || r.Bullish.Scored != 0 || r.All.Invalidated != 13 || r.Since == nil {
		t.Fatalf("record %+v", r)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("GET read the market %d times", n)
	}
}

func containsAllKeys(want string, got []string) bool {
	for _, k := range got {
		if !strings.Contains(" "+want+" ", " "+k+" ") {
			return false
		}
	}
	return true
}

// Compatibility (§9): a board stored before steady-v1 (no invalidation, no
// ledger part) still reads and validates, never enters the ledger, and the
// first steady refresh drops its picks unrecorded and fills from scratch.
func TestLegacyBoardStaysOutOfTheLedger(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	at := markettest.End.Add(6 * time.Hour)
	t0 := at.Add(-4 * time.Hour)
	old, _ := json.Marshal(map[string]any{"bullish": []any{map[string]any{"symbol": "BTC", "side": "bullish", "score": 40, "priceAtPick": 95_000,
		"pickedAt": t0, "target": 1, "validUntil": t0.Add(valid), "volTier": "medium", "rationale": "Old.", "headlines": []any{},
		"factors": map[string]any{"trend": "uptrend"}}}, "bearish": []any{}})
	if _, err := e.DB.Exec(ctx, "insert into signal_boards (board, created_at) values ($1, $2)", researchtest.Board(t, e.DB, old), t0); err != nil {
		t.Fatal(err)
	}
	if b := latestOrFatal(t, e); b == nil || b.ledger != nil || b.Bullish[0].Invalidation != 0 {
		t.Fatalf("the legacy board %+v", b)
	}
	if err := refresh(ctx, e.Deps, at); err != nil {
		t.Fatal(err)
	}
	syncOrFatal(t, e)
	b := latestOrFatal(t, e)
	var n int
	if err := e.DB.QueryRow(ctx, "select count(*) from signal_picks").Scan(&n); err != nil || n != 0 || b == nil || !b.CreatedAt.Equal(at) ||
		len(b.Bullish) != 0 || b.ledger == nil || len(b.ledger.Exits) != 0 {
		t.Fatalf("after the first steady refresh: %d ledger rows (%v), board %+v", n, err, b)
	}
}
