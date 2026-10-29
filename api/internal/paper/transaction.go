package paper

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Ledger holds the account lock for the caller's transaction. Automation can
// commit its delivery cursor and books together with the fills. The caller
// owns commit/rollback and must lock the user before taking this account lock.
type Ledger struct {
	tx        pgx.Tx
	userID    string
	acct      account
	positions map[string]position
	marks     map[string]float64
	halted    map[string]bool
}

// LockTx takes the same account lock as manual trades, resets and stops. Quotes
// are one snapshot for this transaction; nil allows a stop/keep during outages.
func (e *Engine) LockTx(ctx context.Context, tx pgx.Tx, userID string, quotes []market.Asset) (*Ledger, error) {
	acct, positions, err := load(ctx, tx, userID, true)
	if err != nil {
		return nil, err
	}
	l := &Ledger{tx: tx, userID: userID, acct: acct, positions: positions, marks: map[string]float64{}, halted: map[string]bool{}}
	for _, a := range quotes {
		l.marks[a.Symbol], l.halted[a.Symbol] = a.Price, a.Halted
	}
	return l, nil
}

func (l *Ledger) Portfolio() Portfolio { return buildPortfolio(l.acct, l.positions, l.marks) }

// Order fills with all paper validation under the retained account lock.
func (l *Ledger) Order(ctx context.Context, req OrderRequest) (Trade, error) {
	o, err := validateOrder(req)
	if err != nil {
		return Trade{}, err
	}
	return fillOrder(ctx, l.tx, l.userID, o, &l.acct, l.positions, l.marks, l.halted)
}
