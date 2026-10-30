// Package paper is the simulated trading ledger. Market orders fill at the
// latest market price (Binance, Hyperliquid or the stock quote feed, in USD)
// ± 2 bps, pay a 0.1% fee, and update signed, average-cost positions in
// Postgres. Nothing ever reaches a real venue.
package paper

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

type OrderRequest struct {
	Symbol     string   `json:"symbol"`
	Side       string   `json:"side"`
	Notional   *float64 `json:"notional"`
	Qty        *float64 `json:"qty"`
	StopLoss   *float64 `json:"stopLoss"`
	TakeProfit *float64 `json:"takeProfit"`
	Source     string   `json:"source"`
	Note       string   `json:"note"`
	ProposalID *string  `json:"proposalId"` // the agent card being confirmed: a second fill is ErrProposalFilled
	RefID      *string  `json:"-"`          // server-only: the AI portfolio of an invest leg, the alert that fired, or the copy plan
}

type Trade struct {
	ID          string    `json:"id"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"`
	Qty         float64   `json:"qty"`
	Price       float64   `json:"price"`
	Notional    float64   `json:"notional"`
	Fee         float64   `json:"fee"`
	RealizedPnl float64   `json:"realizedPnl"` // net of fee; an opening trade shows −fee
	Source      string    `json:"source"`
	Note        string    `json:"note"`
	CreatedAt   time.Time `json:"createdAt"`
	Ref         *TradeRef `json:"ref" db:"-"` // where the trade came from; only GET /api/portfolio/trades fills it

	refID string // trades.ref_id, read by history for per-portfolio P&L
}

// TradeRef links a trade back to the chat, AI portfolio, autopilot, alert or copy plan that placed it.
type TradeRef struct {
	Kind  string `json:"kind"` // chat | portfolio | autopilot | alert | copy
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Position struct {
	Symbol           string   `json:"symbol"`
	Qty              float64  `json:"qty"`
	AvgPrice         float64  `json:"avgPrice"`
	MarkPrice        float64  `json:"markPrice"`
	MarketValue      float64  `json:"marketValue"`
	UnrealizedPnl    float64  `json:"unrealizedPnl"`
	UnrealizedPnlPct float64  `json:"unrealizedPnlPct"`
	StopLoss         *float64 `json:"stopLoss"`
	TakeProfit       *float64 `json:"takeProfit"`
}

type Portfolio struct {
	missingMarks     map[string]bool
	halted           map[string]bool // captured alongside marks for private review facts
	Revision         int64           `json:"revision"` // the account's mutation count; a higher one is a later state
	Cash             float64         `json:"cash"`
	Equity           float64         `json:"equity"`
	StartingBalance  float64         `json:"startingBalance"`
	UnrealizedPnl    float64         `json:"unrealizedPnl"`
	RealizedPnl      float64         `json:"realizedPnl"`
	TotalPnl         float64         `json:"totalPnl"`
	TotalPnlPct      float64         `json:"totalPnlPct"`
	GrossExposure    float64         `json:"grossExposure"`
	BuyingPower      float64         `json:"buyingPower"`
	Positions        []Position      `json:"positions"`
	PredictionStakes float64         `json:"predictionStakes"` // Σ cost of the open prediction-market positions (predictions.go); in equity at cost
}

type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// HaltedError refuses an order that opens, adds to or flips a position in a
// pair Binance has halted (market.Asset.Halted): it would fill at a frozen
// price. Reducing and closing still fill at the last price, so no one is stuck.
// A quote-feed stock whose latest quote is stale refuses every order: its last
// price is days old (market.QuoteStaleAfter), or the feed has not answered
// while its market is open (market.QuoteSilentAfter).
type HaltedError struct{ Symbol string }

func (e *HaltedError) Error() string {
	venue := "Binance"
	if a, ok := market.Lookup(e.Symbol); ok && a.QuoteFeed() {
		return e.Symbol + " has no fresh quote (none from the last 4 days, or none for 20 minutes while its market is open), " +
			"so paper orders in it are paused until a fresh quote arrives."
	} else if ok && a.Perp() {
		venue = "Hyperliquid"
	}
	return e.Symbol + " is halted on " + venue + ", so new orders are paused; reducing or closing a position still fills at its last price."
}

var (
	ErrInsufficient   = errors.New("insufficient buying power")
	ErrNoPosition     = errors.New("no open position")
	ErrProposalFilled = errors.New("proposal already filled")
	ErrLevelsChanged  = errors.New("position levels changed")
	errStale          = errors.New("position no longer matches the trigger")
)

// account and position are the stored rows.
type account struct {
	Cash, StartingBalance, RealizedPnl float64
	Revision                           int64
	Staked                             float64 // Σ cost of the open market_positions
}

type position struct {
	Qty, AvgPrice        float64
	StopLoss, TakeProfit *float64
}

type Engine struct {
	db     *pgxpool.Pool
	market *market.Client
}

func NewEngine(db *pgxpool.Pool, m *market.Client) *Engine {
	return &Engine{db: db, market: m}
}

// order is a validated instruction for execute.
type order struct {
	symbol, side         string
	qty, notional        float64 // qty when > 0, else notional in USD
	closeAll             bool    // trade the whole position; side and qty come from it
	trigger              string  // watcher closes: only if this trigger still holds
	stopLoss, takeProfit *float64
	source, note         string
	proposalID, refID    *string
}

// Order validates and fills a market order, returning the trade and the
// updated portfolio. Callers outside HTTP (aipf, chat) go through the same checks.
func (e *Engine) Order(ctx context.Context, userID string, req OrderRequest) (Trade, Portfolio, error) {
	trades, p, err := e.Orders(ctx, userID, []OrderRequest{req})
	if err != nil {
		return Trade{}, Portfolio{}, err
	}
	return trades[0], p, nil
}

func validateOrder(req OrderRequest) (order, error) {
	info, ok := market.Lookup(req.Symbol)
	if !ok {
		return order{}, invalid("unknown symbol %q", req.Symbol)
	}
	o := order{symbol: info.Symbol, side: req.Side, stopLoss: req.StopLoss, takeProfit: req.TakeProfit,
		source: cmp.Or(req.Source, "manual"), note: strings.TrimSpace(req.Note), refID: req.RefID}
	switch {
	case o.side != "buy" && o.side != "sell":
		return order{}, invalid("side must be buy or sell")
	case (req.Notional == nil) == (req.Qty == nil):
		return order{}, invalid("give exactly one of notional or qty")
	case req.Notional != nil && !(*req.Notional >= 10):
		return order{}, invalid("notional must be at least $10")
	case req.Qty != nil && !(*req.Qty > 0):
		return order{}, invalid("qty must be positive")
	case req.StopLoss != nil && !(*req.StopLoss > 0), req.TakeProfit != nil && !(*req.TakeProfit > 0):
		return order{}, invalid("stopLoss and takeProfit must be positive prices")
	case len(o.note) > 500:
		return order{}, invalid("note is too long")
	}
	switch o.source {
	case "manual", "copilot", "agent", "portfolio", "autopilot", "alert", "copy":
	default:
		return order{}, invalid("source must be manual, copilot, agent, portfolio, autopilot, alert or copy")
	}
	if req.Qty != nil {
		o.qty = *req.Qty
	} else {
		o.notional = *req.Notional
	}
	if req.ProposalID != nil {
		if _, err := uuid.Parse(*req.ProposalID); err != nil || len(*req.ProposalID) != 36 { // canonical 8-4-4-4-12 only
			return order{}, invalid("proposalId must be a UUID.")
		}
		o.proposalID = req.ProposalID
	}
	return o, nil
}

// Orders fills a basket atomically under one account lock and one price
// snapshot. A failed leg rolls back all positions, cash and trade records.
func (e *Engine) Orders(ctx context.Context, userID string, reqs []OrderRequest) ([]Trade, Portfolio, error) {
	orders := make([]order, len(reqs))
	for i, req := range reqs {
		o, err := validateOrder(req)
		if err != nil {
			return nil, Portfolio{}, err
		}
		orders[i] = o
	}
	return e.execute(ctx, userID, orders...)
}

// Close flattens the whole position at market. source is "manual" from the
// API, or "stop_loss"/"take_profit" from the watcher.
func (e *Engine) Close(ctx context.Context, userID, symbol, source string) (Trade, Portfolio, error) {
	info, ok := market.Lookup(symbol)
	if !ok {
		return Trade{}, Portfolio{}, invalid("unknown symbol %q", symbol)
	}
	trades, p, err := e.execute(ctx, userID, order{symbol: info.Symbol, closeAll: true, source: cmp.Or(source, "manual")})
	if err != nil {
		return Trade{}, Portfolio{}, err
	}
	return trades[0], p, nil
}

// execute fills orders atomically under one price snapshot, inside a
// transaction that locks the user's account row, so a user's orders are
// serialized. The portfolio is the state it commits.
func (e *Engine) execute(ctx context.Context, userID string, orders ...order) ([]Trade, Portfolio, error) {
	symbols := make([]string, len(orders))
	for i, o := range orders {
		symbols[i] = o.symbol
	}
	marks, halted, err := e.marks(ctx, symbols...)
	if err != nil {
		return nil, Portfolio{}, err
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return nil, Portfolio{}, err
	}
	defer tx.Rollback(ctx)
	acct, positions, err := load(ctx, tx, userID, true)
	if err != nil {
		return nil, Portfolio{}, err
	}
	trades := make([]Trade, 0, len(orders))
	for _, o := range orders {
		t, err := fillOrder(ctx, tx, userID, o, &acct, positions, marks, halted)
		if err != nil {
			return nil, Portfolio{}, err
		}
		trades = append(trades, t)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, Portfolio{}, err
	}
	return trades, buildPortfolio(acct, positions, marks), nil
}

// fillOrder books one leg in the caller's transaction and updates its in-memory
// snapshot, so subsequent legs use the resulting exposure and buying power.
func fillOrder(ctx context.Context, tx pgx.Tx, userID string, o order, acct *account, positions map[string]position,
	marks map[string]float64, halted map[string]bool) (Trade, error) {
	mark, ok := marks[o.symbol]
	if !ok { // Binance stopped quoting the pair (market.Assets dropped it)
		return Trade{}, fmt.Errorf("%w: no live price for %s", market.ErrUpstream, o.symbol)
	}
	pos := positions[o.symbol]
	if o.closeAll {
		if pos.Qty == 0 {
			return Trade{}, ErrNoPosition
		}
		if o.trigger != "" && triggered(pos.Qty, mark, pos.StopLoss, pos.TakeProfit) != o.trigger {
			return Trade{}, errStale
		}
		o.qty, o.side = math.Abs(pos.Qty), "buy"
		if pos.Qty > 0 {
			o.side = "sell"
		}
	}
	price := FillPrice(mark, o.side == "buy")
	qty := o.qty
	if qty == 0 {
		qty = o.notional / price
	}
	signed := qty
	if o.side == "sell" {
		signed = -qty
	}
	f := Book(pos.Qty, pos.AvgPrice, signed, price)
	// A halted pair fills only what shrinks the position on its side. A flip
	// that leaves under $10 (the minimum order) counts: "sell it all" by
	// notional overshoots by the slippage. A stale quote feed fills nothing.
	if info, _ := market.Lookup(o.symbol); halted[o.symbol] && info.QuoteFeed() {
		return Trade{}, &HaltedError{Symbol: o.symbol}
	}
	if halted[o.symbol] && !(math.Abs(f.Qty) <= math.Abs(pos.Qty) && (f.Qty*pos.Qty >= 0 || math.Abs(f.Qty)*price < 10)) {
		return Trade{}, &HaltedError{Symbol: o.symbol}
	}

	// Protective orders: keep the existing ones unless the side changed, then
	// apply the new ones, checked against the resulting side.
	next := position{Qty: f.Qty, AvgPrice: f.AvgPrice}
	if f.Qty != 0 && (f.Qty > 0) == (pos.Qty > 0) {
		next.StopLoss, next.TakeProfit = pos.StopLoss, pos.TakeProfit
	}
	if o.stopLoss != nil || o.takeProfit != nil {
		long := f.Qty > 0
		switch {
		case f.Qty == 0:
			return Trade{}, invalid("this order leaves no position to attach a stop or target to")
		case o.stopLoss != nil && long && *o.stopLoss >= price:
			return Trade{}, invalid("stopLoss must be below the price for a long")
		case o.stopLoss != nil && !long && *o.stopLoss <= price:
			return Trade{}, invalid("stopLoss must be above the price for a short")
		case o.takeProfit != nil && long && *o.takeProfit <= price:
			return Trade{}, invalid("takeProfit must be above the price for a long")
		case o.takeProfit != nil && !long && *o.takeProfit >= price:
			return Trade{}, invalid("takeProfit must be below the price for a short")
		}
		next.StopLoss = cmp.Or(o.stopLoss, next.StopLoss)
		next.TakeProfit = cmp.Or(o.takeProfit, next.TakeProfit)
	}

	// Risk: trades that grow exposure must leave gross ≤ 2 × equity and
	// equity > 0. Reducing and closing are always allowed, so stops can fire.
	// Written to fail closed: a qty that overflows makes equity NaN.
	if math.Abs(f.Qty) > math.Abs(pos.Qty) {
		after := make(map[string]position, len(positions)+1)
		for sym, p := range positions {
			after[sym] = p
		}
		after[o.symbol] = next
		equity, gross := exposure(acct.Cash+acct.Staked+f.CashDelta, after, marks)
		if !(equity > 0 && gross <= maxGross*equity) {
			return Trade{}, ErrInsufficient
		}
	}

	var err error
	if f.Qty == 0 {
		_, err = tx.Exec(ctx, "delete from positions where user_id = $1 and symbol = $2", userID, o.symbol)
	} else {
		_, err = tx.Exec(ctx, `
			insert into positions (user_id, symbol, qty, avg_price, stop_loss, take_profit) values ($1, $2, $3, $4, $5, $6)
			on conflict (user_id, symbol) do update set qty = excluded.qty, avg_price = excluded.avg_price,
				stop_loss = excluded.stop_loss, take_profit = excluded.take_profit, updated_at = now()`,
			userID, o.symbol, next.Qty, next.AvgPrice, next.StopLoss, next.TakeProfit)
	}
	if err != nil {
		return Trade{}, err
	}
	if _, err := tx.Exec(ctx, `update accounts set cash = cash + $2, realized_pnl = realized_pnl + $3, revision = revision + 1,
		updated_at = now() where user_id = $1`, userID, f.CashDelta, f.Realized); err != nil {
		return Trade{}, err
	}
	t := Trade{Symbol: o.symbol, Side: o.side, Qty: qty, Price: price, Notional: qty * price, Fee: f.Fee,
		RealizedPnl: f.Realized, Source: o.source, Note: o.note}
	// Stamped now, under the account lock: now() is when the transaction began,
	// which can predate a request that took the lock first. seq breaks ties.
	if err := tx.QueryRow(ctx, `
		insert into trades (user_id, symbol, side, qty, price, notional, fee, realized_pnl, source, note, proposal_id, ref_id, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, clock_timestamp()) returning id, created_at`,
		userID, t.Symbol, t.Side, t.Qty, t.Price, t.Notional, t.Fee, t.RealizedPnl, t.Source, t.Note, o.proposalID, o.refID,
	).Scan(&t.ID, &t.CreatedAt); err != nil {
		if pg := (*pgconn.PgError)(nil); errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "trades_proposal" {
			return Trade{}, ErrProposalFilled
		}
		return Trade{}, err
	}
	acct.Cash += f.CashDelta
	acct.RealizedPnl += f.Realized
	acct.Revision++
	if f.Qty == 0 {
		delete(positions, o.symbol)
	} else {
		positions[o.symbol] = next
	}
	return t, nil
}

// load creates the account on first use and reads it with its positions;
// writers take an exclusive lock and readers take a shared lock. Both retain
// the lock through the positions read, so cash and holdings cannot be mixed.
func load(ctx context.Context, q pgx.Tx, userID string, forUpdate bool) (account, map[string]position, error) {
	if _, err := q.Exec(ctx, "insert into accounts (user_id) values ($1) on conflict do nothing", userID); err != nil {
		return account{}, nil, err
	}
	lock := " for share"
	if forUpdate {
		lock = " for update"
	}
	return loadAccount(ctx, q, userID, lock)
}

// loadAccount also supports a read-only repeatable snapshot (lock=""). An
// absent account is its default empty view, never a lazy write on that path.
func loadAccount(ctx context.Context, q pgx.Tx, userID, lock string) (account, map[string]position, error) {
	var a account
	err := q.QueryRow(ctx, "select cash, starting_balance, realized_pnl, revision from accounts where user_id = $1"+lock, userID).Scan(&a.Cash, &a.StartingBalance, &a.RealizedPnl, &a.Revision)
	if errors.Is(err, pgx.ErrNoRows) && lock == "" {
		return account{Cash: StartingBalance, StartingBalance: StartingBalance}, map[string]position{}, nil
	}
	if err != nil {
		return a, nil, err
	}
	// Read stakes in the same transaction as cash: writer locks or the read-only
	// repeatable snapshot keep settlement and reset changes consistent.
	if err := q.QueryRow(ctx, "select coalesce(sum(cost), 0) from market_positions where user_id = $1 and status = 'open'", userID).Scan(&a.Staked); err != nil {
		return a, nil, err
	}
	rows, err := q.Query(ctx, "select symbol, qty, avg_price, stop_loss, take_profit from positions where user_id = $1", userID)
	if err != nil {
		return a, nil, err
	}
	defer rows.Close()
	positions := map[string]position{}
	for rows.Next() {
		var sym string
		var p position
		if err := rows.Scan(&sym, &p.Qty, &p.AvgPrice, &p.StopLoss, &p.TakeProfit); err != nil {
			return a, nil, err
		}
		positions[sym] = p
	}
	return a, positions, rows.Err()
}

// marks are the last prices by symbol, a halted pair's included (it is valued
// at its last trade), and the halted symbols: one Assets snapshot, with the
// quote-feed rows among fill (the symbols about to fill) read fresh
// (market.FillAssets), so a fill never uses a Yahoo read over a minute old
// when a newer one can be had.
func (e *Engine) marks(ctx context.Context, fill ...string) (map[string]float64, map[string]bool, error) {
	assets, err := e.market.Assets(ctx)
	if err != nil {
		return nil, nil, err
	}
	marks, halted := make(map[string]float64, len(assets)), map[string]bool{}
	for _, a := range assets {
		marks[a.Symbol] = a.Price
		if a.Halted {
			halted[a.Symbol] = true
		}
	}
	if len(fill) > 0 {
		fresh, err := e.market.FillAssets(ctx, fill)
		if err != nil && ctx.Err() == nil {
			slog.Warn("paper: fresh quote unavailable; filling at the last one", "err", err)
		}
		for sym, a := range fresh {
			marks[sym] = a.Price
			if a.Halted {
				halted[sym] = true
			} else {
				delete(halted, sym)
			}
		}
	}
	return marks, halted, nil
}

func (e *Engine) Portfolio(ctx context.Context, userID string) (Portfolio, error) {
	p, _, err := e.snapshot(ctx, userID)
	return p, err
}

// Trades returns the newest trades first, each with its ref (see refs);
// limit is clamped to 1..200 (default 50).
func (e *Engine) Trades(ctx context.Context, userID string, limit int) ([]Trade, error) {
	return e.TradesFiltered(ctx, userID, TradeFilter{Limit: limit})
}

// TradesFiltered applies ownership and the optional configured symbol before
// limiting newest booked fills. Existing HTTP defaults and the 200-row cap stay.
func (e *Engine) TradesFiltered(ctx context.Context, userID string, filter TradeFilter) ([]Trade, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	symbol := ""
	if filter.Symbol != "" {
		info, ok := market.Lookup(filter.Symbol)
		if !ok {
			return nil, invalid("unknown symbol %q", filter.Symbol)
		}
		symbol = info.Symbol
	}
	rows, err := e.db.Query(ctx, `
		select id, symbol, side, qty, price, notional, fee, realized_pnl, source, note, created_at,
			coalesce(proposal_id::text, ''), coalesce(ref_id::text, '')
		from trades where user_id = $1 and ($3 = '' or symbol = $3)
		order by created_at desc, seq desc limit $2`, userID, min(limit, 200), symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	trades, proposals := []Trade{}, []string{}
	for rows.Next() {
		var t Trade
		var proposal string
		if err := rows.Scan(&t.ID, &t.Symbol, &t.Side, &t.Qty, &t.Price, &t.Notional, &t.Fee,
			&t.RealizedPnl, &t.Source, &t.Note, &t.CreatedAt, &proposal, &t.refID); err != nil {
			return nil, err
		}
		trades, proposals = append(trades, t), append(proposals, proposal)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return trades, e.refs(ctx, userID, trades, proposals)
}

// Reset restores the starting balance and deletes positions, trades and
// prediction-market positions. It needs no prices: with no positions left,
// equity is the cash.
func (e *Engine) Reset(ctx context.Context, userID string) (Portfolio, error) {
	var a account
	if err := pgx.BeginFunc(ctx, e.db, func(tx pgx.Tx) (err error) {
		a, err = reset(ctx, tx, userID)
		return err
	}); err != nil {
		return Portfolio{}, err
	}
	return buildPortfolio(a, nil, nil), nil
}

// ResetTx is Reset inside the caller's transaction (the admin console commits
// it together with its audit row).
func ResetTx(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := reset(ctx, tx, userID)
	return err
}

// reset does the work under the account lock and returns the reset account,
// its revision bumped like every other account mutation.
func reset(ctx context.Context, tx pgx.Tx, userID string) (account, error) {
	var a account
	if _, _, err := load(ctx, tx, userID, true); err != nil {
		return a, err
	}
	for _, sql := range []string{"delete from positions where user_id = $1", "delete from trades where user_id = $1",
		"delete from market_positions where user_id = $1"} {
		if _, err := tx.Exec(ctx, sql, userID); err != nil {
			return a, err
		}
	}
	err := tx.QueryRow(ctx, `update accounts set cash = starting_balance, realized_pnl = 0, revision = revision + 1,
		updated_at = now() where user_id = $1 returning cash, starting_balance, realized_pnl, revision`, userID,
	).Scan(&a.Cash, &a.StartingBalance, &a.RealizedPnl, &a.Revision)
	return a, err
}

// RunWatcher closes positions whose stop or target the mark has touched,
// every 15 s, until ctx ends. Marks come from the market cache.
// ponytail: checks the last price only, so a wick between ticks is missed.
func (e *Engine) RunWatcher(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	e.watch(ctx, t.C)
}

// watch checks stops on every tick until ctx ends; a failed check is logged
// and the next tick tries again.
func (e *Engine) watch(ctx context.Context, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			if err := e.checkStops(ctx); err != nil && ctx.Err() == nil {
				slog.Error("paper watcher", "err", err)
			}
		}
	}
}

func (e *Engine) checkStops(ctx context.Context) error {
	type armed struct {
		userID, symbol       string
		qty                  float64
		stopLoss, takeProfit *float64
	}
	rows, err := e.db.Query(ctx, `select user_id, symbol, qty, stop_loss, take_profit from positions
		where stop_loss is not null or take_profit is not null`)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (armed, error) {
		var a armed
		return a, r.Scan(&a.userID, &a.symbol, &a.qty, &a.stopLoss, &a.takeProfit)
	})
	if err != nil || len(list) == 0 {
		return err // nothing armed: no market call
	}
	marks, _, err := e.marks(ctx)
	if err != nil {
		return err
	}
	for _, a := range list {
		mark, ok := marks[a.symbol]
		trigger := triggered(a.qty, mark, a.stopLoss, a.takeProfit)
		if !ok || trigger == "" { // no quote: a missing mark is not a price of 0
			continue
		}
		note := "Stop loss triggered"
		if trigger == "take_profit" {
			note = "Take profit triggered"
		}
		_, _, err := e.execute(ctx, a.userID, order{symbol: a.symbol, closeAll: true, trigger: trigger, source: trigger, note: note})
		if err != nil && !errors.Is(err, errStale) && !errors.Is(err, ErrNoPosition) {
			slog.Error("paper watcher close", "user", a.userID, "symbol", a.symbol, "err", err)
		}
	}
	return nil
}
