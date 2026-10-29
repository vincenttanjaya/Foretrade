package paper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// level is one optional JSON price: absent (!set), null (set, nil value: clear it) or a price.
type level struct {
	set bool
	v   *float64
}

func (l *level) UnmarshalJSON(b []byte) error {
	l.set = true
	return json.Unmarshal(b, &l.v)
}

// CheckProtection puts a stop and target (nil = not checked) on the right
// side of the mark for a position of qty: long stop < mark < target, short
// the reverse. The agent's set_protection runs the same check.
func CheckProtection(qty, mark float64, stop, target *float64) error {
	long := qty > 0
	side, below, above := "long", "below", "above"
	if !long {
		side, below, above = "short", "above", "below"
	}
	for _, p := range []*float64{stop, target} {
		if p != nil && !(*p > 0 && !math.IsInf(*p, 0)) {
			return invalid("stopLoss and takeProfit must be positive prices.")
		}
	}
	switch {
	case stop != nil && (long && *stop >= mark || !long && *stop <= mark):
		return invalid("For a %s, the stop must be %s the price (%s).", side, below, Money(mark))
	case target != nil && (long && *target <= mark || !long && *target >= mark):
		return invalid("For a %s, the target must be %s the price (%s).", side, above, Money(mark))
	}
	return nil
}

// levels are a position's stop and target; nil = none.
type levels struct {
	StopLoss   *float64 `json:"stopLoss"`
	TakeProfit *float64 `json:"takeProfit"`
}

func sameLevel(a, b *float64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// protect sets (a price) or clears (nil) the stop and target of an open
// position, each only when set, checked against the live mark under the
// account lock. With expect (an agent card's "current"), it applies only while
// the position still has those levels, so an old card can't undo a later move.
// It bumps the revision; the watcher reads the new levels on its next tick.
func (e *Engine) protect(ctx context.Context, userID, symbol string, stop, target level, expect *levels) (Portfolio, error) {
	info, ok := market.Lookup(symbol)
	switch {
	case !ok:
		return Portfolio{}, invalid("unknown symbol %q", symbol)
	case !stop.set && !target.set:
		return Portfolio{}, invalid("Send stopLoss or takeProfit.")
	}
	marks, _, err := e.marks(ctx)
	if err != nil {
		return Portfolio{}, err
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return Portfolio{}, err
	}
	defer tx.Rollback(ctx)
	acct, positions, err := load(ctx, tx, userID, true)
	if err != nil {
		return Portfolio{}, err
	}
	pos, ok := positions[info.Symbol]
	if !ok {
		return Portfolio{}, ErrNoPosition
	}
	if expect != nil && !(sameLevel(pos.StopLoss, expect.StopLoss) && sameLevel(pos.TakeProfit, expect.TakeProfit)) {
		return Portfolio{}, ErrLevelsChanged
	}
	mark, quoted := marks[info.Symbol]
	if !quoted { // Binance stopped quoting the pair: levels can't be checked against 0
		return Portfolio{}, fmt.Errorf("%w: no live price for %s", market.ErrUpstream, info.Symbol)
	}
	if err := CheckProtection(pos.Qty, mark, stop.v, target.v); err != nil {
		return Portfolio{}, err
	}
	if stop.set {
		pos.StopLoss = stop.v
	}
	if target.set {
		pos.TakeProfit = target.v
	}
	if _, err := tx.Exec(ctx, `update positions set stop_loss = $3, take_profit = $4, updated_at = now()
		where user_id = $1 and symbol = $2`, userID, info.Symbol, pos.StopLoss, pos.TakeProfit); err != nil {
		return Portfolio{}, err
	}
	if _, err := tx.Exec(ctx, "update accounts set revision = revision + 1, updated_at = now() where user_id = $1", userID); err != nil {
		return Portfolio{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Portfolio{}, err
	}
	acct.Revision++
	positions[info.Symbol] = pos
	return buildPortfolio(acct, positions, marks), nil
}

// SetStop moves userID's stop on symbol (an autopilot's trailing stop): the
// same check as a PATCH against the live mark, the target left alone, no expect.
func (e *Engine) SetStop(ctx context.Context, userID, symbol string, stop float64) error {
	_, err := e.protect(ctx, userID, symbol, level{set: true, v: &stop}, level{}, nil)
	return err
}

// handleProtect is PATCH /api/portfolio/positions/{symbol} {stopLoss?, takeProfit?, expect?}.
func (e *Engine) handleProtect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StopLoss   level   `json:"stopLoss"`
		TakeProfit level   `json:"takeProfit"`
		Expect     *levels `json:"expect"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	symbol := r.PathValue("symbol")
	p, err := e.protect(r.Context(), httpx.UserID(r.Context()), symbol, req.StopLoss, req.TakeProfit, req.Expect)
	if errors.Is(err, ErrNoPosition) {
		info, _ := market.Lookup(symbol)
		httpx.Error(w, http.StatusNotFound, "not_found", fmt.Sprintf("No open %s position.", info.Symbol))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"portfolio": p})
}

// Money formats a price for messages: $84,650 · $2,640.50 · $142.31 · $0.2154
// (cents from $1,000 up unless they are .00, so BTC and ETH levels keep theirs).
func Money(x float64) string {
	switch {
	case x >= 1000:
		s, cents, _ := strings.Cut(strconv.FormatFloat(x, 'f', 2, 64), ".")
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + "," + s[i:]
		}
		if cents != "00" {
			s += "." + cents
		}
		return "$" + s
	case x >= 1:
		return "$" + strconv.FormatFloat(x, 'f', 2, 64)
	}
	return "$" + strconv.FormatFloat(x, 'g', 4, 64)
}
