package forecast

// Paper prediction positions: YES or NO on a completed report's
// matched Polymarket market, bought from paper cash at the live price + 1¢
// and settled when the forecast resolves (track.go → paper.SettleTx).
// 0 credits; nothing is sent to Polymarket.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// MarketPosition is contract MarketPosition.
type MarketPosition struct {
	ID               string     `json:"id"`
	ForecastID       string     `json:"forecastId"`
	ForecastQuestion string     `json:"forecastQuestion"`
	Question         string     `json:"question"` // the market's
	URL              string     `json:"url"`
	Outcome          string     `json:"outcome"` // "Yes" for binary, else the option the market prices
	Side             string     `json:"side"`    // yes | no
	Price            float64    `json:"price"`   // per $1 contract, incl. the 1¢ slippage
	Contracts        float64    `json:"contracts"`
	Cost             float64    `json:"cost"`
	MaxPayout        float64    `json:"maxPayout"` // contracts × $1
	Status           string     `json:"status"`    // open | won | lost
	Payout           *float64   `json:"payout"`    // null while open
	Pnl              *float64   `json:"pnl"`       // payout − cost once settled
	MarkPrice        *float64   `json:"markPrice"` // open only: this side's price in the forecast's stored market when it is still this question
	CreatedAt        time.Time  `json:"createdAt"`
	SettledAt        *time.Time `json:"settledAt"`
}

const (
	slippage = 0.01 // per contract, on top of the market's price
	minStake = 10.0
	maxStake = 1000.0
)

var errResolved = &app.Conflict{Msg: "This forecast has resolved."}

// sidePrice is the market's price of one side: yesPrice, or 1 − yesPrice for no.
func sidePrice(yesPrice float64, side string) float64 {
	if side == "no" {
		return 1 - yesPrice
	}
	return yesPrice
}

// fillPrice is what a $1 contract on side costs: its price + 1¢, at most 99¢.
func fillPrice(yesPrice float64, side string) float64 {
	return round(min(sidePrice(yesPrice, side)+slippage, 0.99), 4)
}

// handleOpenPosition is POST /api/forecasts/{id}/market-position {side, stake}.
func (h *handler) handleOpenPosition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Side  string  `json:"side"`
		Stake float64 `json:"stake"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	switch {
	case req.Side != "yes" && req.Side != "no":
		app.WriteError(w, &app.BadRequest{Msg: "side must be yes or no."})
		return
	case !(req.Stake >= minStake && req.Stake <= maxStake):
		app.WriteError(w, &app.BadRequest{Msg: "stake must be $10 to $1,000."})
		return
	}
	ctx, user := r.Context(), httpx.UserID(r.Context())
	f, err := h.own(ctx, user, r.PathValue("id"))
	if err == nil {
		err = h.positionable(ctx, f)
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	// Live price first: a stale one must not fill.
	if err := h.reprice(ctx, f.Draft, f.Result); err != nil {
		app.WriteError(w, fmt.Errorf("%w: %w", market.ErrUpstream, err))
		return
	}
	if err := h.storeResult(ctx, user, f.ID, *f.Result); err != nil {
		app.WriteError(w, err)
		return
	}
	m := f.Result.Market
	pos := MarketPosition{ForecastID: f.ID, ForecastQuestion: f.Question, Question: m.Question, URL: m.URL, Outcome: cmp.Or(m.Outcome, "Yes"),
		Side: req.Side, Price: fillPrice(m.YesPrice, req.Side), Cost: req.Stake, Status: "open", MarkPrice: ptr(sidePrice(m.YesPrice, req.Side))}
	pos.Contracts = req.Stake / pos.Price
	pos.MaxPayout = pos.Contracts
	p, err := h.d.Paper.Stake(ctx, user, req.Stake, func(ctx context.Context, tx pgx.Tx) error {
		// Under the forecast's row lock: a resolution that commits first refuses the stake, one that waits settles it.
		// ponytail: a resolve racing a stake of the same user can deadlock (Postgres aborts one; a retry works).
		var open bool
		err := tx.QueryRow(ctx, `select true from forecasts where id = $1 and status = 'completed' and outcome is null for share`, f.ID).Scan(&open)
		if errors.Is(err, pgx.ErrNoRows) {
			return errResolved
		}
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `insert into market_positions (user_id, forecast_id, outcome, side, question, url, price, contracts, cost)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning id, created_at`,
			user, f.ID, pos.Outcome, pos.Side, pos.Question, pos.URL, pos.Price, pos.Contracts, pos.Cost).Scan(&pos.ID, &pos.CreatedAt)
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"position": pos, "portfolio": p})
}

// positionable is nil when f takes positions: completed, unresolved, its
// lineage's newest run, with a matched market; else the 409.
func (h *handler) positionable(ctx context.Context, f Forecast) error {
	switch {
	case f.Status != "completed" || f.Result == nil:
		return &app.Conflict{Msg: "Only a completed forecast takes positions."}
	case f.Shape == "ordered":
		// ponytail: per-strike settlement needs the resolved close vs each strike, not the winning bucket; stakes on ladder rows come later.
		return &app.Conflict{Msg: "Paper stakes on a strike ladder are coming later."}
	case f.Resolution != nil:
		return errResolved
	}
	var superseded bool
	if err := h.d.DB.QueryRow(ctx, `select exists (select 1 from forecasts where parent_id = $1 and status = 'completed')`, f.ID).Scan(&superseded); err != nil {
		return err
	}
	switch {
	case superseded:
		return &app.Conflict{Msg: "A newer run of this forecast exists; take positions on it."}
	case f.Result.Market == nil:
		return &app.Conflict{Msg: "This forecast has no matched market."}
	}
	return nil
}

// handlePositions is GET /api/market-positions?forecastId=: the caller's
// positions, newest first (≤ 100); with forecastId, those on that run and the
// runs it updates.
func (h *handler) handlePositions(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), httpx.UserID(r.Context())
	var ids []string // nil: every forecast
	if id := r.URL.Query().Get("forecastId"); id != "" {
		if !validID(id) {
			app.WriteError(w, &app.BadRequest{Msg: "forecastId must be a UUID."})
			return
		}
		var err error
		if ids, err = ancestry(ctx, h.d.DB, id); err != nil {
			app.WriteError(w, err)
			return
		}
		if len(ids) == 0 {
			httpx.JSON(w, http.StatusOK, map[string]any{"positions": []MarketPosition{}})
			return
		}
	}
	rows, err := h.d.DB.Query(ctx, `
		select p.id, p.forecast_id, f.question, p.question, p.url, p.outcome, p.side, p.price, p.contracts, p.cost, p.status, p.payout,
		       p.created_at, p.settled_at, f.result->'market'->>'question', (f.result->'market'->>'yesPrice')::float8
		from market_positions p join forecasts f on f.id = p.forecast_id
		where p.user_id = $1 and ($2::uuid[] is null or p.forecast_id = any($2))
		order by p.created_at desc limit 100`, user, ids)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (MarketPosition, error) {
		var p MarketPosition
		var question *string
		var yes *float64
		if err := r.Scan(&p.ID, &p.ForecastID, &p.ForecastQuestion, &p.Question, &p.URL, &p.Outcome, &p.Side, &p.Price, &p.Contracts, &p.Cost,
			&p.Status, &p.Payout, &p.CreatedAt, &p.SettledAt, &question, &yes); err != nil {
			return p, err
		}
		p.MaxPayout = p.Contracts
		if p.Payout != nil {
			p.Pnl = ptr(*p.Payout - p.Cost)
		}
		if p.Status == "open" && question != nil && *question == p.Question && yes != nil {
			p.MarkPrice = ptr(sidePrice(*yes, p.Side))
		}
		return p, nil
	})
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"positions": list})
}

// ancestry is id and the runs it updates, up the parent_id chain (at most 20 up).
func ancestry(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, id string) ([]string, error) {
	rows, err := q.Query(ctx, `with recursive up as (
			select id, parent_id, 0 as depth from forecasts where id = $1
			union all
			select f.id, f.parent_id, up.depth + 1 from forecasts f join up on f.id = up.parent_id where up.depth < 20)
		select id from up`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
