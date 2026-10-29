package forecast

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

var (
	eventSlug  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`) // a Polymarket event slug
	shapes     = []string{"binary", "ordered", "categorical"}
	categories = []string{"markets", "crypto", "macro", "tech", "politics", "sports", "other"}
	yesNo      = []string{"Yes", "No"}
)

const (
	maxOutcome  = 120  // runes per outcome label
	maxCriteria = 2000 // runes of resolution criteria
	maxContext  = 4000 // runes of user context
)

const draftSystem = `You are the question editor of a forecasting desk. Turn the user's rough idea into one precise, resolvable forecasting question.

Today is %[1]s (UTC). If the idea gives no timeframe, resolve on %[2]s (the end of the current quarter). "End of the year" means %[3]d-12-31.

Pick the shape:
- "binary": a yes/no event or a single threshold ("above 120k", "will X happen"). outcomes must be exactly ["Yes","No"].
- "ordered": a numeric quantity with no single threshold ("where will BTC trade", "how many rate cuts"). 4-7 contiguous buckets, ordered low to high, covering the whole number line with no gaps or overlaps: "< A", "A – B", "B – C", ..., "≥ Z". Use round edges in the quantity's unit; when live data is given, centre the middle buckets on the current level. For a horizon beyond six months, space the edges as multiples of the current level (for example 0.5×, 0.75×, 1×, 1.5×, 2×, 3×), not in equal steps, and put one edge at the current level.
- "categorical": which of several named options wins or ranks first. 2-8 outcomes; add "Other" unless the named options are exhaustive.

Write:
- question: one sentence, 10-280 characters, naming the threshold or quantity with its unit, the source of truth and the date. Example: "Will the Binance BTC/USDT price be above $120,000 at 23:59 UTC on 2026-12-31?"
- resolutionCriteria: 2-4 sentences: the source of truth (exchange and pair, official statistic, company filings, league results...), the exact time and timezone, and edge cases (a value exactly on a boundary, the source unavailable or revised, the event cancelled or postponed).
- resolutionDate: YYYY-MM-DD, after today.
- category: one of markets, crypto, macro, tech, politics, sports, other.
- priceRule: only for a binary question about the Binance spot price of one universe asset (%[4]s): {"symbol": "...", "op": "above" | "below", "threshold": <number>, "mode": "on_date" | "by_date"} matching the question exactly (on_date = the daily close on the resolution date; by_date = any daily close up to it); otherwise null. Phrase such questions as daily closes, e.g. "Will the Binance SOL/USDT daily close on 2026-12-31 be above $130?"

Never state a current price or statistic unless it appears in the live data you are given. Do not answer the question.

Reply with only this JSON object:
{"shape": "binary" | "ordered" | "categorical", "question": "...", "outcomes": ["...", "..."], "resolutionCriteria": "...", "resolutionDate": "YYYY-MM-DD", "category": "...", "priceRule": {"symbol": "...", "op": "above" | "below", "threshold": <number>, "mode": "on_date" | "by_date"} | null}`

// draft asks the model for a draft question and repairs what it returns.
// Live spot prices of assets the idea mentions let ordered buckets sit around
// the current level; without market data the draft still works.
func (h *handler) draft(ctx context.Context, idea string, today time.Time) (Draft, error) {
	d, _, err := h.draftWith(ctx, idea, today)
	return d, err
}

// draftTimeout bounds the live price and history a draft reads for its subject (both cached).
const draftTimeout = 6 * time.Second

// draftWith is draft plus, with the methods on (round 9b), the idea's one
// subject: a listed stock's live price goes in the prompt, and a long-horizon
// ordered draft gets ranges cut at multiples of today's level (logBuckets),
// whose reference it returns (nil otherwise).
func (h *handler) draftWith(ctx context.Context, idea string, today time.Time) (Draft, *ViewRef, error) {
	prompt := "Idea: " + idea
	assets, aerr := h.d.Market.Assets(ctx)
	if aerr == nil {
		if spot := spotLines(research.QuestionSymbols(idea), assets, time.Now()); spot != "" {
			prompt += "\n\n" + spot
		}
	}
	var sub *subject
	var ref *ViewRef
	var ccy string
	deadline := time.Now().Add(draftTimeout)
	if h.d.ForecastMethods {
		if sub = subjectOf(Draft{Question: idea}); sub != nil {
			ref, ccy = h.draftSpot(ctx, sub, assets, deadline)
		}
		if ref != nil && sub.Class != market.Crypto {
			at, _ := time.Parse(time.RFC3339, ref.AsOf)
			prompt += fmt.Sprintf("\n\nLive price: %s (%s, %s) %s, regular-market price as of %s (tool data, safe to cite).", sub.Symbol,
				strings.TrimSuffix(strings.TrimPrefix(ref.Label, sub.Symbol+", "), " regular-market price"), strings.TrimSuffix(ref.Source, " (delayed)"),
				fmtPrice(ref.Value, Unit{Prefix: cmp.Or(currencyMark(ccy), ccy+" ")}), at.UTC().Format("2006-01-02 15:04 UTC"))
		}
	}
	system := fmt.Sprintf(draftSystem, today.Format(time.DateOnly), endOfQuarter(today).Format(time.DateOnly), today.Year(), market.CryptoSymbolList())
	var raw Draft
	if err := h.d.LLM.CompleteJSON(ctx, system, prompt, llm.Low, &raw); err != nil {
		return Draft{}, nil, err
	}
	d := repair(raw, idea, today)
	if ref == nil {
		return d, nil, nil
	}
	sctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if out, ok := h.logBuckets(sctx, d, idea, sub, ref, ccy, today); ok {
		d.Outcomes = out
		return d, ref, nil
	}
	return d, nil, nil
}

// draftSpot is the subject's live level for a draft: Binance spot for
// crypto (from assets), else the listing's Yahoo regular-market price, by
// deadline. nil without one.
func (h *handler) draftSpot(ctx context.Context, sub *subject, assets []market.Asset, deadline time.Time) (*ViewRef, string) {
	now := time.Now().UTC()
	if sub.Class == market.Crypto {
		for _, a := range assets {
			if a.Symbol == sub.Symbol && a.Price > 0 {
				return &ViewRef{Value: a.Price, Label: sub.Symbol + " spot", AsOf: now.Format(time.RFC3339), Source: "Binance spot"}, "USD"
			}
		}
		return nil, ""
	}
	if sub.Yahoo == "" {
		return nil, ""
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	q, err := h.d.YahooSource().Quote(ctx, sub.Yahoo)
	if err != nil || !(q.Price > 0) {
		slog.Info("forecast draft without a live quote", "symbol", sub.Symbol, "err", err)
		return nil, ""
	}
	return &ViewRef{Value: q.Price, Label: strings.TrimSpace(sub.Symbol + ", " + q.Meta.Exchange() + " regular-market price"),
		AsOf: q.Time.UTC().Format(time.RFC3339), Source: "Yahoo Finance (delayed)"}, q.Currency
}

// currencyMark is the label mark of an ISO currency ("" when the desk has none).
func currencyMark(ccy string) string {
	for _, p := range unitPrefixes {
		if p != "US$" && slices.Contains(currencyOf[p], ccy) && !(ccy == "CNY" && p == "¥") {
			return p
		}
	}
	return ""
}

// logBuckets replaces a long-horizon ordered draft's ranges with ones cut at
// multiples of today's level (market.LadderEdges on σ√T, σ the subject's
// 5-year volatility, else its 1-year, else for a ladder in US dollars its
// long outlook's), so a view
// shows as shape instead of even mass by construction. Not when the horizon
// is 180 days or less, the draft links a market, the model wrote its ranges
// in another unit or currency, or the user named the ranges (userEdges).
// Labels carry no percentages: they are the resolution keys.
func (h *handler) logBuckets(ctx context.Context, d Draft, idea string, sub *subject, ref *ViewRef, ccy string, today time.Time) ([]string, bool) {
	days := horizonOf(d, today)
	bounds, ok := bucketBounds(d.Outcomes)
	u := unitOf(d.Outcomes)
	if d.Shape != "ordered" || !ok || days <= longHorizonDays || d.LinkedMarket != nil || u.Suffix != "" ||
		!slices.Contains(currencyOf[u.Prefix], ccy) || !plausible(ref.Value, bounds) || userEdges(idea, bounds) {
		return nil, false
	}
	vol := 0.0
	// The ladder is in the listing's own currency (ccy, the labels' too: a ₩
	// draft on 005930.KS), so σ is its own closes', never the dollar series.
	if s, err := h.d.Market.DailyHistory(ctx, sub.Symbol); err == nil {
		st := market.ComputeHistoryStats(s, days)
		for _, k := range []string{"y5", "y1"} {
			if v := st.VolAnnPct[k]; v != nil && vol == 0 {
				vol = *v / 100
			}
		}
	}
	if vol == 0 && ccy == "USD" { // the long outlook's σ is US dollars' (venue marks, or the series in USD)
		if o, err := h.d.Market.LongOutlook(ctx, sub.Symbol, days); err == nil {
			vol = o.VolAnnPct / 100
		}
	}
	if !(vol > 0) {
		return nil, false
	}
	edges := market.LadderEdges(ref.Value, vol*math.Sqrt(float64(days)/365))
	out := make([]string, 0, len(edges)+1)
	out = append(out, "< "+labelLevel(edges[0], u.Prefix))
	for i := 1; i < len(edges); i++ {
		out = append(out, labelLevel(edges[i-1], u.Prefix)+" – "+labelLevel(edges[i], u.Prefix))
	}
	out = append(out, "≥ "+labelLevel(edges[len(edges)-1], u.Prefix))
	if b, ok := bucketBounds(out); !ok || len(b) != len(edges)+1 {
		return nil, false // never a label set that does not resolve
	}
	return out, true
}

// userEdges reports an idea that names the ranges itself: two or more of its
// amounts within 1% of the draft's edges ("under 400, 400-600 or above 600").
func userEdges(idea string, bounds [][2]float64) bool {
	xs, _ := amounts(strings.ToLower(idea))
	hit := 0
	for _, e := range edgesOf(bounds) {
		if slices.ContainsFunc(xs, func(x float64) bool { return math.Abs(x-e) <= 0.01*math.Abs(e) }) {
			hit++
		}
	}
	return hit >= 2
}

// labelLevel writes a ladder edge for a label: grouped digits, no trailing
// zeros ("$1,500", "$0.0615", "₩1,000,000"). A ladder edge has at most 3
// significant figures; the label is printed from x rounded exactly to 6, so
// float noise never reaches a resolution key ("₩999,999.9999999999").
func labelLevel(x float64, prefix string) string {
	s := strconv.FormatFloat(roundSig(x, 6), 'f', -1, 64)
	whole, frac, _ := strings.Cut(s, ".")
	if frac != "" {
		frac = "." + frac
	}
	return prefix + groupDigits(whole) + frac
}

// repair makes the model's draft valid wherever a default is safe: trimmed,
// deduplicated outcomes, binary forced to Yes/No, a future date, a known
// category. The user still reviews and edits it before running.
// ponytail: ordered buckets are not checked for contiguity; the prompt asks for it and the user edits.
func repair(d Draft, idea string, today time.Time) Draft {
	d.Question = truncate(strings.TrimSpace(d.Question), 280)
	if utf8.RuneCountInString(d.Question) < 10 {
		d.Question = truncate(idea, 280)
	}
	d.ResolutionCriteria = truncate(strings.TrimSpace(d.ResolutionCriteria), maxCriteria)
	if d.Category = strings.ToLower(strings.TrimSpace(d.Category)); !slices.Contains(categories, d.Category) {
		d.Category = "other"
	}

	var outcomes []string
	seen := map[string]bool{}
	for _, o := range d.Outcomes {
		o = truncate(strings.TrimSpace(o), maxOutcome)
		if k := key(o); o != "" && !seen[k] {
			seen[k] = true
			outcomes = append(outcomes, o)
		}
	}
	outcomes = outcomes[:min(len(outcomes), 8)]
	switch shape := strings.ToLower(strings.TrimSpace(d.Shape)); {
	case shape == "binary", len(outcomes) < 2, len(outcomes) == 2 && seen["yes"] && seen["no"]:
		d.Shape, d.Outcomes = "binary", slices.Clone(yesNo)
	case shape == "ordered":
		d.Shape, d.Outcomes = shape, outcomes
	default:
		d.Shape, d.Outcomes = "categorical", outcomes
	}

	if date, err := time.Parse(time.DateOnly, strings.TrimSpace(d.ResolutionDate)); err != nil || !date.After(today) {
		d.ResolutionDate = endOfQuarter(today).Format(time.DateOnly)
	} else {
		d.ResolutionDate = date.Format(time.DateOnly)
	}
	d.PriceRule = cleanRule(d.PriceRule, d.Shape)
	d.Context = ""       // the user's to add, never the model's
	d.LinkedMarket = nil // only a board draft links a market
	return d
}

// validate trims d in place and checks it against the contract.
func validate(d *Draft, today time.Time) error {
	bad := func(msg string) error { return &app.BadRequest{Msg: msg} }
	d.Question = strings.TrimSpace(d.Question)
	d.ResolutionCriteria = strings.TrimSpace(d.ResolutionCriteria)
	d.Category = cmp.Or(strings.TrimSpace(d.Category), "other")
	d.Context = strings.TrimSpace(d.Context)
	for i := range d.Outcomes {
		d.Outcomes[i] = strings.TrimSpace(d.Outcomes[i])
	}
	switch n := utf8.RuneCountInString(d.Question); {
	case !slices.Contains(shapes, d.Shape):
		return bad("shape must be binary, ordered or categorical.")
	case n < 10 || n > 280:
		return bad("question must be 10..280 characters.")
	case len(d.Outcomes) < 2 || len(d.Outcomes) > maxOutcomes:
		return bad(fmt.Sprintf("outcomes must have 2..%d entries.", maxOutcomes))
	case d.Shape == "binary" && !slices.Equal(d.Outcomes, yesNo):
		return bad(`binary outcomes must be exactly ["Yes","No"].`)
	case utf8.RuneCountInString(d.ResolutionCriteria) > criteriaLimit(d.LinkedMarket):
		return bad(fmt.Sprintf("resolutionCriteria must be at most %d characters.", criteriaLimit(d.LinkedMarket)))
	case utf8.RuneCountInString(d.Context) > maxContext:
		return bad("context must be at most 4,000 characters.")
	case !slices.Contains(categories, d.Category):
		return bad("category must be one of markets, crypto, macro, tech, politics, sports, other.")
	}
	seen := map[string]bool{}
	for _, o := range d.Outcomes {
		if o == "" || utf8.RuneCountInString(o) > maxOutcome {
			return bad(fmt.Sprintf("outcomes must be non-empty and at most %d characters.", maxOutcome))
		}
		if seen[key(o)] {
			return bad("outcomes must be unique.")
		}
		seen[key(o)] = true
	}
	if d.PriceRule != nil {
		if d.PriceRule = cleanRule(d.PriceRule, d.Shape); d.PriceRule == nil {
			return bad("priceRule is invalid.")
		}
	}
	if d.LinkedMarket != nil && !linkedPredicate(*d) {
		return bad("linkedMarket is invalid.")
	}
	date, err := time.Parse(time.DateOnly, strings.TrimSpace(d.ResolutionDate))
	if err != nil {
		return bad("resolutionDate must be a date (YYYY-MM-DD).")
	}
	if !date.After(today) {
		return bad("resolutionDate must be in the future.")
	}
	d.ResolutionDate = date.Format(time.DateOnly)
	return nil
}

// endOfQuarter is the last day of today's quarter, or of the next quarter
// when today is that last day (the date must be in the future).
func endOfQuarter(today time.Time) time.Time {
	endMonth := (int(today.Month())-1)/3*3 + 3
	end := time.Date(today.Year(), time.Month(endMonth+1), 0, 0, 0, 0, 0, time.UTC) // day 0 = last day of endMonth
	if !end.After(today) {
		end = time.Date(today.Year(), time.Month(endMonth+4), 0, 0, 0, 0, 0, time.UTC)
	}
	return end
}

// key folds an outcome label for matching and dedupe: models change case,
// spacing and dash style when they echo labels back.
func key(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(keyFold.Replace(s)), ""))
}

var keyFold = strings.NewReplacer("–", "-", "—", "-", "−", "-", "≥", ">=", "≤", "<=")

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}

func criteriaLimit(l *LinkedMarket) int {
	if l != nil {
		return maxLinkedCriteria
	}
	return maxCriteria
}
