package forecast

// Desk vs market by strike: an ordered price report next to
// Polymarket's daily "<asset> above ___ on <date>" ladder. The desk's
// P(close above each strike) comes from its buckets; each strike's market is
// priced like Desk vs market (edge, EV, lean). Comparison only.

import (
	"context"
	"log/slog"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

const maxLadder = 8 // strikes shown

// amount is a number with an optional scale word ("$80k", "$1.2T", "$950bn",
// "3 million"); any other word ("300 to 450") leaves it as written.
var amount = regexp.MustCompile(`\$?\s*(\d[\d,]*(?:\.\d+)?)\s*([a-zA-Z]+)?`)

var amountScale = map[string]float64{"k": 1e3, "m": 1e6, "mn": 1e6, "million": 1e6, "b": 1e9, "bn": 1e9, "billion": 1e9,
	"t": 1e12, "tn": 1e12, "trillion": 1e12}

// bucketBounds parses ordered outcomes ("< $80,000", "$80,000 – $90,000", …,
// "≥ $120k"; scale words as amount reads them, "$1.2T", and signed numbers,
// "< -1%") into [lo, hi) bounds: the first is (−Inf, A), the last [Z, +Inf).
// ok is false when a label does not parse, there are fewer than 3 buckets, or
// they are not contiguous (each hi within 0.01% of the next lo) and increasing.
func bucketBounds(outcomes []string) ([][2]float64, bool) {
	n := len(outcomes)
	if n < 3 {
		return nil, false
	}
	out := make([][2]float64, n)
	for i, o := range outcomes {
		l := strings.ToLower(strings.TrimSpace(o))
		xs, ok := amounts(l)
		if !ok {
			return nil, false
		}
		prefix := func(ps ...string) bool {
			return slices.ContainsFunc(ps, func(p string) bool { return strings.HasPrefix(l, p) })
		}
		switch {
		case i == 0 && len(xs) == 1 && prefix("<", "under", "below"):
			out[i] = [2]float64{math.Inf(-1), xs[0]}
		case i == n-1 && len(xs) == 1 && (prefix("≥", ">", "above", "over") || strings.HasSuffix(l, "+")):
			out[i] = [2]float64{xs[0], math.Inf(1)}
		case i > 0 && i < n-1 && len(xs) == 2 && xs[0] < xs[1]:
			out[i] = [2]float64{xs[0], xs[1]}
		default:
			return nil, false
		}
	}
	for i := 1; i < n; i++ {
		if hi, lo := out[i-1][1], out[i][0]; math.Abs(hi-lo) > 1e-4*math.Max(math.Abs(hi), math.Abs(lo)) {
			return nil, false
		}
	}
	return out, true
}

// amounts is every number in the lower-cased text l, scaled by its word
// ("$1.2t", "80k") and signed ("< -1%"). ok is false on a number that does
// not parse.
func amounts(l string) ([]float64, bool) {
	var xs []float64
	for _, m := range amount.FindAllStringSubmatchIndex(l, -1) {
		x, err := strconv.ParseFloat(strings.ReplaceAll(l[m[2]:m[3]], ",", ""), 64)
		if err != nil {
			return nil, false
		}
		if m[4] >= 0 {
			if s, ok := amountScale[l[m[4]:m[5]]]; ok {
				x *= s
			}
		}
		if negative(l[:m[2]]) {
			x = -x
		}
		xs = append(xs, x)
	}
	return xs, true
}

// parseAmount is the one number in text ("$611", "≈$1,480", "$1.2T").
func parseAmount(text string) (float64, bool) {
	xs, ok := amounts(strings.ToLower(strings.TrimSpace(text)))
	if !ok || len(xs) != 1 {
		return 0, false
	}
	return xs[0], true
}

// negative reports a minus sign right before a number ("-1%", "< −0.5%",
// "-$3"): a hyphen or minus at the label's start or after a space or a
// comparison, never the dash of a range ("2.5-3", "2.5 - 3").
func negative(before string) bool {
	before = strings.TrimSuffix(before, "$")
	r, size := utf8.DecodeLastRuneInString(before)
	if r != '-' && r != '−' {
		return false
	}
	prev, _ := utf8.DecodeLastRuneInString(before[:len(before)-size])
	return len(before) == size || unicode.IsSpace(prev) || strings.ContainsRune("<>≥≤=(", prev)
}

// deskAbove is the desk's P(close ≥ k): the final P of every bucket at or
// above k, plus the straddling bucket's P scaled linearly by the share of it
// above k. probs are matched to outcomes by label. Defined only for k between
// the first bucket's top and the last bucket's bottom (no open-ended tail is
// split); non-increasing in k.
func deskAbove(bounds [][2]float64, outcomes []string, probs []OutcomeP, k float64) (float64, bool) {
	n := len(bounds)
	if n == 0 || n != len(outcomes) || k < bounds[0][1] || k > bounds[n-1][0] {
		return 0, false
	}
	p := 0.0
	for i, b := range bounds {
		switch {
		case b[0] >= k:
			p += pOf(probs, outcomes[i])
		case b[1] > k:
			p += pOf(probs, outcomes[i]) * (b[1] - k) / (b[1] - b[0])
		}
	}
	return p, true
}

// ladder prices a ladder set's rows against the desk's buckets (edge, EV,
// lean as for one market) and returns them; rows the buckets cannot price
// drop out, and fewer than 2 left is no ladder. nil for any other set.
func (ms marketSet) ladder(outcomes []string, probs []OutcomeP, confidence string) []*MarketOdds {
	bounds, ok := bucketBounds(outcomes)
	if !ok || len(ms) == 0 || ms[0].Strike == 0 {
		return nil
	}
	var out []*MarketOdds
	for _, m := range ms {
		if p, ok := deskAbove(bounds, outcomes, probs, m.Strike); ok {
			m.price(p, confidence)
			out = append(out, m)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// findLadder finds the Polymarket strike ladder for an ordered question: the
// first universe asset the question names (not PAXG), its "above" markets
// ending within a day of the resolution date whose strikes fall inside the
// buckets' finite edges, from the first lookup step that gives 2+ strikes.
// Unpriced; nil when there is none.
func (h *handler) findLadder(ctx context.Context, d Draft) (marketSet, error) {
	bounds, ok := bucketBounds(d.Outcomes)
	date, err := time.Parse(time.DateOnly, d.ResolutionDate)
	named := research.QuestionSymbols(d.Question)
	i := slices.IndexFunc(market.CryptoUniverse, func(a market.AssetInfo) bool { return slices.Contains(named, a.Symbol) })
	if !ok || err != nil || i < 0 || market.CryptoUniverse[i].Symbol == "PAXG" {
		return nil, nil
	}
	name := polyName(market.CryptoUniverse[i])
	var all []candidate
	for step := range aboveSteps {
		cs, err := h.aboveCandidates(ctx, step, name, date)
		if err != nil {
			return nil, err
		}
		all = append(all, cs...)
		if rows := ladderRows(all, name, date, bounds[0][1], bounds[len(bounds)-1][0]); len(rows) >= 2 {
			return rows, nil
		}
	}
	slog.Debug("polymarket ladder miss", "question", d.Question, "candidates", firstQuestions(all, 5))
	return nil, nil
}

// ladderRows keeps the usable strikes among cands: an "above" market on the
// asset, not intraday, ending within a day of date, with a strike in [lo, hi].
// They come from one event (POST /market re-reads it by slug): the first
// ending on date, else the first found. One market per strike (the busier),
// ascending; over maxLadder, every ⌈n/8⌉-th, keeping the lowest and highest.
func ladderRows(cands []candidate, name string, date time.Time, lo, hi float64) marketSet {
	usable := slices.DeleteFunc(slices.Clone(cands), func(c candidate) bool {
		k, q := strike(c.question), strings.ToLower(c.question)
		return !strings.Contains(q, "above") || !strings.Contains(q, strings.ToLower(name)) || intraday(c) ||
			math.Abs(days(c.end, date)) > 1 || k <= 0 || k < lo || k > hi
	})
	if len(usable) == 0 {
		return nil
	}
	event := usable[max(0, slices.IndexFunc(usable, func(c candidate) bool { return days(c.end, date) == 0 }))].event
	best := map[float64]candidate{}
	for _, c := range usable {
		if k := strike(c.question); c.event == event && (best[k].question == "" || volume(c) > volume(best[k])) {
			best[k] = c
		}
	}
	ks := slices.Sorted(maps.Keys(best))
	if n := len(ks); n > maxLadder {
		var kept []float64
		for i := 0; i < n; i += (n + maxLadder - 1) / maxLadder {
			kept = append(kept, ks[i])
		}
		kept[len(kept)-1] = ks[n-1]
		ks = kept
	}
	rows := make(marketSet, len(ks))
	for i, k := range ks {
		c, match := best[k], "close"
		if days(c.end, date) == 0 {
			match = "exact"
		}
		rows[i] = odds(c, match)
		rows[i].Strike = k
	}
	return rows
}
