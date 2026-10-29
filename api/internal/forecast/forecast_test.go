package forecast

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func ps(pairs ...any) []OutcomeP {
	var out []OutcomeP
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, OutcomeP{pairs[i].(string), pairs[i+1].(float64)})
	}
	return out
}

func TestNormalize(t *testing.T) {
	buckets := []string{"< $100k", "$100k – $120k", "≥ $120k"}
	// Loose label matching (case, spacing, dash style).
	got, ok := normalize(buckets, ps("< $100K", 0.2, "$100k - $120k", 0.5, ">= $120k", 0.3), false)
	if !ok || !near(got[0].P, 0.2) || !near(got[1].P, 0.5) || !near(got[2].P, 0.3) || got[1].Outcome != buckets[1] {
		t.Fatalf("matching: %v %v", got, ok)
	}
	// Complete percentages are accepted; explicit zeros receive epsilon.
	got, _ = normalize(buckets, ps("< $100k", 60.0, "$100k – $120k", 40.0, "≥ $120k", 0.0), false)
	sum := got[0].P + got[1].P + got[2].P
	if !near(sum, 1) || !near(got[2].P, epsilon/(1+epsilon)) || !near(got[0].P, 0.6/(1+epsilon)) {
		t.Fatalf("epsilon/percent: %v", got)
	}
	if _, ok := normalize(buckets, ps("Yes", 0.7), false); ok {
		t.Fatal("nothing matched must not be ok")
	}
}

func TestSanitize(t *testing.T) {
	d := Draft{Shape: "binary", Outcomes: []string{"Yes", "No"}}
	ensemble := ps("Yes", 0.4, "No", 0.6)
	res := sanitize(Result{
		Probabilities: ps("yes", 1.0, "no", 0.0),
		Drivers:       []Driver{{"ETF flows", "UP", "huge"}, {"", "up", "low"}, {"Macro", "sideways", "low"}},
		Evidence:      []Evidence{{Claim: " Spot is 110k ", Stance: "Supports"}, {Claim: ""}},
		Flags:         []Flag{{"low information", "thin evidence"}, {"vibes", "x"}, {"stale", ""}},
		Confidence:    "very high",
	}, d, ensemble, nil, nil, nil, nil)
	if !near(res.Probabilities[0].P, 0.99) || !near(res.Probabilities[1].P, 0.01) {
		t.Fatalf("binary clamp: %v", res.Probabilities)
	}
	if len(res.Drivers) != 1 || res.Drivers[0] != (Driver{"ETF flows", "up", "medium"}) {
		t.Fatalf("drivers: %+v", res.Drivers)
	}
	if len(res.Evidence) != 1 || res.Evidence[0].Stance != "supports" || res.Evidence[0].Claim != "Spot is 110k" || res.Evidence[0].AsOf == "" {
		t.Fatalf("evidence: %+v", res.Evidence)
	}
	if len(res.Flags) != 1 || res.Flags[0].Type != "low_information" || res.Confidence != "medium" || res.Summary == "" {
		t.Fatalf("flags/confidence/summary: %+v %q %q", res.Flags, res.Confidence, res.Summary)
	}
	if res.WhatWouldChange == nil {
		t.Fatal("slices must encode as [] not null")
	}
	// No usable probabilities: fall back to the ensemble (still clamped for binary).
	if res := sanitize(Result{Probabilities: ps("Maybe", 1.0)}, d, ensemble, nil, nil, nil, nil); !near(res.Probabilities[0].P, 0.4) {
		t.Fatalf("ensemble fallback: %v", res.Probabilities)
	}
}

// M1 maps to the market shown to the panel, and only when one was shown.
func TestSanitizeMapsM1(t *testing.T) {
	d := Draft{Shape: "binary", Outcomes: yesNo}
	raw := Result{Probabilities: ps("Yes", 0.4, "No", 0.6), ChangeNote: " " + strings.Repeat("x", 450),
		Evidence: []Evidence{{Claim: "The crowd says 55%", SourceID: "[m1]", Source: "Poly", AsOf: "2026-10-01"}}}
	m := &MarketOdds{URL: "https://polymarket.com/event/x"}
	res := sanitize(raw, d, ps("Yes", 0.4, "No", 0.6), nil, nil, nil, m)
	if e := res.Evidence[0]; e.Source != "Polymarket" || e.URL == nil || *e.URL != m.URL || e.AsOf != "live" || e.SourceID != "" || len(res.ChangeNote) != 400 {
		t.Fatalf("M1: %+v, change note %d chars", e, len(res.ChangeNote))
	}
	if e := sanitize(raw, d, ps("Yes", 0.4, "No", 0.6), nil, nil, nil, nil).Evidence[0]; e.Source != "Poly" || e.URL != nil || e.AsOf != "2026-10-01" {
		t.Fatalf("M1 without a market: %+v", e)
	}
	if res := sanitize(Result{Probabilities: ps("Maybe", 1.0), ChangeNote: "moved"}, d, ps("Yes", 0.4, "No", 0.6), nil, nil, nil, m); res.ChangeNote != "" {
		t.Fatalf("the ensemble fallback drops the change note: %+v", res)
	}
}

func TestNormalizeRejectsIncompleteAndInvalidDistributions(t *testing.T) {
	for name, got := range map[string][]OutcomeP{
		"negative value":       ps("Yes", 1.2, "No", -0.2),
		"non-finite value":     ps("Yes", 0.2, "No", math.Inf(1)),
		"non-finite only side": ps("Yes", math.NaN()),
		"invalid total":        ps("Yes", 0.2, "No", 0.2),
	} {
		t.Run(name, func(t *testing.T) {
			if result, ok := normalize([]string{"Yes", "No"}, got, true); ok {
				t.Fatalf("malformed distribution accepted: %+v", result)
			}
		})
	}
}

func TestSanitizeDisclosesInvalidAggregatorFallback(t *testing.T) {
	d := Draft{Shape: "binary", Outcomes: []string{"Yes", "No"}}
	res := sanitize(Result{
		Probabilities: ps("Yes", 0.2, "No", 0.2),
		Summary:       "The aggregator says Yes has a 20% chance.",
		Confidence:    "high",
	}, d, ps("Yes", 0.4, "No", 0.6), nil, nil, nil, nil)
	if !near(res.Probabilities[0].P, 0.4) || res.Confidence != "low" || strings.Contains(res.Summary, "20%") {
		t.Fatalf("fallback must retain the ensemble and discard unsupported confidence/summary: %+v", res)
	}
	if !slices.ContainsFunc(res.Flags, func(f Flag) bool { return f.Type == "low_information" && strings.Contains(f.Note, "ensemble") }) {
		t.Fatalf("fallback is not disclosed: %+v", res.Flags)
	}
}

// A reply that names an outcome twice (the review's Yes:20%, No:80%, Yes:95%)
// or names one the question does not have contradicts itself: the aggregator
// distribution is invalid and the ensemble is shown, rather than keeping the
// first copy or silently dropping the extra label. Matching stays loose.
func TestSanitizeRejectsDuplicateAndUnexpectedLabels(t *testing.T) {
	binary := Draft{Shape: "binary", Outcomes: []string{"Yes", "No"}}
	ordered := Draft{Shape: "ordered", Outcomes: []string{"< $100k", "$100k – $120k", "≥ $120k"}}
	yesNo, buckets := ps("Yes", 0.4, "No", 0.6), ps("< $100k", 0.3, "$100k – $120k", 0.4, "≥ $120k", 0.3)
	for _, c := range []struct {
		name     string
		d        Draft
		got      []OutcomeP
		ensemble []OutcomeP
		want     float64 // first outcome's probability
	}{
		{"duplicate Yes", binary, ps("Yes", 20.0, "No", 80.0, "Yes", 95.0), yesNo, 0.4},
		{"duplicate whose last copy sums", binary, ps("Yes", 0.95, "No", 0.2, "Yes", 0.8), yesNo, 0.4},
		{"duplicate after case and spacing", binary, ps("Yes", 0.2, "No", 0.8, " YES ", 0.95), yesNo, 0.4},
		{"unexpected label", binary, ps("Yes", 0.2, "No", 0.8, "Maybe", 0.0), yesNo, 0.4},
		{"duplicate bucket by dash style", ordered, ps("< $100k", 0.2, "$100k-$120k", 0.5, "$100k – $120k", 0.9, "≥ $120k", 0.3), buckets, 0.3},
		{"unlisted bucket", ordered, ps("< $100k", 0.2, "$100k – $120k", 0.5, "≥ $120k", 0.3, "Other", 0.0), buckets, 0.3},
		{"case and spacing variants", binary, ps(" yes ", 0.2, "NO", 0.8), yesNo, 0.2},
		{"bucket spelling variants", ordered, ps("< $100K", 0.2, "$100k-$120k", 0.5, ">= $120k", 0.3), buckets, 0.2},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := sanitize(Result{Probabilities: c.got, Summary: "From the aggregator."}, c.d, c.ensemble, nil, nil, nil, nil)
			fellBack := slices.ContainsFunc(res.Flags, func(f Flag) bool { return f.Type == "low_information" })
			if !near(res.Probabilities[0].P, c.want) || fellBack != (c.want != c.got[0].P) {
				t.Fatalf("%v → %+v", c.got, res)
			}
		})
	}
}

// A binary answer that gives only P(Yes), or only P(No), implies the other.
func TestSanitizeCompletesABinaryComplement(t *testing.T) {
	d := Draft{Shape: "binary", Outcomes: []string{"Yes", "No"}}
	ensemble := ps("Yes", 0.4, "No", 0.6)
	for _, c := range []struct {
		got  []OutcomeP
		want float64 // P(Yes)
	}{
		{ps("Yes", 0.2), 0.2},
		{ps(" no ", 70.0), 0.3}, // percent
		{ps("Yes", 1.0), 0.99},  // clamped
		{ps("Yes", 1.2), 0.4},   // no valid complement: ensemble
		{ps("No", -0.1), 0.4},
		{ps("Yes", 0.2, "Yes", 0.2), 0.4}, // still a duplicate
	} {
		res := sanitize(Result{Probabilities: c.got, Summary: "From the aggregator."}, d, ensemble, nil, nil, nil, nil)
		if !near(res.Probabilities[0].P, c.want) || !near(res.Probabilities[0].P+res.Probabilities[1].P, 1) ||
			(c.want != 0.4) != (res.Summary == "From the aggregator.") {
			t.Errorf("%v → %+v", c.got, res)
		}
	}
	// Only a binary question implies the other side: two categories are not a complement.
	two := Draft{Shape: "categorical", Outcomes: []string{"Lakers", "Celtics"}}
	if res := sanitize(Result{Probabilities: ps("Lakers", 0.3)}, two, ps("Lakers", 0.6, "Celtics", 0.4), nil, nil, nil, nil); !near(res.Probabilities[0].P, 0.6) {
		t.Errorf("categorical answer completed: %+v", res.Probabilities)
	}
}

func TestSanitizeDoesNotReuseClaimsForARejectedTopOutcome(t *testing.T) {
	d := Draft{Shape: "categorical", Outcomes: []string{"A", "B", "C"}}
	res := sanitize(Result{
		Probabilities:   ps("A", 0.7),
		Drivers:         []Driver{{Factor: "A launches first", Direction: "up", Weight: "high"}},
		Evidence:        []Evidence{{Claim: "A is close to launching", Stance: "supports"}},
		WhatWouldChange: []string{"A postpones its launch"},
	}, d, ps("A", 0.2, "B", 0.6, "C", 0.2), nil, nil, nil, nil)
	if !near(res.Probabilities[1].P, 0.6) || len(res.Drivers) != 0 || len(res.Evidence) != 0 || len(res.WhatWouldChange) != 0 {
		t.Fatalf("fallback to B retained outcome-relative claims about A: %+v", res)
	}
}

func TestEnsembleAndConflict(t *testing.T) {
	outcomes := []string{"Yes", "No"}
	spec := func(name string, yes float64) Specialist {
		return Specialist{Name: name, Probabilities: ps("Yes", yes, "No", 1-yes)}
	}
	panel := []Specialist{spec("A", 0.40), spec("B", 0.66), spec("C", 0.50)}
	if m := mean(outcomes, panel); !near(m[0].P, 0.52) || !near(m[0].P+m[1].P, 1) {
		t.Fatalf("mean: %v", m)
	}
	if note := conflict(outcomes, panel); !strings.Contains(note, "26 points") || !strings.Contains(note, "A 40% vs B 66%") {
		t.Fatalf("26-point spread must conflict: %q", note)
	}
	if note := conflict(outcomes, []Specialist{spec("A", 0.40), spec("B", 0.64)}); note != "" {
		t.Fatalf("24-point spread must not conflict: %q", note)
	}
}

func TestHeadline(t *testing.T) {
	if h := headline("binary", ps("Yes", 0.3, "No", 0.7)); h.Outcome != "Yes" || h.P != 0.3 {
		t.Fatalf("binary headline is Yes: %+v", h)
	}
	if h := headline("categorical", ps("Nvidia", 0.3, "Apple", 0.5, "Other", 0.2)); h.Outcome != "Apple" {
		t.Fatalf("argmax: %+v", h)
	}
	if headline("ordered", nil) != nil {
		t.Fatal("no result → nil")
	}
}

func TestRepair(t *testing.T) {
	today := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	d := repair(Draft{Shape: "Binary", Question: " Will BTC close above $120,000 on 2026-12-31? ", Outcomes: []string{"Yes", "No", "Maybe"},
		ResolutionDate: "2025-12-31", Category: "Crypto"}, "btc 120k", today)
	if d.Shape != "binary" || !slices.Equal(d.Outcomes, yesNo) || d.ResolutionDate != "2026-12-31" || d.Category != "crypto" ||
		strings.HasPrefix(d.Question, " ") {
		t.Fatalf("binary repair: %+v", d)
	}
	d = repair(Draft{Shape: "categorical", Question: "Largest company?", Outcomes: []string{" Nvidia", "nvidia", "Apple", "", "Other"},
		ResolutionDate: "2026-12-31", Category: "finance"}, "biggest company", today)
	if d.Shape != "categorical" || !slices.Equal(d.Outcomes, []string{"Nvidia", "Apple", "Other"}) || d.Category != "other" {
		t.Fatalf("categorical repair: %+v", d)
	}
	if d := repair(Draft{Shape: "ordered", Question: "short", Outcomes: []string{"< 1"}}, "what will ETH trade at in March", today); d.Shape != "binary" ||
		d.Question != "what will ETH trade at in March" || d.ResolutionDate != "2026-12-31" {
		t.Fatalf("degenerate draft: %+v", d)
	}
	if d := repair(Draft{Shape: "poll", Outcomes: []string{"no", "YES"}}, "x", today); d.Shape != "binary" || !slices.Equal(d.Outcomes, yesNo) {
		t.Fatalf("yes/no outcomes imply binary: %+v", d)
	}
	many := repair(Draft{Shape: "categorical", Outcomes: strings.Fields("a b c d e f g h i j")}, "x", today)
	if len(many.Outcomes) != 8 {
		t.Fatalf("at most 8 outcomes: %v", many.Outcomes)
	}
	for _, tc := range [][2]string{{"2026-10-02", "2026-12-31"}, {"2026-09-30", "2026-12-31"}, {"2026-12-31", "2027-03-31"}, {"2026-02-10", "2026-03-31"}} {
		day, _ := time.Parse(time.DateOnly, tc[0])
		if got := endOfQuarter(day).Format(time.DateOnly); got != tc[1] {
			t.Errorf("endOfQuarter(%s) = %s, want %s", tc[0], got, tc[1])
		}
	}
}

func TestValidate(t *testing.T) {
	today := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	ok := func() Draft {
		return Draft{Shape: "ordered", Question: "Where will BTC trade on 2026-12-31?", Outcomes: []string{"< $100k", " $100k – $120k ", "≥ $120k"},
			ResolutionDate: "2026-12-31"}
	}
	d := ok()
	if err := validate(&d, today); err != nil || d.Outcomes[1] != "$100k – $120k" || d.Category != "other" {
		t.Fatalf("valid draft: %v %+v", err, d)
	}
	for name, mutate := range map[string]func(*Draft){
		"binary outcomes": func(d *Draft) { d.Shape, d.Outcomes = "binary", []string{"No", "Yes"} },
		"duplicates":      func(d *Draft) { d.Outcomes = []string{"Apple", "apple "} },
		"one outcome":     func(d *Draft) { d.Outcomes = d.Outcomes[:1] },
		"today":           func(d *Draft) { d.ResolutionDate = "2026-10-02" },
		"bad date":        func(d *Draft) { d.ResolutionDate = "31/12/2026" },
		"short question":  func(d *Draft) { d.Question = "BTC up?" },
		"shape":           func(d *Draft) { d.Shape = "range" },
		"category":        func(d *Draft) { d.Category = "finance" },
	} {
		d := ok()
		mutate(&d)
		var bad *app.BadRequest
		if err := validate(&d, today); !errors.As(err, &bad) {
			t.Errorf("%s: err = %v, want BadRequest", name, err)
		}
	}
}

func TestPriceOutlookThresholds(t *testing.T) {
	o := market.OutlookFrom("BTC", 100_000, 0.5, 90)
	if p := below(o, 100_000); !near(p, 0.5) {
		t.Fatalf("P(below spot) = %v, want 0.5 (zero drift)", p)
	}
	if p := below(o, o.P95); math.Abs(p-0.95) > 1e-6 {
		t.Fatalf("P(below p95) = %v", p)
	}
	view := thresholdView(o, []float64{120_000, 90_000, 120_000, -1})
	levels := view["levels"]
	ranges := view["ranges"]
	if toJSON(levels) == "[]" || strings.Count(toJSON(ranges), `"p"`) != 3 {
		t.Fatalf("levels %s ranges %s", toJSON(levels), toJSON(ranges))
	}
	if !strings.HasPrefix(toJSON(ranges), `[{"from":null,"to":90000`) || !strings.Contains(toJSON(ranges), `{"from":120000,"to":null`) {
		t.Fatalf("ranges must be sorted, deduped, open-ended: %s", toJSON(ranges))
	}
}

func TestSpotLines(t *testing.T) {
	assets := []market.Asset{{Symbol: "BTC", Name: "Bitcoin", Price: 118000.5}, {Symbol: "PAXG", Name: "PAX Gold (gold proxy)", Price: 2650}}
	at := time.Date(2026, 10, 2, 8, 30, 0, 0, time.UTC)
	got := spotLines(research.QuestionSymbols("Will bitcoin be above 120k? And gold?"), assets, at)
	if !strings.Contains(got, "BTC (Bitcoin): $118000.5") || !strings.Contains(got, "PAXG") || !strings.Contains(got, "2026-10-02 08:30 UTC") {
		t.Fatalf("spotLines: %q", got)
	}
	if got := spotLines(research.QuestionSymbols("Which company will be largest: Nvidia or Apple?"), assets, at); got != "" {
		t.Fatalf("no asset mentioned: %q", got)
	}
	// Word tickers and common words name no asset; the pair, $ticker or the name does.
	assets = append(assets, market.Asset{Symbol: "NEAR", Name: "NEAR Protocol", Price: 3}, market.Asset{Symbol: "LINK", Name: "Chainlink", Price: 20},
		market.Asset{Symbol: "ETH", Name: "Ethereum", Price: 4000}, market.Asset{Symbol: "SOL", Name: "Solana", Price: 200}, market.Asset{Symbol: "DOGE", Name: "Dogecoin", Price: 0.2})
	if got := spotLines(research.QuestionSymbols("Will the near-term rally link to a bitcoin high?"), assets, at); strings.Contains(got, "NEAR") || strings.Contains(got, "LINK") || !strings.Contains(got, "BTC") {
		t.Fatalf("word tickers: %q", got)
	}
	if got := spotLines(research.QuestionSymbols("Will $NEAR or Chainlink double?"), assets, at); !strings.Contains(got, "NEAR (") || !strings.Contains(got, "LINK (") {
		t.Fatalf("cashtag and name: %q", got)
	}
	// Ideas as people type them, which the draft quotes spot for.
	for idea, sym := range map[string]string{"will eth hit 5k by eoy": "ETH", "where will sol trade end of year": "SOL", "$sol to 300?": "SOL",
		"Eth above 5k": "ETH", "doge to 1 dollar": "DOGE", "link/usdt to 50?": "LINK"} {
		if got := spotLines(research.QuestionSymbols(idea), assets, at); !strings.HasPrefix(got, "Live Binance spot") || strings.Count(got, "\n- ") != 1 || !strings.Contains(got, "- "+sym+" (") {
			t.Errorf("idea %q: %q", idea, got)
		}
	}
	// A run quotes what its question and criteria name, and always its price rule's asset.
	link := Draft{Question: "Will the Binance LINK/USDT daily close on 2026-12-31 be above $30?",
		ResolutionCriteria: "Resolves Yes if the Binance LINKUSDT 1d candle that opens at 00:00 UTC on 2026-12-31 closes above $30; otherwise No."}
	for _, d := range []Draft{link, {Question: "Will it close above $30 on 2026-12-31?", PriceRule: &PriceRule{"LINK", "above", 30, "on_date"}}} {
		if syms := spotSymbols(d); !slices.Equal(syms, []string{"LINK"}) || !strings.Contains(spotLines(syms, assets, at), "- LINK (Chainlink): $20, 24h") {
			t.Errorf("run spot for %q: %v", d.Question, syms)
		}
	}
	link.PriceRule = &PriceRule{"LINK", "above", 30, "on_date"}
	if syms := spotSymbols(link); !slices.Equal(syms, []string{"LINK"}) {
		t.Errorf("the rule's asset once: %v", syms)
	}
	if n := knowledgeNote(false, nil, at, "none"); n != "Model knowledge (training data) as of 2026-10-02 08:30 UTC; no live news feed." {
		t.Fatalf("knowledgeNote without market data: %q", n)
	}
	if n := knowledgeNote(true, nil, at, "none"); !strings.Contains(n, "live Binance spot data") {
		t.Fatalf("knowledgeNote with market data: %q", n)
	}
}

// TestStore covers owner scoping, list filters and headline, archive, and the
// restart recovery against DATABASE_URL_TEST.
func TestStore(t *testing.T) {
	pool := storetest.Pool(t)
	alice, bob := storetest.User(t, pool), storetest.User(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &app.Deps{DB: pool, Market: testMarketClient(markettest.New(t).URL)} // Recover's resolver reads Binance
	h := &handler{d}

	draft := Draft{Shape: "binary", Question: "Will BTC be above $120k?", Outcomes: yesNo, ResolutionDate: "2030-01-01", Category: "crypto"}
	f, err := h.insert(ctx, alice, draft, nil, nil)
	if err != nil || f.Status != "running" {
		t.Fatalf("insert: %v %+v", err, f)
	}
	if _, err := h.get(ctx, bob, f.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another user's forecast: %v", err)
	}
	if err := h.archive(ctx, bob, f.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("another user's archive: %v", err)
	}
	if _, err := h.get(ctx, alice, "not-a-uuid"); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	list, err := h.list(ctx, alice, "running", "", false)
	if err != nil || len(list) != 1 || list[0].Headline != nil || list[0].ResolutionDate != "2030-01-01" {
		t.Fatalf("running list: %v %+v", err, list)
	}

	res := Result{Probabilities: ps("Yes", 0.3, "No", 0.7), Summary: "s"}
	if err := h.complete(ctx, f.ID, res); err != nil {
		t.Fatal(err)
	}
	got, err := h.get(ctx, alice, f.ID)
	if err != nil || got.Status != "completed" || got.Result == nil || got.Result.Summary != "s" || got.CompletedAt == nil ||
		!slices.Equal(got.Outcomes, yesNo) || got.ResolutionDate != "2030-01-01" {
		t.Fatalf("completed: %v %+v", err, got)
	}
	if list, _ := h.list(ctx, alice, "", "binary", false); len(list) != 1 || list[0].Headline == nil || list[0].Headline.P != 0.3 {
		t.Fatalf("headline: %+v", list)
	}
	if list, _ := h.list(ctx, alice, "", "ordered", false); len(list) != 0 {
		t.Fatalf("shape filter: %+v", list)
	}
	if err := h.archive(ctx, alice, f.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := h.list(ctx, alice, "", "", false); len(list) != 0 {
		t.Fatalf("archived must be hidden: %+v", list)
	}

	// Recover fails only runs older than 10 minutes.
	stale, _ := h.insert(ctx, alice, draft, nil, nil)
	fresh, _ := h.insert(ctx, alice, draft, nil, nil)
	if _, err := pool.Exec(ctx, "update forecasts set created_at = now() - interval '11 minutes' where id = $1", stale.ID); err != nil {
		t.Fatal(err)
	}
	Recover(ctx, d)
	if got, _ := h.get(ctx, alice, stale.ID); got.Status != "failed" || got.Error == nil || *got.Error != "interrupted by restart" {
		t.Fatalf("stale run: %+v", got)
	}
	if got, _ := h.get(ctx, alice, fresh.ID); got.Status != "running" {
		t.Fatalf("fresh run: %+v", got)
	}
	if err := h.fail(ctx, fresh.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := h.complete(ctx, fresh.ID, res); err != nil { // a late completion must not revive a failed run
		t.Fatal(err)
	}
	if got, _ := h.get(ctx, alice, fresh.ID); got.Status != "failed" || got.Result != nil {
		t.Fatalf("failed run revived: %+v", got)
	}
}
