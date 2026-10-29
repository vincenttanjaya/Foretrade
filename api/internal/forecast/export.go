package forecast

import (
	"context"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
)

// DraftIdea turns a rough idea into a draft question, as POST
// /api/forecasts/draft does, for the agent chat's draft_forecast tool. The
// caller spends (and refunds) the draft credit.
func DraftIdea(ctx context.Context, d *app.Deps, idea string) (Draft, error) {
	return (&handler{d: d}).draft(ctx, idea, todayUTC())
}

// DerivedView is the desk view an ordered result stored before round 9b
// reads as (GET's derived view: no reference), for the agent chat's
// get_forecast; nil when its ranges do not parse.
func DerivedView(outcomes []string, probs []OutcomeP, resolutionDate string, completedAt *time.Time) *DeskView {
	d := Draft{Shape: "ordered", Outcomes: outcomes, ResolutionDate: resolutionDate}
	at := time.Now()
	if completedAt != nil {
		at = *completedAt
	}
	v := deskView(d, probs, nil, horizonOf(d, runDay(at, nil)), noBasis)
	if v != nil {
		v.Derived = true
	}
	return v
}
