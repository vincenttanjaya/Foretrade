package forecast

// The numeric frames' method tools: history_stats (measured
// history and its rolling windows, for the base rate), scenario_model (EPS ×
// P/E or level scenarios priced as a variance-matched mixture, for the
// quant) and stress_mix (priced regime and tail events on the measured base,
// for the skeptic). Each specialist's last successful call of each tool is
// the server's record of what its method gave, whatever its reasoning says:
// the page, the pool and the scenarios read the record.

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
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

const (
	numericMaxTurns = 6
	toolCallTimeout = 15 * time.Second
)

// toolLimits are the calls each specialist may make of a method tool.
var toolLimits = map[string]int{"history_stats": 4, "scenario_model": 3, "stress_mix": 2}

var errToolLimit = errors.New("limit reached; answer from what you have")

// deskKit is what a numeric run's tools share: the frame, the subject's
// measurement and the texts an input's basis is verified against.
type deskKit struct {
	h        *handler
	d        Draft
	fr       frame
	m        *measurement
	sources  []Source
	findings []Finding
	logs     map[string]*toolLog // per role; numeric frames only
}

// seat is role r's system prompt and tools in the kit's frame: an event
// frame's quant keeps today's crypto tools (eventTools), every other event
// member answers in one call; a numeric frame's member gets its method's
// tools (toolsFor), recorded in its log.
func (k *deskKit) seat(r role, eventTools []llm.Tool, r1 bool) seat {
	st := seat{system: specialistPrompt(r, k.fr, r1, k.m)}
	if !k.fr.numeric() {
		if r.ID == "quant" {
			st.tools, st.turns, st.data = eventTools, 4, true
		}
		return st
	}
	log := k.logs[r.ID]
	if log == nil {
		log = newToolLog()
		k.logs[r.ID] = log
	}
	st.tools, st.turns, log.tool = k.toolsFor(r, log), numericMaxTurns, methodTool(r.ID, k.fr)
	st.data = k.fr.Subject != nil && k.fr.Subject.Class == market.Crypto // Binance tools and history; a listing's are Yahoo's
	return st
}

// toolLog is one specialist's tool record, under a mutex (an agent's tool
// calls may run concurrently).
type toolLog struct {
	mu      sync.Mutex
	tool    string         // the tool its method rests on (methodTool)
	calls   map[string]int // every call, for the limits
	ok      map[string]int
	failed  map[string]int
	hist    []float64            // the last history_stats blend on the question's ranges
	scen    *scenarioRecord      // the last scenario_model
	stress  *stressRecord        // the last stress_mix
	outlook *market.PriceOutlook // the last price_outlook
	symbols []string             // history_stats symbols that answered, in order
}

type scenarioRecord struct {
	levels  []market.ScenarioLevel
	buckets []float64 // on the question's ranges
	width   float64   // the mixture's 80% log width
	ref     float64   // the level the scenarios were valued against
}

type stressRecord struct {
	events     []stressEvent
	buckets    []float64
	baseMedian float64
}

func newToolLog() *toolLog {
	return &toolLog{calls: map[string]int{}, ok: map[string]int{}, failed: map[string]int{}}
}

// begin counts a call against its limit.
func (l *toolLog) begin(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := toolLimits[name]; n > 0 && l.calls[name] >= n {
		return errToolLimit
	}
	l.calls[name]++
	return nil
}

// end records a call's outcome; rec runs under the lock on success.
func (l *toolLog) end(name string, err error, rec func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.failed[name]++
		return
	}
	l.ok[name]++
	if rec != nil {
		rec()
	}
}

// tracked wraps a tool's Run with the limit, the per-call timeout and the record.
func (l *toolLog) tracked(t llm.Tool) llm.Tool {
	run := t.Run
	t.Run = func(ctx context.Context, input json.RawMessage) (string, error) {
		if err := l.begin(t.Name); err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(ctx, toolCallTimeout)
		defer cancel()
		out, err := run(ctx, input)
		if err != nil { // a successful run records itself inside run, under end
			l.end(t.Name, err, nil)
		}
		return out, err
	}
	return t
}

// defaultTool is a role's method tool for a log no seat set (methodTool).
var defaultTool = map[string]string{"base_rate": "history_stats", "quant": "scenario_model", "skeptic": "stress_mix"}

// method is what the server saw role use: its tool's method when a call
// succeeded (a short-horizon stock's quant: history_stats' lognormal block,
// a volatility model), fallback when it called its method's tool and every
// call failed, judgment when it never did. hasH1: the base rate had measured
// history.
func (l *toolLog) method(role string, hasH1 bool) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	tool := cmp.Or(l.tool, defaultTool[role])
	switch {
	case role == "base_rate" && (hasH1 || l.ok["history_stats"] > 0):
		return "history"
	case role == "quant" && l.ok["scenario_model"] > 0:
		return "fundamentals"
	case role == "quant" && (l.ok["price_outlook"] > 0 || tool == "history_stats" && l.ok["history_stats"] > 0):
		return "volatility"
	case role == "skeptic" && l.ok["stress_mix"] > 0:
		return "regime"
	case l.failed[tool] > 0 && l.ok[tool] == 0:
		return "fallback"
	}
	return "judgment"
}

// family is the input family of a member with method (inputFamily): H1's
// history for history, regime and a volatility member whose σ came from
// history_stats; price_outlook's own for the other volatility members.
func (l *toolLog) family(method string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case method == "history" || method == "regime":
		return "history"
	case method == "volatility" && l.ok["price_outlook"] == 0 && l.ok["history_stats"] > 0:
		return "history"
	}
	return method
}

// verifyInput labels a scenario or stress input's basis (market.ClassifyBasis)
// and, for a cited id, checks that the cited text states the value as field
// (market.NumberInField: a P/E as 25× or next to "P/E", a growth as a
// percentage; dates, years and counts never count): a web finding's claim
// (F), a source's title, snippet and read text (S, with deep research's full
// page), or the H1 block. Any id that does not state it, or an unknown id, is
// relabelled "assumption" (never rejected, never shown as sourced); why says
// so. The user's own materials are unverified: they never verify a number.
func (k *deskKit) verifyInput(field string, value float64, basis string) (in market.Input, why string) {
	label, kind := market.ClassifyBasis(basis)
	in = market.Input{Value: value, Basis: label, Kind: kind}
	if kind != "source" {
		return in, ""
	}
	ids := idToken.FindAllString(strings.ToUpper(basis), -1)
	for _, id := range ids {
		if text, ok := k.citable(id); ok && market.NumberInField(field, value, text) {
			in.Basis, in.Verified = id, true
			return in, ""
		}
	}
	in.Basis, in.Kind = "assumption", "assumption"
	switch {
	case len(ids) == 0:
		why = "is not a finding, source or H1 id"
	case !slices.ContainsFunc(ids, func(id string) bool { _, ok := k.citable(id); return ok }):
		why = "is not an id of this run's research"
	default:
		why = "does not state it"
	}
	return in, why
}

// citable is the text an id stands for in this run.
func (k *deskKit) citable(id string) (string, bool) {
	switch {
	case id == "H1":
		if k.m != nil && k.m.h1 {
			return h1Facts(k.m.text), true
		}
	case strings.HasPrefix(id, "F"):
		if i := slices.IndexFunc(k.findings, func(f Finding) bool { return f.ID == id && f.Origin != "material" }); i >= 0 {
			return k.findings[i].Claim, true
		}
	case strings.HasPrefix(id, "S"):
		if i := slices.IndexFunc(k.sources, func(s Source) bool { return s.ID == id }); i >= 0 {
			s := k.sources[i]
			return s.Title + "\n" + s.text + "\n" + s.page, true
		}
	}
	return "", false
}

// stamp is a date or a time in the H1 block ("2026-10-05", "13:30 UTC").
var stamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}|\d{2}:\d{2} UTC`)

// h1Facts is the H1 block a cited number is checked against: its measured
// facts only, without the line that restates the question's own ranges and
// the shares on them (a level at a range edge, or a P/E of 13.5 against
// "13.5%", is not a measurement) and without its dates and times ("13:30
// UTC" states no P/E of 13, "1997-10-01" no growth of -10%).
func h1Facts(text string) string {
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "- On this question's ranges") {
			keep = append(keep, line)
		}
	}
	return stamp.ReplaceAllString(strings.Join(keep, "\n"), "")
}

// known reports whether id names something this run's prompt held.
func (k *deskKit) known(id string) bool {
	_, ok := k.citable(id)
	return ok
}

// historySymbols are what history_stats may read: the subject, every universe
// symbol and the benchmark allowlist (never an arbitrary ticker).
func (k *deskKit) historySymbols() []string {
	var out []string
	if s := k.fr.Subject; s != nil {
		out = append(out, s.Symbol)
	}
	for _, a := range market.Universe {
		out = append(out, a.Symbol)
	}
	for key := range market.Benchmarks {
		out = append(out, key)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// series is a history_stats symbol's daily history, in the currency of the
// level its windows map onto and of the thresholds (the question's): H1's own
// series for the subject, else dailySeries, so a dollar question reads a
// peer's or the subject's non-USD listing, and a benchmark index, in dollars,
// never its local closes. A series still in another currency than the
// question's (a dollar listing in a ₩ question: there is no conversion to
// won) gives its statistics, but its windows are never mapped onto the
// question's level.
func (k *deskKit) series(ctx context.Context, symbol string) (market.DailySeries, error) {
	if s := k.m; s != nil && s.h1 && s.sub.Symbol == symbol {
		return s.series, nil
	}
	return k.h.dailySeries(ctx, symbol, inUSD(k.d))
}

type levelInput struct {
	Value float64 `json:"value"`
	Basis string  `json:"basis"`
}

// level resolves a tool's level: a cited figure that its source states, else
// the subject's measured price.
func (k *deskKit) level(in *levelInput) (float64, error) {
	if in != nil {
		if !(in.Value > 0) {
			return 0, fmt.Errorf("level must be positive")
		}
		if v, why := k.verifyInput("level", in.Value, in.Basis); !v.Verified {
			return 0, fmt.Errorf("level must cite a source that states it: %s %s", strings.TrimSpace(in.Basis), why)
		}
		return in.Value, nil
	}
	if k.fr.Measure == "price" && k.m.spot() > 0 {
		return k.m.spot(), nil
	}
	return 0, fmt.Errorf("no level to apply the windows to: pass level with the source id that states it")
}

type rangeP struct {
	From *float64 `json:"from"`
	To   *float64 `json:"to"`
	P    float64  `json:"p"`
}

func ranges(edges []float64, ps []float64) []rangeP {
	out := make([]rangeP, len(ps))
	for i := range ps {
		r := rangeP{P: ps[i]}
		if i > 0 {
			r.From = &edges[i-1]
		}
		if i < len(edges) {
			r.To = &edges[i]
		}
		out[i] = r
	}
	return out
}

type outcomeRange struct {
	Outcome string  `json:"outcome"`
	P       float64 `json:"p"`
}

func onOutcomes(outcomes []string, ps []float64) []outcomeRange {
	out := make([]outcomeRange, len(ps))
	for i := range ps {
		out[i] = outcomeRange{outcomes[i], ps[i]}
	}
	return out
}

// sameEdges reports thresholds equal to the question's finite edges.
func sameEdges(xs, edges []float64) bool {
	if len(xs) != len(edges) {
		return false
	}
	for i := range xs {
		if math.Abs(xs[i]-edges[i]) > 1e-6*math.Max(1, math.Abs(edges[i])) {
			return false
		}
	}
	return true
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func cleanThresholds(xs []float64) ([]float64, error) {
	if len(xs) > 10 {
		return nil, fmt.Errorf("at most 10 thresholds")
	}
	out := slices.DeleteFunc(slices.Clone(xs), func(x float64) bool { return math.IsNaN(x) || math.IsInf(x, 0) })
	slices.Sort(out)
	return slices.Compact(out), nil
}

// historyTool is history_stats.
func (k *deskKit) historyTool(log *toolLog) llm.Tool {
	return log.tracked(llm.Tool{
		Name:        "history_stats",
		Description: "Measured daily price history for the question's subject, a universe symbol or a benchmark (SPX, NDX, SOX, TWII, N225, HSI, KOSPI, STI, JKSE, UST10Y): realized volatility (1y, 5y, full), trailing returns and CAGRs, the largest drawdowns with dates and recovery, and every rolling window of horizon_days in its history (ending-multiple quantiles, share higher / doubled / halved, how many windows are independent). With thresholds it maps each window's multiple onto today's level of the subject (or level, when the question measures something other than this price) and returns the share of windows ending in each range: the measured base rate (when the windows' median grew faster than the capped drift, every multiple is first scaled down to it, so a survivor's past return never sets the centre). Price returns exclude dividends. At most 4 calls.",
		Schema: map[string]any{"type": "object", "required": []string{"symbol", "horizon_days"}, "properties": map[string]any{
			"symbol":       map[string]any{"type": "string", "enum": k.historySymbols()},
			"horizon_days": map[string]any{"type": "integer", "minimum": 1, "maximum": market.MaxOutlookDays, "description": "Days until the resolution date."},
			"thresholds": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "maxItems": 10,
				"description": "The question's range edges in its unit, e.g. every bucket edge."},
			"level": map[string]any{"type": "object", "required": []string{"value", "basis"}, "properties": map[string]any{
				"value": map[string]any{"type": "number"}, "basis": map[string]any{"type": "string", "description": "The F or S id that states it."}},
				"description": "Today's level of what the question measures, when it is not the subject's price (a market cap)."},
		}},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Symbol      string      `json:"symbol"`
				HorizonDays int         `json:"horizon_days"`
				Thresholds  []float64   `json:"thresholds"`
				Level       *levelInput `json:"level"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", err
			}
			in.Symbol = strings.ToUpper(strings.TrimSpace(in.Symbol))
			if !slices.Contains(k.historySymbols(), in.Symbol) {
				return "", fmt.Errorf("unknown symbol %q: the subject, a universe symbol or a benchmark key", in.Symbol)
			}
			if in.HorizonDays < 1 || in.HorizonDays > market.MaxOutlookDays {
				return "", fmt.Errorf("horizon_days must be 1..%d", market.MaxOutlookDays)
			}
			xs, err := cleanThresholds(in.Thresholds)
			if err != nil {
				return "", err
			}
			s, err := k.series(ctx, in.Symbol)
			if err != nil {
				return "", fmt.Errorf("history unavailable for %s: %v", in.Symbol, err)
			}
			hs := market.ComputeHistoryStats(s, in.HorizonDays)
			hs.DriftHalvedPct = nil // uncapped: the desk's one drift is the capped one (the lognormal block's)
			out := map[string]any{"history": hs}
			var shares, logn []float64
			if want := currencyOf[unitOf(k.d.Outcomes).Prefix]; len(xs) > 0 && len(want) > 0 && !slices.Contains(want, s.Currency) {
				return "", fmt.Errorf("%s's history is in %s, not this question's %s: its windows cannot map onto the question's level (call it without thresholds for its statistics alone)",
					in.Symbol, cmp.Or(s.Currency, "an unknown currency"), strings.Join(want, " or "))
			}
			if len(xs) > 0 {
				level, err := k.level(in.Level)
				if err != nil {
					return "", err
				}
				bounds := boundsOf(xs)
				years, scale := float64(in.HorizonDays)/365, 1.0
				var mu float64
				var c market.DriftCap
				if s.Kind != "level" { // a level's windows are differences, not multiples
					var note string
					mu, c, note = k.h.cappedDrift(ctx, s, in.Symbol)
					scale = windowScale(hs.Window, mu, years)
					if note != "" {
						out["driftCapNote"] = note
					}
				}
				out["level"] = level
				if hs.Window != nil { // else too short: no windows to scale, so no shares at all
					var indep int
					shares, indep = market.WindowShares(s, in.HorizonDays, level*scale, bounds)
					out["independent"], out["buckets"] = indep, ranges(xs, shares)
				}
				if scale < 1 {
					out["windowScale"] = scale
					out["windowNote"] = fmt.Sprintf("each window's multiple is scaled ×%.2f before it is mapped onto the ranges: the windows' median (×%.2f) grew faster than the capped drift (an assumption), so the shares keep the history's spread, not its own return",
						scale, hs.Window.Q[3])
				}
				if vol := s.VolAnn(); vol > 0 && s.Kind != "level" {
					logn = market.DriftMixture(level, years, vol, mu).Buckets(xs)
					out["lognormal"] = map[string]any{"label": "reference only: lognormal at the full-history volatility with the historical drift, halved and capped (an assumption)",
						"volAnnPct": vol * 100, "driftAnnPct": (math.Exp(mu) - 1) * 100, "driftCapPct": c.CapPct, "buckets": ranges(xs, logn)}
				}
				switch indep, _ := out["independent"].(int); {
				case hs.Window == nil:
					out["note"] = fmt.Sprintf("no window shares: %s's history (%.1f years) is too short for %s-day windows, which need the horizon plus a year of history",
						in.Symbol, s.Years(), groupDigits(strconv.Itoa(in.HorizonDays)))
				case indep < 2:
					out["note"] = "fewer than 2 independent windows of this length: the shares are anecdotes"
				}
			}
			log.end("history_stats", nil, func() {
				log.symbols = append(log.symbols, in.Symbol)
				if edges := edgesOf(k.fr.Bounds); len(xs) > 0 && sameEdges(xs, edges) && (shares != nil || logn != nil) {
					w := 0.0
					if st, _ := out["independent"].(int); st >= 2 {
						w = min(0.5, float64(st)/10)
					}
					blend := slices.Clone(shares)
					switch {
					case shares == nil: // too short for windows: the lognormal alone
						blend = slices.Clone(logn)
					case logn != nil:
						for i := range blend {
							blend[i] = w*shares[i] + (1-w)*logn[i]
						}
					}
					floorRescale(blend)
					log.hist = blend
				}
			})
			return toJSON(out), nil
		},
	})
}

// boundsOf is the ranges thresholds cut the line into, open at both ends.
func boundsOf(xs []float64) [][2]float64 {
	out := make([][2]float64, len(xs)+1)
	for i := range out {
		lo, hi := math.Inf(-1), math.Inf(1)
		if i > 0 {
			lo = xs[i-1]
		}
		if i < len(xs) {
			hi = xs[i]
		}
		out[i] = [2]float64{lo, hi}
	}
	return out
}

// scenarioTool is scenario_model.
func (k *deskKit) scenarioTool(log *toolLog) llm.Tool {
	item := map[string]any{"type": "object", "required": []string{"name", "kind", "p"}, "properties": map[string]any{
		"name": map[string]any{"type": "string", "maxLength": 60}, "kind": map[string]any{"type": "string", "enum": []string{"tail", "bear", "base", "bull"}},
		"p":   map[string]any{"type": "number"},
		"eps": map[string]any{"type": "number"}, "eps_year": map[string]any{"type": "integer"}, "eps_basis": map[string]any{"type": "string"},
		"growth_pct": map[string]any{"type": "number"}, "exit_year": map[string]any{"type": "integer"}, "growth_basis": map[string]any{"type": "string"},
		"pe": map[string]any{"type": "number"}, "pe_basis": map[string]any{"type": "string"},
		"level": map[string]any{"type": "number"}, "level_basis": map[string]any{"type": "string"},
	}}
	props := map[string]any{"scenarios": map[string]any{"type": "array", "minItems": 2, "maxItems": 5, "items": item}}
	if k.fr.Measure == "market_cap" {
		props["level"] = map[string]any{"type": "object", "required": []string{"value", "basis"}, "properties": map[string]any{
			"value": map[string]any{"type": "number"}, "basis": map[string]any{"type": "string"}},
			"description": "Today's market cap, with the F or S id that states it: the scenarios' levels are market caps."}
	}
	return log.tracked(llm.Tool{
		Name:        "scenario_model",
		Description: "Turns 2-5 explicit scenarios into a price distribution at the resolution date. EPS mode: forward EPS of the exit fiscal year = eps × (1 + growth_pct/100)^(exit_year − eps_year), price = that EPS × pe. Level mode: a direct level. Every input needs a basis: a finding or source id that states it (F3, S4, H1), 'model knowledge, as of YYYY-MM', or 'assumption'; a number the cited text does not contain is relabelled 'assumption'. Returns each scenario's price, its change vs today and implied annual return, the mixture of the scenarios (spread so that its total dispersion matches H1's measured volatility) over the question's ranges, its median, 10th/90th percentiles and P(above today), and a reverse check: the EPS growth to the exit year today's price implies at each scenario's P/E. At most 3 calls.",
		Schema:      map[string]any{"type": "object", "required": []string{"scenarios"}, "properties": props},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Scenarios []market.ScenarioInput `json:"scenarios"`
				Level     *levelInput            `json:"level"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", err
			}
			spot := k.m.spot()
			if k.fr.Measure == "market_cap" {
				if in.Level == nil {
					return "", fmt.Errorf("pass level: today's market cap with the F or S id that states it")
				}
				var err error
				if spot, err = k.level(in.Level); err != nil {
					return "", err
				}
			}
			if !(spot > 0) {
				return "", fmt.Errorf("no measured price for the subject this run: describe your scenarios in words and lower your confidence")
			}
			if slices.ContainsFunc(in.Scenarios, func(s market.ScenarioInput) bool { return s.CAGRPct != nil }) {
				return "", fmt.Errorf("cagr_pct is not a mode here: give each scenario EPS × P/E (eps, growth_pct, pe) or a level, each with its basis")
			}
			if k.fr.Measure == "market_cap" && slices.ContainsFunc(in.Scenarios, func(s market.ScenarioInput) bool { return s.Level == nil }) {
				return "", fmt.Errorf("this question measures market capitalization: give each scenario's market cap at the resolution date in level mode (level and level_basis); EPS × P/E is a price per share")
			}
			date, _ := time.Parse(time.DateOnly, k.d.ResolutionDate)
			years := float64(k.fr.HorizonDays) / 365
			lv, warns, err := market.ValidateScenariosIn(in.Scenarios, spot, years, date.Year(), k.m.mark(k.d))
			if err != nil {
				return "", err
			}
			for i := range lv {
				for j := range lv[i].Inputs {
					p := &lv[i].Inputs[j]
					if p.Kind != "source" {
						continue
					}
					v, why := k.verifyInput(p.Field, p.Value, p.Basis)
					if !v.Verified {
						warns = append(warns, fmt.Sprintf("%s: %s %s cited %s %s: labelled assumption", lv[i].Name, p.Field, strconv.FormatFloat(p.Value, 'g', 6, 64), p.Basis, why))
					}
					p.Basis, p.Kind, p.Verified = v.Basis, v.Kind, v.Verified
				}
			}
			vol, volBasis := 0.0, "assumed 30%/yr volatility (no measured history)"
			if k.m != nil && k.m.vol > 0 {
				vol, volBasis = k.m.vol, fmt.Sprintf("H1 full-history %.1f%%", k.m.vol*100)
			}
			mix := market.NewMixture(spot, years, vol, lv)
			buckets := mix.Buckets(edgesOf(k.fr.Bounds))
			type scen struct {
				Name      string         `json:"name"`
				Kind      string         `json:"kind"`
				P         float64        `json:"p"`
				Price     float64        `json:"price"`
				ChangePct float64        `json:"changePct"`
				AnnualPct *float64       `json:"annualPct"`
				Inputs    []market.Input `json:"inputs"`
			}
			type reverse struct {
				Name             string   `json:"name"`
				ImpliedGrowthPct *float64 `json:"impliedGrowthPct"` // null outside EPS mode
			}
			var scens []scen
			var rev []reverse
			for _, s := range lv {
				sc := scen{Name: s.Name, Kind: s.Kind, P: s.P, Price: s.Level, ChangePct: (s.Level/spot - 1) * 100, Inputs: s.Inputs}
				if years >= 1 {
					sc.AnnualPct = ptr((math.Pow(s.Level/spot, 1/years) - 1) * 100)
				}
				scens = append(scens, sc)
				rev = append(rev, reverse{s.Name, impliedGrowth(s, spot)})
			}
			p10, p90 := mix.Quantile(0.10), mix.Quantile(0.90)
			out := map[string]any{"scenarios": scens, "buckets": onOutcomes(k.d.Outcomes, buckets), "median": mix.Quantile(0.5), "p10": p10, "p90": p90,
				"pAbove": 1 - mix.CDF(spot), "sigmaPath": mix.SigmaPath, "volBasis": volBasis, "reverse": rev, "warnings": orEmpty(warns)}
			log.end("scenario_model", nil, func() {
				log.scen = &scenarioRecord{levels: lv, buckets: buckets, width: math.Log(p90 / p10), ref: spot}
			})
			return toJSON(out), nil
		},
	})
}

// impliedGrowth is the annual EPS growth to the exit year at which today's
// price equals the scenario's EPS × P/E: nil outside EPS mode.
func impliedGrowth(s market.ScenarioLevel, spot float64) *float64 {
	var eps, pe float64
	var from, to int
	for _, in := range s.Inputs {
		switch in.Field {
		case "eps":
			eps = in.Value
			if in.Year != nil {
				from = *in.Year
			}
		case "growthPct":
			if in.Year != nil {
				to = *in.Year
			}
		case "pe":
			pe = in.Value
		}
	}
	if !(eps > 0 && pe > 0) || to <= from {
		return nil
	}
	return ptr((math.Pow(spot/(eps*pe), 1/float64(to-from)) - 1) * 100)
}

// lnMix is a mixture of lognormals in x: weights, log means and log sds.
type lnMix struct{ w, m, s []float64 }

func (l lnMix) cdf(x float64) float64 {
	if !(x > 0) {
		return 0
	}
	p := 0.0
	for i := range l.w {
		p += l.w[i] * 0.5 * math.Erfc(-(math.Log(x)-l.m[i])/l.s[i]/math.Sqrt2)
	}
	return p
}

func (l lnMix) quantile(q float64) float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	for i := range l.m {
		lo, hi = min(lo, l.m[i]-12*l.s[i]), max(hi, l.m[i]+12*l.s[i])
	}
	for range 80 {
		mid := (lo + hi) / 2
		if l.cdf(math.Exp(mid)) < q {
			lo = mid
		} else {
			hi = mid
		}
	}
	return math.Exp((lo + hi) / 2)
}

func (l lnMix) buckets(edges []float64) []float64 {
	out := make([]float64, len(edges)+1)
	prev := 0.0
	for i, e := range edges {
		c := l.cdf(e)
		out[i], prev = c-prev, c
	}
	out[len(edges)] = 1 - prev
	return out
}

// stressBase is stress_mix's base: H1's lognormal (full-history σ, the
// capped drift) at the subject's price, or for crypto without H1 the long
// outlook's σ at zero drift. ok false without one.
func (k *deskKit) stressBase(ctx context.Context) (spot, logMean, logSD float64, ok bool) {
	years := float64(k.fr.HorizonDays) / 365
	if m := k.m; m.spot() > 0 && m.vol > 0 && k.fr.Measure == "price" {
		return m.spot(), math.Log(m.spot()) + m.mu*years, m.vol * math.Sqrt(years), true
	}
	if s := k.fr.Subject; s != nil && s.Class == market.Crypto && k.fr.Measure == "price" {
		if o, err := k.h.d.Market.LongOutlook(ctx, s.Symbol, k.fr.HorizonDays); err == nil && o.Spot > 0 {
			return o.Spot, math.Log(o.Spot), max(o.VolAnnPct/100, 0.01) * math.Sqrt(years), true
		}
	}
	return 0, 0, 0, false
}

// stressTool is stress_mix.
func (k *deskKit) stressTool(log *toolLog) llm.Tool {
	ev := map[string]any{"type": "object", "required": []string{"name", "p", "move_pct", "p_basis", "move_basis"}, "properties": map[string]any{
		"name": map[string]any{"type": "string", "maxLength": 60}, "p": map[string]any{"type": "number", "minimum": 0.01, "maximum": 0.30},
		"move_pct": map[string]any{"type": "number", "minimum": -95, "maximum": 300}, "p_basis": map[string]any{"type": "string"}, "move_basis": map[string]any{"type": "string"}}}
	return log.tracked(llm.Tool{
		Name:        "stress_mix",
		Description: "Prices 1-4 regime or tail events (each with its probability over the whole horizon, 0.01-0.30, summing to at most 0.40, and its price move in %, -95..300) on the measured base: H1's lognormal at the full-history volatility and the capped historical drift (an assumption). With probability 1 − Σp the base; with each event's p the base moved by the event. Returns the mixture over the question's ranges, its median and P(at or above today), and each event's median. Each p and move needs a basis: an F or S id that states it, 'model knowledge, as of YYYY-MM' or 'assumption'. At most 2 calls.",
		Schema: map[string]any{"type": "object", "required": []string{"events"}, "properties": map[string]any{
			"events": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": ev}}},
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Events []struct {
					Name      string  `json:"name"`
					P         float64 `json:"p"`
					MovePct   float64 `json:"move_pct"`
					PBasis    string  `json:"p_basis"`
					MoveBasis string  `json:"move_basis"`
				} `json:"events"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", err
			}
			if len(in.Events) < 1 || len(in.Events) > 4 {
				return "", fmt.Errorf("give 1 to 4 events")
			}
			spot, m0, sd, ok := k.stressBase(ctx)
			if !ok {
				return "", fmt.Errorf("no base distribution; describe the tails in words")
			}
			mix := lnMix{w: []float64{1}, m: []float64{m0}, s: []float64{sd}}
			var events []stressEvent
			var warns []string
			sum := 0.0
			for i, e := range in.Events {
				name := strings.TrimSpace(e.Name)
				if name == "" {
					return "", fmt.Errorf("event %d needs a name", i+1)
				}
				if utf8.RuneCountInString(name) > 60 {
					name = string([]rune(name)[:60])
				}
				if !(e.P >= 0.01 && e.P <= 0.30) {
					return "", fmt.Errorf("event %q: p must be 0.01..0.30", name)
				}
				if !(e.MovePct >= -95 && e.MovePct <= 300) {
					return "", fmt.Errorf("event %q: move_pct must be -95..300", name)
				}
				if strings.TrimSpace(e.PBasis) == "" || strings.TrimSpace(e.MoveBasis) == "" {
					return "", fmt.Errorf("event %q: each p and move needs a basis: an F or S id, 'model knowledge, as of YYYY-MM' or 'assumption'", name)
				}
				sum += e.P
				pIn, why := k.verifyInput("eventP", e.P, e.PBasis)
				if !pIn.Verified && why != "" {
					warns = append(warns, fmt.Sprintf("%s: p %g cited %s %s: labelled assumption", name, e.P, e.PBasis, why))
				}
				mIn, why := k.verifyInput("movePct", e.MovePct, e.MoveBasis)
				if !mIn.Verified && why != "" {
					warns = append(warns, fmt.Sprintf("%s: move_pct %g cited %s %s: labelled assumption", name, e.MovePct, e.MoveBasis, why))
				}
				pIn.Field, mIn.Field = "eventP", "movePct"
				events = append(events, stressEvent{Name: name, P: e.P, Move: e.MovePct / 100, Inputs: []market.Input{pIn, mIn}})
				mix.w = append(mix.w, e.P)
				mix.m = append(mix.m, m0+math.Log1p(e.MovePct/100))
				mix.s = append(mix.s, sd)
			}
			if sum > 0.40+1e-9 {
				return "", fmt.Errorf("the events' probabilities sum to %.2f: at most 0.40", sum)
			}
			mix.w[0] = 1 - sum
			buckets := mix.buckets(edgesOf(k.fr.Bounds))
			type per struct {
				Name      string  `json:"name"`
				P         float64 `json:"p"`
				MovePct   float64 `json:"movePct"`
				Median    float64 `json:"median"`
				BelowSpot bool    `json:"moveBelowSpot"`
			}
			var pers []per
			for i, e := range events {
				med := math.Exp(mix.m[i+1])
				pers = append(pers, per{e.Name, e.P, e.Move * 100, med, med < spot})
			}
			var inputs []market.Input
			for _, e := range events {
				inputs = append(inputs, e.Inputs...)
			}
			out := map[string]any{"buckets": onOutcomes(k.d.Outcomes, buckets), "median": mix.quantile(0.5), "pAbove": 1 - mix.cdf(spot),
				"baseMedian": math.Exp(m0), "perEvent": pers, "inputs": inputs, "warnings": orEmpty(warns)}
			log.end("stress_mix", nil, func() {
				log.stress = &stressRecord{events: events, buckets: buckets, baseMedian: math.Exp(m0)}
			})
			return toJSON(out), nil
		},
	})
}

// longOutlookTool is the quant's price_outlook in numeric frames: LongOutlook
// up to market.MaxOutlookDays. Its longHorizon.driftCase is dropped: that
// drift is the uncapped halved CAGR, and the desk shows no drift past a year
// but the capped one (H1, history_stats).
func (k *deskKit) longOutlookTool(log *toolLog) llm.Tool {
	t := k.h.quantTools(nil)[0]
	t.Description = "Zero-drift lognormal price distribution for one crypto asset at a horizon up to 20 years: the live spot, with volatility from the last 90 daily closes up to a year and from the longest daily history beyond. Returns the live spot, 7 price buckets with probabilities, p5/p50/p95, past a year the odds of 0.5x/2x/5x/10x, and for the thresholds you pass, P(price below) at each threshold plus the probability of every range between consecutive thresholds: the neutral volatility-only baseline, not a forecast of direction."
	schema := t.Schema["properties"].(map[string]any)
	schema["horizon_days"] = map[string]any{"type": "integer", "minimum": 1, "maximum": market.MaxOutlookDays, "description": "Days until the resolution date."}
	t.Run = func(ctx context.Context, input json.RawMessage) (string, error) {
		var in struct {
			Symbol      string    `json:"symbol"`
			HorizonDays int       `json:"horizon_days"`
			Thresholds  []float64 `json:"thresholds"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return "", err
		}
		if _, ok := market.LookupCrypto(in.Symbol); !ok {
			return "", fmt.Errorf("forecast market tools require a crypto symbol")
		}
		o, err := k.h.d.Market.LongOutlook(ctx, in.Symbol, in.HorizonDays)
		if err != nil {
			return "", err
		}
		if o.LongHorizon != nil {
			o.LongHorizon.DriftCase = nil
		}
		out := thresholdView(o, in.Thresholds)
		if in.HorizonDays > o.HorizonDays {
			out["note"] = fmt.Sprintf("horizon clamped to %d days", o.HorizonDays)
		}
		log.end("price_outlook", nil, func() {
			if s := k.fr.Subject; s != nil && s.Symbol == o.Symbol {
				log.outlook = &o
			}
		})
		return toJSON(out), nil
	}
	return log.tracked(t)
}

// outlookAnchor is a price_outlook record's distribution on the question's
// ranges (its own σ and horizon, zero drift).
func outlookAnchor(o *market.PriceOutlook, fr frame) []float64 {
	if o == nil {
		return nil
	}
	edges := edgesOf(fr.Bounds)
	out := make([]float64, len(edges)+1)
	prev := 0.0
	for i, e := range edges {
		c := 0.0
		if e > 0 {
			c = below(*o, e)
		}
		out[i], prev = c-prev, c
	}
	out[len(edges)] = 1 - prev
	floorRescale(out)
	return out
}

// toolsFor is role's tools in fr (nil: none, so it answers in one call).
// Event frames keep today's: the quant's crypto tools only.
func (k *deskKit) toolsFor(r role, log *toolLog) []llm.Tool {
	fr := k.fr
	switch {
	case fr.Kind == "event", fr.Kind == "level":
		return nil
	case r.ID == "base_rate":
		return []llm.Tool{k.historyTool(log)}
	case r.ID == "quant" && fr.Plan == "long_stock":
		return []llm.Tool{k.scenarioTool(log), k.historyTool(log)}
	case r.ID == "quant" && fr.Subject.Class == market.Crypto:
		tools := k.h.quantTools(nil)
		tools[0] = k.longOutlookTool(log)
		if fr.Kind == "price_long" {
			tools = append(tools, k.historyTool(log))
		}
		return tools
	case r.ID == "quant":
		return []llm.Tool{k.historyTool(log)}
	case r.ID == "skeptic" && fr.Kind == "price_long":
		return []llm.Tool{k.stressTool(log)}
	}
	return nil
}

// methodTool is the tool role's method rests on in fr: the one a retried
// member is told to call.
func methodTool(role string, fr frame) string {
	switch {
	case role == "base_rate":
		return "history_stats"
	case role == "quant" && fr.Plan == "long_stock":
		return "scenario_model"
	case role == "quant" && fr.Subject != nil && fr.Subject.Class != market.Crypto:
		return "history_stats" // a short-horizon stock or index: its lognormal block
	case role == "quant":
		return "price_outlook"
	}
	return "stress_mix"
}

// anchorOf is the distribution role's method gave, on the question's
// ranges: history the H1 blend (else its last history_stats on the question's
// edges), fundamentals the last scenario_model, volatility the last
// price_outlook (a short-horizon stock's quant has none: its history_stats
// reads the base rate's series, so it is no independent anchor), regime the
// last stress_mix; nil otherwise.
func anchorOf(method string, log *toolLog, m *measurement, fr frame, outcomes []string) []OutcomeP {
	log.mu.Lock()
	defer log.mu.Unlock()
	var p []float64
	switch method {
	case "history":
		p = log.hist
		if m != nil && m.blend != nil {
			p = m.blend
		}
	case "fundamentals":
		if log.scen != nil {
			p = log.scen.buckets
		}
	case "volatility":
		p = outlookAnchor(log.outlook, fr)
	case "regime":
		if log.stress != nil {
			p = log.stress.buckets
		}
	}
	if len(p) != len(outcomes) {
		return nil
	}
	out := make([]OutcomeP, len(p))
	for i := range p {
		out[i] = OutcomeP{outcomes[i], p[i]}
	}
	return out
}
