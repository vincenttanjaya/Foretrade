package forecast

// The historical replay: the same panel re-run as of past dates with only the
// Binance daily bars and dated news published before each date, on a fixed
// set of two-week crypto threshold questions. Its resolved rows give the desk
// a Brier score from day one (cmd/forecast-replay generates replay_seed.json).

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

var (
	replayAssets = []string{"BTC", "ETH", "SOL", "BNB", "XRP", "DOGE", "ADA", "LINK"}
	replayDates  = []string{"2026-07-15", "2026-08-12", "2026-09-09"}
	replayK      = []float64{0.94, 0.98, 1.02, 1.06} // threshold = spot × k
)

const (
	replayHorizon = 14 // days from asOf to the resolution date
	newsWindow    = 14 // days of news before asOf
)

// ReplayQuestion is one question of the replay set, as of AsOf (00:00 UTC).
type ReplayQuestion struct {
	Key        string // replay_key "<SYM>-<asOf>-<k>"
	AsOf       time.Time
	Draft      Draft   // binary, with its PriceRule
	AnchorP    float64 // P(Yes) under the as-of 14-day lognormal outlook
	Outcome    *string // decided from the Binance closes; nil while undecidable
	ResolvedAt *time.Time
}

// ReplayQuestions builds the deterministic replay set: every replay asset ×
// replay date, threshold = the last daily close before asOf × k rounded to
// 3 significant figures, outcome decided from the closes by now. Dates within
// 14 days of the model's knowledge cutoff are refused.
func ReplayQuestions(ctx context.Context, m *market.Client, cutoff, now time.Time) ([]ReplayQuestion, error) {
	var out []ReplayQuestion
	for a, sym := range replayAssets {
		for j, day := range replayDates {
			asOf, _ := time.Parse(time.DateOnly, day)
			if err := afterCutoff(asOf, cutoff); err != nil {
				return nil, err
			}
			bars, err := m.CandlesBefore(ctx, sym, "1d", asOf, 90)
			if err != nil {
				return nil, err
			}
			if len(bars) < 3 || bars[len(bars)-1].T != asOf.AddDate(0, 0, -1).Unix() {
				return nil, fmt.Errorf("replay: no %s daily bar for the day before %s", sym, day)
			}
			closes := market.Closes(bars)
			spot, k := closes[len(closes)-1], replayK[(a*len(replayDates)+j)%len(replayK)]
			x := roundSig(spot*k, 3)
			res := asOf.AddDate(0, 0, replayHorizon)
			resDate := res.Format(time.DateOnly)
			rule := PriceRule{Symbol: sym, Op: "above", Threshold: x, Mode: "on_date"}
			q := ReplayQuestion{
				Key:  fmt.Sprintf("%s-%s-%v", sym, day, k),
				AsOf: asOf,
				Draft: Draft{Shape: "binary", Outcomes: []string{"Yes", "No"}, ResolutionDate: resDate, Category: "crypto", PriceRule: &rule,
					Question: fmt.Sprintf("Will the Binance %s/USDT daily close on %s be above $%s?", sym, resDate, money(x)),
					ResolutionCriteria: fmt.Sprintf("Resolves Yes if the Binance %sUSDT 1d candle that opens at 00:00 UTC on %s closes above $%s; otherwise No.",
						sym, resDate, money(x))},
				AnchorP: 1 - below(market.OutlookFrom(sym, spot, market.RealizedVolAnn(closes), replayHorizon), x),
			}
			after, err := m.CandlesBefore(ctx, sym, "1d", res.AddDate(0, 0, 1), replayHorizon+2)
			if err != nil {
				return nil, err
			}
			if o, at, ok := decide(rule, after, asOf, res, now); ok {
				q.Outcome, q.ResolvedAt = &o, &at
			}
			out = append(out, q)
		}
	}
	return out, nil
}

// Replay runs the panel on q as of q.AsOf. It needs d.Market, d.LLM and
// d.Research (a nil pool is fine: replays never read pages).
func Replay(ctx context.Context, d *app.Deps, q ReplayQuestion) (Result, error) {
	if err := afterCutoff(q.AsOf, d.LLM.KnowledgeCutoff()); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, quickRunTimeout)
	defer cancel()
	return (&handler{d}).forecast(ctx, q.Draft, q.AsOf, nil, nil, nil) // no row to trace, no materials
}

// afterCutoff refuses replay dates the model may have seen in training.
func afterCutoff(asOf, cutoff time.Time) error {
	if asOf.Before(cutoff.AddDate(0, 0, newsWindow)) {
		return fmt.Errorf("replay date %s is within 14 days of the model's knowledge cutoff %s", asOf.Format(time.DateOnly), cutoff.Format(time.DateOnly))
	}
	return nil
}

// replayIndex is i = assetIdx*3 + dateIdx, the row's place in the replay set.
func replayIndex(symbol, asOf string) int {
	return max(slices.Index(replayAssets, symbol), 0)*len(replayDates) + max(slices.Index(replayDates, asOf), 0)
}

// history is a replay run's Binance daily bars before asOf, fetched once per
// symbol (CandlesBefore is not cached).
type history struct {
	m    *market.Client
	asOf time.Time

	mu   sync.Mutex // ponytail: held across the fetch; a run asks for one or two symbols
	bars map[string][]market.Candle
}

func (hs *history) get(ctx context.Context, symbol string) (string, []market.Candle, error) {
	info, ok := market.LookupCrypto(symbol)
	if !ok {
		return "", nil, fmt.Errorf("unknown symbol %q", symbol)
	}
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if b, ok := hs.bars[info.Symbol]; ok {
		return info.Symbol, b, nil
	}
	b, err := hs.m.CandlesBefore(ctx, info.Symbol, "1d", hs.asOf, 300)
	if err != nil {
		return "", nil, err
	}
	if len(b) < 3 {
		return "", nil, fmt.Errorf("no %s daily history before %s", info.Symbol, hs.asOf.Format(time.DateOnly))
	}
	hs.bars[info.Symbol] = b
	return info.Symbol, b, nil
}

// outlook is market.Outlook as of asOf: spot = the last close before it, σ
// from the 90 closes before it.
func (hs *history) outlook(ctx context.Context, symbol string, horizonDays int) (market.PriceOutlook, error) {
	sym, bars, err := hs.get(ctx, symbol)
	if err != nil {
		return market.PriceOutlook{}, err
	}
	closes := market.Closes(bars)
	return market.OutlookFrom(sym, closes[len(closes)-1], market.RealizedVolAnn(closes[max(len(closes)-90, 0):]),
		min(max(horizonDays, 1), 365)), nil
}

// analyze is market.Analyze on the daily bars before asOf (the only interval a replay offers).
func (hs *history) analyze(ctx context.Context, symbol, _ string) (market.Analysis, error) {
	sym, bars, err := hs.get(ctx, symbol)
	if err != nil {
		return market.Analysis{}, err
	}
	return market.AnalyzeCandles(sym, "1d", bars, market.Closes(bars)), nil
}

// spotLines is the replay's spot section: the last daily close before asOf
// of every universe asset among symbols.
func (hs *history) spotLines(ctx context.Context, symbols []string) string {
	var b strings.Builder
	for _, a := range market.CryptoUniverse {
		if !slices.Contains(symbols, a.Symbol) {
			continue
		}
		_, bars, err := hs.get(ctx, a.Symbol)
		if err != nil {
			slog.Warn("replay without daily history", "symbol", a.Symbol, "err", err)
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): $%s\n", a.Symbol, a.Name, strconv.FormatFloat(bars[len(bars)-1].C, 'f', -1, 64))
	}
	if b.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("Binance daily close on %s (the last close before simulated today, %s; tool data, safe to cite):\n",
		hs.asOf.AddDate(0, 0, -1).Format(time.DateOnly), hs.asOf.Format(time.DateOnly)) + b.String()
}

// asOfResearch is the replay's research stage: the same query writer with
// today = asOf, then Brave News only (count 6 per query) over the 14 days
// before asOf, keeping results dated before asOf 00:00 UTC whose title and
// snippet name no day on or after it. No web search and no page reads: a
// live page may carry later edits.
func (h *handler) asOfResearch(ctx context.Context, d Draft, asOf time.Time) (Research, []Source) {
	if !h.d.Research.Enabled() {
		return Research{Queries: []string{}, Status: "replay", Note: noteReplay}, []Source{}
	}
	ctx, cancel := context.WithTimeout(ctx, researchTimeout)
	defer cancel()
	var out struct {
		Queries []string `json:"queries"`
	}
	if err := h.d.LLM.CompleteJSON(ctx, queriesSystem, brief(d, asOf, ""), llm.Low, &out); err != nil {
		slog.Warn("replay research queries failed; searching the question", "err", err)
	}
	queries := searchQueries(out.Queries, d.Question)
	window := asOf.AddDate(0, 0, -newsWindow).Format(time.DateOnly) + "to" + asOf.AddDate(0, 0, -1).Format(time.DateOnly)
	lists := make([][]research.Result, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Go(func() {
			res, err := h.d.Research.SearchNews(ctx, q, window, 6)
			if err != nil {
				slog.Warn("replay research search failed", "err", err)
				return
			}
			lists[i] = slices.DeleteFunc(res, func(r research.Result) bool {
				return r.PublishedAt == nil || !r.PublishedAt.Before(asOf) || datedAfter(r.Title+" "+r.Description, asOf)
			})
		})
	}
	wg.Wait()
	return Research{Queries: queries, Status: "replay", Note: noteReplay}, pack(lists, asOf)
}

// dateInText finds day-precise English dates: "2 October 2026", "Oct. 2, 2026", "2026-10-02".
var dateInText = regexp.MustCompile(`(?i)\b(?:(\d{1,2})(?:st|nd|rd|th)?\s+(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?,?\s+(20\d\d)|` +
	`(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(20\d\d)|(20\d\d-\d\d-\d\d))\b`)

// datedAfter reports whether text names a day on or after day: an evergreen
// page ("Why BTC Is Up – 02 October 2026") keeps an old publish date while its
// text moves on.
// ponytail: English day-precise dates only; a bare month may be a target ("XRP to $27 by October 2026").
func datedAfter(text string, day time.Time) bool {
	for _, m := range dateInText.FindAllStringSubmatch(text, -1) {
		s, layout := m[7], time.DateOnly
		switch {
		case m[3] != "":
			s, layout = m[3]+" "+m[2]+" "+m[1], "2006 Jan 2"
		case m[6] != "":
			s, layout = m[6]+" "+m[4]+" "+m[5], "2006 Jan 2"
		}
		if t, err := time.Parse(layout, s); err == nil && !t.Before(day) {
			return true
		}
	}
	return false
}

// roundSig rounds x > 0 to figs significant figures, exactly for whole numbers.
func roundSig(x float64, figs int) float64 {
	if !(x > 0) {
		return x
	}
	e := int(math.Floor(math.Log10(x))) - figs + 1
	if e >= 0 {
		p := math.Pow(10, float64(e))
		return math.Round(x/p) * p
	}
	p := math.Pow(10, float64(-e))
	return math.Round(x*p) / p
}

// money formats a threshold for a question: 118,000 · 3,720 · 152 · 0.215.
func money(x float64) string {
	if x < 1000 {
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	s := strconv.FormatFloat(math.Round(x), 'f', 0, 64)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
