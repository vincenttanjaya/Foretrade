package paper

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// serve mounts e's endpoints behind a stand-in session check: the X-Test-User
// header is the signed-in user, its absence a 401.
func serve(e *Engine) func(method, path, user, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	e.Register(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := r.Header.Get("X-Test-User")
			if user == "" {
				httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Please sign in.")
				return
			}
			next.ServeHTTP(w, r.WithContext(httpx.WithUser(r.Context(), user)))
		})
	})
	return func(method, path, user, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if user != "" {
			r.Header.Set("X-Test-User", user)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
}

func code(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error struct{ Code string } }
	json.Unmarshal(w.Body.Bytes(), &body)
	return body.Error.Code
}

func TestHandlers(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	do := serve(NewEngine(pool, testMarketClient(bin.URL)))

	for _, r := range [][2]string{{"GET", "/api/portfolio"}, {"GET", "/api/portfolio/trades"}, {"POST", "/api/orders"},
		{"POST", "/api/portfolio/close"}, {"POST", "/api/portfolio/reset"}} {
		if w := do(r[0], r[1], "", "{}"); w.Code != 401 {
			t.Errorf("%s %s without a session: %d", r[0], r[1], w.Code)
		}
	}

	w := do("GET", "/api/portfolio", user, "")
	var pf struct{ Portfolio Portfolio }
	json.Unmarshal(w.Body.Bytes(), &pf)
	if w.Code != 200 || pf.Portfolio.Cash != StartingBalance || pf.Portfolio.BuyingPower != 2*StartingBalance ||
		!strings.Contains(w.Body.String(), `"positions":[]`) {
		t.Fatalf("fresh portfolio: %d %s", w.Code, w.Body)
	}

	for body, want := range map[string]string{
		`{"symbol":"BTC","side":"buy","notional":`:                          "bad_request",
		`{"symbol":"BTC","side":"hold","notional":100}`:                     "bad_request",
		`{"symbol":"BTC","side":"buy","notional":"100"}`:                    "bad_request",
		`{"symbol":"BTC","side":"buy","notional":300000}`:                   "insufficient_buying_power",
		`{"symbol":"BTC","side":"buy","notional":100,"source":"stop_loss"}`: "bad_request",
	} {
		if w := do("POST", "/api/orders", user, body); w.Code != 400 || code(t, w) != want {
			t.Errorf("order %s: %d %s, want %s", body, w.Code, w.Body, want)
		}
	}

	w = do("POST", "/api/orders", user, `{"symbol":"eth","side":"buy","notional":1000,"stopLoss":3000,"source":"agent","note":" from chat "}`)
	var fill struct {
		Trade     Trade
		Portfolio Portfolio
	}
	json.Unmarshal(w.Body.Bytes(), &fill)
	if w.Code != 200 || fill.Trade.Symbol != "ETH" || fill.Trade.Source != "agent" || fill.Trade.Note != "from chat" ||
		!approx(fill.Trade.Price, 4000*1.0002) || len(fill.Portfolio.Positions) != 1 || *fill.Portfolio.Positions[0].StopLoss != 3000 {
		t.Fatalf("order: %d %s", w.Code, w.Body)
	}

	for path, want := range map[string]int{"/api/portfolio/trades": 1, "/api/portfolio/trades?limit=200": 1} {
		if w := do("GET", path, user, ""); w.Code != 200 || strings.Count(w.Body.String(), `"id":`) != want {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	for _, limit := range []string{"0", "201", "ten"} {
		if w := do("GET", "/api/portfolio/trades?limit="+limit, user, ""); w.Code != 400 {
			t.Errorf("trades limit %s: %d", limit, w.Code)
		}
	}

	if w := do("POST", "/api/portfolio/close", user, `{"symbol":"PEPE"}`); w.Code != 400 {
		t.Errorf("close unknown symbol: %d", w.Code)
	}
	if w := do("POST", "/api/portfolio/close", user, `nope`); w.Code != 400 {
		t.Errorf("close bad JSON: %d", w.Code)
	}
	if w := do("POST", "/api/portfolio/close", user, `{"symbol":"ETH"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"side":"sell"`) {
		t.Fatalf("close: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/portfolio/close", user, `{"symbol":"ETH"}`); w.Code != 404 || code(t, w) != "not_found" {
		t.Fatalf("close again: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/portfolio/reset", user, `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"realizedPnl":0`) {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}

	bin.Down("", true)
	down := serve(NewEngine(pool, testMarketClient(bin.URL)))
	for _, r := range [][3]string{{"GET", "/api/portfolio", ""}, {"POST", "/api/orders", `{"symbol":"BTC","side":"buy","notional":100}`},
		{"POST", "/api/portfolio/close", `{"symbol":"BTC"}`}} {
		if w := down(r[0], r[1], user, r[2]); w.Code != 502 || code(t, w) != "upstream" {
			t.Errorf("%s %s with Binance down: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}

	// Reset needs no prices; a dead database is a generic 500.
	broken := storetest.Pool(t)
	broken.Close()
	bin.Down("", false)
	dead := serve(NewEngine(broken, testMarketClient(bin.URL)))
	for _, r := range [][3]string{{"GET", "/api/portfolio", ""}, {"GET", "/api/portfolio/trades", ""}, {"POST", "/api/portfolio/reset", "{}"}} {
		if w := dead(r[0], r[1], user, r[2]); w.Code != 500 || code(t, w) != "internal" {
			t.Errorf("%s %s with the db down: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}
}

// The watcher closes a touched position on each tick, survives a failed
// check and stops with its context.
func TestWatch(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	e := NewEngine(pool, testMarketClient(bin.URL))
	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "SOL", Side: "sell", Notional: ptr(500), TakeProfit: ptr(150)}); err != nil {
		t.Fatal(err)
	}
	// The short's target now sits above the mark: the next check takes profit.
	if _, err := pool.Exec(ctx, "update positions set take_profit = 1000 where user_id = $1", user); err != nil {
		t.Fatal(err)
	}

	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() { e.watch(ctx, tick); close(done) }()
	tick <- time.Now()
	deadline := time.Now().Add(5 * time.Second)
	for {
		trades, _ := e.Trades(ctx, user, 1)
		if len(trades) == 1 && trades[0].Source == "take_profit" {
			if trades[0].Side != "buy" || trades[0].Note != "Take profit triggered" {
				t.Fatalf("watcher trade: %+v", trades[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the watcher did not close the position")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// An armed position with Binance down: every check fails, the loop goes on.
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(500), StopLoss: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	bin.Down("", true)
	failing := NewEngine(pool, testMarketClient(bin.URL))
	failTick, failDone := make(chan time.Time), make(chan struct{})
	go func() { failing.watch(ctx, failTick); close(failDone) }()
	failTick <- time.Now()
	failTick <- time.Now() // accepted only if the failed check did not end the loop
	if err := failing.checkStops(ctx); err == nil {
		t.Fatal("checkStops with no prices must fail")
	}
	cancel()
	for _, ch := range []chan struct{}{done, failDone} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("watch did not return after cancel")
		}
	}
	go e.RunWatcher(ctx) // already canceled: returns at once
}

// Every snapshot carries the account revision it was committed or read at, and
// every mutation (order, basket, close, watcher fill, reset) bumps it in its
// own transaction. Of two snapshots the higher revision is the later state, so
// a client can drop a response that lands after a newer one (the review saw
// cash revert $97,998 → $98,999 when overlapping trade responses crossed).
func TestSnapshotsCarryTheAccountRevision(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	ctx := context.Background()
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	do := serve(e)
	type snap struct {
		Revision int64
		Cash     float64
	}
	read := func(w *httptest.ResponseRecorder) snap {
		t.Helper()
		var body struct{ Portfolio snap }
		if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != 200 || err != nil || !strings.Contains(w.Body.String(), `"revision":`) {
			t.Fatalf("snapshot without a revision: %d %s", w.Code, w.Body)
		}
		return body.Portfolio
	}
	last := read(do("GET", "/api/portfolio", user, ""))
	// next checks that a mutation's snapshot is newer than the last one and is what a read now sees.
	next := func(w *httptest.ResponseRecorder) {
		t.Helper()
		s := read(w)
		if s.Revision <= last.Revision {
			t.Fatalf("revision %d after %d", s.Revision, last.Revision)
		}
		if now := read(do("GET", "/api/portfolio", user, "")); now != s {
			t.Fatalf("read %+v after mutation %+v", now, s)
		}
		last = s
	}

	// Overlapping orders: whatever order the responses arrive in, sorting by
	// revision recovers the order the fills committed in (each buy spends cash).
	ws := make([]*httptest.ResponseRecorder, 6)
	var wg sync.WaitGroup
	for i := range ws {
		wg.Go(func() { ws[i] = do("POST", "/api/orders", user, `{"symbol":"BTC","side":"buy","notional":1000}`) })
	}
	wg.Wait()
	snaps := make([]snap, len(ws))
	for i, w := range ws {
		snaps[i] = read(w)
	}
	slices.SortFunc(snaps, func(a, b snap) int { return cmp.Compare(a.Revision, b.Revision) })
	for _, s := range snaps {
		if s.Revision <= last.Revision || s.Cash >= last.Cash {
			t.Fatalf("snapshot %+v does not follow %+v", s, last)
		}
		last = s
	}
	if now := read(do("GET", "/api/portfolio", user, "")); now != last {
		t.Fatalf("read %+v after the newest fill %+v", now, last)
	}

	next(do("POST", "/api/portfolio/close", user, `{"symbol":"BTC"}`))
	next(do("POST", "/api/orders", user, `{"symbol":"SOL","side":"sell","notional":500,"takeProfit":150}`))
	if _, err := pool.Exec(ctx, "update positions set take_profit = 1000 where user_id = $1", user); err != nil {
		t.Fatal(err)
	}
	if err := e.checkStops(ctx); err != nil { // the watcher takes profit
		t.Fatal(err)
	}
	next(do("GET", "/api/portfolio", user, ""))
	next(do("POST", "/api/portfolio/reset", user, `{}`))
}

// Reset needs no prices: during a market-data outage it clears the account
// and answers with the committed snapshot, not a 502 after the reset happened.
func TestResetDuringAMarketOutage(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	if _, _, err := NewEngine(pool, testMarketClient(bin.URL)).Order(context.Background(), user,
		OrderRequest{Symbol: "ETH", Side: "buy", Notional: ptr(1000)}); err != nil {
		t.Fatal(err)
	}
	bin.Down("", true)
	do := serve(NewEngine(pool, testMarketClient(bin.URL)))
	w := do("POST", "/api/portfolio/reset", user, `{}`)
	var body struct {
		Portfolio struct {
			Revision                                   int64
			Cash, Equity, StartingBalance, BuyingPower float64
			Positions                                  []Position
		}
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	p := body.Portfolio
	if w.Code != 200 || p.Revision < 2 || p.Cash != StartingBalance || p.Equity != StartingBalance ||
		p.StartingBalance != StartingBalance || p.BuyingPower != 2*StartingBalance || p.Positions == nil || len(p.Positions) != 0 {
		t.Fatalf("reset with Binance down: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/portfolio/trades", user, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"trades":[]`) {
		t.Fatalf("trades after reset: %d %s", w.Code, w.Body)
	}
}

// A proposalId fills once: a repeat, even a concurrent one, is a 409 that
// leaves the book untouched; the trade row keeps the id; a malformed id is a
// 400; orders without one are unchanged.
func TestOrderProposalID(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	do := serve(NewEngine(pool, testMarketClient(markettest.New(t).URL)))
	ctx := context.Background()
	const id = "6f1c2c1e-1111-4000-8000-00000000000a"
	order := `{"symbol":"BTC","side":"buy","notional":1000,"source":"agent","proposalId":"` + id + `"}`

	for _, bad := range []string{"nope", "6f1c2c1e11114000800000000000000a", "{6f1c2c1e-1111-4000-8000-00000000000a}", ""} {
		w := do("POST", "/api/orders", user, `{"symbol":"BTC","side":"buy","notional":1000,"proposalId":"`+bad+`"}`)
		if w.Code != 400 || code(t, w) != "bad_request" || !strings.Contains(w.Body.String(), "proposalId must be a UUID.") {
			t.Errorf("proposalId %q: %d %s", bad, w.Code, w.Body)
		}
	}

	ws := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := range ws {
		wg.Go(func() { ws[i] = do("POST", "/api/orders", user, order) })
	}
	wg.Wait()
	slices.SortFunc(ws, func(a, b *httptest.ResponseRecorder) int { return cmp.Compare(a.Code, b.Code) })
	if ws[0].Code != 200 || ws[1].Code != 409 || code(t, ws[1]) != "conflict" ||
		!strings.Contains(ws[1].Body.String(), "This proposal was already filled.") || strings.Contains(ws[0].Body.String(), "proposal") {
		t.Fatalf("concurrent confirms: %d %s / %d %s", ws[0].Code, ws[0].Body, ws[1].Code, ws[1].Body)
	}
	var fill struct{ Trade Trade }
	json.Unmarshal(ws[0].Body.Bytes(), &fill)
	before := do("GET", "/api/portfolio", user, "").Body.String()
	if w := do("POST", "/api/orders", user, order); w.Code != 409 {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	if after := do("GET", "/api/portfolio", user, "").Body.String(); after != before {
		t.Fatalf("a refused replay changed the book:\n%s\n%s", before, after)
	}

	if w := do("POST", "/api/orders", user, `{"symbol":"ETH","side":"buy","notional":500}`); w.Code != 200 {
		t.Fatalf("order without proposalId: %d %s", w.Code, w.Body)
	}
	var stored []string
	rows, _ := pool.Query(ctx, "select coalesce(proposal_id::text, '-') || ' ' || id from trades where user_id = $1 order by seq", user)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		stored = append(stored, s)
	}
	if len(stored) != 2 || stored[0] != id+" "+fill.Trade.ID || !strings.HasPrefix(stored[1], "- ") {
		t.Fatalf("stored proposal ids: %q", stored)
	}
}

// PATCH /api/portfolio/positions/{symbol} sets (a price) or clears (null)
// a position's stop and target, leaves absent keys alone, checks the levels
// against the live mark for the position's side, bumps the revision, and the
// watcher then fills the new stop.
func TestProtect(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	e := NewEngine(pool, testMarketClient(bin.URL))
	do := serve(e)
	ctx := context.Background()
	patch := func(symbol, body string) *httptest.ResponseRecorder {
		return do("PATCH", "/api/portfolio/positions/"+symbol, user, body)
	}
	read := func(w *httptest.ResponseRecorder) Portfolio {
		t.Helper()
		var body struct{ Portfolio Portfolio }
		if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != 200 || err != nil {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return body.Portfolio
	}
	btc := func(p Portfolio) Position {
		return p.Positions[slices.IndexFunc(p.Positions, func(x Position) bool { return x.Symbol == "BTC" })]
	}

	if w := do("PATCH", "/api/portfolio/positions/BTC", "", `{"stopLoss":1}`); w.Code != 401 {
		t.Fatalf("without a session: %d", w.Code)
	}
	if w := patch("btc", `{"stopLoss":90000}`); w.Code != 404 || code(t, w) != "not_found" || !strings.Contains(w.Body.String(), "No open BTC position.") {
		t.Fatalf("no position: %d %s", w.Code, w.Body)
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(1000), TakeProfit: ptr(120_000)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "SOL", Side: "sell", Notional: ptr(500)}); err != nil {
		t.Fatal(err)
	}
	before := do("GET", "/api/portfolio", user, "").Body.String()
	for _, tc := range []struct{ symbol, body, want string }{
		{"BTC", `{}`, "Send stopLoss or takeProfit."},
		{"BTC", `{"stopLoss":0}`, "stopLoss and takeProfit must be positive prices."},
		{"BTC", `{"takeProfit":-5}`, "stopLoss and takeProfit must be positive prices."},
		{"BTC", `{"stopLoss":"95000"}`, "not valid JSON"},
		{"BTC", `{"stopLoss":100000}`, "For a long, the stop must be below the price ($100,000)."},
		{"BTC", `{"stopLoss":95000,"takeProfit":99000}`, "For a long, the target must be above the price ($100,000)."},
		{"SOL", `{"stopLoss":150}`, "For a short, the stop must be above the price ($200.00)."},
		{"SOL", `{"takeProfit":200}`, "For a short, the target must be below the price ($200.00)."},
		{"PEPE", `{"stopLoss":1}`, `unknown symbol \"PEPE\"`},
	} {
		if w := patch(tc.symbol, tc.body); w.Code != 400 || code(t, w) != "bad_request" || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s %s: %d %s, want %q", tc.symbol, tc.body, w.Code, w.Body, tc.want)
		}
	}
	if after := do("GET", "/api/portfolio", user, "").Body.String(); after != before {
		t.Fatalf("a refused patch changed the book:\n%s\n%s", before, after)
	}

	last := read(do("GET", "/api/portfolio", user, ""))
	for _, tc := range []struct {
		body         string
		stop, target *float64
	}{
		{`{"stopLoss":95000}`, ptr(95_000), ptr(120_000)}, // absent target: unchanged
		{`{"takeProfit":null}`, ptr(95_000), nil},         // null: cleared
		{`{"stopLoss":null,"takeProfit":130000}`, nil, ptr(130_000)},
	} {
		p := read(patch("BTC", tc.body))
		pos := btc(p)
		if p.Revision != last.Revision+1 || !reflect.DeepEqual(pos.StopLoss, tc.stop) || !reflect.DeepEqual(pos.TakeProfit, tc.target) || p.Cash != last.Cash {
			t.Fatalf("%s: revision %d after %d, stop %v, target %v", tc.body, p.Revision, last.Revision, pos.StopLoss, pos.TakeProfit)
		}
		if now := read(do("GET", "/api/portfolio", user, "")); now.Revision != p.Revision || !reflect.DeepEqual(btc(now), pos) {
			t.Fatalf("%s: read %+v after %+v", tc.body, btc(now), pos)
		}
		last = p
	}
	// An agent card sends the levels it was drafted against: it applies once, and
	// re-confirming it after a reload (or after any other move) is refused.
	card := `{"stopLoss":96000,"expect":{"stopLoss":null,"takeProfit":130000}}`
	if pos := btc(read(patch("BTC", card))); *pos.StopLoss != 96_000 || *pos.TakeProfit != 130_000 {
		t.Fatalf("card: %+v", pos)
	}
	read(patch("BTC", `{"stopLoss":97000}`))
	if w := patch("BTC", card); w.Code != 409 || code(t, w) != "conflict" || !strings.Contains(w.Body.String(), "changed since the card was drafted") {
		t.Fatalf("stale card: %d %s", w.Code, w.Body)
	}
	if pos := btc(read(do("GET", "/api/portfolio", user, ""))); *pos.StopLoss != 97_000 {
		t.Fatalf("a stale card moved the stop: %+v", pos)
	}
	if p := read(patch("SOL", `{"stopLoss":210,"takeProfit":180}`)); *p.Positions[1].StopLoss != 210 || *p.Positions[1].TakeProfit != 180 {
		t.Fatalf("short levels: %+v", p.Positions[1])
	}

	// The watcher uses the new stop: moved up to 99,000, then the price falls through it.
	read(patch("BTC", `{"stopLoss":99000}`))
	bin.SetPrice("BTCUSDT", 98_500)
	if err := NewEngine(pool, testMarketClient(bin.URL)).checkStops(ctx); err != nil { // a fresh client: no cached ticker
		t.Fatal(err)
	}
	trades, err := e.Trades(ctx, user, 1)
	if err != nil || len(trades) != 1 || trades[0].Source != "stop_loss" || trades[0].Symbol != "BTC" || trades[0].Side != "sell" {
		t.Fatalf("watcher fill: %+v %v", trades, err)
	}
}
