package forecast

// The volatility-only reference R1: for a binary price-rule
// question the server prices the rule under the same zero-drift lognormal
// the replay's anchor uses (spot and 90-day realized volatility as of the
// run), shows it to the panel and the aggregator, stores it as anchor_p, and
// keeps the desk from drifting toward 50% from it without cited evidence.

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// shrinkSlack is how much closer to 50% than R1 the desk may land without a
// referenceNote: two points, rounding noise.
const shrinkSlack = 0.02

const guardNote = "Server check: the desk moved toward 50% from the volatility-only reference without citing evidence, so the final probability is the reference's."

// Reference is R1 as of the run: P(Yes) of the question's price rule under a
// zero-drift lognormal with σ from the last 90 daily closes.
type Reference struct {
	Kind        string    `json:"kind"` // volatility
	PYes        float64   `json:"pYes"` // within [0.01, 0.99]
	Spot        float64   `json:"spot"`
	VolAnnPct   float64   `json:"volAnnPct"`
	HorizonDays int       `json:"horizonDays"`
	AsOf        time.Time `json:"asOf"` // the run's clock: now for a live run, asOf 00:00 UTC for a replay
}

// ReferenceNote is the aggregator's reason for landing on the other side of
// 50% from R1, or closer to 50% than R1 by more than shrinkSlack, with the
// ids of this run's findings or sources that support it.
type ReferenceNote struct {
	Reason string   `json:"reason"`
	IDs    []string `json:"ids"`
}

// referenceP is P(Yes) of rule under o. on_date: P(the close beyond the
// threshold at the horizon). by_date: the driftless first-passage chance by
// the reflection principle, min(2·P(end beyond), 1). Clamped to [0.01, 0.99]
// like every binary forecast. ok is false for a by_date rule whose spot is
// already beyond the threshold: only daily closes from the creation day
// count, so the question is not decided yet, and a first-passage chance from
// past the barrier is no reference (it would read as a near-certain 0.99).
func referenceP(rule PriceRule, o market.PriceOutlook) (p float64, ok bool) {
	end, beyond := below(o, rule.Threshold), o.Spot < rule.Threshold
	if rule.Op == "above" {
		end, beyond = 1-end, o.Spot > rule.Threshold
	}
	p = end
	if rule.Mode == "by_date" {
		if beyond {
			return 0, false
		}
		p = min(2*end, 1)
	}
	return min(max(p, 0.01), 0.99), true
}

// reference is R1 for d as of now (today: its UTC date), horizon = the days
// from today to the resolution date. hist set: a replay's bars before asOf,
// so R1 equals the question's AnchorP. nil without a valid price rule (binary
// only), when the market data call fails, or for a by_date rule whose spot is
// already beyond the threshold (both logged; the run goes on).
func (h *handler) reference(ctx context.Context, d Draft, now, today time.Time, hist *history) *Reference {
	rule := cleanRule(d.PriceRule, d.Shape)
	date, err := time.Parse(time.DateOnly, d.ResolutionDate)
	if rule == nil || err != nil {
		return nil
	}
	days := max(int(date.Sub(today).Hours()/24), 1)
	outlook := h.d.Market.Outlook
	if hist != nil {
		outlook = hist.outlook
	}
	o, err := outlook(ctx, rule.Symbol, days)
	if err != nil {
		slog.Warn("forecast without a volatility reference", "symbol", rule.Symbol, "err", err)
		return nil
	}
	if o.HorizonDays < days { // past a year the outlook clamps: the same σ over the whole horizon
		o = market.OutlookFrom(o.Symbol, o.Spot, o.VolAnnPct/100, days)
	}
	p, ok := referenceP(*rule, o)
	if !ok {
		slog.Info("forecast without a volatility reference", "symbol", rule.Symbol, "op", rule.Op, "mode", rule.Mode,
			"reason", "spot already beyond a by_date threshold; only closes from the creation day count")
		return nil
	}
	return &Reference{Kind: "volatility", PYes: p, Spot: o.Spot, VolAnnPct: o.VolAnnPct, HorizonDays: days, AsOf: now.UTC()}
}

// referencePrompt is the R1 line every panel member and the aggregator see,
// with the spot in the brief's trusted data ("" without R1).
func referencePrompt(ref *Reference, d Draft) string {
	if ref == nil || d.PriceRule == nil {
		return ""
	}
	r := d.PriceRule
	event := fmt.Sprintf("the %s daily close on %s is %s the threshold", r.Symbol, d.ResolutionDate, r.Op)
	method := "zero-drift lognormal"
	if r.Mode == "by_date" {
		event = fmt.Sprintf("any %s daily close through %s is %s the threshold", r.Symbol, d.ResolutionDate, r.Op)
		method += ", first passage by the reflection principle"
	}
	return fmt.Sprintf("\nR1 · Volatility-only reference (server-computed from Binance daily closes, %s; tool data, safe to cite as R1): P(Yes) %.1f%% that %s; spot $%s, threshold $%s, horizon %d days, 90-day realized volatility %.1f%%/yr, as of %s.\n",
		method, ref.PYes*100, event, strconv.FormatFloat(ref.Spot, 'f', -1, 64), money(r.Threshold), ref.HorizonDays, ref.VolAnnPct,
		ref.AsOf.UTC().Format("2006-01-02 15:04 UTC"))
}

// cleanReferenceNote keeps a note with a reason, its ids upper-cased and
// limited to this run's web findings and sources (at most 6, once each); nil
// otherwise. The user's own material (S0, the D ids and the findings read
// from them) is unverified, so it cannot keep a hedge.
func cleanReferenceNote(raw *ReferenceNote, sources []Source, findings []Finding) *ReferenceNote {
	if raw == nil {
		return nil
	}
	n := &ReferenceNote{Reason: truncate(strings.TrimSpace(raw.Reason), 400), IDs: []string{}}
	if n.Reason == "" {
		return nil
	}
	for _, id := range raw.IDs {
		id = strings.ToUpper(strings.Trim(id, "[] "))
		known := slices.ContainsFunc(sources, func(s Source) bool { return s.ID == id }) || slices.ContainsFunc(findings, func(f Finding) bool { return f.ID == id && f.Origin != "material" })
		if known && !slices.Contains(n.IDs, id) && len(n.IDs) < 6 {
			n.IDs = append(n.IDs, id)
		}
	}
	return n
}

// shrinkGuard checks a sanitized binary result against its R1: it triggers
// when the final P(Yes) d is on the other side of 50% from r, or closer to
// 50% than r by more than shrinkSlack. A triggered result keeps d only with a
// referenceNote citing at least one of the run's findings or sources;
// otherwise P(Yes) becomes r, a low_information flag says why and Guard is
// "shrink". It never moves d away from r or past it. The note is kept only
// when it kept d.
func shrinkGuard(res *Result) (triggered, kept bool) {
	ref := res.Reference
	if ref == nil || len(res.Probabilities) != 2 {
		res.ReferenceNote = nil
		return false, false
	}
	d, r := res.Probabilities[0].P, ref.PYes
	triggered = (d-0.5)*(r-0.5) < 0 || math.Abs(d-0.5) < math.Abs(r-0.5)-shrinkSlack
	kept = triggered && res.ReferenceNote != nil && len(res.ReferenceNote.IDs) > 0
	slog.Info("forecast reference", "d", d, "r", r, "triggered", triggered, "kept", kept)
	if !kept {
		res.ReferenceNote = nil
	}
	if triggered && !kept {
		res.Probabilities[0].P, res.Probabilities[1].P = r, 1-r
		res.Flags = append(res.Flags, Flag{"low_information", guardNote})
		res.Guard = "shrink"
	}
	return triggered, kept
}
