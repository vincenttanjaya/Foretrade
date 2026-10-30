package chat

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

var horizonToday = time.Date(2026, 10, 7, 15, 4, 0, 0, time.UTC)

// until is the days from horizonToday to a date.
func until(y int, m time.Month, d int) int {
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Sub(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)).Hours() / 24)
}

func TestExplicitHorizon(t *testing.T) {
	end2031 := int(time.Date(2031, 12, 31, 0, 0, 0, 0, time.UTC).Sub(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	for _, c := range []struct {
		text   string
		days   int
		phrase string
	}{
		{"where will TSM be in 5 years?", 1826, "in 5 years"},
		{"TSM price by end of 2031", end2031, "by end of 2031"},
		{"TSM by the end of 2031?", end2031, "by the end of 2031"},
		{"BTC next 3 months", 92, "next 3 months"},
		{"ETH this week", 7, "this week"},
		{"a 5-year outlook for NVDA", 1826, "5-year"},
		{"over the next ten years", 3653, "over the next ten years"},
		{"SOL in a year", 365, "in a year"},
		{"SOL by Dec 2027", 450, "by Dec 2027"},
		{"what about BTC on 2026-12-31", 85, "2026-12-31"},
		{"BTC by year-end", 85, "by year-end"},
		{"next year for ETH", 450, "next year"},
		{"ETH this quarter", 85, "this quarter"},
		{"within 2 weeks", 14, "within 2 weeks"},
		{"in 5 years, or by 2028?", 1826, "in 5 years"}, // the earliest phrase wins
		{"TSM in 18 months", 548, "in 18 months"},
		{"where will TSM be by 2030", 1546, "by 2030"},
		{"BTC over the next decade", 3653, "the next decade"},
		{"NVDA within a decade", 3653, "within a decade"},
		{"ETH this year", 85, "this year"},
		{"TSM 5 years from now", 1826, "5 years from now"},
		{"BTC two years from now", 731, "two years from now"},
		{"TSM over the coming year", 365, "the coming year"},
		{"TSM by mid-2030", 1362, "mid-2030"},
		{"TSM end of 2027?", 450, "end of 2027"},
		{"TSM by Oct 6, 2031", 1825, "by Oct 6, 2031"},
		{"TSM in Q4 2027", 450, "in Q4 2027"},
		{"TSM by Q2 '28", 632, "by Q2 '28"},
		{"a 12-month target for TSM", 365, "12-month"},
		// an indicator's lookback or a bond's tenor is not the horizon
		{"is BTC above its 200-day moving average? where next 3 months", 92, "next 3 months"},
		{"TSM if the 10-year yield hits 5%, in 2 years?", 731, "in 2 years"},
		// The owner's phrasings from a live check and their neighbours
		{"NVDA 10 years outlook", 3653, "10 years outlook"},
		{"NVDA 10 year outlook", 3653, "10 year outlook"},
		{"10 year outlook for NVDA", 3653, "10 year outlook"},
		{"NVDA 10-year view", 3653, "10-year"},
		{"NVDA 10yr outlook", 3653, "10yr outlook"},
		{"NVDA ten year outlook", 3653, "ten year outlook"},
		{"TSM 5y outlook", 1826, "5y outlook"},
		{"TSM 5 years", 1826, "5 years"},
		{"TSM over 5 years", 1826, "over 5 years"},
		{"NVDA 10 years?", 3653, "10 years"},
		{"what's the 3 year forecast for TSM", 1096, "3 year forecast"},
		{"TSMC 20 year horizon", 7300, "20 year horizon"}, // 7,305 days with leap days: the longest view
		{"ETH 1 year price target", 365, "1 year price"},
		{"BTC 18 months", 548, "18 months"},
		{"TSM 2027", until(2027, 12, 31), "TSM 2027"},
		{"TSM price 2027", until(2027, 12, 31), "price 2027"},
		{"TSM end 2027", until(2027, 12, 31), "end 2027"},
		{"TSM price end 2027", until(2027, 12, 31), "end 2027"},
		{"nvda 2030?", until(2030, 12, 31), "nvda 2030"},
		{"TSM before 2028", 450, "before 2028"},
		{"TSM before the end of 2028", until(2028, 12, 31), "before the end of 2028"},
		{"where will TSM be in 2 years", 731, "in 2 years"},
		{"TSM price by end of 2027", 450, "by end of 2027"},
		{"it doubled in 5 years; where will it be in 10 years?", 3653, "in 10 years"},
		{"BTC 2027-03-31", until(2027, 3, 31), "2027-03-31"},
		{"if TSM is at $500 in 5 years, is that good?", 1826, "in 5 years"},
		// review: ranges run to their far end; a forward statistic over a span is still ahead
		{"TSM 3-5 year outlook", 1826, "3-5 year outlook"},
		{"where will TSM be in 3-5 years?", 1826, "in 3-5 years"},
		{"TSM in the next 12-18 months", 548, "in the next 12-18 months"},
		{"TSM 2 to 3 years", 1096, "2 to 3 years"},
		{"what's the expected return over the next 5 years for TSM", 1826, "over the next 5 years"},
		{"what will TSM's CAGR be in 5 years", 1826, "in 5 years"},
		{"NVDA one year outlook", 365, "one year outlook"},
		{"NVDA a year view", 365, "a year view"},
		{"TSM is up 40% in 6 months; where in 2 years?", 731, "in 2 years"},
	} {
		days, phrase, ok := explicitHorizon(c.text, horizonToday)
		if !ok || days != c.days || phrase != c.phrase {
			t.Errorf("%q: %d %q %v, want %d %q", c.text, days, phrase, ok, c.days, c.phrase)
		}
	}
	for _, text := range []string{
		"can TSMC 10x, if yes by when", "how long until NVDA doubles?", "is SOL a good long-term hold?", "could AMD be a 5-bagger?",
		"TSM fell 10 years ago", "since the 2008 crash", "in the last 5 years TSM tripled", "in 2008 it crashed",
		"3x leverage on BTC", "TSM near its 52-week high", "the past 5-year return", "TSM Q4 30% growth",
		// past moves, lookbacks and fiscal years stay not-a-horizon
		"it doubled in 5 years", "TSM went up 300% over 10 years", "NVDA CAGR 10 years", "TSM performance over the past 5 years",
		"what did TSM do in 10 years", "where is the 10 year yield", "TSM 2027 EPS", "TSM FY2027 guidance", "NVDA trades at 25x earnings",
		"TSM 2008", "TSM 5 year price return", "TSM rallied 40% in the last 2 years", "TSM 5 years ago", "I've held TSM for 5 years",
		// review: indicators after a prepositional span, rates, present-perfect moves, statistics over a span
		"TSM trading over the 200 day moving average, outlook?", "TSM broke out over the 200-day moving average", "TSM is in the 52 week high zone",
		"Can TSM keep compounding 15% a year?", "TSM pays 2% a year", "I rebalance once a year", "TSM grows 20% per year",
		"NVDA is up 200% in 2 years. Outlook?", "TSM has risen 40% in 6 months, where next", "TSM has been flat for 5 years",
		"TSM CAGR over 10 years", "TSM's average annual return over 10 years", "TSM volatility over 3 years",
	} {
		if days, phrase, ok := explicitHorizon(text, horizonToday); ok {
			t.Errorf("%q: %d %q, want no timeframe", text, days, phrase)
		}
	}
}

func TestTargetMultiple(t *testing.T) {
	for _, c := range []struct {
		text       string
		x          float64
		byWhen, ok bool
	}{
		{"can TSMC 10x, if yes by when", 10, true, true},
		{"how long until NVDA doubles?", 2, true, true},
		{"could AMD be a 5-bagger?", 5, false, true},
		{"When will BTC 2.5×?", 2.5, true, true},
		{"how many years for SOL to triple", 3, true, true},
		{"a ten-bagger", 10, false, true},
		{"a 3-fold move by when", 3, true, true},
		{"3x leverage on BTC", 0, false, false}, // leverage, not a price multiple
		{"TSM trades at 25x earnings, could it double, by when?", 2, true, true},
		{"with a P/E of 30x, can NVDA 10x?", 10, false, true},
		{"can TSM go up 10 times", 10, false, true},
		{"TSM tenfold by when", 10, true, true},
		{"in what year could TSM 10x", 10, true, true},
		{"timeline for TSM to 10x", 10, true, true},
		{"when is TSM likely to double", 2, true, true},
		{"could TSM quadruple", 4, false, true},
		{"where will TSM be in 5 years?", 0, false, false},
		{"how long should I hold ETH?", 0, true, false},
		// by when without "will/could"
		{"TSMC 10x when", 10, true, true},
		{"10x when?", 10, true, true},
		{"10x by when", 10, true, true},
		{"NVDA 5x, and when", 5, true, true},
		{"can TSMC 10x? when", 10, true, true},
		// "when <a condition>" is a condition, not by when
		{"can TSMC 10x when the cycle turns", 10, false, true},
		{"Could NVDA 2x when rates fall?", 2, false, true},
		{"can TSMC 10x, and when would that be", 10, true, true},
		{"TSMC 10x, if so when?", 10, true, true},
	} {
		x, byWhen, ok := targetMultiple(c.text)
		if x != c.x || byWhen != c.byWhen || ok != c.ok {
			t.Errorf("%q: %v %v %v, want %v %v %v", c.text, x, byWhen, ok, c.x, c.byWhen, c.ok)
		}
	}
}

// With several timeframes in the message, the one horizon_days matches wins;
// else the earliest.
func TestResolveHorizonNamed(t *testing.T) {
	text := "where will TSM be in 2 weeks, and in 5 years?"
	for _, c := range []struct {
		model, days int
		phrase      string
	}{{1825, 1825, "in 5 years"}, {14, 14, "in 2 weeks"}, {365, 14, "in 2 weeks"}} {
		days, h, _, _, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &c.model}, text, horizonToday)
		if err != nil || days != c.days || h.Basis != "explicit" || h.Reason != "From your question: \""+c.phrase+"\"" {
			t.Errorf("horizon_days %d: %d %+v %v, want %d %q", c.model, days, h, err, c.days, c.phrase)
		}
	}
	// A past move over a span is not a timeframe: "fell 30% in 2 weeks" never picks 14 days.
	h14 := 14
	days, h, _, warns, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h14}, "TSM fell 30% in 2 weeks; where will it be in 5 years?", horizonToday)
	if err != nil || days != 1826 || h.Reason != `From your question: "in 5 years"` || len(warns) != 1 {
		t.Errorf("past move: %d %+v %q %v", days, h, warns, err)
	}
}

// A past move's period word ("ran 20% this week", "fell 8% today") is not the
// outlook's horizon; a by-when question then gets the by-when card. The
// owner's repros.
func TestPastMovePeriodWords(t *testing.T) {
	ten, two := 10.0, 2.0
	for _, c := range []struct {
		text   string
		target *float64
	}{
		{"NVDA ran 20% this week. Can it 10x?", &ten},
		{"BTC fell 8% today, can it still double?", &two},
		{"TSM is up 60% this year - can it 10x from here?", &ten},
		{"TSM has done well this year. Can it 10x? By when?", &ten},
		// without target_multiple the multiple comes from the text
		{"NVDA ran 20% this week. Can it 10x?", nil},
		{"BTC fell 8% today, can it still double?", nil},
		{"TSM is up 60% this year - can it 10x from here?", nil},
		{"TSM has done well this year. Can it 10x? By when?", nil},
	} {
		if days, phrase, ok := explicitHorizon(c.text, horizonToday); ok {
			t.Errorf("%q: %d %q, want no timeframe", c.text, days, phrase)
		}
		days, h, x, warns, err := resolveHorizon(outlookInput{Symbol: "TSM", TargetMultiple: c.target}, c.text, horizonToday)
		want := 10.0
		if c.target != nil {
			want = *c.target
		} else if strings.Contains(c.text, "double") {
			want = 2
		}
		if err != nil || days != 0 || h.Basis != "by_when" || h.Days != nil || x == nil || *x != want || len(warns) != 0 {
			t.Errorf("%q (target %v): %d %+v %v %q %v", c.text, c.target, days, h, x, warns, err)
		}
	}
	// "today" after an entry verb starts a holding period; the hypothetical hold is the horizon.
	text := "If I bought TSM today and held it for 10 years, what would it be worth?"
	if days, phrase, ok := explicitHorizon(text, horizonToday); !ok || days != 3653 || phrase != "for 10 years" {
		t.Errorf("%q: %d %q %v", text, days, phrase, ok)
	}
	h3650 := 3650
	if days, h, _, warns, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h3650}, text, horizonToday); err != nil || days != 3650 ||
		h.Basis != "explicit" || h.Reason != `From your question: "for 10 years"` || len(warns) != 0 {
		t.Errorf("%q + 3650: %d %+v %q %v", text, days, h, warns, err)
	}
	h365 := 365
	text = "BTC dropped 5% today. Where could it be in 2 years?"
	if days, h, _, warns, err := resolveHorizon(outlookInput{Symbol: "BTC", HorizonDays: &h365}, text, horizonToday); err != nil || days != 731 ||
		h.Basis != "explicit" || len(warns) != 1 || !strings.HasPrefix(warns[0], `Horizon set from the question ("in 2 years": 731 days), not horizon_days 365`) {
		t.Errorf("%q + 365: %d %+v %q %v", text, days, h, warns, err)
	}
	// A multiple with no horizon at all: the by-when card, not a tool error.
	for text, want := range map[string]float64{"can NVDA double": 2, "Will NVDA 2x?": 2, "Is a 10x possible for TSM?": 10, "TSMC 10x, possible?": 10, "Is TSMC a 10 bagger?": 10} {
		days, h, x, _, err := resolveHorizon(outlookInput{Symbol: "NVDA"}, text, horizonToday)
		if err != nil || days != 0 || h.Basis != "by_when" || x == nil || *x != want {
			t.Errorf("%q: %d %+v %v %v", text, days, h, x, err)
		}
	}
	// A multiple the asset already made asks no target: still the model's call (a tool error), never "You asked … 2×".
	for _, text := range []string{"TSM doubled this year. What's the outlook?", "NVDA 10x'd since 2020, where next?",
		"NVDA tripled in 2 years, what now?", "It went up 5x since the IPO. Thoughts?", "SMCI was a 10 bagger, what now?"} {
		if _, h, x, _, err := resolveHorizon(outlookInput{Symbol: "NVDA"}, text, horizonToday); err == nil || x != nil {
			t.Errorf("%q: %+v %v %v, want the horizon_days error", text, h, x, err)
		}
	}
	// … unless the question asks it again by when.
	if _, h, x, _, err := resolveHorizon(outlookInput{Symbol: "TSM"}, "TSM doubled this year. When could it double again?", horizonToday); err != nil ||
		h.Basis != "by_when" || x == nil || *x != 2 {
		t.Errorf("doubled, by when: %+v %v %v", h, x, err)
	}
	// A multiple with the model's horizon_days and no by-when word stays the model's call.
	h1825 := 1825
	if _, h, x, _, err := resolveHorizon(outlookInput{Symbol: "NVDA", HorizonDays: &h1825, HorizonReason: "a multi-bagger needs years"}, "can NVDA double", horizonToday); err != nil ||
		h.Basis != "chosen" || x != nil {
		t.Errorf("double + horizon_days: %+v %v %v", h, x, err)
	}
	// Forward period words still work.
	for _, c := range []struct {
		text   string
		days   int
		phrase string
	}{
		{"Can BTC double this year?", 85, "this year"}, {"ETH this week", 7, "this week"}, {"BTC by year-end", 85, "by year-end"},
		{"if it ran like NVDA over the next 5 years, where would TSM be?", 1826, "over the next 5 years"},
		{"Should I hold TSM for 5 years if I buy today?", 1826, "for 5 years"},
	} {
		if days, phrase, ok := explicitHorizon(c.text, horizonToday); !ok || days != c.days || phrase != c.phrase {
			t.Errorf("%q: %d %q %v, want %d %q", c.text, days, phrase, ok, c.days, c.phrase)
		}
	}
	// "N or M units" runs to its far end; the near end stays readable for a horizon_days that names it, as "1 day" (not "1 days").
	if hs := explicitHorizons("NVDA in 1 or 2 days", horizonToday); len(hs) < 2 || hs[0].phrase != "in 1 or 2 days" || hs[0].days != 2 || hs[1].phrase != "1 day" || hs[1].days != 1 {
		t.Errorf("1 or 2 days: %+v", hs)
	}
	// A past narrative stays the past.
	for _, text := range []string{"I bought TSM in 2020 and held it for 5 years", "TSM dropped 30% in 2026", "NVDA has rallied this quarter", "Should I buy BTC today?"} {
		if days, phrase, ok := explicitHorizon(text, horizonToday); ok {
			t.Errorf("%q: %d %q, want no timeframe", text, days, phrase)
		}
	}
}

// Enumerations, the "what's up" subject rule, through/till and
// year ranges, halves, spans past the longest outlook, date snapping and the
// basis wording.
func TestHorizonParserGaps(t *testing.T) {
	for _, text := range []string{"Compare TSM 5 years vs 10 years", "TSM in 5 and 10 years", "TSM 5yr and 10yr outlook", "TSM over 5 or 10 years"} {
		var got []int
		for _, h := range explicitHorizons(text, horizonToday) {
			got = append(got, h.days)
		}
		if !slices.Contains(got, 1826) || !slices.Contains(got, 3653) {
			t.Errorf("%q: %v, want 1826 and 3653", text, got)
		}
		if text == "TSM in 5 and 10 years" { // the shared unit's phrase reads as written: "5 years"
			if hs := explicitHorizons(text, horizonToday); len(hs) == 0 || hs[0].phrase != "5 years" {
				t.Errorf("%q: %+v", text, hs)
			}
		}
		for _, model := range []int{1826, 3653} {
			days, h, _, warns, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &model}, text, horizonToday)
			if err != nil || days != model || h.Basis != "explicit" || len(warns) != 0 {
				t.Errorf("%q + %d: %d %+v %q %v", text, model, days, h, warns, err)
			}
		}
	}
	h1826 := 1826
	if _, h, _, _, _ := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h1826}, "TSM in 5 and 10 years", horizonToday); h.Reason != `From your question: "5 years"` {
		t.Errorf("shared unit phrase: %q", h.Reason)
	}
	for _, text := range []string{"TSM's 50 and 200 day moving averages", "TSM between 3 and 5 years"} {
		for _, h := range explicitHorizons(text, horizonToday) {
			if h.days == until(2029, 10, 7) || h.days == 50 || h.days == 200 {
				t.Errorf("%q: %+v", text, h)
			}
		}
	}

	h365 := 365
	days, h, _, warns, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h365}, "what's up with TSM in 5 years", horizonToday)
	if err != nil || days != 1826 || h.Basis != "explicit" || len(warns) != 1 || !strings.HasPrefix(warns[0], `Horizon set from the question ("in 5 years": 1,826 days)`) {
		t.Errorf("what's up: %d %+v %q %v", days, h, warns, err)
	}
	if days, phrase, _ := explicitHorizon("TSM's up 20% in 5 days, where in a year?", horizonToday); days != 365 || phrase != "in a year" {
		t.Errorf("TSM's up: %d %q", days, phrase)
	}

	for _, c := range []struct {
		text   string
		days   int
		phrase string
	}{
		{"TSM 2026-2028 outlook", 816, "2026-2028"},
		{"TSM 2026–2028 outlook", 816, "2026–2028"},
		{"TSM from 2027 to 2030", 1546, "from 2027 to 2030"},
		{"SOL outlook through 2028", 816, "through 2028"},
		{"TSM till 2030", 1546, "till 2030"},
		{"TSM up to 2030", 1546, "up to 2030"},
		{"TSM over 2027-2030", 1546, "over 2027-2030"},
		{"TSM in 2026-2028", 816, "in 2026-2028"},
		{"TSM in H2 2027", 450, "in H2 2027"},
		{"TSM in H1 2027", 266, "in H1 2027"},
		{"TSM before H2 2027", 266, "before H2 2027"},
		{"TSM by 2H 2027", 450, "by 2H 2027"},
		{"TSM in H2 '27", 450, "in H2 '27"},
		{"TSM in 30 years", 7300, "in 30 years"},
		{"BTC 2027-03-31", until(2027, 3, 31), "2027-03-31"},
	} {
		if days, phrase, ok := explicitHorizon(c.text, horizonToday); !ok || days != c.days || phrase != c.phrase {
			t.Errorf("%q: %d %q %v, want %d %q", c.text, days, phrase, ok, c.days, c.phrase)
		}
	}
	for _, text := range []string{"TSM FY2028 outlook", "TSM rose 300% over 2020-2024", "TSM vs the 30-year mortgage rate"} {
		if days, phrase, ok := explicitHorizon(text, horizonToday); ok {
			t.Errorf("%q: %d %q, want no timeframe", text, days, phrase)
		}
	}
	// A span past the longest outlook is the longest outlook, said so.
	days, h, _, warns, err = resolveHorizon(outlookInput{Symbol: "TSM"}, "TSM in 30 years", horizonToday)
	if err != nil || days != 7300 || h.Basis != "explicit" || !slices.Contains(warns, "30 years is past the longest outlook (20 years): showing 20 years") {
		t.Errorf("30 years: %d %+v %q %v", days, h, warns, err)
	}

	// A named date snaps: horizon_days within 25% is replaced silently; a span keeps it.
	h1200, h1825 := 1200, 1825
	for _, c := range []struct {
		text        string
		model, days int
	}{{"TSM before 2028?", h365, 450}, {"where is NVDA headed by 2030", h1200, 1546}, {"TSM in 5 years", h1825, 1825}, {"TSM 2026-2028 outlook", 700, 816}} {
		model := c.model
		days, h, _, warns, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &model}, c.text, horizonToday)
		if err != nil || days != c.days || h.Basis != "explicit" || len(warns) != 0 {
			t.Errorf("%q + %d: %d %+v %q %v", c.text, c.model, days, h, warns, err)
		}
	}
	// Scenarios written for the user's date are kept when horizon_days is within 25%; far off is still an error.
	sc := []market.ScenarioInput{{Name: "a"}}
	if days, _, _, _, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h1200, Scenarios: sc}, "NVDA by 2030", horizonToday); err != nil || days != 1546 {
		t.Errorf("snap with scenarios: %d %v", days, err)
	}
	if _, _, _, _, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h365, Scenarios: sc}, "NVDA by 2030", horizonToday); err == nil {
		t.Error("scenarios far from the named date: no error")
	}

	// Wording: the agent's choice, and a reason quoting the user shown as given.
	if _, h, _, _, _ := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h1826}, "is TSM a buy?", horizonToday); h.Basis != "agent" ||
		h.Reason != "Horizon chosen by the agent: 1,826 days (5 years)" {
		t.Errorf("agent basis: %+v", h)
	}
	// A reason quoting the user that names no timeframe the parser reads is the user's (basis "user").
	if _, h, _, _, _ := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h1826, HorizonReason: "the user asked about the long haul"}, "is TSM a buy?", horizonToday); h.Basis != "user" ||
		h.Reason != "The user asked about the long haul" {
		t.Errorf("user-quoting reason: %+v", h)
	}
	for in, want := range map[string]string{"The user asked through 2030": "The user asked through 2030", "long run: 5 years": "No timeframe given: long run: 5 years",
		"no timeframe given; a hold": "No timeframe given; a hold"} {
		if got := chosenReason(in); got != want {
			t.Errorf("chosenReason(%q) = %q, want %q", in, got, want)
		}
	}
	// The reason's own date reads when the text names none.
	if days, h, _, _, _ := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h1200, HorizonReason: "The user asked through 2030"}, "TSM long term?", horizonToday); days != 1546 ||
		h.Reason != `From your question: "through 2030"` {
		t.Errorf("reason through 2030: %d %+v", days, h)
	}
}

// The parser never panics and never names a horizon outside 1..MaxOutlookDays.
func FuzzHorizonParser(f *testing.F) {
	for _, s := range []string{"TSM in 2H 2027", "TSM in H1 '27", "Compare TSM 5 years vs 10 years", "TSM 2026-28 outlook", "between 3 and 5 years",
		"If I bought TSM today and held it for 10 years", "what's up with TSM in 5 years", "TSM in 30 years", "by Q2 '28", "2027-03-31", "5yr and 10yr",
		"NVDA ran 20% this week. Can it 10x?", "TSM from 2027 to 2030", "h2 2027 and 2H 2028 or 1h29",
		"within a year or two", "a year and a half", "1, 3 and 5 year", "1y, 3y, 5y", "by Dec 31", "31 Feb 2027", "by Q4", "EOY 27", "by Friday",
		"12/31/2026", "13/13/2027", "the next year or 3", "1,000, 2 years"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		for _, h := range explicitHorizons(text, horizonToday) {
			if h.days < 1 || h.days > market.MaxOutlookDays || h.at < 0 || h.at > len(text) {
				t.Fatalf("%q: %+v", text, h)
			}
		}
		targetMultiple(text)
	})
}

// The parser reads what the user wrote (today 2026-10-13):
// ranges to their far end, halves, a 12-month "next year", lists, dates
// without a year and shorthand, each with no override warning.
func TestHorizonReadsWhatTheUserWrote(t *testing.T) {
	today := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	to := func(y int, m time.Month, d int) int {
		return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Sub(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	}
	for _, c := range []struct {
		text         string
		model, days  int
		reason, want string
	}{
		{"Where will BTC be within a year or two?", 730, 730, "", "within a year or two"},
		{"ETH in a month or two?", 61, 61, "", "in a month or two"},
		{"NVDA over the next year or two", 730, 730, "", "over the next year or two"},
		{"Where will TSM be in 2 or 3 years?", 1096, 1096, "", "in 2 or 3 years"},
		{"Where will NVDA be in a year and a half?", 548, 548, "", "in a year and a half"},
		{"Where will NVDA be in one and a half years?", 548, 548, "", "in one and a half years"},
		{"Where will BTC be over the next year?", 365, 365, "", "over the next year"},
		{"Where will BTC be in the next year?", 365, 365, "", "in the next year"},
		{"Where will BTC be this time next year?", 365, 365, "", "this time next year"},
		{"BTC over the next 12 months", 365, 365, "", "over the next 12 months"},
		{"TSM 1y, 3y, 5y outlook", 365, 365, "", "1y"},
		{"TSM 1y, 3y, 5y outlook", 1096, 1096, "", "3y"},
		{"TSM 1y, 3y, 5y outlook", 1826, 1826, "", "5y"},
		{"TSM: 1, 3 and 5 year outlook", 365, 365, "", "1 year"},
		{"TSM: 1, 3 and 5 year outlook", 1096, 1096, "", "3 year"},
		{"Where will BTC be in 1 month, 6 months and 1 year?", 183, 183, "", "6 months"},
		{"Will BTC be above 150k by December?", 79, 79, "the user asked by December", "by December"},
		{"BTC EOY target?", 79, 79, "", "EOY"},
		{"BTC price by EOY 2027", 444, 444, "", "by EOY 2027"},
		{"SOL by 31 Dec 2026", 79, 79, "", "by 31 Dec 2026"},
		{"SOL by 12/31/2026", 79, 79, "", "12/31/2026"},
		{"SOL by 31/12/2026", 79, 79, "", "31/12/2026"},
		{"Where will SOL be by Dec 31?", 79, 79, "", "by Dec 31"},
		{"Will SOL hit $300 by March?", 169, to(2027, 3, 31), "", "by March"},
		{"How high can NVDA go by Q4?", 79, 79, "", "by Q4"},
		{"NVDA by Q1?", 169, to(2027, 3, 31), "", "by Q1"},
		{"ETH by EOM", 18, 18, "", "by EOM"},
		{"ETH by the end of the month", 18, 18, "", "by the end of the month"},
		{"TSM this decade", 1175, to(2029, 12, 31), "", "this decade"},
		{"Where will NVDA be by the end of the decade?", 1175, to(2029, 12, 31), "", "by the end of the decade"},
		{"What will NVDA be worth 10 years later?", 3653, 3653, "", "10 years later"},
		{"BTC by Friday", 3, to(2026, 10, 16), "", "by Friday"},
		{"BTC by next Friday", 3, to(2026, 10, 16), "", "by next Friday"},
		{"Can BTC double next year?", 444, to(2027, 12, 31), "", "next year"}, // bare "next year" is the calendar year
	} {
		model := c.model
		days, h, _, warns, err := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &model, HorizonReason: c.reason}, c.text, today)
		if err != nil || days != c.days || h.Basis != "explicit" || h.Reason != `From your question: "`+c.want+`"` || len(warns) != 0 {
			t.Errorf("%q + %d: %d %+v %q %v, want %d %q", c.text, c.model, days, h, warns, err, c.days, c.want)
		}
	}
	for _, text := range []string{"SOL by 03/04/2027", "TSM Q4 30% growth", "TSM Q4 earnings", "NVDA may 10x", "TSM in 1,000 years",
		"the 20, 50 and 200 day moving averages", "it IPO'd and 10 years later it had tripled", "TSM rose 40% in March",
		// A date with no year after "in" or "on" (or none) needs a forward word or an outlook word: a day behind is not next year's.
		"What happened to TSM in March and where is it heading?", "I bought TSM in March, what's the long-term outlook?",
		"In March I bought TSM. What's the outlook?", "I bought ETH on 15 Oct and it's down. Thoughts?", "I bought ETH on Oct 15. Thoughts?",
		"Since Dec 31 BTC is up 20%, now what?", "What happened on Monday?", "NVDA reports in Q4, any thoughts?"} {
		if days, phrase, ok := explicitHorizon(text, today); ok {
			t.Errorf("%q: %d %q, want no timeframe", text, days, phrase)
		}
	}
	for _, c := range []struct {
		text, phrase string
		days         int
	}{
		{"Where will SOL be in March?", "in March", to(2027, 3, 31)},
		{"SOL in March?", "in March", to(2027, 3, 31)},
		{"Will SOL, ETH and BTC rally in March?", "in March", to(2027, 3, 31)},
		{"SOL price on Dec 31", "on Dec 31", 79},
		{"What will BTC be on Friday?", "on Friday", to(2026, 10, 16)},
		{"Where will NVDA be in Q1?", "in Q1", to(2027, 3, 31)},
		{"I bought ETH on 15 Oct, where will it be in 3 months?", "in 3 months", 92},
		{"What happened to TSM in March? Where will it be in 2 years?", "in 2 years", 731},
		// "or M" is the far end only when M is a span's count, not a rate or a multiple
		{"Can TSM gain 50% in 5 years or 10% a year?", "in 5 years", 1826},
		{"Can ETH 2x in a year or 3x in two?", "in a year", 365},
	} {
		if days, phrase, ok := explicitHorizon(c.text, today); !ok || days != c.days || phrase != c.phrase {
			t.Errorf("%q: %d %q %v, want %d %q", c.text, days, phrase, ok, c.days, c.phrase)
		}
	}
	// A reason quoting a timeframe of the user's that neither text parses is the user's, as given.
	h1096 := 1096
	if _, h, _, _, _ := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h1096, HorizonReason: "the user asked about the next few years"}, "TSM in the next few years", today); h.Basis != "user" ||
		h.Reason != "The user asked about the next few years" {
		t.Errorf("user basis: %+v", h)
	}
	if _, h, _, _, _ := resolveHorizon(outlookInput{Symbol: "TSM", HorizonDays: &h1096, HorizonReason: "the user did not give a timeframe"}, "TSM?", today); h.Basis != "chosen" {
		t.Errorf("no timeframe: %+v", h)
	}
	// The chip: a date as a date, a span past a year in years, never a 4-digit day count.
	for _, c := range []struct{ in, text, want string }{
		{`{"symbol":"tsm","horizon_days":3366,"target_multiple":10}`, "Can TSM 10x by 2035?", "Modeling TSM price range to Dec 31, 2035"},
		{`{"symbol":"tsm","horizon_days":548}`, "TSM outlook?", "Modeling TSM 1.5-year price range"},
		{`{"symbol":"tsm","horizon_days":180}`, "TSM outlook?", "Modeling TSM 180-day price range"},
		{`{"symbol":"tsm","horizon_days":1540}`, "where is TSM by 2030", "Modeling TSM price range to Dec 31, 2030"},
		{`{"symbol":"tsm","horizon_days":444}`, "TSM through 2027", "Modeling TSM price range to Dec 31, 2027"},
	} {
		if got := outlookLabel(json.RawMessage(c.in), c.text, today); got != c.want {
			t.Errorf("outlookLabel(%s, %q) = %q, want %q", c.in, c.text, got, c.want)
		}
	}
}
