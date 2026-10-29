package aipf

import (
	"context"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
)

// List preserves the HTTP50 view, saved benchmark and nullable investment.
func List(ctx context.Context, d *app.Deps, userID string, limit int) ([]Portfolio, error) {
	return list(ctx, d, userID, limit, false)
}

// ListInvested selects investment eligibility from the owned ledger before
// LIMIT, independently of live valuation. Reset removes its eligible fills.
func ListInvested(ctx context.Context, d *app.Deps, userID string, limit int) ([]Portfolio, error) {
	return list(ctx, d, userID, limit, true)
}

func list(ctx context.Context, d *app.Deps, userID string, limit int, investedOnly bool) ([]Portfolio, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.DB.Query(ctx, selectPortfolio+` where user_id=$1 and ($3=false or exists (
		select 1 from trades t where t.user_id=$1 and t.ref_id=ai_portfolios.id and t.source='portfolio' and t.side='buy' and t.notional>0))
		order by created_at desc,id desc limit $2`, userID, min(limit, 50), investedOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Portfolio{}
	for rows.Next() {
		var p Portfolio
		if err := scan(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Release the list cursor before the existing accounting/market reads.
	rows.Close()
	h := handler{d}
	h.invested(ctx, userID, out)
	return out, nil
}
