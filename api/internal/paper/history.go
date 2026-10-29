package paper

import (
	"cmp"
	"context"
	"maps"
	"math"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Account history (contract "Paper trading": equity curve and P&L by source),
// rebuilt from the trade log and the prediction-market positions on every request.
// ponytail: a full replay per request; a daily snapshot table if accounts grow to many thousand trades.
// ponytail: a reset deletes trades, so history restarts flat at the reset.

type Point struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

type EquityCurve struct {
	Range           string  `json:"range"`
	Interval        string  `json:"interval"`
	StartingBalance float64 `json:"startingBalance"`
	Points          []Point `json:"points"`
}

type SourcePnl struct {
	Source        string  `json:"source"`
	Trades        int     `json:"trades"`      // opening or adding trades
	RealizedPnl   float64 `json:"realizedPnl"` // closed lots, net of the source's fees
	UnrealizedPnl float64 `json:"unrealizedPnl"`
	Fees          float64 `json:"fees"`
	TotalPnl      float64 `json:"totalPnl"`
}

// ranges maps an equity range to its window and grid interval.
var ranges = map[string]struct {
	window   time.Duration
	interval string
	step     int64 // seconds
}{
	"1d":  {24 * time.Hour, "15m", 900},
	"7d":  {7 * 24 * time.Hour, "1h", 3600},
	"30d": {30 * 24 * time.Hour, "4h", 14400},
}

// RegisterHistory mounts the equity curve, P&L attribution and the portfolio
// health review behind require.
func (e *Engine) RegisterHistory(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("GET /api/portfolio/equity", require(http.HandlerFunc(e.handleEquity)))
	mux.Handle("GET /api/portfolio/attribution", require(http.HandlerFunc(e.handleAttribution)))
	mux.Handle("GET /api/portfolio/review", require(http.HandlerFunc(e.handleReview)))
}

func (e *Engine) handleReview(w http.ResponseWriter, r *http.Request) {
	rev, err := e.Review(r.Context(), httpx.UserID(r.Context()), time.Now())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rev)
}

func (e *Engine) handleEquity(w http.ResponseWriter, r *http.Request) {
	c, err := e.Equity(r.Context(), httpx.UserID(r.Context()), cmp.Or(r.URL.Query().Get("range"), "7d"), time.Now())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (e *Engine) handleAttribution(w http.ResponseWriter, r *http.Request) {
	a, err := e.Attribution(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}

func bySource(t Trade) string { return t.Source }

// stake is one prediction-market position since the reset (positions are
// not trades): open ones sit in equity at cost, a settled one moves equity
// by pnl at settledAt.
type stake struct {
	settledAt *time.Time
	pnl       float64 // payout − cost once settled, else 0
}

// history is the portfolio, every trade since the last reset (oldest first)
// and every prediction-market position, read under one read-only repeatable
// snapshot so they describe the same state, including a concurrently reset or
// first-created account. No account locks or lazy writes are needed on a read.
func (e *Engine) history(ctx context.Context, userID string) (Portfolio, []Trade, []stake, error) {
	marks, halted, err := e.marks(ctx)
	if err != nil {
		return Portfolio{}, nil, nil, err
	}
	tx, err := e.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Portfolio{}, nil, nil, err
	}
	defer tx.Rollback(ctx)
	acct, positions, err := loadAccount(ctx, tx, userID, "")
	if err != nil {
		return Portfolio{}, nil, nil, err
	}
	rows, err := tx.Query(ctx, `select id, symbol, side, qty, price, notional, fee, realized_pnl, source, note, created_at,
		coalesce(ref_id::text, '') from trades where user_id = $1 order by created_at, seq`, userID)
	if err != nil {
		return Portfolio{}, nil, nil, err
	}
	trades, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Trade, error) {
		var t Trade
		return t, r.Scan(&t.ID, &t.Symbol, &t.Side, &t.Qty, &t.Price, &t.Notional, &t.Fee,
			&t.RealizedPnl, &t.Source, &t.Note, &t.CreatedAt, &t.refID)
	})
	if err != nil {
		return Portfolio{}, nil, nil, err
	}
	rows, err = tx.Query(ctx, `select settled_at, coalesce(payout - cost, 0) from market_positions where user_id = $1 order by created_at`, userID)
	if err != nil {
		return Portfolio{}, nil, nil, err
	}
	stakes, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stake, error) {
		var s stake
		return s, r.Scan(&s.settledAt, &s.pnl)
	})
	if err != nil {
		return Portfolio{}, nil, nil, err
	}
	p := buildPortfolio(acct, positions, marks)
	p.halted = halted
	p.missingMarks = map[string]bool{}
	for symbol, q := range positions {
		if q.Qty != 0 && marks[symbol] <= 0 {
			p.missingMarks[symbol] = true
		}
	}
	return p, trades, stakes, tx.Commit(ctx)
}

// equity is the account's equity at every grid boundary of the range up to
// now, then a last point at now with the live equity. Holdings are valued at
// the close of the candle ending at each boundary.
func (e *Engine) equity(ctx context.Context, userID, rng string, now time.Time) (EquityCurve, error) {
	_, ok := ranges[rng]
	if !ok {
		return EquityCurve{}, invalid("range must be 1d, 7d or 30d")
	}
	p, trades, stakes, err := e.history(ctx, userID)
	if err != nil {
		return EquityCurve{}, err
	}
	return e.equitySnapshot(ctx, p, trades, stakes, rng, now)
}

func (e *Engine) equitySnapshot(ctx context.Context, p Portfolio, trades []Trade, stakes []stake, rng string, now time.Time) (EquityCurve, error) {
	g, ok := ranges[rng]
	if !ok {
		return EquityCurve{}, invalid("range must be 1d, 7d or 30d")
	}
	c := EquityCurve{Range: rng, Interval: g.interval, StartingBalance: p.StartingBalance}
	end := now.Unix()
	start := (now.Add(-g.window).Unix() / g.step) * g.step
	if len(trades) == 0 && len(stakes) == 0 {
		c.Points = []Point{{start, p.StartingBalance}, {end, p.Equity}}
		return c, nil
	}
	var grid []int64
	for b := start; b < end; b += g.step {
		grid = append(grid, b)
	}
	// Candles for every symbol held at some point of the window.
	held := map[string]bool{}
	for sym, q := range holdings(trades, time.Unix(start, 0)) {
		held[sym] = q != 0
	}
	for _, t := range trades {
		if t.CreatedAt.Unix() >= start {
			held[t.Symbol] = true
		}
	}
	bars := int(g.window.Seconds())/int(g.step) + 2
	candles := map[string][]Point{} // per symbol: candle close time → close
	for sym, ok := range held {
		if !ok {
			continue
		}
		cs, err := e.market.Candles(ctx, sym, g.interval, min(bars, 1000))
		if err != nil {
			return EquityCurve{}, err
		}
		for _, k := range cs {
			candles[sym] = append(candles[sym], Point{k.T + g.step, k.C})
		}
		if len(cs) > 0 {
			candles[sym] = slices.Insert(candles[sym], 0, Point{cs[0].T, cs[0].O}) // before the first close: its open
		}
	}
	c.Points = append(replay(p.StartingBalance, trades, stakes, grid, func(sym string, t int64) float64 {
		return closeAt(candles[sym], t)
	}), Point{end, p.Equity})
	return c, nil
}

// replay values the account at each grid time (unix s, ascending): cash and
// holdings after every trade booked at or before it, holdings at price(sym, t),
// plus the P&L of every prediction stake settled by then. Cash moves exactly
// as Book's CashDelta; a stake counts at cost until it settles, so buying one
// moves nothing.
func replay(startingBalance float64, trades []Trade, stakes []stake, grid []int64, price func(sym string, t int64) float64) []Point {
	cash, qty := startingBalance, map[string]float64{}
	out := make([]Point, 0, len(grid))
	i := 0
	for _, t := range grid {
		for ; i < len(trades) && trades[i].CreatedAt.Unix() <= t; i++ {
			q := signedQty(trades[i])
			cash += -q*trades[i].Price - trades[i].Fee
			if qty[trades[i].Symbol] += q; math.Abs(qty[trades[i].Symbol]) < dust {
				delete(qty, trades[i].Symbol)
			}
		}
		v := cash
		for sym, q := range qty {
			v += q * price(sym, t)
		}
		for _, s := range stakes { // ponytail: O(grid × stakes); a running sum over sorted settlements if accounts hold thousands
			if s.settledAt != nil && s.settledAt.Unix() <= t {
				v += s.pnl
			}
		}
		out = append(out, Point{t, v})
	}
	return out
}

// holdings is the signed quantity per symbol after the trades booked before at.
func holdings(trades []Trade, at time.Time) map[string]float64 {
	qty := map[string]float64{}
	for _, t := range trades {
		if !t.CreatedAt.Before(at) {
			break
		}
		qty[t.Symbol] += signedQty(t)
		if math.Abs(qty[t.Symbol]) < dust {
			qty[t.Symbol] = 0
		}
	}
	return qty
}

// closeAt is the last close at or before t from (close time, price) points,
// else the first point's price.
func closeAt(points []Point, t int64) float64 {
	i := sort.Search(len(points), func(i int) bool { return points[i].T > t })
	if i == 0 {
		if len(points) == 0 {
			return 0
		}
		return points[0].V
	}
	return points[i-1].V
}

func signedQty(t Trade) float64 {
	if t.Side == "sell" {
		return -t.Qty
	}
	return t.Qty
}

// lot is an open slice of a position, owned by the key (source, ref) of the trade that opened it.
type lot struct {
	qty, price float64 // qty signed
	source     string
}

// attribute splits P&L by key(trade) (bySource for P&L by source, the ref
// for ByRef): positions are first-in-first-out lots tagged with the opening
// trade's key. An opening or adding trade opens a
// lot and charges its fee to its source; a reducing trade closes the oldest
// lots first, booking their P&L and its fee pro rata to each lot's source,
// and a flip opens a lot with the remainder. Stops and targets only ever
// reduce, so their P&L lands on the source that opened the position. Open
// lots are marked at marks. Sorted by |totalPnl|, largest first.
func attribute(trades []Trade, marks map[string]float64, key func(Trade) string) []SourcePnl {
	by := map[string]*SourcePnl{}
	src := func(name string) *SourcePnl {
		if by[name] == nil {
			by[name] = &SourcePnl{Source: name}
		}
		return by[name]
	}
	lots := map[string][]lot{}
	for _, t := range trades {
		q, left := signedQty(t), t.Qty
		book := lots[t.Symbol]
		for len(book) > 0 && left > dust && (book[0].qty > 0) != (q > 0) {
			l := &book[0]
			take := min(math.Abs(l.qty), left)
			s := src(l.source)
			s.RealizedPnl += (t.Price - l.price) * take * sign(l.qty)
			s.Fees += t.Fee * take / t.Qty
			l.qty -= take * sign(l.qty)
			left -= take
			if math.Abs(l.qty) < dust {
				book = book[1:]
			}
		}
		if left > dust { // open, add, or the rest of a flip
			s := src(key(t))
			s.Trades++
			s.Fees += t.Fee * left / t.Qty
			book = append(book, lot{qty: left * sign(q), price: t.Price, source: key(t)})
		}
		lots[t.Symbol] = book
	}
	for sym, book := range lots {
		for _, l := range book {
			src(l.source).UnrealizedPnl += (marks[sym] - l.price) * l.qty
		}
	}
	out := []SourcePnl{}
	for _, s := range by {
		s.RealizedPnl -= s.Fees
		s.TotalPnl = s.RealizedPnl + s.UnrealizedPnl
		out = append(out, *s)
	}
	sortSources(out)
	return out
}

func sortSources(out []SourcePnl) {
	slices.SortFunc(out, func(a, b SourcePnl) int {
		return cmp.Or(cmp.Compare(math.Abs(b.TotalPnl), math.Abs(a.TotalPnl)), cmp.Compare(a.Source, b.Source))
	})
}

// withPredictions appends the "prediction" source when there are stakes: the
// positions opened, and their settled P&L as realized (no fees, no open P&L:
// open stakes sit at cost).
func withPredictions(sources []SourcePnl, stakes []stake) []SourcePnl {
	if len(stakes) == 0 {
		return sources
	}
	s := SourcePnl{Source: "prediction", Trades: len(stakes)}
	for _, st := range stakes {
		s.RealizedPnl += st.pnl
	}
	s.TotalPnl = s.RealizedPnl
	sources = append(sources, s)
	sortSources(sources)
	return sources
}

// refs fills Trade.Ref, one query per kind: an agent fill links to the
// caller's chat whose stored message holds its trade card (proposals[i] is
// trades[i]'s proposal id, "" for none), an invest leg to its AI portfolio
// (none once deleted), an autopilot fill to the caller's autopilot on
// that symbol that was running when it filled (+1 min for a closing fill),
// an alert fill to its alert, labelled by its note ("Alert: SOL below $175.00"),
// and a copy fill to its copy plan, labelled by its note ("Copy · Trend Rider").
func (e *Engine) refs(ctx context.Context, userID string, trades []Trade, proposals []string) error {
	var pids, portfolios []string
	alerts, copies := []string{}, []string{}
	autopilot := false
	for i, t := range trades {
		if proposals[i] != "" {
			pids = append(pids, proposals[i])
		}
		if t.refID != "" && t.Source != "alert" && t.Source != "copy" {
			portfolios = append(portfolios, t.refID)
		}
		if t.refID != "" && t.Source == "alert" {
			alerts = append(alerts, t.refID)
		}
		if t.refID != "" && t.Source == "copy" {
			copies = append(copies, t.refID)
		}
		autopilot = autopilot || t.Source == "autopilot"
	}
	chats, names := map[string]TradeRef{}, map[string]TradeRef{}
	origins := map[string]bool{}
	for _, target := range []struct {
		table string
		ids   []string
	}{{"alerts", alerts}, {"copy_plans", copies}} {
		if len(target.ids) == 0 {
			continue
		}
		// Only fixed owning tables enter the query; IDs are typed ledger UUIDs.
		rows, err := e.db.Query(ctx, "select id::text from "+target.table+" where user_id=$1 and id=any($2::uuid[])", userID, target.ids)
		if err != nil {
			return err
		}
		var id string
		if _, err := pgx.ForEachRow(rows, []any{&id}, func() error { origins[target.table+":"+id] = true; return nil }); err != nil {
			return err
		}
	}
	if len(pids) > 0 {
		// ponytail: scans every card of the caller's chat messages; store the chat id on the trade if chats grow large.
		rows, err := e.db.Query(ctx, `
			select distinct on (pid) card->'data'->>'proposalId' as pid, c.id::text, c.title
			from chats c join chat_messages m on m.chat_id = c.id, jsonb_array_elements(m.cards) card
			where c.user_id = $1 and m.role = 'assistant' and card->>'type' = 'trade_proposal'
			  and card->'data'->>'proposalId' = any($2::text[])
			order by pid, m.created_at`, userID, pids)
		if err != nil {
			return err
		}
		var pid string
		var r TradeRef
		if _, err := pgx.ForEachRow(rows, []any{&pid, &r.ID, &r.Label}, func() error {
			chats[pid] = TradeRef{Kind: "chat", ID: r.ID, Label: r.Label}
			return nil
		}); err != nil {
			return err
		}
	}
	if len(portfolios) > 0 {
		rows, err := e.db.Query(ctx, "select id::text, name from ai_portfolios where user_id = $1 and id = any($2::uuid[])", userID, portfolios)
		if err != nil {
			return err
		}
		var r TradeRef
		if _, err := pgx.ForEachRow(rows, []any{&r.ID, &r.Label}, func() error {
			names[r.ID] = TradeRef{Kind: "portfolio", ID: r.ID, Label: r.Label}
			return nil
		}); err != nil {
			return err
		}
	}
	type pilot struct {
		ref     TradeRef
		symbol  string
		started time.Time
		stopped *time.Time
	}
	var pilots []pilot
	if autopilot {
		// ponytail: matched by symbol and running window, which relies on one running autopilot per user.
		rows, err := e.db.Query(ctx, `select strategy_id, name, spec->>'symbol', started_at, stopped_at
			from autopilots where user_id = $1 order by started_at desc`, userID)
		if err != nil {
			return err
		}
		var p pilot
		if _, err := pgx.ForEachRow(rows, []any{&p.ref.ID, &p.ref.Label, &p.symbol, &p.started, &p.stopped}, func() error {
			p.ref.Kind = "autopilot"
			pilots = append(pilots, p)
			return nil
		}); err != nil {
			return err
		}
	}
	for i := range trades {
		t := &trades[i]
		if r, ok := chats[proposals[i]]; ok {
			t.Ref = &r
		} else if r, ok := names[t.refID]; ok {
			t.Ref = &r
		} else if (t.Source == "alert" || t.Source == "copy") && t.refID != "" {
			table := "alerts"
			if t.Source == "copy" {
				table = "copy_plans"
			}
			if origins[table+":"+t.refID] {
				t.Ref = &TradeRef{Kind: t.Source, ID: t.refID, Label: t.Note}
			}
		} else if t.Source == "autopilot" {
			for _, p := range pilots {
				if p.symbol == t.Symbol && !t.CreatedAt.Before(p.started) && (p.stopped == nil || !t.CreatedAt.After(p.stopped.Add(time.Minute))) {
					t.Ref = &p.ref
					break
				}
			}
		}
	}
	return nil
}

// RefPnl is how the lots opened by one ref's trades (an AI portfolio's
// invest legs) are doing.
type RefPnl struct {
	Amount                     float64 // Σ buy notional of its trades
	Trades                     int
	FirstAt                    time.Time
	RealizedPnl, UnrealizedPnl float64 // as attribute books them, fees included
}

// ByRef is RefPnl per ref id, at live marks, since the last reset. Market
// data failure is market.ErrUpstream.
func (e *Engine) ByRef(ctx context.Context, userID string) (map[string]RefPnl, error) {
	p, trades, _, err := e.history(ctx, userID)
	if err != nil {
		return nil, err
	}
	marks := map[string]float64{}
	for _, pos := range p.Positions {
		marks[pos.Symbol] = pos.MarkPrice
	}
	return refPnl(trades, marks), nil
}

// ByRefTx attributes trades from the caller's read snapshot at supplied marks.
// A repeatable-read transaction keeps copy books, fills and attribution aligned
// without acquiring account locks or creating a lazy account on a read.
func (e *Engine) ByRefTx(ctx context.Context, tx pgx.Tx, userID string, marks map[string]float64) (map[string]RefPnl, error) {
	if marks == nil {
		return nil, market.ErrUpstream
	}
	marks = maps.Clone(marks)
	rows, err := tx.Query(ctx, "select symbol,avg_price from positions where user_id=$1", userID)
	if err != nil {
		return nil, err
	}
	var sym string
	var avg float64
	if _, err := pgx.ForEachRow(rows, []any{&sym, &avg}, func() error {
		if marks[sym] <= 0 {
			marks[sym] = avg
		}
		return nil
	}); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `select id,symbol,side,qty,price,notional,fee,realized_pnl,source,note,created_at,coalesce(ref_id::text,'')
 from trades where user_id=$1 order by created_at,seq`, userID)
	if err != nil {
		return nil, err
	}
	trades, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Trade, error) {
		var t Trade
		return t, r.Scan(&t.ID, &t.Symbol, &t.Side, &t.Qty, &t.Price, &t.Notional, &t.Fee, &t.RealizedPnl, &t.Source, &t.Note, &t.CreatedAt, &t.refID)
	})
	if err != nil {
		return nil, err
	}
	return refPnl(trades, marks), nil
}

func refPnl(trades []Trade, marks map[string]float64) map[string]RefPnl {
	out := map[string]RefPnl{}
	for _, t := range trades {
		if t.refID == "" {
			continue
		}
		r := out[t.refID]
		if r.Trades == 0 {
			r.FirstAt = t.CreatedAt
		}
		r.Trades++
		if t.Side == "buy" {
			r.Amount += t.Notional
		}
		out[t.refID] = r
	}
	for _, s := range attribute(trades, marks, func(t Trade) string { return t.refID }) {
		if r, ok := out[s.Source]; ok {
			r.RealizedPnl, r.UnrealizedPnl = s.RealizedPnl, s.UnrealizedPnl
			out[s.Source] = r
		}
	}
	return out
}
