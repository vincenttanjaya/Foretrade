package forecast

// Fresh odds (round 5): POST /api/forecasts/{id}/market re-prices a completed
// report's matched market (round 6: or its strike ladder) from Gamma, or
// searches again when it has none (or the caller asks). 0 credits.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
)

const (
	refreshBudget = 5 * time.Second
	rematchGap    = 10 * time.Minute // a rematch within this of the last search answers from the stored result
)

func (h *handler) handleRefreshMarket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rematch bool `json:"rematch"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	ctx, user := r.Context(), httpx.UserID(r.Context())
	f, err := h.own(ctx, user, r.PathValue("id"))
	if err == nil && (f.Status != "completed" || f.Result == nil) {
		err = &app.Conflict{Msg: "Only a completed report has market odds."}
	}
	if err == nil {
		err = h.refreshMarket(ctx, user, &f, req.Rematch)
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"forecast": f})
}

// refreshMarket re-prices f's market or ladder and stores it, or, with
// neither (or rematch), searches Gamma again unless the last search was under
// rematchGap ago. A failed re-price keeps the stored odds (logged); it is
// never an error.
func (h *handler) refreshMarket(ctx context.Context, user string, f *Forecast, rematch bool) error {
	res := f.Result
	if f.LinkedMarket != nil {
		if err := h.reprice(ctx, f.Draft, res); err != nil && res.Market != nil {
			status := "unavailable"
			var changed *app.Conflict
			var bad *app.BadRequest
			if !f.LinkedMarket.canonical() {
				status = "legacy"
			} else if errors.As(err, &changed) || errors.As(err, &bad) {
				status = "changed"
			}
			copy := *res.Market
			copy.LinkStatus, copy.Signal = status, "wait"
			res.Market = &copy
		}
		return h.storeResult(ctx, user, f.ID, *res)
	}
	switch {
	case (res.Market != nil || res.Ladder != nil) && !rematch:
		reprice := h.reprice
		if res.Ladder != nil {
			reprice = h.repriceLadder
		}
		if err := reprice(ctx, f.Draft, res); err != nil {
			slog.Warn("forecast market refresh", "forecast", f.ID, "err", err)
			return nil
		}
	case res.MarketCheckedAt != nil && time.Since(*res.MarketCheckedAt) < rematchGap:
		return nil
	default:
		// The query writer and the judge: two low-effort calls at 0 credits (a ladder: none).
		// ponytail: rate-limited only by marketCheckedAt (10 min); charge credits if anyone abuses it
		ms := h.lookupMarket(ctx, f.Draft)
		res.Market, res.Ladder = ms.pick(res.Probabilities, res.Confidence), ms.ladder(f.Outcomes, res.Probabilities, res.Confidence)
		res.MarketCheckedAt = ptr(time.Now().UTC())
	}
	return h.storeResult(ctx, user, f.ID, *res)
}

// own is the caller's own forecast: 404 for an unknown id, another user's,
// a replay row or an archived one.
func (h *handler) own(ctx context.Context, user, id string) (Forecast, error) {
	f, err := h.get(ctx, user, id)
	if err == nil && (f.Replay != nil || f.Status == "archived") {
		err = app.ErrNotFound
	}
	return f, err
}

// storeResult rewrites a completed forecast's result (its market). What GET
// derived for an older row (derive) is not stored: nothing is backfilled.
func (h *handler) storeResult(ctx context.Context, user, id string, res Result) error {
	if res.View != nil && res.View.Derived {
		res.View = nil
	}
	res.Specialists = slices.Clone(res.Specialists)
	for i := range res.Specialists {
		if s := &res.Specialists[i]; s.Stats != nil && s.Stats.derived {
			s.Stats = nil
		}
	}
	_, err := h.d.DB.Exec(ctx, `update forecasts set result = $2 where id = $1 and user_id = $3 and status = 'completed'`, id, res, user)
	return err
}

// reprice reads res.Market again from Gamma's /events by its event slug,
// within refreshBudget: the market whose question is the stored one, still
// active and open, with outcomes exactly Yes/No. Its price (1 − Yes for a
// "below" price rule), volume, end, thin flag and asOf replace the stored
// ones, and the edge is re-priced against the desk's final P of
// market.outcome (binary: Yes). An error (Gamma down, the market closed or
// gone) leaves res unchanged.
func (h *handler) reprice(ctx context.Context, d Draft, res *Result) error {
	if d.LinkedMarket != nil {
		if !linkedPredicate(d) {
			return &app.Conflict{Msg: "Legacy or inconsistent linked predicate. Review a fresh draft."}
		}
		_, next, err := readPinned(ctx, h.d, d.LinkedMarket)
		if err != nil {
			return err
		}
		next.price(pOf(res.Probabilities, "Yes"), res.Confidence)
		res.Market = next
		return nil
	}
	m := res.Market
	if m == nil {
		return errors.New("no matched market")
	}
	cs, err := h.eventMarkets(ctx, m.URL)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(cs, func(c candidate) bool { return c.question == strings.TrimSpace(m.Question) })
	if i < 0 {
		return fmt.Errorf("polymarket market %q is closed or gone", m.Question)
	}
	c := cs[i]
	if d.PriceRule != nil && d.PriceRule.Op == "below" {
		c.yes = 1 - c.yes
	}
	next := reread(*m, c)
	next.price(pOf(res.Probabilities, cmp.Or(m.Outcome, "Yes")), res.Confidence)
	res.Market = &next
	return nil
}

// repriceLadder re-reads res.Ladder from its event (one Gamma call): each row
// by its stored question as reprice does, rows closed or gone dropped, the
// edges re-priced against the desk's buckets. An error, or fewer than 2 rows
// left (the event closed), leaves res unchanged.
func (h *handler) repriceLadder(ctx context.Context, d Draft, res *Result) error {
	cs, err := h.eventMarkets(ctx, res.Ladder[0].URL)
	if err != nil {
		return err
	}
	var next marketSet
	for _, m := range res.Ladder {
		if i := slices.IndexFunc(cs, func(c candidate) bool { return c.question == strings.TrimSpace(m.Question) }); i >= 0 {
			n := reread(*m, cs[i])
			next = append(next, &n)
		}
	}
	ladder := next.ladder(d.Outcomes, res.Probabilities, res.Confidence)
	if ladder == nil {
		return fmt.Errorf("polymarket ladder %q is closed or gone", res.Ladder[0].URL)
	}
	res.Ladder = ladder
	return nil
}

// eventMarkets is the open Yes/No markets of the event a market URL names
// (GET /events?slug=), within refreshBudget.
func (h *handler) eventMarkets(ctx context.Context, marketURL string) ([]candidate, error) {
	_, rest, _ := strings.Cut(marketURL, "/event/")
	slug, err := url.PathUnescape(rest)
	if err != nil || slug == "" {
		return nil, fmt.Errorf("market url %q has no event slug", marketURL)
	}
	ctx, cancel := context.WithTimeout(ctx, refreshBudget)
	defer cancel()
	var events []gammaEvent
	if err := h.gammaGet(ctx, "/events?"+url.Values{"slug": {slug}}.Encode(), &events); err != nil {
		return nil, err
	}
	return candidates(events), nil
}

// reread is m with c's price, volume, end and thin flag, read now.
func reread(m MarketOdds, c candidate) MarketOdds {
	m.YesPrice, m.VolumeUSD, m.EndDate, m.Thin, m.AsOf = round(c.yes, 4), c.volume, c.endDate, thin(c.volume), time.Now().UTC()
	return m
}

// pOf is outcome's final P, 0 when it has none.
func pOf(probs []OutcomeP, outcome string) float64 {
	if i := slices.IndexFunc(probs, func(p OutcomeP) bool { return p.Outcome == outcome }); i >= 0 {
		return probs[i].P
	}
	return 0
}
