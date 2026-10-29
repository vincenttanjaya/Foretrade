package copilot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// DeskRead is the user's latest live forecast on a symbol, as the trade page
// and the plan show it (contract DeskRead).
type DeskRead struct {
	ForecastID     string     `json:"forecastId"`
	Question       string     `json:"question"`
	ResolutionDate string     `json:"resolutionDate"`
	Outcome        string     `json:"outcome"` // the headline: Yes for a binary question, else the likeliest
	P              float64    `json:"p"`
	Lean           string     `json:"lean"`      // "No 72%" when a binary Yes is under 50%, else "<outcome> <p>%"
	PriceRule      *priceRule `json:"priceRule"` // binary Binance price questions only
	Stance         *string    `json:"stance"`    // supports | opposes | split for the side asked about; null without a price rule on this symbol
}

// priceRule is the forecast's auto-resolution rule (forecast.PriceRule's JSON).
type priceRule struct {
	Symbol    string  `json:"symbol"`
	Op        string  `json:"op"` // above | below
	Threshold float64 `json:"threshold"`
	Mode      string  `json:"mode"`
}

type outcomeP struct {
	Outcome string  `json:"outcome"`
	P       float64 `json:"p"`
}

func (h handler) desk(w http.ResponseWriter, r *http.Request) {
	info, ok := market.Lookup(r.URL.Query().Get("symbol"))
	if !ok {
		app.WriteError(w, &app.BadRequest{Msg: "Unknown symbol."})
		return
	}
	d, err := readDesk(r.Context(), h.d, httpx.UserID(r.Context()), info, "long")
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"desk": d})
}

// readDesk is the caller's newest completed, unresolved, unexpired forecast
// on the asset: one whose price rule is on the symbol first, else one whose
// question or criteria name it (ticker or name, as a word). nil when none.
// The stance is for direction (long | short; anything else → nil).
func readDesk(ctx context.Context, d *app.Deps, user string, info market.AssetInfo, direction string) (*DeskRead, error) {
	name, _, _ := strings.Cut(info.Name, " (")                                              // "PAX Gold (gold proxy)" → "PAX Gold"
	pattern := `\m(` + regexp.QuoteMeta(info.Symbol) + "|" + regexp.QuoteMeta(name) + `)\M` // Postgres ARE word boundaries
	var r DeskRead
	var shape string
	var date time.Time
	var probs []outcomeP
	err := d.DB.QueryRow(ctx, `
		select id, question, shape, resolution_date, coalesce(result->'probabilities', '[]'), price_rule
		from forecasts
		where user_id = $1 and status = 'completed' and outcome is null and resolution_date >= $4::date
			and (price_rule->>'symbol' = $2 or question ~* $3 or resolution_criteria ~* $3)
		order by coalesce(price_rule->>'symbol' = $2, false) desc, created_at desc
		limit 1`, user, info.Symbol, pattern, time.Now().UTC().Format(time.DateOnly)).Scan(
		&r.ForecastID, &r.Question, &shape, &date, &probs, &r.PriceRule)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for i, p := range probs { // the headline: Yes for a binary question, else the likeliest
		if i == 0 || (shape != "binary" && p.P > r.P) {
			r.Outcome, r.P = p.Outcome, p.P
		}
	}
	o, p := r.Outcome, r.P
	if shape == "binary" && p < 0.5 {
		o, p = "No", 1-p
	}
	r.ResolutionDate, r.Lean = date.Format(time.DateOnly), fmt.Sprintf("%s %.0f%%", o, p*100)
	r.Stance = stance(r.PriceRule, info.Symbol, r.P, direction)
	return &r, nil
}

// stance is how the desk reads a long or a short: only a price rule on the
// symbol says which side a Yes favours. Within 3 points of a coin flip the
// desk is split. nil without such a rule or for any other direction.
// ponytail: duplicates chat's forecastRef.stance (internal/chat/desk.go); keep the two in step,
// or move both to one package when a third reader appears.
func stance(rule *priceRule, symbol string, pYes float64, direction string) *string {
	if rule == nil || rule.Symbol != symbol || (direction != "long" && direction != "short") {
		return nil
	}
	bullish := pYes
	if rule.Op == "below" {
		bullish = 1 - pYes
	}
	sideP := bullish
	if direction == "short" {
		sideP = 1 - bullish
	}
	s := "split"
	switch {
	case sideP >= 0.53:
		s = "supports"
	case sideP <= 0.47:
		s = "opposes"
	}
	return &s
}

// deskLine is the plan prompt's line about the desk read.
func deskLine(d *DeskRead) string {
	return fmt.Sprintf("\nForecast desk (the user's own AI forecasting panel; may be stale): '%s' → %s, resolves %s. "+
		"If your target sits beyond the threshold the desk leans against, lower your confidence and say so in one rationale line.\n",
		d.Question, d.Lean, d.ResolutionDate)
}
