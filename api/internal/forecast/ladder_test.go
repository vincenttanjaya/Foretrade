package forecast

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
)

func TestBucketBounds(t *testing.T) {
	inf := math.Inf(1)
	for _, c := range []struct {
		outcomes []string
		want     [][2]float64 // nil: not ok
	}{
		{[]string{"< $80,000", "$80,000 – $90,000", "$90,000 – $120k", "≥ $120k"}, [][2]float64{{-inf, 8e4}, {8e4, 9e4}, {9e4, 12e4}, {12e4, inf}}},
		{[]string{"Under $80,000", "$80k–$90k", "$90K-$100K", "$100k+"}, [][2]float64{{-inf, 8e4}, {8e4, 9e4}, {9e4, 1e5}, {1e5, inf}}},
		{[]string{"Below $1M", "$1M – $1.2M", "Above $1.2M"}, [][2]float64{{-inf, 1e6}, {1e6, 1.2e6}, {1.2e6, inf}}},
		{[]string{"< 2.5", "2.5 - 3", "over 3"}, [][2]float64{{-inf, 2.5}, {2.5, 3}, {3, inf}}},
		{[]string{"< $80,000", "$80,005 – $90,000", ">= $90,000"}, [][2]float64{{-inf, 8e4}, {80005, 9e4}, {9e4, inf}}}, // within 0.01%
		{[]string{"< $80,000", "≥ $80,000"}, nil},                                               // two buckets
		{[]string{"< $80,000", "$80,010 – $90,000", "≥ $90,000"}, nil},                          // a gap over 0.01%
		{[]string{"< $80,000", "$85,000 – $90,000", "≥ $90,000"}, nil},                          // a gap
		{[]string{"< $80,000", "$80,000 – $90,000", "$1.2M"}, nil},                              // the top bucket has no direction
		{[]string{"< $80,000", "$90,000", "≥ $90,000"}, nil},                                    // a middle bucket with one edge
		{[]string{"< $80,000", "$90,000 – $80,000", "≥ $80,000"}, nil},                          // decreasing
		{[]string{"≥ $120k", "$80,000 – $90,000", "< $80,000"}, nil},                            // high to low
		{[]string{"Low", "Mid", "High"}, nil},                                                   // no numbers
		{[]string{"< $80,000", "$80,000 – $90,000", "$90,000 – $95,000 – $99k", "≥ $99k"}, nil}, // three numbers
		// Round 9b: scale words (TestAmountSuffix) and signed numbers.
		{[]string{"< $800B", "$800B – $1.2T", "$1.2T – $1.5tn", "≥ $1.5 trillion"}, [][2]float64{{-inf, 8e11}, {8e11, 1.2e12}, {1.2e12, 1.5e12}, {1.5e12, inf}}},
		{[]string{"Under $950bn", "$950bn – $1.1 tn", "Over $1.1tn"}, [][2]float64{{-inf, 9.5e11}, {9.5e11, 1.1e12}, {1.1e12, inf}}},
		{[]string{"< 300", "300 to 450", "≥ 450"}, [][2]float64{{-inf, 300}, {300, 450}, {450, inf}}}, // any other word means ×1
		{[]string{"< 2 million", "2 million – 3mn", "≥ 3 mn"}, [][2]float64{{-inf, 2e6}, {2e6, 3e6}, {3e6, inf}}},
		{[]string{"< -1%", "-1% – 1%", "1% – 3%", "≥ 3%"}, [][2]float64{{-inf, -1}, {-1, 1}, {1, 3}, {3, inf}}},
		{[]string{"< −0.5%", "−0.5% – 2.5-3", "≥ 3%"}, nil}, // "2.5-3" is a range's dash, not a sign: three numbers
	} {
		got, ok := bucketBounds(c.outcomes)
		if ok != (c.want != nil) || !slices.Equal(got, c.want) {
			t.Errorf("bucketBounds(%q) = %v %v, want %v", c.outcomes, got, ok, c.want)
		}
	}
}

var btcBuckets = []string{"< $80k", "$80k – $90k", "$90k – $100k", "≥ $100k"}

func TestDeskAbove(t *testing.T) {
	bounds, _ := bucketBounds(btcBuckets)
	probs := ps("≥ $100k", 0.2, "< $80k", 0.1, "$90k – $100k", 0.4, "$80k – $90k", 0.3) // matched by label, any order
	for k, want := range map[float64]float64{80000: 0.9, 85000: 0.75, 90000: 0.6, 97500: 0.3, 100000: 0.2} {
		if p, ok := deskAbove(bounds, btcBuckets, probs, k); !ok || math.Abs(p-want) > 1e-12 {
			t.Errorf("deskAbove(%v) = %v %v, want %v", k, p, ok, want)
		}
	}
	for _, k := range []float64{79999, 100001} { // inside an open-ended tail: undefined
		if _, ok := deskAbove(bounds, btcBuckets, probs, k); ok {
			t.Errorf("deskAbove(%v) is defined", k)
		}
	}
	if _, ok := deskAbove(bounds, btcBuckets[:3], probs, 85000); ok {
		t.Error("outcomes that do not match the bounds")
	}
	r := rand.New(rand.NewPCG(1, 2))
	ks := make([]float64, 200)
	for i := range ks {
		ks[i] = 80000 + r.Float64()*20000
	}
	slices.Sort(ks)
	prev := 1.0
	for _, k := range ks {
		p, ok := deskAbove(bounds, btcBuckets, probs, k)
		if !ok || p > prev+1e-12 || p < 0 {
			t.Fatalf("not non-increasing at %v: %v after %v", k, p, prev)
		}
		prev = p
	}
}

// ladderMarket is one "Bitcoin above $<k> on October <day>" market of the event for that day.
func ladderMarket(k, day int, yes float64, volume any, mods ...func(map[string]any)) map[string]any {
	return gammaMarket(fmt.Sprintf("Will the price of Bitcoin be above $%s on October %d?", money(float64(k)), day), fmt.Sprintf("2026-10-%02dT16:00:00Z", day), yes, volume, mods...)
}

func strikesOf(ms marketSet) []float64 {
	out := make([]float64, len(ms))
	for i, m := range ms {
		out[i] = m.Strike
	}
	return out
}

func TestFindLadder(t *testing.T) {
	var oct9Slug []map[string]any
	for k := 66000; k <= 104000; k += 2000 { // 20 strikes; 70k..100k (16) fall inside the buckets' edges
		oct9Slug = append(oct9Slug, ladderMarket(k, 9, 1-float64(k-66000)/40000, 5000))
	}
	oct9Slug = append(oct9Slug, gammaMarket("Will the price of Bitcoin be above $74k on October 9?", "2026-10-09T16:00:00Z", 0.91, 9e5)) // the busier $74k
	base, seen := gamma(t, map[string][]map[string]any{
		"bitcoin-above-on-october-9-2026":        oct9Slug,
		"bitcoin-above-on-october-10-2026":       {ladderMarket(72000, 10, 0.5, 1e7), ladderMarket(76000, 10, 0.5, 1e7)},
		"bitcoin-above-on-october-9-2026-2pm-et": {gammaMarket("Will the price of Bitcoin be above $71,000 on October 9, 2PM ET?", "2026-10-09T18:00:00Z", 0.5, 1e7)},
		"ethereum-above-on-october-9-2026":       {gammaMarket("Will the price of Ethereum be above $80,000 on October 9?", "2026-10-09T16:00:00Z", 0.01, 1e7)},
	})
	h := &handler{&app.Deps{PolymarketBase: base}}
	ctx := context.Background()
	d := Draft{Shape: "ordered", Question: "Where will the Binance BTC/USDT daily close be on 2026-10-09?",
		Outcomes: []string{"< $70,000", "$70,000 – $85,000", "$85,000 – $100,000", "≥ $100,000"}, ResolutionDate: "2026-10-09"}

	ms, err := h.findLadder(ctx, d)
	if err != nil || !slices.Equal(strikesOf(ms), []float64{70000, 74000, 78000, 82000, 86000, 90000, 94000, 100000}) {
		t.Fatalf("every 2nd of 16 strikes, the highest kept: %v %v", strikesOf(ms), err)
	}
	if strings.Join(seen(), " | ") != "slug:bitcoin-above-on-october-9-2026" {
		t.Fatalf("the slug alone: %q", seen())
	}
	for _, m := range ms {
		if m.Match != "exact" || m.URL != "https://polymarket.com/event/bitcoin-above-on-october-9-2026" || m.Signal != "" || m.Thin ||
			time.Since(m.AsOf) > time.Minute {
			t.Errorf("row %v: %+v", m.Strike, m)
		}
	}
	if ms[1].YesPrice != 0.91 || *ms[1].VolumeUSD != 9e5 || !strings.Contains(ms[1].Question, "$74k") {
		t.Fatalf("one market per strike, the busier: %+v", ms[1])
	}
	if ms.pick(ps("< $70,000", 0.1, "$70,000 – $85,000", 0.4, "$85,000 – $100,000", 0.4, "≥ $100,000", 0.1), "medium") != nil || ms.first() != nil {
		t.Fatal("a ladder set has no single market and no M1")
	}

	// No event on the day: the dated search finds the next day's, a close match.
	d.ResolutionDate, d.Question = "2026-10-11", "Where will BTC close on 2026-10-11?"
	base, seen = gamma(t, map[string][]map[string]any{"bitcoin-above-on-october-10-2026": {ladderMarket(72000, 10, 0.6, 1e4), ladderMarket(76000, 10, 0.4, 1e4), ladderMarket(76000, 10, 0.3, 10)}})
	ms, err = (&handler{&app.Deps{PolymarketBase: base}}).findLadder(ctx, d)
	if err != nil || !slices.Equal(strikesOf(ms), []float64{72000, 76000}) || ms[0].Match != "close" || ms[1].YesPrice != 0.4 ||
		strings.Join(seen(), " | ") != "slug:bitcoin-above-on-october-11-2026 | Bitcoin above on October 11" {
		t.Fatalf("close ladder: %v %v %q", strikesOf(ms), err, seen())
	}

	// One usable strike after every step, buckets that do not parse, gold, no asset: none.
	base, seen = gamma(t, map[string][]map[string]any{"bitcoin-above-on-october-11-2026": {ladderMarket(72000, 11, 0.6, 1e4), ladderMarket(66000, 11, 0.9, 1e4)}})
	h = &handler{&app.Deps{PolymarketBase: base}}
	if ms, err := h.findLadder(ctx, d); ms != nil || err != nil || len(seen()) != 3 {
		t.Fatalf("one strike in range: %v %v %q", strikesOf(ms), err, seen())
	}
	for _, x := range []Draft{
		{Shape: "ordered", Question: d.Question, Outcomes: []string{"Low", "Mid", "High"}, ResolutionDate: d.ResolutionDate},
		{Shape: "ordered", Question: "Where will gold close on 2026-10-11?", Outcomes: d.Outcomes, ResolutionDate: d.ResolutionDate},
		{Shape: "ordered", Question: "How many rate cuts by 2026-10-11?", Outcomes: d.Outcomes, ResolutionDate: d.ResolutionDate},
	} {
		if ms, err := h.findLadder(ctx, x); ms != nil || err != nil || len(seen()) != 3 {
			t.Errorf("%q %q: %v %v, %d requests", x.Question, x.Outcomes, strikesOf(ms), err, len(seen()))
		}
	}

	// A word ticker counts by its pair (or name, or $ticker), never as the bare word.
	base, seen = gamma(t, map[string][]map[string]any{"chainlink-above-on-october-9-2026": {
		gammaMarket("Will the price of Chainlink be above $18 on October 9?", "2026-10-09T16:00:00Z", 0.7, 1e4),
		gammaMarket("Will the price of Chainlink be above $22 on October 9?", "2026-10-09T16:00:00Z", 0.3, 1e4)}})
	h = &handler{&app.Deps{PolymarketBase: base}}
	linkBuckets := []string{"< $15", "$15 – $20", "$20 – $25", "≥ $25"}
	for _, q := range []string{"Where will the Binance LINK/USDT daily close be on 2026-10-09?", "Where will linkusdt close on 2026-10-09?", "Where will $link close on 2026-10-09?"} {
		ms, err := h.findLadder(ctx, Draft{Shape: "ordered", Question: q, Outcomes: linkBuckets, ResolutionDate: "2026-10-09"})
		if err != nil || !slices.Equal(strikesOf(ms), []float64{18, 22}) {
			t.Errorf("%q: %v %v", q, strikesOf(ms), err)
		}
	}
	n := len(seen())
	if ms, err := h.findLadder(ctx, Draft{Shape: "ordered", Question: "Where will the near-term link price close on 2026-10-09?", Outcomes: linkBuckets,
		ResolutionDate: "2026-10-09"}); ms != nil || err != nil || len(seen()) != n {
		t.Fatalf("bare words name no asset: %v %v %q", strikesOf(ms), err, seen())
	}

	// Gamma down is an error (logged by lookupMarket, never the run's).
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) }))
	defer down.Close()
	if _, err := (&handler{&app.Deps{PolymarketBase: down.URL}}).findLadder(ctx, d); err == nil {
		t.Fatal("gamma down")
	}
}

// ladder prices each row against the desk's P(above): the edge is desk −
// market; a row outside the buckets' edges drops out; under 2 rows is none.
func TestLadder(t *testing.T) {
	row := func(k float64, yes float64, volume float64) *MarketOdds {
		return &MarketOdds{Question: fmt.Sprintf("Will the price of Bitcoin be above $%s on October 9?", money(k)), YesPrice: yes, VolumeUSD: &volume, Thin: volume < thinVolume, Strike: k}
	}
	probs := ps("< $80k", 0.1, "$80k – $90k", 0.3, "$90k – $100k", 0.4, "≥ $100k", 0.2)
	ms := marketSet{row(80000, 0.95, 1e5), row(85000, 0.66, 1e5), row(90000, 0.62, 500), row(120000, 0.05, 1e5)}
	got := ms.ladder(btcBuckets, probs, "medium")
	if len(got) != 3 || got[0].EdgePp != -5 || got[0].Signal != "no" || got[1].EdgePp != 9 || got[1].EVYes != 0.09 || got[1].Signal != "yes" ||
		got[2].EdgePp != -2 || got[2].Signal != "wait" || !got[2].Thin {
		t.Fatalf("priced: %+v %+v %+v", got[0], got[1], got[2])
	}
	if ms[:1].ladder(btcBuckets, probs, "medium") != nil || (marketSet{row(80000, 0.9, 1e5), row(120000, 0.1, 1e5)}).ladder(btcBuckets, probs, "medium") != nil {
		t.Fatal("fewer than 2 priceable rows is no ladder")
	}
	if ms.ladder([]string{"Low", "Mid", "High"}, probs, "medium") != nil || (marketSet{nvdaMarket()}).ladder(btcBuckets, probs, "medium") != nil {
		t.Fatal("unparsable buckets or a non-ladder set")
	}
}

const oct9Slug = "bitcoin-above-on-october-9-2026"

// storedLadder is an ordered report's ladder as the run stored it, read two hours ago.
func storedLadder() *Result {
	var rows []*MarketOdds
	for _, r := range []struct{ k, yes float64 }{{80000, 0.9}, {90000, 0.4}, {95000, 0.2}} {
		rows = append(rows, &MarketOdds{Source: "Polymarket", Question: fmt.Sprintf("Will the price of Bitcoin be above $%s on October 9?", money(r.k)),
			URL: "https://polymarket.com/event/" + oct9Slug, YesPrice: r.yes, VolumeUSD: ptr(5e4), EndDate: "2026-10-09T16:00:00Z", Match: "exact", Strike: r.k,
			AsOf: time.Now().Add(-2 * time.Hour)})
	}
	res := &Result{Probabilities: ps("< $80k", 0.1, "$80k – $90k", 0.3, "$90k – $100k", 0.4, "≥ $100k", 0.2), Confidence: "medium"}
	res.Ladder = (marketSet(rows)).ladder(btcBuckets, res.Probabilities, res.Confidence)
	return res
}

// Re-pricing a ladder reads its event by slug once and each row by its
// question; a closed or vanished row drops out; Gamma down, or the event
// closed, changes nothing.
func TestRepriceLadder(t *testing.T) {
	var ms []map[string]any
	calls := 0
	base := gammaEvents(t, oct9Slug, func() []map[string]any { calls++; return ms })
	h := &handler{&app.Deps{PolymarketBase: base}}
	d := Draft{Shape: "ordered", Outcomes: btcBuckets}
	ctx := context.Background()

	ms = []map[string]any{ladderMarket(80000, 9, 0.92, 2e5), ladderMarket(90000, 9, 0.5, 3e5), ladderMarket(95000, 9, 0.1, 1e5, func(m map[string]any) { m["closed"] = true })}
	r := storedLadder()
	if err := h.repriceLadder(ctx, d, r); err != nil || calls != 1 || len(r.Ladder) != 2 {
		t.Fatalf("re-priced: %v, %d calls, %+v", err, calls, r.Ladder)
	}
	if m := r.Ladder[1]; m.Strike != 90000 || m.YesPrice != 0.5 || *m.VolumeUSD != 3e5 || m.EdgePp != 10 || m.Signal != "yes" || time.Since(m.AsOf) > time.Minute || m.Match != "exact" {
		t.Fatalf("row: %+v", m)
	}

	for name, m := range map[string][]map[string]any{
		"event closed": {ladderMarket(80000, 9, 0.92, 2e5, func(m map[string]any) { m["closed"] = true }), ladderMarket(90000, 9, 0.5, 3e5, func(m map[string]any) { m["closed"] = true })},
		"one row left": {ladderMarket(80000, 9, 0.92, 2e5)},
	} {
		ms = m
		r := storedLadder()
		if err := h.repriceLadder(ctx, d, r); err == nil || len(r.Ladder) != 3 || r.Ladder[0].YesPrice != 0.9 {
			t.Errorf("%s: %v %+v", name, err, r.Ladder)
		}
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) }))
	defer down.Close()
	if r := storedLadder(); (&handler{&app.Deps{PolymarketBase: down.URL}}).repriceLadder(ctx, d, r) == nil || r.Ladder[1].YesPrice != 0.4 {
		t.Fatalf("gamma down: %+v", r.Ladder)
	}
}
