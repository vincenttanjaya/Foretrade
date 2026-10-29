package autopilot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/strategy"
)

// Run checks every running autopilot every 30 s until ctx ends; main starts
// it once at boot. A bar is decided on once, within 30 s of its close.
func Run(ctx context.Context, d *app.Deps) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			Check(ctx, d, now)
		}
	}
}

// Check steps the running autopilots one after another (Run's 30 s tick; the
// integration test calls it directly); a failed step is logged and retried on
// the next tick.
func Check(ctx context.Context, d *app.Deps, now time.Time) {
	rows, _ := d.DB.Query(ctx, "select id from autopilots where status = 'running' order by started_at")
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("autopilot tick", "err", err)
		}
		return
	}
	for _, id := range ids {
		release, ok, err := hold(ctx, d, true)
		if ok { // else another instance is acting; this tick skips the row
			err = load(ctx, d, id, now)
			release()
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil { // no rows: stopped meanwhile
			slog.Warn("autopilot step", "id", id, "err", err)
		}
	}
}

// load steps the running autopilot id, or stops it when an admin disabled
// its account (an account disabled before StopForDisabled existed, or by
// hand): the position stays in the account, the books close at the mark when
// market data answers.
func load(ctx context.Context, d *app.Deps, id string, now time.Time) error {
	p, err := scan(d.DB.QueryRow(ctx, "select "+columns+" from autopilots where id = $1 and status = 'running'", id))
	if err != nil {
		return err
	}
	var disabled bool
	if err := d.DB.QueryRow(ctx, "select disabled_at is not null from users where id = $1", p.UserID).Scan(&disabled); err != nil {
		return err
	}
	if !disabled {
		_, _, err = step(ctx, d, &p, now)
		return err
	}
	var mark *float64
	if m, err := d.Market.Price(ctx, p.Spec.Symbol); err == nil {
		mark = &m
	}
	p.disable(mark, now)
	return p.save(ctx, d.DB)
}

// step advances one running autopilot at now and saves it: sync with the
// paper position, decide on a newly closed bar (never the same bar twice),
// then mark equity and enforce the drawdown cap. It returns its fills and
// the latest portfolio it saw. The caller holds the autopilot lock (hold).
func step(ctx context.Context, d *app.Deps, p *pilot, now time.Time) (fills []paper.Trade, pf *paper.Portfolio, err error) {
	defer func() {
		saveCtx, cancel := app.Cleanup(ctx)
		defer cancel()
		if serr := p.save(saveCtx, d.DB); err == nil {
			err = serr
		}
	}()
	s := p.Spec
	cur, err := d.Paper.Portfolio(ctx, p.UserID)
	if err != nil {
		return nil, nil, err
	}
	pf = &cur
	if err := p.sync(ctx, d, cur, now); err != nil {
		return nil, pf, err
	}
	candles, err := d.Market.Candles(ctx, s.Symbol, s.Interval, min(1000, s.Bars))
	if err != nil {
		return nil, pf, err
	}
	mark, err := d.Market.Price(ctx, s.Symbol)
	if err != nil {
		return nil, pf, err
	}
	iv := market.BarSeconds(s.Interval)
	closed := candles
	for len(closed) > 0 && closed[len(closed)-1].T+iv > now.Unix() {
		closed = closed[:len(closed)-1]
	}
	if n := len(closed); n > 0 && (p.LastBarT == nil || closed[n-1].T > *p.LastBarT) {
		if want := strategy.Decide(s, closed, p.side()); want != p.side() {
			trades, after, err := p.trade(ctx, d, want, mark, now)
			if err != nil {
				return nil, pf, err // the bar is retried next tick
			}
			fills = append(fills, trades...)
			if after != nil {
				pf = after
			}
		}
		t := closed[n-1].T
		p.LastBarT = &t
	}

	if t := s.TrailingStopPct; t > 0 && p.Qty != 0 {
		// ponytail: trails on the 30 s mark (the backtest trails on prior bar highs/lows), and moves aren't logged; log once per bar if users ask
		dir := math.Copysign(1, p.Qty)
		want := mark * (1 - dir*t/100)
		if pos := position(*pf, s.Symbol); pos != nil && (pos.StopLoss == nil || dir*(want-*pos.StopLoss*(1+dir*0.001)) > 0) {
			if err := d.Paper.SetStop(ctx, p.UserID, s.Symbol, want); err != nil {
				slog.Warn("autopilot trailing stop", "id", p.ID, "err", err)
			}
		}
	}

	eq := p.equity(mark)
	p.Peak = max(p.Peak, eq)
	if dd := (p.Peak - eq) / p.Peak * 100; dd >= p.MaxDD {
		text := fmt.Sprintf("Max drawdown %g%% hit: autopilot stopped.", p.MaxDD)
		if p.Qty != 0 {
			trades, after, err := p.trade(ctx, d, "flat", mark, now)
			if err != nil {
				return fills, pf, err
			}
			if p.Qty != 0 {
				return fills, pf, nil // the close was refused and logged; the next tick tries again
			}
			fills = append(fills, trades...)
			pf = after
			text = fmt.Sprintf("Max drawdown %g%% hit: position closed, autopilot stopped.", p.MaxDD)
		}
		p.stop("max_drawdown", now)
		p.log(now, "stop", text)
	}
	return fills, pf, nil
}

// trade moves the autopilot from its side to want in one atomic basket:
// close what it holds, then open the new side with allocation × positionPct
// and the spec's stop and target around mark. A refusal (buying power,
// validation, a halted pair) is logged and returns no error; err is a market
// or database failure.
func (p *pilot) trade(ctx context.Context, d *app.Deps, want string, mark float64, now time.Time) ([]paper.Trade, *paper.Portfolio, error) {
	s := p.Spec
	note := "Autopilot · " + p.Name
	var legs []paper.OrderRequest
	// ponytail: the close leg sells the quantity sync saw a moment ago; a stop the
	// watcher fills in between turns it into a small untracked short (the engine
	// has no reduce-only order). Add one if that ever shows up.
	if p.Qty != 0 {
		side := "sell"
		if p.Qty < 0 {
			side = "buy"
		}
		legs = append(legs, paper.OrderRequest{Symbol: s.Symbol, Side: side, Qty: ptr(math.Abs(p.Qty)), Source: "autopilot", Note: note})
	}
	if want != "flat" {
		notional := p.Allocation * s.PositionPct / 100
		dir := map[string]float64{"long": 1, "short": -1}[want]
		leg := paper.OrderRequest{Symbol: s.Symbol, Side: "buy", Notional: &notional, Source: "autopilot", Note: note}
		if dir < 0 {
			leg.Side = "sell"
		}
		if s.StopLossPct > 0 {
			leg.StopLoss = ptr(mark * (1 - dir*s.StopLossPct/100))
		}
		if trail := mark * (1 - dir*s.TrailingStopPct/100); s.TrailingStopPct > 0 && (leg.StopLoss == nil || dir*(trail-*leg.StopLoss) > 0) {
			leg.StopLoss = &trail // the tighter one, as in the backtest
		}
		if s.TakeProfitPct > 0 {
			leg.TakeProfit = ptr(mark * (1 + dir*s.TakeProfitPct/100))
		}
		if notional < minOrder {
			p.log(now, "error", fmt.Sprintf("Each entry is %s, under the $10 minimum order; entry skipped.", usd(notional)))
		} else {
			legs = append(legs, leg)
		}
	}
	if len(legs) == 0 {
		return nil, nil, nil
	}
	trades, pf, err := d.Paper.Orders(ctx, p.UserID, legs)
	var invalid *paper.ValidationError
	var halted *paper.HaltedError
	switch {
	case errors.Is(err, paper.ErrInsufficient):
		p.log(now, "error", "Not enough buying power; skipped.")
		return nil, nil, nil
	case errors.As(err, &invalid):
		p.log(now, "error", "Order refused: "+invalid.Msg+".")
		return nil, nil, nil
	case errors.As(err, &halted):
		p.log(now, "error", "Order refused: "+halted.Error())
		return nil, nil, nil
	case err != nil:
		return nil, nil, err
	}
	for _, t := range trades {
		was, wasAvg := p.Qty, p.Avg
		signed := t.Qty
		if t.Side == "sell" {
			signed = -t.Qty
		}
		// ponytail: the autopilot's own average cost; account-level P&L differs
		// if you also trade the symbol by hand.
		f := paper.Book(p.Qty, p.Avg, signed, t.Price)
		p.Qty, p.Avg, p.Realized = f.Qty, f.AvgPrice, p.Realized+f.Realized
		if was != 0 && f.Qty == 0 {
			// The round trip: this fill's P&L net of its fee, less the entry fees booked when it opened
			// (on the average cost), so the exits add up to the card's P&L.
			net := f.Realized - math.Abs(was)*wasAvg*paper.FeeRate
			p.log(now, "exit", fmt.Sprintf("Closed %s at %s: %s net of fees.", holding(was, s.Symbol), usd(t.Price), signedUSD(net)))
		} else {
			text := fmt.Sprintf("Opened %s at %s", holding(f.Qty, s.Symbol), usd(t.Price))
			if pos := position(pf, s.Symbol); pos != nil && (pos.StopLoss != nil || pos.TakeProfit != nil) {
				text += levels(pos.StopLoss, pos.TakeProfit)
			}
			p.log(now, "entry", text+".")
		}
	}
	return trades, &pf, nil
}

// sync reconciles the autopilot with the account's position in its symbol:
// gone or flipped means it was closed outside the autopilot (a stop or
// target books its P&L from that fill); smaller with the same sign is clamped.
func (p *pilot) sync(ctx context.Context, d *app.Deps, pf paper.Portfolio, now time.Time) error {
	if p.Qty == 0 {
		return nil
	}
	held := 0.0
	if pos := position(pf, p.Spec.Symbol); pos != nil {
		held = pos.Qty
	}
	switch {
	case held == 0 || (held > 0) != (p.Qty > 0):
		var price, fee, qty float64
		var source string
		err := d.DB.QueryRow(ctx, `
			select price, fee, qty, source from trades
			where user_id = $1 and symbol = $2 and source <> 'autopilot' and created_at > coalesce(
				(select max(created_at) from trades where user_id = $1 and symbol = $2 and source = 'autopilot'), $3)
			order by created_at desc, seq desc limit 1`, p.UserID, p.Spec.Symbol, p.StartedAt).Scan(&price, &fee, &qty, &source)
		how := "account reset" // a reset deletes the trades
		switch {
		case err == nil:
			how = strings.ReplaceAll(source, "_", " ")
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if source == "stop_loss" || source == "take_profit" {
			p.Realized += (price-p.Avg)*p.Qty - fee*min(1, math.Abs(p.Qty)/qty)
		}
		p.Qty, p.Avg = 0, 0
		p.log(now, "sync", fmt.Sprintf("Position closed outside the autopilot (%s).", how))
	case math.Abs(held) < math.Abs(p.Qty):
		p.Qty = held
		p.log(now, "sync", fmt.Sprintf("Position reduced outside the autopilot; now %s.", holding(held, p.Spec.Symbol)))
	}
	return nil
}

func position(pf paper.Portfolio, symbol string) *paper.Position {
	for i := range pf.Positions {
		if pf.Positions[i].Symbol == symbol {
			return &pf.Positions[i]
		}
	}
	return nil
}

func levels(stop, target *float64) string {
	var parts []string
	if stop != nil {
		parts = append(parts, "stop "+usd(*stop))
	}
	if target != nil {
		parts = append(parts, "target "+usd(*target))
	}
	return " · " + strings.Join(parts, " · ")
}

func signedUSD(x float64) string {
	if x >= 0 {
		return "+" + usd(x)
	}
	return usd(x)
}
