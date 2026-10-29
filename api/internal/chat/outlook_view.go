package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// The price_outlook card leads with a view that carries a direction: the
// model's scenarios when it passes them, else the asset's own historical
// drift, halved (an assumption, labelled). The zero-drift distribution stays
// on the card only as the labelled volatility-only baseline.

const (
	byWhenRefDays = 3650 // a by-when card without a horizon: the baseline's reference horizon (10 years)
	listingGap    = 0.02 // the listing's last close is shown when it differs from the perp's mark by more
	// a scenarios view whose downside (ln median/p5) is under this share of the
	// baseline's is too thin: it needs a tail case
	tailWidth = 0.7
)

var outlookMultiples = []float64{0.5, 2, 5, 10}

type outlookInput struct {
	Symbol         string                 `json:"symbol"`
	HorizonDays    *int                   `json:"horizon_days"`
	HorizonReason  string                 `json:"horizon_reason"`
	TargetMultiple *float64               `json:"target_multiple"`
	Scenarios      []market.ScenarioInput `json:"scenarios"`
}

func parseOutlookInput(input json.RawMessage) (outlookInput, error) {
	var in outlookInput
	if err := json.Unmarshal(input, &in); err != nil {
		return in, fmt.Errorf("invalid input (horizon_days must be a whole number of days): %v", err)
	}
	if d := in.HorizonDays; d != nil && (*d < 1 || *d > market.MaxOutlookDays) {
		return in, fmt.Errorf("horizon_days must be 1..%d", market.MaxOutlookDays)
	}
	if x := in.TargetMultiple; x != nil && !(*x >= 1.2 && *x <= 100) {
		return in, errors.New("target_multiple must be 1.2..100 (a multiple of today's price)")
	}
	in.HorizonReason = strings.Join(strings.Fields(in.HorizonReason), " ")
	if utf8.RuneCountInString(in.HorizonReason) > 160 {
		in.HorizonReason = string([]rune(in.HorizonReason)[:160])
	}
	var err error
	in.Symbol, err = lookup(in.Symbol)
	return in, err
}

// resolveHorizon picks the card's horizon (0: a by-when card with no
// at-horizon view) and the target multiple. The user's own timeframe wins over
// horizon_days that differs from it by more than 25% (with several, the one
// horizon_days matches, else the earliest); within 25% a span keeps
// horizon_days and a named date snaps to the date. A by-when question needs
// no horizon unless it has scenarios; anything else needs horizon_days. A
// horizon_reason that quotes the user's timeframe the parser can read counts
// as the user's; without a reason the card says the agent chose the horizon
// (basis "agent") and the model gets a warning, not an error.
func resolveHorizon(in outlookInput, userText string, today time.Time) (days int, h market.OutlookHorizon, multiple *float64, warns []string, err error) {
	days, h, multiple, warns, hints, _, err := pickHorizon(in, userText, today)
	return days, h, multiple, append(warns, hints...), err
}

// pickHorizon is resolveHorizon with its notes split: warns for the card,
// hints for the model only (the horizon line already tells the user), and
// whether the horizon is a date the user named ("by 2035", "by December"),
// not a span: the chip then names the date.
func pickHorizon(in outlookInput, userText string, today time.Time) (days int, h market.OutlookHorizon, multiple *float64, warns, hints []string, dated bool, err error) {
	named := explicitHorizons(userText, today)
	x, byWhen, ok, made := readMultiple(userText)
	multiple = in.TargetMultiple
	// "can NVDA double?" with neither horizon_days nor target_multiple: the
	// by-when card for the user's multiple, not a tool error that costs a turn.
	// A multiple with horizon_days and no by-when word stays the model's call,
	// and so does one the asset already made ("TSM doubled this year. Outlook?").
	if multiple == nil && ok && x >= 1.2 && x <= 100 && (byWhen || len(named) == 0 && in.HorizonDays == nil && !made) {
		multiple = &x
	}
	if len(named) == 0 && !(multiple != nil && (in.HorizonDays == nil || byWhen)) {
		// "the user asked for 10 years": the model read a timeframe the parser missed
		named = userTimeframes(in.HorizonReason, today)
	}
	explicit := len(named) > 0
	var pick horizonPhrase
	if explicit {
		pick = named[0]
		// With several timeframes ("5 years vs 10 years") the model's
		// horizon_days picks the one it matches; else the earliest.
		for _, n := range named {
			if in.HorizonDays != nil && math.Abs(float64(*in.HorizonDays)/float64(n.days)-1) <= 0.25 {
				pick = n
				break
			}
		}
	}
	exDays, phrase := pick.days, pick.phrase
	switch {
	case explicit:
		days = exDays
		if pick.capped != "" {
			warns = append(warns, fmt.Sprintf("%s is past the longest outlook (%s): showing %s", pick.capped, horizonWords(market.MaxOutlookDays), horizonWords(market.MaxOutlookDays)))
		}
		if in.HorizonDays != nil {
			if math.Abs(float64(*in.HorizonDays)/float64(exDays)-1) <= 0.25 {
				if !pick.dated { // "in 5 years" ≈ 1825; "by 2030" is that date, whatever horizon_days rounds to
					days = *in.HorizonDays
				}
			} else if len(in.Scenarios) > 0 {
				// the scenarios' levels and exit years were written for horizon_days: redo them, do not stretch them
				return 0, h, nil, nil, nil, false, fmt.Errorf("the question's timeframe is %q (%s days), not horizon_days %d: give horizon_days %d and scenarios at that horizon",
					phrase, commas(int64(exDays)), *in.HorizonDays, exDays)
			} else {
				hints = append(hints, fmt.Sprintf("Horizon set from the question (%q: %s days), not horizon_days %d", phrase, commas(int64(exDays)), *in.HorizonDays))
			}
		}
		h, dated = market.OutlookHorizon{Days: &days, Basis: "explicit", Reason: fmt.Sprintf("From your question: %q", phrase)}, pick.dated
	case multiple != nil && (in.HorizonDays == nil || byWhen):
		if in.HorizonDays == nil {
			if len(in.Scenarios) > 0 {
				return 0, h, nil, nil, nil, false, errors.New("give horizon_days for your scenarios")
			}
			h = market.OutlookHorizon{Basis: "by_when", Reason: fmt.Sprintf("You asked whether and when it could reach %s: odds by year, not one horizon", multipleWord(*multiple))}
			return 0, h, multiple, nil, nil, false, nil
		}
		days = *in.HorizonDays
		h = market.OutlookHorizon{Days: &days, Basis: "by_when", Reason: cmp.Or(in.HorizonReason,
			fmt.Sprintf("You asked by when it could reach %s: odds by year, with the view at %s", multipleWord(*multiple), horizonWords(days)))}
	default:
		if in.HorizonDays == nil {
			return 0, h, nil, nil, nil, false, errors.New("horizon_days: give the days ahead from the user's timeframe, or target_multiple for a by-when question")
		}
		days = *in.HorizonDays
		if in.HorizonReason == "" {
			// no round trip: the card says whose choice the horizon was
			reason := fmt.Sprintf("Horizon chosen by the agent: %s days", commas(int64(days)))
			if days >= 365 { // "1,826 days (5 years)"; "180 days", never "180 days (180 days)"
				reason += " (" + horizonWords(days) + ")"
			}
			h = market.OutlookHorizon{Days: &days, Basis: "agent", Reason: reason}
			hints = append(hints, fmt.Sprintf("No horizon_reason: the card says the agent chose %s days; when the question names no timeframe, say why the horizon fits it", commas(int64(days))))
			break
		}
		if quotesUserRe.MatchString(in.HorizonReason) && !noTimeframe.MatchString(in.HorizonReason) && len(explicitHorizons(in.HorizonReason, today)) == 0 {
			// the reason quotes a timeframe of the user's the parser reads in neither text ("the user asked about the next
			// few years"): theirs, as given; a reason naming its own span ("the user wants a long hold, 3 years") is chosen
			h = market.OutlookHorizon{Days: &days, Basis: "user", Reason: chosenReason(in.HorizonReason)}
			break
		}
		h = market.OutlookHorizon{Days: &days, Basis: "chosen", Reason: chosenReason(in.HorizonReason)}
	}
	return days, h, multiple, warns, hints, dated, nil
}

// chosenReason is horizon_reason as the card shows it. A reason that quotes
// the user ("the user asked through 2030") or already says there is no
// timeframe is shown as given, capitalised; only the model's own reasoning
// ("long run: 5 years") is prefixed "No timeframe given:".
func chosenReason(reason string) string {
	if !quotesUserRe.MatchString(reason) && !noTimeframe.MatchString(reason) {
		return "No timeframe given: " + reason
	}
	if r, n := utf8.DecodeRuneInString(reason); n > 0 {
		reason = string(unicode.ToUpper(r)) + reason[n:]
	}
	return reason
}

var (
	quotesUserRe = regexp.MustCompile(`(?i)\b(?:user|users|you|your|question|asked|asks|asking|named|names|said|says|wants|wanted|requested|mentioned|specified|stated|wrote)\b`)
	noTimeframe  = regexp.MustCompile(`(?i)\bno\s+(?:timeframe|time\s*frame|date|horizon|period)\b|\bnot\s+(?:given|named|stated|specified)\b|\bdid(?:n't| not)\b|\bwithout\s+a\s+(?:date|timeframe|horizon)\b`)
)

// userTimeframes are the timeframes horizon_reason quotes as the user's: a
// phrase the parser reads in the same clause as a reference to the user or
// the question ("the user asked for 10 years", "the question says \"10 year
// outlook\""), never in a reason that says there is none. The model's own
// choice ("long-term hold with no date: a 3-year holding period", "a long-term
// hold, 3 years") stays its own.
func userTimeframes(reason string, today time.Time) []horizonPhrase {
	if noTimeframe.MatchString(reason) {
		return nil
	}
	var out []horizonPhrase
	for _, h := range explicitHorizons(reason, today) {
		clause := reason[:h.at]
		if i := strings.LastIndexAny(clause, ".;:,"); i >= 0 {
			clause = clause[i+1:]
		}
		if quotesUserRe.MatchString(clause) {
			out = append(out, h)
		}
	}
	return out
}

// buildView adds the history, the view, the scenarios and the time to target
// to the baseline o (LongOutlook at ref days, the horizon or byWhenRefDays).
// days is 0 for a by-when card without a horizon. An invalid scenario is the
// model's to fix: a tool error.
func (a *agentRun) buildView(ctx context.Context, o *market.PriceOutlook, in outlookInput, days int, multiple *float64) error {
	spot, ref := o.Spot, o.HorizonDays
	years := float64(ref) / 365
	volView := o.VolAnnPct / 100
	volLong := volView
	info, _ := market.Lookup(in.Symbol)

	// The card's spot is USD, so its history, drift and CAGRs are too: a
	// non-USD listing reads its USD series. Only an FX failure falls back to the
	// listing's own currency, and says so; the two are never mixed.
	series, herr := a.d.Market.DailyHistoryUSD(ctx, in.Symbol)
	if errors.Is(herr, market.ErrFXHistory) {
		if s, err := a.d.Market.DailyHistory(ctx, in.Symbol); err == nil {
			series, herr = s, nil
			a.warn(o, fmt.Sprintf("FX history unavailable: drift and CAGR measured in %s, not USD", s.Currency))
		}
	}
	var hs market.HistoryStats
	switch {
	case herr != nil:
		a.warn(o, fmt.Sprintf("No long history available: volatility from the venue's %s daily bars since %s", commas(int64(o.VolBasis.Days)), o.VolBasis.Since))
	default:
		hs = market.ComputeHistoryStats(series, ref)
		o.History = market.CompactHistory(hs)
		if v := hs.VolAnnPct["all"]; v != nil {
			volLong = *v / 100
		}
		if info.Perp() && !info.QuoteFeed() && series.Ticker == info.Pair { // a quote feed's Pair is its listing
			a.warn(o, fmt.Sprintf("Only the venue's own history (%s daily closes since %s): no listing history to measure", commas(int64(hs.Sessions)), hs.From))
		}
	}

	view := &market.OutlookView{}
	var mk func(vol float64) market.Mixture
	var scen []market.ScenarioLevel
	mu, drift := 0.0, false
	if herr == nil {
		mu, drift = market.HalvedDrift(series)
	}
	switch {
	case len(in.Scenarios) > 0:
		lv, warns, err := market.ValidateScenarios(in.Scenarios, spot, years, a.clock().AddDate(0, 0, ref).Year())
		if err != nil {
			return err
		}
		if err := a.localEPS(lv, info); err != nil { // a cited EPS or level in another currency
			return err
		}
		if err := levelBounds(lv, spot, info); err != nil {
			return err
		}
		cards, hints := a.verifyInputs(lv)
		for _, w := range append(warns, cards...) {
			a.warn(o, w)
		}
		for _, h := range hints {
			a.hint(h)
		}
		for _, w := range lowPEs(lv, spot, a.clock().Year()) {
			a.warn(o, w)
			a.hint(w + ": if it is a local-currency figure, call price_outlook again with it converted; " + epsUnit(info))
		}
		scen = lv
		mk = func(vol float64) market.Mixture { return market.NewMixture(spot, years, vol, lv) }
		view.Kind, view.Label = "scenarios", "Our scenarios (subjective weights)"
	case drift:
		// A backward-looking number alone must not overpromise: cap it at the
		// listing's market's long-run return + 2 points, at most 10%/yr.
		key := market.HomeBenchmark(info)
		var bench *market.DailySeries
		if key != "" {
			read := a.d.Market.TickerHistoryUSD // in the drift's currency
			if series.Currency != "USD" {
				read = a.d.Market.TickerHistory
			}
			if s, err := read(ctx, key); err == nil {
				bench = &s
			} else if errors.Is(err, market.ErrFXHistory) { // the index read; its currency's FX did not
				a.warn(o, fmt.Sprintf("The %s benchmark's USD history is unavailable (no FX history): the drift is capped at the %g%%/yr ceiling alone", key, 10.0))
			} else {
				a.warn(o, fmt.Sprintf("The %s benchmark's history is unavailable: the drift is capped at the %g%%/yr ceiling alone", key, 10.0))
			}
		}
		var dc market.DriftCap
		mu, dc = market.CapDrift(mu, key, bench)
		if bench != nil && dc.Benchmark == "" { // under 10 years (or unusable): not a long-run return
			a.warn(o, fmt.Sprintf("The %s benchmark's history (%.1f years) is too short for a long-run return: the drift is capped at the %g%%/yr ceiling alone", key, bench.Years(), dc.CapPct))
		}
		mk = func(vol float64) market.Mixture { return market.DriftMixture(spot, years, vol, mu) }
		view.Kind, view.MuAnnPct, view.DriftCap = "historical_drift", ptr(mu*100), &dc
		switch {
		case !dc.Applied:
			view.Label = fmt.Sprintf("Historical drift since %s, halved (assumption)", hs.From[:4])
		case dc.Benchmark != "" && *dc.BenchmarkCAGRPct+2 < dc.CapPct+1e-9: // the market's return + 2 binds, not the ceiling
			view.Label = fmt.Sprintf("Historical drift since %s, capped at the market's long-run return + 2 pts (assumption)", hs.From[:4])
		default:
			view.Label = fmt.Sprintf("Historical drift since %s, capped at %g%%/yr (assumption)", hs.From[:4], dc.CapPct)
		}
	default:
		mk = func(vol float64) market.Mixture { return market.ZeroDrift(spot, years, vol) }
		view.Kind, view.Label = "none", "No directional view: volatility-only"
		a.hint("No view: pass scenarios to give a direction") // the card's "No directional view" label tells the user
	}

	// The view supersedes the baseline's drift case: its drift is the uncapped
	// halved CAGR, which the model would otherwise read beside the capped view.
	if o.LongHorizon != nil {
		o.LongHorizon.DriftCase = nil
	}
	m, base := mk(volView), market.ZeroDrift(spot, years, volView)
	view.Median, view.P5, view.P25, view.P75, view.P95 = m.Quantile(0.5), m.Quantile(0.05), m.Quantile(0.25), m.Quantile(0.75), m.Quantile(0.95)
	view.PAbove = 1 - m.CDF(spot)
	if len(scen) > 0 { // the weights' own sum, so the model never adds them up as the chance of a gain
		w := 0.0
		for _, sc := range scen {
			if sc.Level >= spot {
				w += sc.P
			}
		}
		view.PAboveByWeights = &w
	}
	view.ChangePct = (view.Median/spot - 1) * 100
	if ref >= 365 {
		view.AnnualPct = ptr((math.Pow(view.Median/spot, 365/float64(ref)) - 1) * 100)
	}
	view.VolAnnPct, view.VolAssumed = m.VolAnn*100, m.VolAssumed
	if days > 0 { // a by-when card with no horizon has no at-horizon panel
		edges := market.ViewLadderEdges(spot, m.VolAnn*math.Sqrt(years), m)
		p, pb := m.Buckets(edges), base.Buckets(edges)
		for i := range p {
			b := market.ViewBucket{P: p[i], PBaseline: pb[i]}
			switch {
			case i == 0:
				b.Hi, b.Label = &edges[0], "< "+market.FormatLevel(edges[0])
			case i == len(edges):
				b.Lo, b.Label = &edges[i-1], "≥ "+market.FormatLevel(edges[i-1])
			default:
				b.Lo, b.Hi, b.Label = &edges[i-1], &edges[i], market.FormatLevel(edges[i-1])+" – "+market.FormatLevel(edges[i])
			}
			view.Buckets = append(view.Buckets, b)
		}
		// The downside's width against each distribution's own median, not the
		// levels: a directional view's 5th percentile rises with its median, and a
		// view whose downside matches measured volatility needs no extra tail.
		if bMed, bP5 := base.Quantile(0.5), base.Quantile(0.05); view.Kind == "scenarios" && math.Log(view.Median/view.P5) < tailWidth*math.Log(bMed/bP5) {
			thin := fmt.Sprintf("The scenarios' downside is thinner than the volatility baseline's: 5th percentile %.0f%% below the median, against %.0f%%",
				(1-view.P5/view.Median)*100, (1-bP5/bMed)*100)
			a.warn(o, thin)
			a.hint(thin + ": consider a tail scenario on the downside")
		}
		xs := slices.Clone(outlookMultiples)
		if multiple != nil && !slices.Contains(xs, *multiple) {
			xs = append(xs, *multiple)
			slices.Sort(xs)
		}
		for _, x := range xs {
			vm := market.ViewMultiple{X: x}
			vm.PEnd, vm.PTouch = m.Passage(x, years)
			vm.BaselinePEnd, vm.BaselinePTouch = base.Passage(x, years)
			view.Multiples = append(view.Multiples, vm)
		}
	}
	if herr == nil && series.Ticker != info.Pair && series.Currency == "USD" && !series.Converted() { // a USD listing's own close
		if last := series.Closes[len(series.Closes)-1].C; math.Abs(last/spot-1) > listingGap {
			view.ListingSpot = &last
		}
	}
	o.View = view

	for _, s := range scen {
		os := market.OutlookScenario{Name: s.Name, Kind: s.Kind, P: s.P, Level: s.Level, ChangePct: (s.Level/spot - 1) * 100, Inputs: s.Inputs}
		if ref >= 365 {
			os.AnnualPct = ptr((math.Pow(s.Level/spot, 1/years) - 1) * 100)
		}
		o.Scenarios = append(o.Scenarios, os)
	}
	if multiple != nil {
		var own *float64
		var since *string
		if herr == nil {
			own, since = hs.CAGRPct["all"], &hs.From
		}
		tt := market.NewTimeToTarget(mk(volLong), market.ZeroDrift(spot, years, volLong), *multiple, own, since)
		o.TimeToTarget = &tt
	}
	return nil
}

func (a *agentRun) warn(o *market.PriceOutlook, w string) { o.Warnings = append(o.Warnings, w) }

// hint is a note for the model only: it goes in price_outlook's result
// string under "Notes for you", never on the card (gap round G19).
func (a *agentRun) hint(w string) { a.hints = append(a.hints, w) }

// levelBounds holds every scenario, whatever its mode, to level mode's range
// spot/50..spot×200: an EPS in the listing's currency (Samsung's ₩4,950
// against a $200 card) or a 150%/yr CAGR over 10 years is a tool error for the
// model to fix, never a view.
func levelBounds(lv []market.ScenarioLevel, spot float64, info market.AssetInfo) error {
	lo, hi := spot/50, spot*200
	for _, s := range lv {
		if s.Level >= lo && s.Level <= hi {
			continue
		}
		msg := fmt.Sprintf("scenario %q: level %s is outside %s..%s (spot/50..spot×200)", s.Name, market.FormatLevel(s.Level), market.FormatLevel(lo), market.FormatLevel(hi))
		if s.Level > 20*spot && slices.ContainsFunc(s.Inputs, func(in market.Input) bool { return in.Field == "eps" }) {
			msg += "; " + epsUnit(info)
		}
		return errors.New(msg)
	}
	return nil
}

// epsUnit is the model's instruction for an EPS in the card's unit. An ADR's
// get_stock_quote is in USD (usd null, no rate), so its home-currency EPS per
// share needs the home rate and the ADS ratio its listing note gives
// ("NYSE: TSM ADS = 5 TWSE 2330 shares").
func epsUnit(info market.AssetInfo) string {
	if l := info.Listing; l != nil && strings.Contains(l.Underlying, " ADS") {
		return "EPS must be US dollars per ADS, the card's price currency (" + l.Underlying + "): multiply a home-currency EPS per share by the shares per ADS " +
			"and by usd.rate from get_stock_quote on the home listing, or use a USD-per-ADS EPS a source states"
	}
	return "EPS must be per share (per ADS for an ADR) in USD, the card's price currency; convert a local EPS with get_stock_quote's usd.rate"
}

// localEPS refuses an EPS or a level its cited source gives only in another
// currency ("NT$134.2 per share", Citi's NT$3,200 target for the TWSE share,
// on TSM's USD ADS card): read as US dollars each puts its level about 6×
// too high, inside levelBounds, and would show as verified.
func (a *agentRun) localEPS(lv []market.ScenarioLevel, info market.AssetInfo) error {
	for _, s := range lv {
		for _, in := range s.Inputs {
			m := srcIDRe.FindStringSubmatch(in.Basis)
			if in.Field != "eps" && in.Field != "level" || in.Kind != "source" || m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			for _, it := range a.src.items {
				if it.N != n {
					continue
				}
				stated, unit := market.EPSCurrency, epsUnit(info)
				if in.Field == "level" {
					stated, unit = market.LevelCurrency, levelUnit(info)
				}
				if mark := stated(in.Value, it.Title+"\n"+it.text); mark != "" {
					return fmt.Errorf("scenario %q: %s %s cites [%d], which gives it in %s, not US dollars; %s", s.Name, in.Field, strconv.FormatFloat(in.Value, 'g', 6, 64), n, mark, unit)
				}
			}
		}
	}
	return nil
}

// levelUnit is epsUnit for a level: the card's price, per ADS for an ADR.
func levelUnit(info market.AssetInfo) string {
	if l := info.Listing; l != nil && strings.Contains(l.Underlying, " ADS") {
		return "a level is the card's price in US dollars per ADS (" + l.Underlying + "): multiply a home-listing price by the shares per ADS " +
			"and by usd.rate from get_stock_quote on the home listing, or use a US-dollar level a source states"
	}
	return "a level is the card's price in US dollars; convert a local price with get_stock_quote's usd.rate"
}

// lowPE is the P/E on today's price under which a near-term EPS (to two
// years out) reads as a local-currency or per-share figure more than a
// valuation, so the card and the model are told. TSMC's EPS in NT$ per share
// on its USD ADS card shows about 3× (its forward P/E near 20 ÷ 6.3: five
// shares per ADS at about 31.6 TWD per dollar), a 1-share HKD ADS ÷ 7.8, a
// 2-share one ÷ 3.9; an honest forward P/E under 4, a 25% earnings yield, is
// rare, and keeps its card.
const lowPE = 4

// lowPEs are the card's lines for each near-term EPS under lowPE.
func lowPEs(lv []market.ScenarioLevel, spot float64, thisYear int) []string {
	var out []string
	for _, s := range lv {
		for _, in := range s.Inputs {
			if in.Field != "eps" || in.Year == nil || *in.Year > thisYear+2 || spot/in.Value >= lowPE {
				continue
			}
			w := fmt.Sprintf("EPS $%s for %d puts today's price at %.1f× earnings, an unusually low P/E", strconv.FormatFloat(in.Value, 'g', 6, 64), *in.Year, spot/in.Value)
			if !slices.Contains(out, w) {
				out = append(out, w)
			}
		}
	}
	return out
}

// srcIDRe is a numbered-source basis: "[2]" (anything after it is ignored).
var srcIDRe = regexp.MustCompile(`^\[(\d+)\]`)

// verifyInputs checks every source-cited scenario input against what it cites:
// "[n]" against that numbered source's title, snippet and read-page excerpt,
// "quote" against the numbers in this message's get_stock_quote and
// get_asset_analysis results. A number the cited text does not state, or an
// unknown id, is relabelled "assumption" (never rejected, never shown as
// sourced), with a warning. A cited source must state the number in its
// field's form (market.NumberInField: a P/E as 25× or next to "P/E", a growth
// as a percentage; dates, years and counts never count).
func (a *agentRun) verifyInputs(sc []market.ScenarioLevel) (cards, hints []string) {
	for i := range sc {
		for j := range sc[i].Inputs {
			in := &sc[i].Inputs[j]
			if in.Kind != "source" {
				continue
			}
			id, ok, why := a.cited(in.Field, in.Value, in.Basis)
			if ok {
				in.Basis, in.Verified = id, true
				continue
			}
			cards = append(cards, unstated(in.Field, in.Value, id, why))
			hints = append(hints, fmt.Sprintf("%s %s cited %s %s: labelled assumption", inputName[in.Field], strconv.FormatFloat(in.Value, 'g', 6, 64), in.Basis, why))
			in.Basis, in.Kind = "assumption", "assumption"
		}
	}
	return cards, hints
}

var inputName = map[string]string{"eps": "eps", "growthPct": "growth_pct", "pe": "pe", "level": "level", "cagrPct": "cagr_pct"}

// unstated is the card's line for a cited input its source does not state,
// in words: "P/E 20× is not stated in source [1]: shown as an assumption".
func unstated(field string, value float64, id, why string) string {
	v := strconv.FormatFloat(value, 'g', 6, 64)
	words := map[string]string{"eps": "EPS $" + v, "growthPct": "EPS growth " + v + "%/yr", "pe": "P/E " + v + "×",
		"level": "level " + market.FormatLevel(value), "cagrPct": "CAGR " + v + "%/yr"}[field]
	if words == "" {
		words = v
	}
	switch {
	case strings.HasPrefix(why, "not found in this message's"):
		return words + " is not in this answer's quote data: shown as an assumption"
	case strings.HasPrefix(why, "not found in "):
		return words + " is not stated in source " + id + ": shown as an assumption"
	case strings.HasPrefix(why, "is not a source in"):
		return words + " cites " + id + ", which is not a source of this answer: shown as an assumption"
	}
	return words + " cites no numbered source: shown as an assumption"
}

// cited is the normalized id of basis and whether its text states value as
// field, or why not.
func (a *agentRun) cited(field string, value float64, basis string) (id string, ok bool, why string) {
	if strings.EqualFold(strings.TrimSpace(basis), "quote") {
		for _, n := range a.toolNums {
			if math.Abs(math.Abs(value)-math.Abs(n)) <= 0.005*math.Abs(n) {
				return "quote", true, ""
			}
		}
		return "quote", false, "not found in this message's get_stock_quote or get_asset_analysis results"
	}
	m := srcIDRe.FindStringSubmatch(strings.TrimSpace(basis))
	if m == nil {
		return basis, false, "is not a source id ([n] or quote)"
	}
	id = "[" + m[1] + "]"
	n, _ := strconv.Atoi(m[1])
	for _, it := range a.src.items {
		if it.N == n {
			if market.NumberInField(field, value, it.Title+"\n"+it.text) {
				return id, true, ""
			}
			return id, false, "not found in " + id
		}
	}
	return id, false, "is not a source in this message"
}

// recordNums keeps every number in a tool result (walking its JSON) for
// verifyInputs' "quote" basis. One message's worth, like src.
func (a *agentRun) recordNums(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	var doc any
	if json.Unmarshal(b, &doc) != nil {
		return
	}
	var walk func(any)
	walk = func(x any) {
		switch x := x.(type) {
		case float64:
			a.toolNums = append(a.toolNums, x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
}

// outlookSummary is the tool chip's line.
func outlookSummary(o market.PriceOutlook, days int) string {
	v := o.View
	var s string
	switch {
	case days == 0:
		s = o.Symbol + " by when"
	case v == nil || v.Kind == "none":
		s = fmt.Sprintf("%s in %s: 90%% range %s – %s", o.Symbol, horizonShort(days), market.FormatLevel(o.P5), market.FormatLevel(o.P95))
	default:
		s = fmt.Sprintf("%s in %s: median %s (view), %.0f%% above today", o.Symbol, horizonShort(days), market.FormatLevel(v.Median), v.PAbove*100)
	}
	if tt := o.TimeToTarget; tt != nil {
		if tt.MedianYears != nil {
			s += fmt.Sprintf(" · %s median about %s years", multipleWord(tt.Multiple), strconv.FormatFloat(*tt.MedianYears, 'f', -1, 64))
			if tt.MedianExtrapolated {
				s += " (extrapolated)"
			}
		} else {
			s += fmt.Sprintf(" · %s median beyond 30 years", multipleWord(tt.Multiple))
		}
	}
	return s
}

// horizonShort is "5y", "2.5y" or "90d".
func horizonShort(days int) string {
	if days < 365 {
		return strconv.Itoa(days) + "d"
	}
	y := float64(days) / 365.25
	if r := math.Round(y); math.Abs(y-r) < 0.05 {
		return strconv.Itoa(int(r)) + "y"
	}
	return strconv.FormatFloat(math.Round(y*10)/10, 'f', -1, 64) + "y"
}

// horizonWords is "5 years" or "90 days".
func horizonWords(days int) string {
	s := horizonShort(days)
	if strings.HasSuffix(s, "y") {
		return strings.TrimSuffix(s, "y") + map[bool]string{true: " year", false: " years"}[s == "1y"]
	}
	return strings.TrimSuffix(s, "d") + " days"
}

func multipleWord(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) + "×" }

func (a *agentRun) clock() time.Time {
	if !a.today.IsZero() {
		return a.today
	}
	return time.Now().UTC()
}
