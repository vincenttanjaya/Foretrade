package signals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

// A cold read with a warm memo validates only the shown board (gap round
// G22): a week of 42 boards shaped like refresh's (124 inputs and a brief
// snapshot each) used to be re-validated on every recompute, 3 s at 42.
// Forget, as a stored refresh does, validates every board again.
func TestColdRecomputeMemo(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	var inputs []research.NewsItem
	for i := range 124 {
		var n research.NewsItem
		if err := e.DB.QueryRow(ctx, `insert into news_items (title,url,source,description,symbols,topic) values ($1,$2,'Reuters',$3,'{BTC}','asset')
			returning id,title,url,source,description,published_at,symbols,topic,regions`,
			fmt.Sprintf("Bitcoin BTC price climbs as ETF flows grow, day %d", i), fmt.Sprintf("https://www.reuters.com/btc-%d", i),
			strings.Repeat("Bitcoin rose as spot ETF inflows continued for another session. ", 3)).
			Scan(&n.ID, &n.Title, &n.URL, &n.Source, &n.Description, &n.PublishedAt, &n.Symbols, &n.Topic, &n.Regions); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, n)
	}
	brief := research.MarketBrief{Headline: "Bitcoin climbs", Summary: "BTC up.", ArticleCount: 100,
		Bullets:   []research.BriefBullet{{Text: "a", Symbols: []string{}, Sources: []string{inputs[0].ID}}, {Text: "b", Symbols: []string{}, Sources: []string{inputs[1].ID}}, {Text: "c", Symbols: []string{}, Sources: []string{inputs[2].ID}}},
		Sentiment: []research.Sentiment{{Symbol: "BTC", Score: 0.4, Label: "bullish", Reason: "flows"}}}
	raw, _ := json.Marshal(struct {
		research.MarketBrief
		Quality research.InputQuality `json:"_quality"`
	}{brief, research.InputQuality{Version: 1, Inputs: inputs[:100]}})
	if err := e.DB.QueryRow(ctx, `insert into market_briefs (brief) values ($1) returning id, created_at`, raw).Scan(&brief.ID, &brief.CreatedAt); err != nil {
		t.Fatal(err)
	}
	s := 0.4
	q := quality{Version: 1, Inputs: inputs, InputCount: len(inputs), Briefs: []research.MarketBrief{brief}}
	picks := []Pick{{Symbol: "BTC", Side: "bullish", Score: 40, PriceAtPick: 100, PickedAt: time.Now(), Target: 110,
		ValidUntil: time.Now().Add(72 * time.Hour), VolTier: "low", Factors: Factors{Sentiment: &s}, Headlines: []Headline{}}}
	board, _ := json.Marshal(struct {
		Bullish []Pick   `json:"bullish"`
		Bearish []Pick   `json:"bearish"`
		Quality *quality `json:"_quality"`
	}{picks, []Pick{}, &q})
	for i := range 42 {
		if _, err := e.DB.Exec(ctx, `insert into signal_boards (board, created_at) values ($1, now() - make_interval(mins => $2))`, board, 240*i+30); err != nil {
			t.Fatal(err)
		}
	}
	Forget(e.Deps)
	before, start := validations.Load(), time.Now()
	if r := get(t, e); r.Board == nil {
		t.Fatal("no board")
	}
	cold := time.Since(start)
	if n := validations.Load() - before; n != 42 {
		t.Fatalf("a first read validates every board once: %d", n)
	}
	f := flightOf(e.Deps)
	f.mu.Lock()
	f.view = nil // the view cache expired; the memo is warm
	f.mu.Unlock()
	before, start = validations.Load(), time.Now()
	if r := get(t, e); r.Board == nil {
		t.Fatal("no board")
	}
	took := time.Since(start)
	if n := validations.Load() - before; n != 1 {
		t.Fatalf("a recompute with a warm memo validated %d boards, want only the shown one", n)
	}
	t.Logf("42 boards: cold recompute %v with a warm memo (one validation), %v without (42)", took.Round(time.Millisecond), cold.Round(time.Millisecond))
	// The count above is the gate. The clock is compared with the same run's
	// 42-board read, so machine load cancels: about 1/20 of it at rest (95-127 ms
	// against 2-3 s at load 0.4); a third would mean per-board work came back.
	if took > cold/3 {
		t.Errorf("warm-memo recompute took %v, the 42-board read %v", took, cold)
	}
	// After Forget every board is validated again. Twelve boards show it: one
	// validation of this shape takes about 2 s under -race on a loaded machine.
	if _, err := e.DB.Exec(ctx, `delete from signal_boards where id not in (select id from signal_boards order by created_at desc limit 12)`); err != nil {
		t.Fatal(err)
	}
	Forget(e.Deps)
	before = validations.Load()
	get(t, e)
	if n := validations.Load() - before; n != 12 {
		t.Fatalf("after Forget every board is validated again: %d", n)
	}
	// A remembered validation is reused within memoTTL, and redone after it.
	all, err := stored(ctx, e.DB)
	if err != nil || len(all) != 12 {
		t.Fatal(len(all), err)
	}
	before = validations.Load()
	if ok, err := f.validOlder(ctx, e.DB, &all[11]); !ok || err != nil || validations.Load() != before {
		t.Fatalf("remembered: %v %v %d", ok, err, validations.Load()-before)
	}
	defer func(ttl time.Duration) { memoTTL = ttl }(memoTTL)
	memoTTL = 0
	if ok, err := f.validOlder(ctx, e.DB, &all[11]); !ok || err != nil || validations.Load() != before+1 {
		t.Fatalf("expired: %v %v %d", ok, err, validations.Load()-before)
	}
	// Steady boards (with a ledger part) leave through the ledger: a cold read
	// validates the shown board alone, memo or not, and walks no older board.
	if _, err := e.DB.Exec(ctx, `update signal_boards set board = board || '{"_ledger": {"version": 1, "rule": "steady-v1", "universe": [], "exits": []}}'`); err != nil {
		t.Fatal(err)
	}
	Forget(e.Deps)
	before = validations.Load()
	if r := get(t, e); r.Board == nil || r.HistoryUnavailable || len(r.History) != 0 {
		t.Fatalf("steady boards: %+v", r)
	}
	if n := validations.Load() - before; n != 1 {
		t.Fatalf("a cold read of steady boards validated %d, want only the shown one", n)
	}
}

// The legacy history (boards stored before steady-v1) reads enough 1h bars to
// reach back to its oldest pick (gap round G22): a target hit 5 h after a pick 216 h ago reads target_hit, where the
// 200 newest bars cut those hours off and read expired.
func TestHistoryWindowReachesEarlyTargetHit(t *testing.T) {
	clock := time.Now().Truncate(time.Hour)
	pickedAt := clock.Add(-216 * time.Hour)
	p := Pick{Symbol: "BTC", Side: "bullish", PickedAt: pickedAt, PriceAtPick: 100, Target: 110, ValidUntil: pickedAt.Add(72 * time.Hour)}
	boards := []Board{{CreatedAt: pickedAt, Bullish: []Pick{p}}, {CreatedAt: clock.Add(-144 * time.Hour), Bullish: []Pick{}}}
	var tape []market.Candle
	for h := -1100; h < 0; h++ {
		c := market.Candle{T: clock.Add(time.Duration(h) * time.Hour).Unix(), O: 100, H: 101, L: 99, C: 100}
		if h == -211 {
			c.H = 112
		}
		tape = append(tape, c)
	}
	newest := func(n int) func(string) ([]market.Candle, error) {
		return func(string) ([]market.Candle, error) { return tape[len(tape)-n:], nil }
	}
	n := historyBars(boards, clock)
	if rows := legacyHistory(boards, newest(n)); n != 300 || len(rows) != 1 || rows[0].Reason != "target_hit" {
		t.Fatalf("%d bars: %+v", n, rows)
	}
	if rows := legacyHistory(boards, newest(200)); len(rows) != 1 || rows[0].Reason != "expired" {
		t.Fatalf("200 bars (before): %+v", rows)
	}
	for _, c := range []struct {
		age  time.Duration
		want int
	}{{0, 200}, {150 * time.Hour, 200}, {198 * time.Hour, 200}, {199 * time.Hour, 300}, {2000 * time.Hour, 1000}} {
		b := []Board{{Bullish: []Pick{{PickedAt: clock.Add(-c.age)}}}}
		if got := historyBars(b, clock); got != c.want {
			t.Errorf("a pick %v old: %d bars, want %d", c.age, got, c.want)
		}
	}
}

// A legacy board a gap-round-2 build stored records why each pick of the
// board before it left (gap round G22): LINK still scored above 20 but four
// stronger picks took the side (outranked), SOL's score fell under the
// threshold (score_fell), ETH had no fresh bar (no_data). The legacy history
// reads the recorded reason; target_hit and expired win, and a board that
// recorded none reads "left". The steady build records none: its exits are in
// its ledger part.
func TestRemovalReasons(t *testing.T) {
	t0 := time.Now().Add(-8 * time.Hour).Truncate(time.Hour)
	at := t0.Add(4 * time.Hour)
	p := func(sym string) Pick {
		return Pick{Symbol: sym, Side: "bullish", PriceAtPick: 100, PickedAt: t0, Target: 1000, ValidUntil: t0.Add(72 * time.Hour)}
	}
	first := Board{CreatedAt: t0, Bullish: []Pick{p("BTC"), p("ETH"), p("SOL"), p("LINK")}}
	b, _ := json.Marshal(struct {
		Bullish []Pick        `json:"bullish"`
		Bearish []Pick        `json:"bearish"`
		Dropped []droppedPick `json:"_dropped"`
	}{[]Pick{p("BTC"), p("XRP"), p("ADA"), p("DOGE")}, []Pick{}, []droppedPick{{Symbol: "SOL", Side: "bullish", Reason: "score_fell", Score: new(-4)},
		{Symbol: "LINK", Side: "bullish", Reason: "outranked", Score: new(25)}, {Symbol: "ETH", Side: "bullish", Reason: "no_data"}}})
	second, err := scanBoard(&fakeRow{vals: []any{"id-2", b, at}}) // the stored row keeps the list across a read
	if err != nil || len(second.dropped) != 3 || !second.legacy() || second.dropped[0].Reason != "score_fell" || *second.dropped[0].Score != -4 {
		t.Fatalf("scanned %+v %v", second.dropped, err)
	}
	rows := legacyHistory([]Board{first, second}, func(string) ([]market.Candle, error) {
		return []market.Candle{{T: t0.Unix(), H: 101, L: 99, C: 100}, {T: t0.Add(2 * time.Hour).Unix(), H: 101, L: 99, C: 100}}, nil
	})
	got := map[string]string{}
	for _, r := range rows {
		got[r.Symbol] = r.Reason
	}
	if got["LINK"] != "outranked" || got["SOL"] != "score_fell" || got["ETH"] != "no_data" || len(got) != 3 {
		t.Fatalf("history %+v", rows)
	}
	// A target the candles reached wins over the recorded reason.
	hit := legacyHistory([]Board{first, second}, func(string) ([]market.Candle, error) {
		return []market.Candle{{T: t0.Unix(), H: 1001, L: 99, C: 100}}, nil
	})
	for _, r := range hit {
		if r.Reason != "target_hit" {
			t.Errorf("hit %s: %s", r.Symbol, r.Reason)
		}
	}
	// A board stored before the reasons were recorded: "left".
	second.dropped = nil
	for _, r := range legacyHistory([]Board{first, second}, func(string) ([]market.Candle, error) {
		return []market.Candle{{T: t0.Unix(), H: 101, L: 99, C: 100}}, nil
	}) {
		if r.Reason != "left" {
			t.Errorf("legacy %s: %s", r.Symbol, r.Reason)
		}
	}
	// A steady board reads its ledger part and no _dropped.
	steady, _ := json.Marshal(struct {
		Bullish []Pick  `json:"bullish"`
		Bearish []Pick  `json:"bearish"`
		Ledger  *ledger `json:"_ledger"`
	}{[]Pick{p("BTC")}, []Pick{}, &ledger{Version: 1, Rule: rule, Universe: []ledgerAsset{}, Exits: []ledgerExit{}}})
	if read, err := scanBoard(&fakeRow{vals: []any{"id-3", steady, at}}); err != nil || read.legacy() || read.dropped != nil || read.ledger.Rule != "steady-v1" {
		t.Fatalf("steady %+v %v", read, err)
	}
}

// fakeRow is a pgx.Row for scanBoard: id, board JSON, created_at.
type fakeRow struct{ vals []any }

func (r *fakeRow) Scan(dest ...any) error {
	*dest[0].(*string) = r.vals[0].(string)
	*dest[1].(*[]byte) = r.vals[1].([]byte)
	*dest[2].(*time.Time) = r.vals[2].(time.Time)
	return nil
}
