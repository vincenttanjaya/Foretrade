package paper

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func at(sec int64) time.Time { return time.Unix(sec, 0) }

func tr(sec int64, symbol, side string, qty, price, fee float64, source string) Trade {
	return Trade{Symbol: symbol, Side: side, Qty: qty, Price: price, Fee: fee, Source: source, CreatedAt: at(sec)}
}

// Buy, add, partial sell, flip to short, and a second symbol opened short,
// valued between the trades at BTC = 100 + t/100 and ETH = 40.
func TestReplay(t *testing.T) {
	trades := []Trade{
		tr(1000, "BTC", "buy", 1, 100, 0.1, "manual"),
		tr(2000, "BTC", "buy", 1, 110, 0.11, "agent"),
		tr(3000, "BTC", "sell", 0.5, 120, 0.06, "manual"),
		tr(4000, "BTC", "sell", 3, 90, 0.27, "agent"),
		tr(5000, "ETH", "sell", 2, 50, 0.1, "copilot"),
	}
	price := func(sym string, t int64) float64 {
		if sym == "ETH" {
			return 40
		}
		return 100 + float64(t)/100
	}
	got := replay(100_000, trades, nil, []int64{500, 1000, 1500, 2500, 3500, 4500, 5500}, price)
	want := []float64{
		100_000,
		99_899.9 + 1*110,            // a trade booked at the boundary counts
		99_899.9 + 1*115,            // 100000 − 100 − 0.1
		99_789.79 + 2*125,           // − 110 − 0.11
		99_849.73 + 1.5*135,         // + 60 − 0.06
		100_119.46 - 1.5*145,        // + 270 − 0.27: short 1.5
		100_219.36 - 1.5*155 - 2*40, // + 100 − 0.1: short 2 ETH
	}
	if len(got) != len(want) {
		t.Fatalf("points: %v", got)
	}
	for i, p := range got {
		if !approx(p.V, want[i]) {
			t.Errorf("point %d (t=%d): %v, want %v", i, p.T, p.V, want[i])
		}
	}
	// The replayed cash is the engine's: Book's cash deltas.
	cash, pos := 100_000.0, map[string][2]float64{}
	for _, t := range trades {
		p := pos[t.Symbol]
		f := Book(p[0], p[1], signedQty(t), t.Price)
		cash += f.CashDelta
		pos[t.Symbol] = [2]float64{f.Qty, f.AvgPrice}
	}
	if last := replay(100_000, trades, nil, []int64{6000}, func(string, int64) float64 { return 0 })[0].V; !approx(last, cash) {
		t.Errorf("replayed cash %v, Book %v", last, cash)
	}
}

// Prediction stakes count at cost, so buying one is no dip; each settled one
// jumps the curve by payout − cost at its settledAt (a boundary included).
func TestReplaySettlements(t *testing.T) {
	won, lost := at(2000), at(3000)
	stakes := []stake{{&won, 16.28}, {&lost, -50}, {nil, 0}}
	got := replay(100_000, nil, stakes, []int64{1000, 1999, 2000, 2999, 3500}, func(string, int64) float64 { return 0 })
	for i, want := range []float64{100_000, 100_000, 100_016.28, 100_016.28, 99_966.28} {
		if !approx(got[i].V, want) {
			t.Errorf("point %d (t=%d): %v, want %v", i, got[i].T, got[i].V, want)
		}
	}
}

func TestCloseAt(t *testing.T) {
	pts := []Point{{100, 9}, {200, 10}, {300, 11}} // the first is the first candle's open
	for at, want := range map[int64]float64{50: 9, 100: 9, 199: 9, 200: 10, 299: 10, 300: 11, 9999: 11} {
		if got := closeAt(pts, at); got != want {
			t.Errorf("closeAt(%d) = %v, want %v", at, got, want)
		}
	}
	if closeAt(nil, 5) != 0 {
		t.Error("no candles")
	}
}

func TestAttribute(t *testing.T) {
	sum := func(s []SourcePnl) (total float64) {
		for _, x := range s {
			total += x.TotalPnl
		}
		return total
	}
	same := func(t *testing.T, got, want SourcePnl) {
		t.Helper()
		if got.Source != want.Source || got.Trades != want.Trades || !approx(got.RealizedPnl, want.RealizedPnl) ||
			!approx(got.UnrealizedPnl, want.UnrealizedPnl) || !approx(got.Fees, want.Fees) || !approx(got.TotalPnl, want.TotalPnl) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	}

	t.Run("two sources, a stop credited to the opener, its fee split", func(t *testing.T) {
		got := attribute([]Trade{
			tr(1, "BTC", "buy", 1, 100, 0.1, "manual"),
			tr(2, "BTC", "buy", 1, 110, 0.11, "agent"),
			tr(3, "BTC", "sell", 1.5, 120, 0.18, "stop_loss"), // closes manual's lot and half of agent's
		}, map[string]float64{"BTC": 130}, bySource)
		if len(got) != 2 {
			t.Fatalf("sources: %+v", got)
		}
		// manual: +20 on its lot, fees 0.1 + 0.18 × 1/1.5; agent: +5 closed, +10 open, fees 0.11 + 0.06.
		same(t, got[0], SourcePnl{Source: "manual", Trades: 1, RealizedPnl: 20 - 0.22, Fees: 0.22, TotalPnl: 19.78})
		same(t, got[1], SourcePnl{Source: "agent", Trades: 1, RealizedPnl: 5 - 0.17, UnrealizedPnl: 10, Fees: 0.17, TotalPnl: 14.83})
		// equity − start: cash 99,969.61 + 0.5 × 130.
		if !approx(sum(got), 34.61) {
			t.Errorf("Σ %v", sum(got))
		}
	})

	t.Run("a flip opens a lot for the flipping source", func(t *testing.T) {
		got := attribute([]Trade{
			tr(1, "ETH", "sell", 2, 50, 0.1, "copilot"),
			tr(2, "ETH", "buy", 3, 40, 0.12, "manual"), // covers 2, then long 1
		}, map[string]float64{"ETH": 45}, bySource)
		same(t, got[0], SourcePnl{Source: "copilot", Trades: 1, RealizedPnl: 20 - 0.18, Fees: 0.18, TotalPnl: 19.82})
		same(t, got[1], SourcePnl{Source: "manual", Trades: 1, RealizedPnl: -0.04, UnrealizedPnl: 5, Fees: 0.04, TotalPnl: 4.96})
	})

	t.Run("Σ total equals the engine's total P&L", func(t *testing.T) {
		trades := []Trade{
			tr(1, "SOL", "buy", 10, 200, 2, "portfolio"),
			tr(2, "SOL", "buy", 5, 210, 1.05, "agent"),
			tr(3, "BTC", "sell", 0.2, 60_000, 12, "copilot"),
			tr(4, "SOL", "sell", 12, 190, 2.28, "manual"),
			tr(5, "BTC", "buy", 0.5, 58_000, 29, "take_profit"), // never happens in the engine; still balances
			tr(6, "SOL", "sell", 8, 205, 1.64, "agent"),
		}
		marks := map[string]float64{"SOL": 199, "BTC": 61_000}
		cash, pos := 100_000.0, map[string][2]float64{}
		for _, t := range trades {
			p := pos[t.Symbol]
			f := Book(p[0], p[1], signedQty(t), t.Price)
			cash += -signedQty(t)*t.Price - t.Fee
			pos[t.Symbol] = [2]float64{f.Qty, f.AvgPrice}
		}
		equity := cash
		for sym, p := range pos {
			equity += p[0] * marks[sym]
		}
		got := attribute(trades, marks, bySource)
		if !approx(sum(got), equity-100_000) {
			t.Fatalf("Σ %v, total P&L %v: %+v", sum(got), equity-100_000, got)
		}
		for i := 1; i < len(got); i++ {
			if math.Abs(got[i].TotalPnl) > math.Abs(got[i-1].TotalPnl) {
				t.Errorf("not sorted by |total|: %+v", got)
			}
		}
	})

	if got := attribute(nil, nil, bySource); got == nil || len(got) != 0 {
		t.Errorf("no trades: %#v", got)
	}
}

// serveHistory mounts the history endpoints behind a stand-in session check
// (X-Test-User is the signed-in user).
func serveHistory(e *Engine) func(path, user string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	e.RegisterHistory(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-User") == "" {
				httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Please sign in.")
				return
			}
			next.ServeHTTP(w, r.WithContext(httpx.WithUser(r.Context(), r.Header.Get("X-Test-User"))))
		})
	})
	return func(path, user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if user != "" {
			r.Header.Set("X-Test-User", user)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
}

func TestHistoryHandlers(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	bin := markettest.New(t)
	e := NewEngine(pool, testMarketClient(bin.URL))
	get := serveHistory(e)
	ctx := context.Background()

	for _, path := range []string{"/api/portfolio/equity", "/api/portfolio/attribution"} {
		if w := get(path, ""); w.Code != 401 {
			t.Errorf("%s without a session: %d", path, w.Code)
		}
	}
	var curve EquityCurve
	decode := func(w *httptest.ResponseRecorder, v any) {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	decode(get("/api/portfolio/equity", user), &curve)
	if curve.Range != "7d" || curve.Interval != "1h" || curve.StartingBalance != StartingBalance || len(curve.Points) != 2 ||
		curve.Points[0].V != StartingBalance || curve.Points[1].V != StartingBalance || curve.Points[1].T-curve.Points[0].T < 7*86400 {
		t.Fatalf("no trades: %+v", curve)
	}
	for rng, iv := range map[string]string{"1d": "15m", "30d": "4h"} {
		decode(get("/api/portfolio/equity?range="+rng, user), &curve)
		if curve.Interval != iv {
			t.Errorf("range %s: interval %s", rng, curve.Interval)
		}
	}
	for _, rng := range []string{"2d", "all", "7D"} {
		if w := get("/api/portfolio/equity?range="+rng, user); w.Code != 400 || code(t, w) != "bad_request" {
			t.Errorf("range %s: %d %s", rng, w.Code, w.Body)
		}
	}

	// A buy booked 3 h ago: the curve is flat before it, then cash + ETH at the
	// candle closes (markettest's bars ended at markettest.End, so at 4000), and
	// it ends at the live equity.
	notional := 1000.0
	trade, _, err := e.Order(ctx, user, OrderRequest{Symbol: "ETH", Side: "buy", Notional: &notional, Source: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	booked := time.Now().Add(-3 * time.Hour)
	if _, err := pool.Exec(ctx, "update trades set created_at = $2 where id = $1", trade.ID, booked); err != nil {
		t.Fatal(err)
	}
	bin.SetPrice("ETHUSDT", 4400)
	e = NewEngine(pool, testMarketClient(bin.URL)) // no cached ticker: the move reaches the marks
	get = serveHistory(e)
	p, err := e.Portfolio(ctx, user)
	if err != nil || p.Positions[0].MarkPrice != 4400 {
		t.Fatalf("portfolio: %+v %v", p, err)
	}
	decode(get("/api/portfolio/equity?range=1d", user), &curve)
	held := StartingBalance - trade.Notional - trade.Fee + trade.Qty*4000
	n := len(curve.Points)
	if n < 96 || !approx(curve.Points[n-1].V, p.Equity) || curve.Points[n-1].V == held {
		t.Fatalf("1d curve: %d points, last %v, portfolio %v", n, curve.Points[n-1], p.Equity)
	}
	for _, pt := range curve.Points[:n-1] {
		want := StartingBalance
		if pt.T >= booked.Unix() {
			want = held
		}
		if !approx(pt.V, want) {
			t.Fatalf("point %d: %v, want %v", pt.T, pt.V, want)
		}
	}

	var attr struct {
		Sources  []SourcePnl
		TotalPnl float64
	}
	decode(get("/api/portfolio/attribution", user), &attr)
	if len(attr.Sources) != 1 || attr.Sources[0].Source != "agent" || attr.Sources[0].Trades != 1 ||
		!approx(attr.Sources[0].TotalPnl, attr.TotalPnl) || !approx(attr.TotalPnl, p.TotalPnl) ||
		!approx(attr.Sources[0].Fees, trade.Fee) || !approx(attr.Sources[0].UnrealizedPnl, (4400-trade.Price)*trade.Qty) {
		t.Fatalf("attribution: %+v, portfolio %+v", attr, p)
	}

	// A Binance outage: no marks, 502 (a fresh client, so no cached ticker).
	bin.Down("", true)
	down := serveHistory(NewEngine(pool, testMarketClient(bin.URL)))
	for _, path := range []string{"/api/portfolio/equity", "/api/portfolio/attribution"} {
		if w := down(path, user); w.Code != 502 || code(t, w) != "upstream" {
			t.Errorf("%s during an outage: %d %s", path, w.Code, w.Body)
		}
	}
}

// Attribution by another key splits the same lots: by ref, the totals still
// sum to the total P&L, and a ref's trades get the same FIFO P&L as a source would.
func TestAttributeByRef(t *testing.T) {
	ref := func(tr Trade, id string) Trade { tr.refID = id; return tr }
	trades := []Trade{
		ref(tr(1, "BTC", "buy", 1, 100, 0.1, "portfolio"), "p1"),
		ref(tr(1, "ETH", "buy", 2, 50, 0.1, "portfolio"), "p1"),
		tr(2, "BTC", "buy", 1, 110, 0.11, "agent"),
		ref(tr(3, "SOL", "buy", 4, 20, 0.08, "portfolio"), "p2"),
		tr(4, "BTC", "sell", 1.5, 120, 0.18, "manual"), // closes p1's BTC lot and half the agent's
	}
	marks := map[string]float64{"BTC": 130, "ETH": 45, "SOL": 25}
	total := func(s []SourcePnl) (sum float64) {
		for _, x := range s {
			sum += x.TotalPnl
		}
		return sum
	}
	bySrc, byRef := attribute(trades, marks, bySource), attribute(trades, marks, func(t Trade) string { return t.refID })
	if !approx(total(bySrc), total(byRef)) {
		t.Fatalf("Σ by source %v, by ref %v", total(bySrc), total(byRef))
	}
	got := map[string]SourcePnl{}
	for _, s := range byRef {
		got[s.Source] = s
	}
	// p1: BTC +20 closed, its fee 0.1 + 0.18 × 1/1.5; ETH −10 open, fee 0.1. p2: SOL +20 open, fee 0.08.
	if p1 := got["p1"]; p1.Trades != 2 || !approx(p1.RealizedPnl, 20-0.32) || !approx(p1.UnrealizedPnl, -10) {
		t.Errorf("p1: %+v", p1)
	}
	if p2 := got["p2"]; !approx(p2.RealizedPnl, -0.08) || !approx(p2.UnrealizedPnl, 20) {
		t.Errorf("p2: %+v", p2)
	}
}

// Trades links each fill to where it came from: an agent fill to the caller's
// chat holding its card, an invest leg to its AI portfolio, an autopilot fill
// to the autopilot running on that symbol, an alert fill to its alert (labelled
// by its note); anything else, a deleted portfolio or another user's chat, has no ref.
func TestTradeRefs(t *testing.T) {
	pool := storetest.Pool(t)
	user, other := storetest.User(t, pool), storetest.User(t, pool)
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	ctx := context.Background()
	const card, stranger = "6f1c2c1e-1111-4000-8000-00000000000b", "6f1c2c1e-1111-4000-8000-00000000000c"
	insert := func(sql string, args ...any) (id string) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql+" returning id::text", args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	cards := func(id string) string {
		return `[{"type":"sources","data":{}},{"type":"trade_proposal","data":{"proposalId":"` + id + `"}}]`
	}
	chat := insert("insert into chats (user_id, title) values ($1, 'Long BTC?')", user)
	insert("insert into chat_messages (chat_id, role, content, cards) values ($1, 'assistant', 'Here is a plan.', $2::jsonb)", chat, cards(card))
	elsewhere := insert("insert into chats (user_id, title) values ($1, 'Not yours')", other)
	insert("insert into chat_messages (chat_id, role, content, cards) values ($1, 'assistant', '', $2::jsonb)", elsewhere, cards(stranger))
	pf := insert(`insert into ai_portfolios (user_id, name, thesis, prompt, holdings, backtest) values ($1, 'Blue Chips', '', '', '[]', '{}')`, user)
	gone := "6f1c2c1e-1111-4000-8000-00000000000d"
	alert := insert(`insert into alerts(user_id,symbol,op,price) values($1,'SOL','below',175)`, user)
	insert(`insert into autopilots (user_id, strategy_id, name, spec, allocation_usd, max_drawdown_pct, status, peak_equity, started_at)
		values ($1, 'lab-sol-ema', 'SOL EMA cross', '{"symbol":"SOL"}', 1000, 10, 'running', 1000, now() - interval '1 hour')`, user)

	for _, o := range []OrderRequest{
		{Symbol: "BTC", Side: "buy", Notional: ptr(500), Source: "agent", ProposalID: ptr2(card)},
		{Symbol: "ETH", Side: "buy", Notional: ptr(500), Source: "portfolio", RefID: ptr2(pf)},
		{Symbol: "SOL", Side: "buy", Notional: ptr(500), Source: "autopilot"},
		{Symbol: "ETH", Side: "buy", Notional: ptr(100), Source: "autopilot"}, // no autopilot on ETH
		{Symbol: "BTC", Side: "buy", Notional: ptr(100), Source: "agent", ProposalID: ptr2(stranger)},
		{Symbol: "ETH", Side: "buy", Notional: ptr(100), Source: "portfolio", RefID: ptr2(gone)},
		{Symbol: "SOL", Side: "buy", Notional: ptr(100)},
		{Symbol: "SOL", Side: "buy", Notional: ptr(100), Source: "alert", Note: "Alert: SOL below $175.00", RefID: ptr2(alert)},
	} {
		if _, _, err := e.Order(ctx, user, o); err != nil {
			t.Fatal(err)
		}
	}
	trades, err := e.Trades(ctx, user, 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tr := range slices.Backward(trades) {
		if tr.Ref == nil {
			got = append(got, "-")
		} else {
			got = append(got, tr.Ref.Kind+" "+tr.Ref.ID+" "+tr.Ref.Label)
		}
	}
	want := []string{"chat " + chat + " Long BTC?", "portfolio " + pf + " Blue Chips", "autopilot lab-sol-ema SOL EMA cross", "-", "-", "-", "-",
		"alert " + alert + " Alert: SOL below $175.00"}
	if !slices.Equal(got, want) {
		t.Fatalf("refs\n got %q\nwant %q", got, want)
	}
	// Other responses carry ref null.
	b, _ := json.Marshal(Trade{})
	if !strings.Contains(string(b), `"ref":null`) {
		t.Fatalf("trade JSON: %s", b)
	}
}

// ByRef reports each AI portfolio's invested amount, legs and FIFO P&L at live marks.
func TestByRef(t *testing.T) {
	pool := storetest.Pool(t)
	user := storetest.User(t, pool)
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	ctx := context.Background()
	const pf = "6f1c2c1e-1111-4000-8000-00000000000e"
	legs, _, err := e.Orders(ctx, user, []OrderRequest{
		{Symbol: "BTC", Side: "buy", Notional: ptr(1000), Source: "portfolio", RefID: ptr2(pf)},
		{Symbol: "ETH", Side: "buy", Notional: ptr(500), Source: "portfolio", RefID: ptr2(pf)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Order(ctx, user, OrderRequest{Symbol: "BTC", Side: "buy", Notional: ptr(300)}); err != nil {
		t.Fatal(err)
	}
	got, err := e.ByRef(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	r := got[pf]
	fees := legs[0].Fee + legs[1].Fee
	open := (100_000-legs[0].Price)*legs[0].Qty + (4000-legs[1].Price)*legs[1].Qty
	if len(got) != 1 || !approx(r.Amount, 1500) || r.Trades != 2 || !r.FirstAt.Equal(legs[0].CreatedAt) ||
		!approx(r.RealizedPnl, -fees) || !approx(r.UnrealizedPnl, open) {
		t.Fatalf("by ref: %+v (fees %v, open %v)", got, fees, open)
	}
}

func ptr2(s string) *string { return &s }
