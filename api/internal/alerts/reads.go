package alerts

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
)

// List is the owned HTTP view, including an independently owned latest run
// and forecast. Selection precedes LIMIT; absent joined data stays null.
func List(ctx context.Context, d *app.Deps, userID, status string, limit int) ([]Alert, error) {
	if status != "all" && status != "active" {
		return nil, bad("status must be active or all.")
	}
	if limit <= 0 {
		limit = 100
	}
	const ownedForecast = `(forecast_id is null or exists(select 1 from forecasts f where f.id=alerts.forecast_id and f.user_id=$1))`
	ownedColumns := strings.NewReplacer(
		"forecast_id::text", `(select f.id::text from forecasts f where f.id=alerts.forecast_id and f.user_id=$1)`,
		"trade_id::text", `(select t.id::text from trades t where t.id=alerts.trade_id and t.user_id=$1)`,
		"proposal_id::text", `case when exists(select 1 from chats c join chat_messages m on m.chat_id=c.id,
			jsonb_array_elements(m.cards) card where c.user_id=$1 and m.role='assistant' and card->>'type'='price_alert'
			and card->'data'->>'proposalId'=alerts.proposal_id::text) then proposal_id::text end`,
		"linked_market", `case when `+ownedForecast+` then linked_market end`,
	).Replace(columns)
	rows, err := d.DB.Query(ctx, "select "+ownedColumns+`, run_at, run_status, run_body, run_error,run_id,run_chat,run_excerpt,run_verdict,
		case when `+ownedForecast+` then coalesce(market_odds, (select result->'market' from forecasts where id = alerts.forecast_id and user_id=$1)) end from alerts
		left join lateral (select ran_at run_at, status run_status, case when alerts.action='research' then left(body,300) else body end run_body, error run_error,id::text run_id,
 (select c.id::text from chats c where c.id=alert_runs.report_chat_id and c.user_id=$1) run_chat,excerpt run_excerpt,verdict run_verdict from alert_runs
			where alert_id = alerts.id and user_id=$1 order by ran_at desc,id desc limit 1) r on true
		where alerts.user_id = $1 and ($2 = 'all' or status = 'active')
		order by alerts.status = 'active' desc, alerts.created_at desc,alerts.id desc limit $3`, userID, status, min(limit, 100))
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Alert, error) {
		var run RunLog
		var at *time.Time
		var status, body, runID *string
		var market *OddsMarket
		a, err := scan(r, &at, &status, &body, &run.Error, &runID, &run.ReportChatID, &run.Excerpt, &run.Verdict, &market)
		a.Market = market
		if at != nil {
			run.At, run.Status, run.Body = *at, *status, *body
			if runID != nil {
				run.ID = *runID
			}
			a.LastRun = &run
		}
		return a, err
	})
	if list == nil {
		list = []Alert{}
	}
	return list, err
}
