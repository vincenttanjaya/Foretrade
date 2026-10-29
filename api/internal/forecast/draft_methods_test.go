package forecast

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// draftReply is the model's ordered draft resolving on date with outcomes.
func draftReply(question, date string, outcomes ...string) llmtest.Reply {
	return llmtest.Reply{Text: fmt.Sprintf(`{"shape":"ordered","question":%q,"outcomes":[%s],"resolutionCriteria":"NYSE official close.","resolutionDate":%q,"category":"markets","priceRule":null}`,
		question, `"`+strings.Join(outcomes, `","`)+`"`, date)}
}

// TestLogBuckets: a long-horizon ordered draft about a listed stock is cut
// at multiples of today's level from its measured volatility (§4.11's TSM
// labels), with the level as the draft's reference; the user's own ranges,
// a short horizon, a missing quote and the methods off keep the model's.
func TestLogBuckets(t *testing.T) {
	five := todayUTC().AddDate(5, 0, 0).Format(time.DateOnly)
	q := "What will be the NYSE closing price of TSM ADR on " + five + "?"
	steps := []string{"< $300", "$300 – $450", "$450 – $600", "$600 – $800", "$800 – $1,100", "≥ $1,100"}
	h, yh := measureEnv(t)
	yh.Set(yahootest.Daily("TSM", "USD", synthCloses(1, 29, 0.10, 0.38, 485.12)...))
	ten := todayUTC().AddDate(10, 0, 0).Format(time.DateOnly)
	yh.Set(yahootest.Daily("005930.KS", "KRW", synthCloses(4, 26, 0.12, 0.41, 262_000)...))
	ai, fake := llmtest.New(t, func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.Contains(r.Prompt, "Samsung"):
			date := ten
			if strings.Contains(r.Prompt, "in 4 years") {
				date = todayUTC().AddDate(4, 0, 0).Format(time.DateOnly)
			}
			return draftReply("What will be the KRX closing price of Samsung Electronics (SMSN) on "+date+"?", date, "< ₩200,000", "₩200,000 – ₩300,000", "₩300,000 – ₩500,000", "≥ ₩500,000")
		case strings.Contains(r.Prompt, "under 400"):
			return draftReply(q, five, "< $400", "$400 – $600", "≥ $600")
		case strings.Contains(r.Prompt, "this quarter"):
			return draftReply("What will be the NYSE closing price of TSM ADR on "+endOfQuarter(todayUTC()).Format(time.DateOnly)+"?",
				endOfQuarter(todayUTC()).AddDate(0, 0, 1).Format(time.DateOnly), "< $450", "$450 – $500", "≥ $500")
		}
		return draftReply(q, five, steps...)
	})
	h.d.LLM = ai
	ctx := context.Background()

	prompt := func(i int) string { return fake.Requests()[i].Prompt }
	d, ref, err := h.draftWith(ctx, "Where will TSM trade in 5 years?", todayUTC())
	want := []string{"< $250", "$250 – $360", "$360 – $485", "$485 – $750", "$750 – $1,000", "$1,000 – $1,500", "≥ $1,500"}
	if err != nil || !slices.Equal(d.Outcomes, want) || ref == nil || ref.Value != 485.12 || ref.Source != "Yahoo Finance (delayed)" {
		t.Fatalf("TSM in 5 years: %q %+v %v", d.Outcomes, ref, err)
	}
	if b, ok := bucketBounds(d.Outcomes); !ok || b[3] != [2]float64{485, 750} {
		t.Fatalf("the labels resolve: %v", b)
	}
	if !strings.Contains(prompt(0), "\n\nLive price: TSM (NYSE, Yahoo Finance) $485.12, regular-market price as of ") || !strings.Contains(prompt(0), "(tool data, safe to cite).") {
		t.Fatalf("the draft quotes the listing: %q", prompt(0))
	}

	// The user named the ranges.
	if d, ref, _ := h.draftWith(ctx, "will TSM be under 400, 400-600 or above 600 in 2031", todayUTC()); ref != nil || !slices.Equal(d.Outcomes, []string{"< $400", "$400 – $600", "≥ $600"}) {
		t.Fatalf("user edges: %q %+v", d.Outcomes, ref)
	}
	if !userEdges("TSM under $1.2T or between $1.2T and 1.5 trillion", [][2]float64{{0, 1.2e12}, {1.2e12, 1.5e12}, {1.5e12, 0}}) ||
		userEdges("TSM in 2031 at 400?", [][2]float64{{0, 400}, {400, 600}, {600, 0}}) {
		t.Fatal("two amounts within 1% of the edges are the user's ranges; one is not")
	}
	// A short horizon keeps the model's ranges.
	if d, ref, _ := h.draftWith(ctx, "Where will TSM close this quarter?", todayUTC()); ref != nil || len(d.Outcomes) != 3 {
		t.Fatalf("short horizon: %q", d.Outcomes)
	}
	// No quote: the model's ranges and no live price.
	yh.SetErr("TSM", &yahoo.UnavailableError{Symbol: "TSM", Reason: "HTTP 503"})
	n := len(fake.Requests())
	if d, ref, _ := h.draftWith(ctx, "Where will TSM trade in 5 years?", todayUTC()); ref != nil || !slices.Equal(d.Outcomes, steps) || strings.Contains(prompt(n), "Live price") {
		t.Fatalf("no quote: %q", d.Outcomes)
	}
	yh.SetErr("TSM", nil)
	// The methods off: today's draft.
	h.d.ForecastMethods = false
	n = len(fake.Requests())
	if d, ref, _ := h.draftWith(ctx, "Where will TSM trade in 5 years?", todayUTC()); ref != nil || !slices.Equal(d.Outcomes, steps) || strings.Contains(prompt(n), "Live price") {
		t.Fatalf("methods off: %q", d.Outcomes)
	}
	if !strings.Contains(draftSystem, "For a horizon beyond six months, space the edges as multiples of the current level") {
		t.Fatal("the draft prompt asks for multiples past six months")
	}
	if labelLevel(0.0615, "$") != "$0.0615" || labelLevel(12500, "S$") != "S$12,500" || labelLevel(1.25, "$") != "$1.25" ||
		labelLevel(999_999.9999999999, "₩") != "₩1,000,000" || labelLevel(1_599_999.9999999998, "$") != "$1,600,000" {
		t.Fatal("labels")
	}

	// A 10-year ₩ ladder reaches past a million: whole won in every label
	// (the live check's Samsung draft read "₩520,000 – ₩999,999.9999999999").
	h.d.ForecastMethods = true
	d, ref, err = h.draftWith(ctx, "Where will Samsung Electronics (SMSN) trade in 10 years, in won?", todayUTC())
	want = []string{"< ₩66,000", "₩66,000 – ₩130,000", "₩130,000 – ₩262,000", "₩262,000 – ₩520,000", "₩520,000 – ₩1,000,000", "₩1,000,000 – ₩2,100,000", "≥ ₩2,100,000"}
	if err != nil || !slices.Equal(d.Outcomes, want) || ref == nil || ref.Value != 262_000 {
		t.Fatalf("Samsung in 10 years: %q %+v %v", d.Outcomes, ref, err)
	}

	// The ladder is in won (the listing's own price), so its σ is the won
	// closes', not the dollar series the gap round measures a USD level on: at
	// four years the won's own swings would move it to the widest row.
	fx := make([]float64, 26*261)
	for i := range fx {
		fx[i] = 0.000744 * math.Exp(0.15*math.Sin(float64(i)/3))
	}
	yh.Set(yahootest.Daily("KRWUSD=X", "USD", fx...))
	d, ref, err = h.draftWith(ctx, "Where will Samsung Electronics (SMSN) trade in 4 years, in won?", todayUTC())
	want = []string{"< ₩130,000", "₩130,000 – ₩200,000", "₩200,000 – ₩262,000", "₩262,000 – ₩400,000", "₩400,000 – ₩520,000", "₩520,000 – ₩800,000", "≥ ₩800,000"}
	if err != nil || !slices.Equal(d.Outcomes, want) || ref == nil || ref.Value != 262_000 {
		t.Fatalf("Samsung in 4 years: %q %+v %v", d.Outcomes, ref, err)
	}
	days := horizonOf(d, todayUTC())
	usd, err := h.d.Market.DailyHistoryUSD(ctx, "SMSN")
	if v := market.ComputeHistoryStats(usd, days).VolAnnPct["y5"]; err != nil || v == nil || market.LadderEdges(262_000, *v/100*math.Sqrt(float64(days)/365))[0] == 130_000 {
		t.Fatalf("the dollar series must cut another ladder: %v %v", v, err)
	}

	// Under a year of won closes: no won σ, and the long outlook's dollar σ
	// never cuts a won ladder, so the model's ranges stand.
	yh.Set(yahootest.Daily("005930.KS", "KRW", synthCloses(4, 0.5, 0.12, 0.41, 262_000)...))
	d, ref, err = h.draftWith(ctx, "Where will Samsung Electronics (SMSN) trade in 4 years, in won?", todayUTC())
	if err != nil || !slices.Equal(d.Outcomes, []string{"< ₩200,000", "₩200,000 – ₩300,000", "₩300,000 – ₩500,000", "≥ ₩500,000"}) || ref != nil {
		t.Fatalf("Samsung with half a year of won closes: %q %+v %v", d.Outcomes, ref, err)
	}
}
