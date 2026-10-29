package strategy

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// wave is n hourly bars swinging around 100, so a short SMA cross trades often.
func wave(n int) []market.Candle {
	var bars []bar
	px := 100.0
	for i := range n {
		next := 100 * (1 + 0.08*math.Sin(float64(i)/6) + 0.02*math.Sin(float64(i)/1.7))
		bars = append(bars, bar{px, max(px, next) * 1.004, min(px, next) * 0.996, next})
		px = next
	}
	return series(bars...)
}

// smaCross is long while the close is above its SMA(n): a window indicator,
// so a run started anywhere after the warm-up sees the same values.
func smaCross(n int) Spec {
	return Spec{Name: "t", Symbol: "BTC", Interval: "1h", Bars: 1000, PositionPct: 100, FeePct: 0.05,
		LongEntry: all(when(price, "crosses_above", ind("sma", n))),
		LongExit:  all(when(price, "crosses_below", ind("sma", n)))}
}

func open(c []market.Candle, i int) time.Time { return time.Unix(c[i].T, 0) }

// The first signal the forward run trades is the close of the bar since
// falls in (or the bar ending at since), filled at the first open at or
// after since: a cross that closed before since is never traded.
func TestForwardNoLookAhead(t *testing.T) {
	s := crossSpec(10, false)
	// The close crosses above 10 on bar 4; the full backtest buys bar 5's open.
	c := series(flat(9), flat(9), flat(9), flat(9), bar{9, 11, 9, 11}, flat(11.5), flat(12), flat(12.5), flat(13))
	if bt := run(t, s, c); len(bt.Trades) != 1 || bt.Trades[0].EntryT != c[5].T {
		t.Fatalf("backtest trades %+v", bt.Trades)
	}
	for name, tc := range map[string]struct {
		since  time.Time
		trades int
		startT int64
	}{
		"since at bar 5's open: bar 4 closed at it, so its cross fills at since": {open(c, 5), 1, c[4].T},
		"since inside bar 4: its close comes after since, so it counts":          {open(c, 4).Add(30 * time.Minute), 1, c[4].T},
		"since at bar 6's open: the cross was known before, never traded":        {open(c, 6), 0, c[5].T},
	} {
		res := forwardRun(s, tc.since, c)
		if res == nil || len(res.Trades) != tc.trades || res.Metrics.StartT != tc.startT || res.Equity[0].V != 100 || res.Benchmark[0].V != 100 {
			t.Fatalf("%s: %+v", name, res)
		}
		for _, tr := range res.Trades {
			if tr.EntryT < tc.since.Unix() {
				t.Errorf("%s: filled at %d, before since %d", name, tr.EntryT, tc.since.Unix())
			}
		}
	}
	// Buy & hold buys the first open at or after since too: bar 6 opens at 12, the last close is 13.
	near(t, "buy & hold", forwardRun(s, open(c, 6), c).Metrics.BuyHoldReturnPct, (13.0/12-1)*100)
	// Too new: 3 bars from the one since falls in must have closed.
	if forwardRun(s, open(c, 7), c) == nil || forwardRun(s, open(c, 8), c) != nil || forwardRun(s, open(c, 8).Add(time.Hour), c) != nil {
		t.Fatal("3 closed bars from the one since falls in are the least a run needs")
	}
}

// A run on fewer bars is the prefix of the run on more: a new bar never
// changes an old one (the cache can key on the last closed bar).
func TestForwardIsCausal(t *testing.T) {
	c := wave(300)
	s := smaCross(10)
	since := open(c, 150)
	full := forwardRun(s, since, c)
	if full == nil || len(full.Trades) < 3 {
		t.Fatalf("full run: %+v", full)
	}
	for _, n := range []int{153, 180, 240, 299} {
		part := forwardRun(s, since, c[:n])
		k := len(part.Equity) - 1 // its last bar marks the open position out, fees included
		if !slices.Equal(part.Equity[:k], full.Equity[:k]) || !slices.Equal(part.Benchmark, full.Benchmark[:k+1]) {
			t.Errorf("%d bars: the curve differs from the longer run's", n)
		}
		closed := slices.DeleteFunc(slices.Clone(part.Trades), func(tr Trade) bool { return tr.Reason == "end" })
		if !slices.Equal(closed, full.Trades[:len(closed)]) {
			t.Errorf("%d bars: trades %+v, longer run %+v", n, closed, full.Trades[:len(closed)])
		}
	}
}

// Started where the full backtest is flat, the forward run books exactly the
// backtest's later trades, and its return is the backtest's over the same
// bars (sizes scale with equity, so only the base differs).
func TestForwardMatchesBacktestOverlap(t *testing.T) {
	c := wave(380) // under maxPoints: curves are not downsampled
	s := smaCross(10)
	bt := run(t, s, c)
	x := -1 // the bar a mid-run signal exit filled on: flat from its open, as the forward run starts
	for j, tr := range bt.Trades[len(bt.Trades)/2 : len(bt.Trades)-1] {
		if tr.Reason == "signal" && bt.Trades[len(bt.Trades)/2+j+1].EntryT > tr.ExitT {
			x = slices.IndexFunc(c, func(k market.Candle) bool { return k.T == tr.ExitT })
			break
		}
	}
	if x < 0 {
		t.Fatalf("no mid-run signal exit in %+v", bt.Trades)
	}
	fwd := forwardRun(s, open(c, x+1), c) // since = the open after the exit bar: the run starts on the exit bar
	later := slices.DeleteFunc(slices.Clone(bt.Trades), func(tr Trade) bool { return tr.EntryT <= c[x].T })
	if fwd == nil || len(later) == 0 || !slices.Equal(fwd.Trades, later) {
		t.Fatalf("forward trades %+v, backtest's after bar %d %+v", fwd, x, later)
	}
	at := slices.IndexFunc(bt.Equity, func(p Point) bool { return p.T == c[x].T })
	near(t, "forward return", fwd.Metrics.TotalReturnPct, (bt.Equity[len(bt.Equity)-1].V/bt.Equity[at].V-1)*100)
}

func TestForwardEarlyAndWarmup(t *testing.T) {
	for _, tc := range []struct {
		m    *Metrics
		want bool
	}{
		{nil, true},
		{&Metrics{EndT: 29 * 86400, Trades: 12}, true},
		{&Metrics{EndT: 30 * 86400, Trades: 2}, true},
		{&Metrics{EndT: 30 * 86400, Trades: 3}, false},
	} {
		if early(tc.m) != tc.want {
			t.Errorf("early(%+v) = %v", tc.m, !tc.want)
		}
	}
	byName := func(name string) Spec {
		return labs[slices.IndexFunc(labs, func(l labsStrategy) bool { return l.spec.Name == name })].spec
	}
	for name, want := range map[string]int{
		"ETH Golden Cross":    804, // SMA(200): 201 back, ×4
		"LINK MACD Momentum":  140, // slow 26 + signal 9
		"ETH RSI Dip Buyer":   100, // RSI(14): the floor
		"BTC EMA Trend Rider": 204,
	} {
		if got := warmBars(byName(name)); got != want {
			t.Errorf("%s: %d warm bars, want %d", name, got, want)
		}
	}
	huge := Spec{LongEntry: all(when(Indicator{Kind: "macd", Fast: 12, Slow: 400, Signal: 400}, ">", k(0)))}
	if warmBars(huge) != 2000 {
		t.Errorf("warm-up is capped: %d", warmBars(huge))
	}
}

// fwdHandler is a handler over the fake market only: forward needs no database.
func fwdHandler(t *testing.T) (*handler, *markettest.Binance) {
	bin := markettest.New(t)
	return &handler{d: &app.Deps{Market: testMarketClient(bin.URL)}}, bin
}

func emaSpec(t *testing.T, interval string) Spec {
	var g generated
	if err := json.Unmarshal([]byte(designExamples[0].reply), &g); err != nil {
		t.Fatal(err)
	}
	g.Spec.Interval = interval
	return g.Spec // BTC EMA 20/50 cross, 4% stop
}

// One run per strategy and since: reused while fresh, kept when a refresh
// fails, a failure backs off, and a list's own deadline is not a failure.
func TestForwardCache(t *testing.T) {
	h, bin := fwdHandler(t)
	ctx := context.Background()
	s := Strategy{ID: "a", Spec: emaSpec(t, "4h"), CreatedAt: markettest.End.Add(-20 * 24 * time.Hour), Community: &CommunityInfo{}}
	f := h.forward(ctx, s)
	if f == nil || f.Metrics == nil || f.Basis != "saved" || !f.Since.Equal(s.CreatedAt) {
		t.Fatalf("forward: %+v", f)
	}
	bin.Down("", true)
	if !reflect.DeepEqual(h.forward(ctx, s), f) {
		t.Fatal("a fresh run is reused")
	}
	e := h.fwd.entry(forwardKey("a", "saved", s.CreatedAt))
	e.at = e.at.Add(-forwardTTL)           // stale, and the fake's newest bar closed long ago
	h.d.Market = testMarketClient(bin.URL) // without the market's own 30 s candle cache
	stale := h.forward(ctx, s)
	if stale.Metrics != f.Metrics || stale.AsOf != f.AsOf || !stale.Stale || stale.Reason == nil || *stale.Reason != "market_unavailable" || e.failedAt.IsZero() {
		t.Fatal("a failed refresh keeps the last run and records the failure")
	}

	other := s
	other.ID, other.Spec.Symbol = "b", "ETH"
	wantForwardReason(t, h.forward(ctx, other), "market_unavailable")
	bin.Down("", false)
	wantForwardReason(t, h.forward(ctx, other), "market_unavailable")
	b := h.fwd.entry(forwardKey("b", "saved", s.CreatedAt))
	b.failedAt = b.failedAt.Add(-forwardRetry)
	if got := h.forward(ctx, other); got == nil || got.Metrics == nil {
		t.Fatal("retried after the back-off")
	}

	// Published: a new since is a new run; just now, so nothing is fetched yet.
	bin.Down("", true)
	now := time.Now()
	pub := s
	pub.Community = &CommunityInfo{PublishedAt: &now, Published: true}
	if got := h.forward(ctx, pub); got == nil || got.Basis != "published" || got.Metrics != nil || !got.Early || got.Equity == nil {
		t.Fatalf("just published: %+v", got)
	}
	same := s
	same.ID, same.CreatedAt = "d", now
	h.forward(ctx, same)
	same.Community = &CommunityInfo{PublishedAt: &now, Published: true} // at the very instant it was saved
	if got := h.forward(ctx, same); got == nil || got.Basis != "published" || !got.Since.Equal(now) {
		t.Fatalf("published as it was saved: %+v", got)
	}

	// A list past its budget retains cached rows with explicit timeout metadata.
	done, cancel := context.WithCancel(ctx)
	cancel()
	sol := s
	sol.ID, sol.Spec.Symbol = "c", "SOL"
	list := []Strategy{s, sol}
	h.forwards(done, list)
	if list[0].Forward.Metrics != f.Metrics || list[0].Forward.Reason == nil || *list[0].Forward.Reason != "timeout" ||
		list[1].Forward.Metrics != nil || list[1].Forward.Reason == nil || *list[1].Forward.Reason != "timeout" || !h.fwd.entry(forwardKey("c", "saved", s.CreatedAt)).failedAt.IsZero() {
		t.Fatalf("budget spent: %+v", list)
	}
}

// A 15m window older than the shared 3000-bar series pages back; a window
// over forwardMaxBars cannot claim a measured run since publication.
func TestForwardPagesOlderBars(t *testing.T) {
	h, _ := fwdHandler(t)
	ctx := context.Background()
	since := markettest.End.Add(-40 * 24 * time.Hour) // 3840 bars of 15m, plus the warm-up
	s := Strategy{ID: "p", Spec: emaSpec(t, "15m"), CreatedAt: since, Community: &CommunityInfo{}}
	f := h.forward(ctx, s)
	if f == nil || f.Metrics == nil || f.Metrics.StartT != since.Unix()-900 || f.Metrics.EndT != markettest.End.Unix() ||
		f.Metrics.Bars != 40*96+2 || f.Early != (f.Metrics.Trades < forwardMinTrades) {
		t.Fatalf("paged run: %+v", f)
	}

	defer func(n int) { forwardMaxBars = n }(forwardMaxBars)
	forwardMaxBars = 3500
	s.ID = "q"
	clipped := h.forward(ctx, s)
	wantForwardReason(t, clipped, "history_unavailable")
	if !clipped.Since.Equal(since) {
		t.Fatalf("clipped run: %+v", clipped)
	}
}

// GET, lists, publish and the Labs board carry the forward; the API's run is
// the pure run over the warm-up anchored at since; Labs backtests end at the
// launch, where their forward starts.
func TestForwardOnReads(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	end := markettest.End
	id := saved(t, e, e.User, "Trend", 12, 1.2, 400) // BTC 4h EMA 20/50
	created := end.Add(-20 * 24 * time.Hour)
	e.DB.Exec(ctx, "update strategies set created_at = $2 where id = $1", id, created)

	w := e.Do("GET", "/api/strategies/"+id, nil)
	s := apptest.JSON[got](t, w).Strategy
	f := s.Forward
	if w.Code != 200 || f == nil || f.Basis != "saved" || !f.Since.Equal(created) || f.Metrics == nil || !f.Early ||
		f.Metrics.StartT != created.Unix()-4*3600 || f.Equity[0].V != 100 || f.Benchmark[0].V != 100 {
		t.Fatalf("saved: %d %s", w.Code, w.Body)
	}
	for _, tr := range f.Trades {
		if tr.EntryT < created.Unix() {
			t.Errorf("a forward trade before since: %+v", tr)
		}
	}
	bars, _ := e.Market.Candles(ctx, "BTC", "4h", 1000)
	from := created.Unix() - 4*3600 - int64(warmBars(s.Spec))*4*3600
	i := slices.IndexFunc(bars, func(c market.Candle) bool { return c.T == from })
	if want := forwardRun(s.Spec, created, orderedClosed(bars[i:], 4*3600, time.Now().Unix())); i < 0 || !reflect.DeepEqual(*f.Metrics, want.Metrics) {
		t.Fatalf("API run %+v, pure run from the anchored warm-up %+v", f.Metrics, want)
	}

	// Published just now: judged from the publish, nothing to run yet.
	w = e.Do("POST", "/api/strategies/"+id+"/publish", map[string]bool{"published": true})
	pub := apptest.JSON[got](t, w).Strategy.Forward
	if pub == nil || pub.Basis != "published" || pub.Metrics != nil || !pub.Early || pub.AsOf != nil || pub.Stale || pub.Reason == nil || *pub.Reason != "too_new" ||
		len(pub.Equity) != 0 || len(pub.Benchmark) != 0 || len(pub.Trades) != 0 {
		t.Fatalf("published: %s", w.Body)
	}
	published := end.Add(-10 * 24 * time.Hour)
	e.DB.Exec(ctx, "update strategies set published_at = $2 where id = $1", id, published)
	other := e.Session(storetest.User(t, e.DB))
	for _, c := range []string{"mine", "community"} {
		w := e.Do("GET", "/api/strategies?scope="+c, nil)
		l := apptest.JSON[struct{ Strategies []Summary }](t, w).Strategies
		if len(l) != 1 || l[0].Forward == nil || l[0].Forward.Basis != "published" || !l[0].Forward.Since.Equal(published) ||
			l[0].Forward.Metrics == nil || l[0].Forward.Metrics.StartT != published.Unix()-4*3600 || strings.Contains(w.Body.String(), `"equity"`) {
			t.Fatalf("%s: %s", c, w.Body)
		}
	}
	if s := apptest.JSON[got](t, e.DoAs(other, "GET", "/api/strategies/"+id, nil)).Strategy; s.Forward == nil || s.Forward.Basis != "published" {
		t.Fatalf("another user reads the published forward: %+v", s.Forward)
	}

	// Saved just now: no fetch, so even an outage answers.
	fresh := saved(t, e, e.User, "Fresh", 12, 1.2, 400)
	e.Binance.Down("", true)
	if w := e.Do("GET", "/api/strategies/"+fresh, nil); !strings.Contains(w.Body.String(), `"basis":"saved","metrics":null,"early":true`) {
		t.Fatalf("fresh: %s", w.Body)
	}
	e.Binance.Down("", false)
}

func TestForwardLabs(t *testing.T) {
	defer func(t0 time.Time) { labsCreated = t0 }(labsCreated)
	labsCreated = markettest.End.Add(-40 * 24 * time.Hour) // inside the fake's history
	e := apptest.New(t, Register, apptest.Options{})
	l := apptest.JSON[struct{ Strategies []Summary }](t, e.Do("GET", "/api/strategies?scope=top", nil)).Strategies
	if len(l) != len(labs) {
		t.Fatalf("top: %d", len(l))
	}
	for _, s := range l {
		f, sec := s.Forward, market.BarSeconds(s.Interval)
		if s.Metrics.EndT >= labsCreated.Unix() || f == nil || f.Basis != "launch" || !f.Since.Equal(labsCreated) || f.Metrics == nil ||
			f.Metrics.StartT != labsCreated.Unix()-sec || !s.CreatedAt.Equal(labsCreated) {
			t.Errorf("%s: backtest ends %d, forward %+v", s.Name, s.Metrics.EndT, f)
		}
	}
	d := apptest.JSON[got](t, e.Do("GET", "/api/strategies/"+l[0].ID, nil)).Strategy
	if d.Forward == nil || d.Forward.Metrics == nil || len(d.Forward.Equity) == 0 || d.Forward.Equity[0].T != labsCreated.Unix()-market.BarSeconds(d.Spec.Interval) {
		t.Fatalf("labs detail forward: %+v", d.Forward)
	}
}
