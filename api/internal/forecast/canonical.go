package forecast

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

const maxLinkedCriteria = 8000
const maxLinkedSource = 2048

var conditionID = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

func (l *LinkedMarket) canonical() bool {
	if l == nil {
		return false
	}
	_, err := time.Parse(time.RFC3339, l.EndDate)
	return marketID.MatchString(l.MarketID) && conditionID.MatchString(l.ConditionID) && validSlug(l.MarketSlug) && validSlug(l.EventSlug) &&
		utf8.RuneCountInString(l.Question) >= 10 && utf8.RuneCountInString(l.Question) <= 280 && l.Description != "" && utf8.RuneCountInString(l.Description) <= maxLinkedCriteria && (l.ResolutionSource == nil || utf8.RuneCountInString(*l.ResolutionSource) <= maxLinkedSource) && err == nil && !l.AsOf.IsZero()
}

func validSlug(s string) bool       { return len(s) <= 200 && eventSlug.MatchString(s) }
func sourceEqual(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func SameLinkedMarket(a, b *LinkedMarket) bool {
	return a.canonical() && b.canonical() && a.MarketID == b.MarketID && a.ConditionID == b.ConditionID && a.MarketSlug == b.MarketSlug && a.EventSlug == b.EventSlug &&
		a.Question == b.Question && a.Description == b.Description && sourceEqual(a.ResolutionSource, b.ResolutionSource) && a.EndDate == b.EndDate
}
func linkedPredicate(d Draft) bool {
	l := d.LinkedMarket
	if !l.canonical() {
		return false
	}
	end, _ := time.Parse(time.RFC3339, l.EndDate)
	return d.Shape == "binary" && slices.Equal(d.Outcomes, yesNo) && d.PriceRule == nil && d.Question == l.Question && d.ResolutionCriteria == l.Description && d.ResolutionDate == end.UTC().Format(time.DateOnly)
}

// ReadLinkedMarket resolves a unique leaf (or numeric ID) directly from Gamma.
// It never consults the discovery cache, a model or stored prices.
func ReadLinkedMarket(ctx context.Context, d *app.Deps, id, slug string) (*LinkedMarket, *MarketOdds, error) {
	l, m, _, err := readCanonical(ctx, d, id, slug, "", time.Now)
	return l, m, err
}

func readCanonical(ctx context.Context, d *app.Deps, id, slug, parent string, clock func() time.Time) (*LinkedMarket, *MarketOdds, gammaEvent, error) {
	bad := func(msg string) (*LinkedMarket, *MarketOdds, gammaEvent, error) {
		return nil, nil, gammaEvent{}, &app.BadRequest{Msg: msg}
	}
	if id != "" && !marketID.MatchString(id) || slug != "" && !validSlug(slug) || id == "" && slug == "" {
		return bad("A valid leaf market slug or numeric ID is required.")
	}
	if d.PolymarketBase == "" {
		return nil, nil, gammaEvent{}, market.ErrUpstream
	}
	ctx, cancel := context.WithTimeout(ctx, refreshBudget)
	defer cancel()
	q := url.Values{}
	if id != "" {
		q.Set("id", id)
	} else {
		q.Set("slug", slug)
	}
	var raw []gammaEventMarket
	if err := (&handler{d}).gammaGet(ctx, "/markets?"+q.Encode(), &raw); err != nil {
		if errors.Is(err, errGammaNotFound) {
			return bad("Unknown leaf market.")
		}
		return nil, nil, gammaEvent{}, fmt.Errorf("%w: %w", market.ErrUpstream, err)
	}
	if len(raw) != 1 {
		return bad("The leaf market must resolve uniquely.")
	}
	// Eligibility and snapshot time describe the completed read, including a
	// leaf that ended while Gamma was responding. Board callers inject a clock.
	now := clock().UTC()
	r := raw[0]
	if id != "" && r.ID != id || slug != "" && r.Slug != slug || !marketID.MatchString(r.ID) || !conditionID.MatchString(r.ConditionID) || !validSlug(r.Slug) {
		return bad("Market identity does not match.")
	}
	yes, end, ok := r.open()
	if !ok || r.AcceptingOrders != nil && !*r.AcceptingOrders || !end.After(now) {
		return bad("The market is closed, paused or unavailable.")
	}
	parents := []gammaEvent{}
	for _, e := range r.Events {
		if validSlug(e.Slug) && !e.Closed && !e.Archived && (e.Active == nil || *e.Active) && (parent == "" || e.Slug == parent) {
			// When Gamma includes child membership, it must contain this leaf.
			if len(e.Markets) > 0 && !slices.ContainsFunc(e.Markets, func(m gammaEventMarket) bool { return m.ID == r.ID }) {
				continue
			}
			parents = append(parents, e)
		}
	}
	if len(parents) != 1 {
		return bad("The market's parent membership is ambiguous or changed.")
	}
	l := &LinkedMarket{MarketID: r.ID, ConditionID: r.ConditionID, MarketSlug: r.Slug, EventSlug: parents[0].Slug, Question: strings.TrimSpace(r.Question), Description: strings.TrimSpace(r.Description), ResolutionSource: r.ResolutionSource, EndDate: r.EndDate, AsOf: now.UTC()}
	if utf8.RuneCountInString(l.Description) > maxLinkedCriteria {
		return nil, nil, gammaEvent{}, &app.Conflict{Msg: "Complete market rules exceed the 8,000-character linked limit."}
	}
	if !l.canonical() {
		return bad("The market lacks compatible complete rules or identity.")
	}
	m := odds(candidate{event: l.EventSlug, question: l.Question, endDate: l.EndDate, yes: yes, volume: r.VolumeNum, end: end}, "exact")
	m.AsOf, m.LinkStatus = l.AsOf, "current"
	return l, m, parents[0], nil
}

func (h *handler) validateLinked(ctx context.Context, d *Draft, freshSnapshot bool) error {
	if d.LinkedMarket == nil {
		return nil
	}
	if !linkedPredicate(*d) {
		return &app.BadRequest{Msg: "linkedMarket does not match the complete manual forecast predicate."}
	}
	l, _, err := readPinned(ctx, h.d, d.LinkedMarket)
	if err != nil {
		return err
	}
	if freshSnapshot {
		d.LinkedMarket = l
	}
	return nil
}

// A valid stored snapshot is pinned by ID. Fresh provider identity/rules are
// compared afterwards, so a changed leaf or parent is a conflict, not a client
// syntax error. Upstream failures remain distinct and never use stored odds.
func readPinned(ctx context.Context, d *app.Deps, link *LinkedMarket) (*LinkedMarket, *MarketOdds, error) {
	changed := &app.Conflict{Msg: "The linked market is unavailable or its identity/rules changed. Review a fresh draft."}
	if !link.canonical() {
		return nil, nil, changed
	}
	l, m, _, err := readCanonical(ctx, d, link.MarketID, "", "", time.Now)
	var bad *app.BadRequest
	if errors.As(err, &bad) || err == nil && !SameLinkedMarket(l, link) {
		return nil, nil, changed
	}
	return l, m, err
}
