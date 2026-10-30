package paper

// Prediction-market stakes: paper positions on a forecast's matched
// Polymarket market are bought from cash and paid out when the forecast
// resolves. The forecast package owns the rows (market_positions); the ledger
// owns the cash, the realized P&L and the revision.

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Stake spends cost dollars of cash on a prediction-market position under the
// account lock: cost must fit in both cash and buying power, else
// ErrInsufficient. insert writes the position row in the same transaction, so
// cash and the stake commit together; an insert error rolls both back.
func (e *Engine) Stake(ctx context.Context, userID string, cost float64, insert func(context.Context, pgx.Tx) error) (Portfolio, error) {
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
	if !(cost <= acct.Cash && cost <= buildPortfolio(acct, positions, marks).BuyingPower) {
		return Portfolio{}, ErrInsufficient
	}
	if _, err := tx.Exec(ctx, `update accounts set cash = cash - $2, revision = revision + 1, updated_at = now() where user_id = $1`,
		userID, cost); err != nil {
		return Portfolio{}, err
	}
	if err := insert(ctx, tx); err != nil {
		return Portfolio{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Portfolio{}, err
	}
	acct.Cash -= cost
	acct.Staked += cost
	acct.Revision++
	return buildPortfolio(acct, positions, marks), nil
}

// SettleTx settles the open positions on forecastIDs at outcome inside the
// caller's transaction (the one recording the resolution). A position wins iff
// (side yes and outcome is its option) or (side no and outcome is another);
// a win pays $1 a contract. Each owner's cash, realized P&L (payout − cost)
// and revision move under its account lock.
// ponytail: settles on the desk's resolution, not Polymarket's; read Gamma closed+outcomePrices if they ever disagree
func SettleTx(ctx context.Context, tx pgx.Tx, forecastIDs []string, outcome string) error {
	type open struct {
		id, user, outcome, side string
		contracts, cost         float64
	}
	rows, err := tx.Query(ctx, `select id, user_id, outcome, side, contracts, cost from market_positions
		where forecast_id = any($1::uuid[]) and status = 'open' order by user_id, created_at for update`, forecastIDs)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (open, error) {
		var p open
		return p, r.Scan(&p.id, &p.user, &p.outcome, &p.side, &p.contracts, &p.cost)
	})
	if err != nil {
		return err
	}
	for _, p := range list {
		status, payout := "lost", 0.0
		if (p.side == "yes") == (outcome == p.outcome) {
			status, payout = "won", p.contracts
		}
		if _, err := tx.Exec(ctx, `update accounts set cash = cash + $2, realized_pnl = realized_pnl + $3, revision = revision + 1,
			updated_at = now() where user_id = $1`, p.user, payout, payout-p.cost); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update market_positions set status = $2, payout = $3, settled_at = now() where id = $1`,
			p.id, status, payout); err != nil {
			return err
		}
	}
	return nil
}
