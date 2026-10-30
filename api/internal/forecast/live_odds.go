package forecast

// Live odds for prediction-market alerts: the alerts loop and POST
// /api/alerts re-price a forecast's matched market without a user request.

import (
	"context"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
)

// LiveOdds re-prices forecastID's matched market from Gamma (reprice: the
// "below"-rule flip and a fresh edge) and stores it in the result, so the
// report and its paper positions show the fresh odds. No user check: internal
// use only. pgx.ErrNoRows when the forecast is not completed or has no
// market; a Gamma error is returned and the stored market is left as is.
func LiveOdds(ctx context.Context, d *app.Deps, forecastID string) (*MarketOdds, error) {
	var draft Draft
	var res *Result
	if err := d.DB.QueryRow(ctx, `select shape, question, outcomes, resolution_criteria, resolution_date::text, price_rule, linked_market, result from forecasts
		where id = $1 and status = 'completed' and jsonb_typeof(result->'market') = 'object'`, forecastID).Scan(
		&draft.Shape, &draft.Question, &draft.Outcomes, &draft.ResolutionCriteria, &draft.ResolutionDate, &draft.PriceRule, &draft.LinkedMarket, &res); err != nil {
		return nil, err
	}
	if err := (&handler{d: d}).reprice(ctx, draft, res); err != nil {
		return nil, err
	}
	_, err := d.DB.Exec(ctx, `update forecasts set result = jsonb_set(result, '{market}', $2::jsonb) where id = $1 and status = 'completed'`,
		forecastID, res.Market)
	return res.Market, err
}
