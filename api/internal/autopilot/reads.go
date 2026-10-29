package autopilot

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
)

// List retains each pilot's own-book valuation and nullable market fields.
func List(ctx context.Context, d *app.Deps, userID string, limit int) ([]Autopilot, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.DB.Query(ctx, "select "+columns+" from autopilots where user_id=$1 order by started_at desc,id desc limit $2", userID, min(limit, 20))
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pilot, error) { return scan(row) })
	if err != nil {
		return nil, err
	}
	h := handler{d}
	marks := h.marks(ctx)
	out := make([]Autopilot, len(list))
	for i := range list {
		out[i] = list[i].view(marks[list[i].Spec.Symbol])
	}
	return out, nil
}
