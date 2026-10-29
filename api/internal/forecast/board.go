package forecast

// Prediction markets board (round 7): live Polymarket markets on crypto and
// macro from Gamma's event listing, each with its crowd odds, a volatility-
// model estimate for price questions ("Will the price of Bitcoin be above
// $86,000 on October 6?") and the caller's own desk read where one of their
// reports matches it. Browsing makes no model call and spends nothing; "Run
// the desk" opens a pre-filled draft (draftFor) and only the run spends.
// Nothing trades.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

const (
	boardTTL     = 5 * time.Minute  // a category's listing is read from Gamma at most this often
	boardRetry   = time.Minute      // after a failed read, the last listing serves this long before the next try
	boardStale   = time.Hour        // a listing older than this is never served
	boardBudget  = 15 * time.Second // one listing read: Gamma (marketBudget), then the estimates
	boardEvents  = 30               // events per category, busiest (24h volume) first
	boardMarkets = 20               // markets per event, the highest volume
	boardLead    = time.Hour        // a market ending sooner is left out

	// quantConfidence: the estimate is a volatility model, not the desk, so it
	// leans like a low-confidence report (only at 10+ points).
	quantConfidence = "low"
)

// boardTags are the categories and their Gamma tags, in the order "all" lists them.
var boardTags = []struct{ category, tag string }{{"crypto", "crypto"}, {"macro", "economy"}}

// BoardEvent is contract PredictionEvent.
type BoardEvent struct {
	Slug         string        `json:"slug"`
	Title        string        `json:"title"`
	URL          string        `json:"url"`      // https://polymarket.com/event/<slug>
	Category     string        `json:"category"` // crypto | macro
	EndDate      string        `json:"endDate"`  // Gamma's, else the latest market end
	Volume24hUSD *float64      `json:"volume24hUsd"`
	Markets      []BoardMarket `json:"markets"`
}

// BoardMarket is contract PredictionMarket.
type BoardMarket struct {
	ID                  string         `json:"id"` // Gamma's market id
	MarketSlug          string         `json:"marketSlug"`
	ConditionID         string         `json:"conditionId"`
	LinkedMarket        *LinkedMarket  `json:"linkedMarket,omitempty"`
	EstimateUnavailable *string        `json:"estimateUnavailable"`
	Question            string         `json:"question"`
	YesPrice            float64        `json:"yesPrice"`    // the market's own Yes, 4 dp
	Change24hPp         *float64       `json:"change24hPp"` // Gamma oneDayPriceChange × 100, 1 dp
	VolumeUSD           *float64       `json:"volumeUsd"`   // lifetime volumeNum
	Volume24hUSD        *float64       `json:"volume24hUsd"`
	EndDate             string         `json:"endDate"`
	Thin                bool           `json:"thin"`
	Rule                *PriceQuestion `json:"rule"`
	Estimate            *Estimate      `json:"estimate"`
	Desk                *DeskRead      `json:"desk"` // per request: the caller's own report on this market

	end         time.Time // EndDate, parsed
	description string    // Gamma's resolution rules, for a draft's criteria
}

// PriceQuestion is a market the volatility model can price: an asset's price
// above or below a strike at the market's end (close) or at any time before
// it (touch).
type PriceQuestion struct {
	Symbol string  `json:"symbol"`
	Op     string  `json:"op"` // above | below
	Strike float64 `json:"strike"`
	Kind   string  `json:"kind"` // close | touch
}

// Estimate is the quant estimate: P(Yes) from spot and realized volatility
// alone, no model call and no view.
type Estimate struct {
	P           float64   `json:"p"`
	Method      string    `json:"method"` // lognormal | touch
	Spot        float64   `json:"spot"`
	VolAnnPct   float64   `json:"volAnnPct"`
	Days        float64   `json:"days"` // to the market's end when the listing was read
	Source      string    `json:"source"`
	SpotAsOf    time.Time `json:"spotAsOf"`
	WindowStart time.Time `json:"windowStart"`
	WindowEnd   time.Time `json:"windowEnd"`
	Closes      int       `json:"closes"`
	Assumptions string    `json:"assumptions"`
	Lean
}

// DeskRead is the caller's report on a board market: the desk's P of its Yes.
type DeskRead struct {
	ForecastID string  `json:"forecastId"`
	Question   string  `json:"question"` // the report's question
	P          float64 `json:"p"`
	Match      string  `json:"match"`
	Lean
}

// Lean is an edge against the market's price and what it suggests.
type Lean struct {
	EdgePp   float64  `json:"edgePp"`   // (p − yesPrice) × 100, 1 dp
	EVPer100 *float64 `json:"evPer100"` // EV of $100 on the signal's side at the market's price; null when waiting
	Signal   string   `json:"signal"`   // yes | no | wait
}

// lean prices p against a market's Yes with MarketOdds' rules (±5 points,
// ±10 at low confidence, thin → wait) and adds the EV of $100 on the side it
// leans to, bought at the market's price (no fee or slippage).
func lean(p, yes float64, thinMarket bool, confidence string) Lean {
	m := MarketOdds{YesPrice: yes, Thin: thinMarket}
	m.price(p, confidence)
	l := Lean{EdgePp: m.EdgePp, Signal: m.Signal}
	switch {
	case m.Signal == "yes" && yes > 0:
		l.EVPer100 = ptr(round(100*(p-yes)/yes, 2))
	case m.Signal == "no" && yes < 1:
		l.EVPer100 = ptr(round(100*(yes-p)/(1-yes), 2))
	}
	return l
}

// board serves the listings. Each category's is cached for boardTTL and read
// by one request at a time, so Gamma sees at most one listing read per
// category per 5 minutes (one a minute while it fails), whatever the traffic.
type board struct {
	d     *app.Deps
	now   func() time.Time
	lists map[string]*listing // by category
}

type listing struct {
	sem    chan struct{} // held by the one request reading Gamma
	mu     sync.Mutex    // guards the fields below
	events []BoardEvent
	at     time.Time // when events were read
	failed time.Time // the last failed read
}

func newBoard(d *app.Deps) *board {
	b := &board{d: d, now: time.Now, lists: map[string]*listing{}}
	for _, t := range boardTags {
		b.lists[t.category] = &listing{sem: make(chan struct{}, 1)}
	}
	return b
}

func (b *board) register(mux *http.ServeMux) {
	auth := func(f http.HandlerFunc) http.Handler { return b.d.Auth.Require(f) }
	mux.Handle("GET /api/predictions/markets", auth(b.handleList))
	mux.Handle("GET /api/predictions/markets/{id}/draft", auth(b.handleDraft))
}

var errBoardDown = fmt.Errorf("%w: prediction markets", market.ErrUpstream)

// serve is the listing as it stands at now: fresh while under boardTTL old,
// else stale while under boardStale old; ok false when there is none to serve.
func (l *listing) serve(now time.Time) (events []BoardEvent, at time.Time, stale, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	age := now.Sub(l.at)
	return l.events, l.at, age >= boardTTL, l.events != nil && age < boardStale
}

// read serves category's listing: the cached one while it is fresh, else one
// Gamma read that the other requests wait for (detached from the caller, so a
// client hanging up does not fail it for everyone). A failed read, or a
// request within boardRetry of one, gets the last listing as stale.
func (b *board) read(ctx context.Context, category string) ([]BoardEvent, time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, false, err
	}
	l := b.lists[category]
	if ev, at, stale, ok := l.serve(b.now()); ok && !stale {
		return ev, at, false, nil
	}
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-ctx.Done():
		return nil, time.Time{}, false, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, false, err
	}
	now := b.now()
	ev, at, stale, ok := l.serve(now) // another request may have read it meanwhile
	if ok && !stale {
		return ev, at, false, nil
	}
	l.mu.Lock()
	retry := now.Sub(l.failed) >= boardRetry
	l.mu.Unlock()
	if retry {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boardBudget)
		events, err := b.fetch(rctx, category)
		l.mu.Lock()
		if expired := rctx.Err(); expired != nil {
			err = expired
		}
		if err == nil {
			l.events, l.at = events, now
		} else {
			l.failed = now
		}
		l.mu.Unlock()
		cancel()
		if err == nil {
			return events, now, false, nil
		}
		slog.Warn("prediction board listing failed", "category", category, "err", err)
	}
	// The failed read may have crossed the maximum stale age.
	if ev, at, _, ok := l.serve(b.now()); ok {
		return ev, at, true, nil
	}
	return nil, time.Time{}, false, errBoardDown
}

// fetch reads category's events from Gamma, keeps their eligible markets and
// adds the quant estimates.
func (b *board) fetch(ctx context.Context, category string) ([]BoardEvent, error) {
	if b.d.PolymarketBase == "" {
		return nil, errors.New("POLYMARKET_BASE is empty")
	}
	i := slices.IndexFunc(boardTags, func(t struct{ category, tag string }) bool { return t.category == category })
	q := url.Values{"tag_slug": {boardTags[i].tag}, "active": {"true"}, "closed": {"false"}, "order": {"volume24hr"},
		"ascending": {"false"}, "limit": {strconv.Itoa(boardEvents)}}
	gctx, cancel := context.WithTimeout(ctx, marketBudget)
	defer cancel()
	var raw []gammaEvent
	if err := (&handler{b.d}).gammaGet(gctx, "/events?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	now := b.now()
	out := []BoardEvent{}
	for _, e := range raw[:min(len(raw), boardEvents)] {
		if ev, ok := boardEvent(e, category, now); ok {
			out = append(out, ev)
		}
	}
	b.estimate(ctx, out, now)
	return out, ctx.Err()
}

// boardEvent keeps e's markets that the desk's search would (open Yes/No with
// a price and an end) and that end more than boardLead from now and are not
// intraday: the boardMarkets busiest, price questions by strike, then the
// rest by Yes price. ok false when none is left.
func boardEvent(e gammaEvent, category string, now time.Time) (BoardEvent, bool) {
	if e.Closed || e.Archived || e.Active != nil && !*e.Active {
		return BoardEvent{}, false
	}
	var ms []BoardMarket
	for _, m := range e.Markets {
		if bm, ok := boardMarket(m, e.Slug, now); ok {
			ms = append(ms, bm)
		}
	}
	if len(ms) == 0 {
		return BoardEvent{}, false
	}
	slices.SortStableFunc(ms, func(a, b BoardMarket) int { return cmp.Compare(deref(b.VolumeUSD), deref(a.VolumeUSD)) })
	ms = ms[:min(len(ms), boardMarkets)]
	slices.SortStableFunc(ms, func(a, b BoardMarket) int {
		switch {
		case a.Rule != nil && b.Rule != nil:
			return cmp.Or(cmp.Compare(a.Rule.Strike, b.Rule.Strike), strings.Compare(a.Rule.Op, b.Rule.Op)) // above before below
		case a.Rule != nil:
			return -1
		case b.Rule != nil:
			return 1
		}
		return cmp.Compare(b.YesPrice, a.YesPrice)
	})
	end := e.EndDate
	if end == "" {
		end = slices.MaxFunc(ms, func(a, b BoardMarket) int { return a.end.Compare(b.end) }).EndDate
	}
	return BoardEvent{Slug: e.Slug, Title: cmp.Or(strings.TrimSpace(e.Title), ms[0].Question), URL: eventURL(e.Slug),
		Category: category, EndDate: end, Volume24hUSD: e.Volume24hr, Markets: ms}, true
}

// boardMarket is m as a board row, ok false when the board leaves it out.
func boardMarket(m gammaEventMarket, slug string, now time.Time) (BoardMarket, bool) {
	yes, end, ok := m.open()
	q := strings.TrimSpace(m.Question)
	if !ok || !validSlug(slug) || !marketID.MatchString(m.ID) || !validSlug(m.Slug) || !conditionID.MatchString(m.ConditionID) || q == "" || end.Sub(now) <= boardLead || intraday(candidate{event: slug, question: q}) {
		return BoardMarket{}, false
	}
	var change *float64
	if m.OneDayPriceChange != nil {
		change = ptr(round(*m.OneDayPriceChange*100, 1))
	}
	return BoardMarket{ID: m.ID, MarketSlug: m.Slug, ConditionID: m.ConditionID, LinkedMarket: &LinkedMarket{MarketID: m.ID, ConditionID: m.ConditionID, MarketSlug: m.Slug, EventSlug: slug, Question: q, Description: strings.TrimSpace(m.Description), ResolutionSource: m.ResolutionSource, EndDate: m.EndDate, AsOf: now.UTC()}, Question: q, YesPrice: round(yes, 4), Change24hPp: change, VolumeUSD: m.VolumeNum,
		Volume24hUSD: m.Volume24hr, EndDate: m.EndDate, Thin: thin(m.VolumeNum), Rule: parseRule(q), end: end,
		description: strings.TrimSpace(m.Description)}, true
}

func eventURL(slug string) string { return "https://polymarket.com/event/" + url.PathEscape(slug) }

func deref(x *float64) float64 {
	if x == nil {
		return 0
	}
	return *x
}

var (
	closeQ = regexp.MustCompile(`(?i)^will (?:the price of )?(.+?) (?:be |close )?(above|greater than|below|less than) \$[\d,.]+k? on\b`)
	touchQ = regexp.MustCompile(`(?i)^will (.+?) (reach|dip to) \$[\d,.]+k? (?:in|by|before|on)\b`)

	// polyAssets maps a crypto asset's Polymarket name and its ticker, lower
	// case, to its symbol. PAXG is left out: Polymarket's gold markets price the metal.
	polyAssets = func() map[string]string {
		out := map[string]string{}
		for _, a := range market.CryptoUniverse {
			if a.Symbol != "PAXG" {
				out[strings.ToLower(polyName(a))], out[strings.ToLower(a.Symbol)] = a.Symbol, a.Symbol
			}
		}
		return out
	}()
)

// parseRule reads a price question: "Will the price of Bitcoin be above
// $86,000 on October 6?" (close), "Will Bitcoin reach $100,000 in October?" /
// "… dip to $80,000 …" (touch). nil for anything else: ranges, FDV markets,
// assets outside the crypto universe.
func parseRule(q string) *PriceQuestion {
	r := &PriceQuestion{Kind: "close"}
	m := closeQ.FindStringSubmatch(q)
	if m == nil {
		if m = touchQ.FindStringSubmatch(q); m == nil {
			return nil
		}
		r.Kind = "touch"
	}
	sym, ok := polyAssets[strings.ToLower(strings.TrimSpace(m[1]))]
	if r.Strike = strike(q); !ok || !(r.Strike > 0) {
		return nil
	}
	r.Symbol, r.Op = sym, "above"
	if op := strings.ToLower(m[2]); op == "below" || op == "less than" || op == "dip to" {
		r.Op = "below"
	}
	return r
}

// estimate adds the quant estimate to every price question whose asset is
// live (not halted) and has a positive realized volatility: one Outlook per
// asset (its spot and σ), concurrently. Market data failing leaves them out.
func (b *board) estimate(ctx context.Context, events []BoardEvent, now time.Time) {
	for i := range events {
		for j := range events[i].Markets {
			m := &events[i].Markets[j]
			if m.Rule != nil {
				reason := "unavailable_price_data"
				if _, ok := market.LookupCrypto(m.Rule.Symbol); !ok {
					m.Estimate = nil
					m.EstimateUnavailable = ptr(reason)
					continue
				}
				if m.Rule.Kind == "touch" {
					reason = "unverified_touch_history"
				}
				m.EstimateUnavailable = ptr(reason)
			}
		}
	}
	syms := map[string]bool{}
	for _, e := range events {
		for _, m := range e.Markets {
			if _, ok := market.LookupCrypto(ruleSymbol(m.Rule)); ok && m.Rule.Kind != "touch" {
				syms[m.Rule.Symbol] = true
			}
		}
	}
	if len(syms) == 0 {
		return
	}
	assets, err := b.d.Market.Assets(ctx)
	if err != nil {
		slog.Warn("prediction board without estimates", "err", err)
		return
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	type input struct {
		spot, vol  float64
		start, end time.Time
		count      int
	}
	inputs := map[string]input{}
	for sym := range syms {
		if i := slices.IndexFunc(assets, func(a market.Asset) bool { return a.Symbol == sym }); i < 0 || assets[i].Halted {
			continue
		}
		wg.Go(func() {
			bars, err := b.d.Market.Candles(ctx, sym, "1d", 300)
			if err != nil {
				return
			}
			closed := slices.DeleteFunc(slices.Clone(bars), func(c market.Candle) bool { return time.Unix(c.T, 0).Add(24 * time.Hour).After(now) })
			if len(closed) < 3 {
				return
			}
			closed = closed[max(0, len(closed)-90):]
			end := time.Unix(closed[len(closed)-1].T, 0).UTC().Add(24 * time.Hour)
			if now.Sub(end) > 48*time.Hour {
				return
			}
			closes := market.Closes(closed)
			vol := market.RealizedVolAnn(closes)
			if !(closes[len(closes)-1] > 0) || !(vol > 0) || math.IsNaN(vol) || math.IsInf(vol, 0) {
				return
			}
			mu.Lock()
			inputs[sym] = input{closes[len(closes)-1], vol, time.Unix(closed[0].T, 0).UTC(), end, len(closed)}
			mu.Unlock()
		})
	}
	wg.Wait()
	for i := range events {
		for j := range events[i].Markets {
			m := &events[i].Markets[j]
			if m.Rule != nil && m.Rule.Kind == "touch" {
				m.EstimateUnavailable = ptr("unverified_touch_history")
				continue
			}
			if o, ok := inputs[ruleSymbol(m.Rule)]; ok {
				m.Estimate = quantEstimate(*m.Rule, o.spot, o.vol, m.end.Sub(now), m.YesPrice, m.Thin)
				m.EstimateUnavailable = nil
				m.Estimate.Source = "Binance completed daily candles"
				m.Estimate.SpotAsOf = o.end
				m.Estimate.WindowStart = o.start
				m.Estimate.WindowEnd = o.end
				m.Estimate.Closes = o.count
				m.Estimate.Assumptions = "Zero-drift log-price proxy from completed daily closes to market end; external observation and resolution rules may differ."
			}
		}
	}
}

func ruleSymbol(r *PriceQuestion) string {
	if r == nil {
		return ""
	}
	return r.Symbol
}

// quantEstimate prices r from spot and annualized volatility over the time
// left: lognormal for a close, the barrier touch for a touch.
func quantEstimate(r PriceQuestion, spot, vol float64, left time.Duration, yes float64, thinMarket bool) *Estimate {
	years := left.Hours() / 24 / 365
	var p float64
	method := "lognormal"
	if r.Kind == "touch" {
		method, p = "touch", market.ProbTouch(spot, r.Strike, vol, years, r.Op == "above")
	} else if p = market.ProbAbove(spot, r.Strike, vol, years); r.Op == "below" {
		p = 1 - p
	}
	p = round(p, 4)
	return &Estimate{P: p, Method: method, Spot: spot, VolAnnPct: round(vol*100, 1), Days: round(left.Hours()/24, 1),
		Lean: lean(p, yes, thinMarket, quantConfidence)}
}

func (b *board) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	category, sortBy := cmp.Or(q.Get("category"), "all"), cmp.Or(q.Get("sort"), "edge")
	if !slices.Contains([]string{"all", "crypto", "macro"}, category) {
		app.WriteError(w, &app.BadRequest{Msg: "category must be all, crypto or macro."})
		return
	}
	if !slices.Contains([]string{"edge", "volume", "ending"}, sortBy) {
		app.WriteError(w, &app.BadRequest{Msg: "sort must be edge, volume or ending."})
		return
	}
	ctx := r.Context()
	var events []BoardEvent
	var asOf time.Time
	stale, served, seen := false, 0, map[string]bool{}
	for _, t := range boardTags {
		if category != "all" && category != t.category {
			continue
		}
		evs, at, st, err := b.read(ctx, t.category)
		if err != nil {
			if ctx.Err() != nil {
				app.WriteError(w, ctx.Err())
				return
			}
			stale = true
			continue
		}
		served++
		stale = stale || st
		if asOf.IsZero() || at.Before(asOf) {
			asOf = at
		}
		for _, e := range evs {
			if !seen[e.Slug] {
				seen[e.Slug] = true
				e.Markets = slices.DeleteFunc(slices.Clone(e.Markets), func(m BoardMarket) bool { return m.end.Sub(b.now()) <= boardLead })
				if len(e.Markets) == 0 {
					continue
				}
				events = append(events, e)
			}
		}
	}
	if served == 0 {
		app.WriteError(w, errBoardDown)
		return
	}
	reads, err := b.deskReads(ctx, httpx.UserID(ctx))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	for i := range events {
		for j := range events[i].Markets {
			m := &events[i].Markets[j]
			if d, ok := reads[[2]string{m.ID, m.ConditionID}]; ok && SameLinkedMarket(d.linked, m.LinkedMarket) {
				d.Lean = lean(d.P, m.YesPrice, m.Thin, d.confidence)
				m.Desk = &d.DeskRead
			}
			m.LinkedMarket = nil // full rules stay in the internal cache and free draft, not every list row
		}
	}
	assets, assetErr := b.d.Market.Assets(ctx)
	for i := range events {
		for j := range events[i].Markets {
			m := &events[i].Markets[j]
			_, crypto := market.LookupCrypto(ruleSymbol(m.Rule))
			available := crypto && slices.ContainsFunc(assets, func(a market.Asset) bool { return a.Symbol == ruleSymbol(m.Rule) && !a.Halted })
			if m.Estimate != nil && (stale || assetErr != nil || !available) {
				m.Estimate = nil
				m.EstimateUnavailable = ptr("stale_or_unavailable_price_data")
			}
			if stale && m.Desk != nil {
				copy := *m.Desk
				copy.Signal, copy.EVPer100 = "wait", nil
				m.Desk = &copy
			}
		}
	}
	sortEvents(events, sortBy)
	if events == nil {
		events = []BoardEvent{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"events": events, "asOf": asOf.UTC(), "stale": stale})
}

// sortEvents orders the board: edge (the largest |edge| over non-thin markets,
// a desk read's before the estimate's; events with none after, by volume),
// volume (24h, descending) or ending (soonest first).
func sortEvents(events []BoardEvent, by string) {
	vol := func(e BoardEvent) float64 { return deref(e.Volume24hUSD) }
	edge := func(e BoardEvent) (float64, bool) {
		best, ok := 0.0, false
		for _, m := range e.Markets {
			var pp *float64
			switch {
			case m.Thin:
			case m.Desk != nil:
				pp = &m.Desk.EdgePp
			case m.Estimate != nil:
				pp = &m.Estimate.EdgePp
			}
			if pp != nil && (!ok || math.Abs(*pp) > best) {
				best, ok = math.Abs(*pp), true
			}
		}
		return best, ok
	}
	end := func(e BoardEvent) time.Time { t, _ := time.Parse(time.RFC3339, e.EndDate); return t }
	slices.SortStableFunc(events, func(a, b BoardEvent) int {
		switch by {
		case "edge":
			ea, oka := edge(a)
			eb, okb := edge(b)
			switch {
			case oka && !okb:
				return -1
			case okb && !oka:
				return 1
			case ea != eb:
				return cmp.Compare(eb, ea)
			}
		case "ending":
			if c := end(a).Compare(end(b)); c != 0 {
				return c
			}
		}
		return cmp.Compare(vol(b), vol(a))
	})
}

// deskRow is one of the caller's reports on a market: the desk's P of the
// market's Yes and the report's confidence (for the lean).
type deskRow struct {
	DeskRead
	confidence string
	linked     *LinkedMarket
}

// deskReads includes only exact canonical manual predicates, private to this caller.
func (b *board) deskReads(ctx context.Context, user string) (map[[2]string]deskRow, error) {
	rows, err := b.d.DB.Query(ctx, `select f.id, f.shape,f.question,f.outcomes,f.resolution_criteria,f.resolution_date::text,f.price_rule,f.linked_market,
 coalesce(f.result->'probabilities','[]'),coalesce(f.result->>'confidence',''),f.result->'market'
 from forecasts f where f.user_id=$1 and f.status='completed' and f.outcome is null and f.linked_market is not null
 and not exists(select 1 from forecasts c where c.parent_id=f.id and c.status='completed') order by f.completed_at desc limit 200`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]deskRow{}
	for rows.Next() {
		var id, confidence string
		var d Draft
		var probs []OutcomeP
		var m *MarketOdds
		if err := rows.Scan(&id, &d.Shape, &d.Question, &d.Outcomes, &d.ResolutionCriteria, &d.ResolutionDate, &d.PriceRule, &d.LinkedMarket, &probs, &confidence, &m); err != nil {
			return nil, err
		}
		if !linkedPredicate(d) || m == nil || m.Match != "exact" || m.Question != d.LinkedMarket.Question || m.URL != eventURL(d.LinkedMarket.EventSlug) {
			continue
		}
		k := [2]string{d.LinkedMarket.MarketID, d.LinkedMarket.ConditionID}
		if _, seen := out[k]; !seen {
			out[k] = deskRow{DeskRead: DeskRead{ForecastID: id, Question: d.Question, P: round(pOf(probs, "Yes"), 4), Match: "exact"}, confidence: confidence, linked: d.LinkedMarket}
		}
	}
	return out, rows.Err()
}

func (b *board) handleDraft(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	m, ev, category, err := b.find(ctx, id)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	d, err := draftFor(m, ev, category, b.now().UTC().Truncate(24*time.Hour))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"draft": d, "market": m,
		"event": map[string]string{"slug": ev.Slug, "title": ev.Title, "url": ev.URL}})
}

var marketID = regexp.MustCompile(`^\d{1,20}$`)

// find always revalidates the child freshly; discovery cache is not action authority.
func (b *board) find(ctx context.Context, id string) (BoardMarket, BoardEvent, string, error) {
	if !marketID.MatchString(id) {
		return BoardMarket{}, BoardEvent{}, "", app.ErrNotFound
	}
	parent, category := "", "other"
	for _, t := range boardTags {
		l := b.lists[t.category]
		l.mu.Lock()
		for _, e := range l.events {
			if slices.ContainsFunc(e.Markets, func(m BoardMarket) bool { return m.ID == id }) {
				parent, category = e.Slug, t.category
			}
		}
		l.mu.Unlock()
	}
	l, o, e, err := readCanonical(ctx, b.d, id, "", parent, b.now)
	if err != nil {
		var bad *app.BadRequest
		if errors.As(err, &bad) {
			err = app.ErrNotFound
		}
		return BoardMarket{}, BoardEvent{}, "", err
	}
	end, _ := time.Parse(time.RFC3339, l.EndDate)
	if end.Sub(b.now()) <= boardLead || intraday(candidate{event: l.EventSlug, question: l.Question}) {
		return BoardMarket{}, BoardEvent{}, "", app.ErrNotFound
	}
	m := BoardMarket{ID: l.MarketID, MarketSlug: l.MarketSlug, ConditionID: l.ConditionID, LinkedMarket: l, Question: l.Question, YesPrice: o.YesPrice, VolumeUSD: o.VolumeUSD, EndDate: l.EndDate, Thin: o.Thin, Rule: parseRule(l.Question), end: end, description: l.Description}
	if m.Rule != nil {
		category = "crypto"
	}
	ev := BoardEvent{Slug: l.EventSlug, Title: e.Title, URL: eventURL(l.EventSlug), Category: category, EndDate: l.EndDate, Markets: []BoardMarket{m}}
	b.estimate(ctx, []BoardEvent{ev}, b.now())
	m = ev.Markets[0]
	ev.Markets = nil
	return m, ev, category, nil
}

var errEndsToday = &app.Conflict{Msg: "This market ends today; the desk needs a resolution date after today."}

// draftFor keeps the complete external predicate and its manual tracking date.
func draftFor(m BoardMarket, ev BoardEvent, category string, today time.Time) (Draft, error) {
	day := m.end.UTC().Truncate(24 * time.Hour)
	if !day.After(today) {
		return Draft{}, errEndsToday
	}
	if !m.LinkedMarket.canonical() {
		return Draft{}, &app.Conflict{Msg: "The market lacks compatible complete rules or identity."}
	}
	return Draft{Shape: "binary", Question: m.Question, Outcomes: slices.Clone(yesNo), ResolutionCriteria: m.LinkedMarket.Description, ResolutionDate: day.Format(time.DateOnly), Category: category, LinkedMarket: m.LinkedMarket}, nil
}
