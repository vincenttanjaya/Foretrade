package market

import "testing"

// A cited number counts only in its field's form, never as a date, a time,
// a year or a count.
func TestNumberInField(t *testing.T) {
	const s3 = "TSMC third-quarter revenue beats estimates\nPublished Oct 25, 2026. TSMC said third-quarter revenue rose 30.4%.\n" +
		"HSINCHU, Oct 16, 2026 - TSMC posted revenue of NT$990 billion. Some 35 analysts cover the stock; shares are up 20 times since 2006."
	for _, c := range []struct {
		field string
		value float64
		text  string
		want  bool
	}{
		// The review's probe: every one of these was "verified" by a date or a count.
		{"pe", 25, s3, false},
		{"pe", 35, s3, false},
		{"pe", 20, s3, false},
		{"growthPct", 16, s3, false},
		{"pe", 35, "Some 35 analysts", false},
		{"pe", 20, "up 20 times since 2006", false},
		{"growthPct", 16, "Oct 16, 2026", false},
		{"level", 2026, "in 2026", false},
		{"pe", 13.5, "13:30 UTC", false},
		{"pe", 25, "$25", false},
		{"pe", 25, "25 October", false},
		{"pe", 25, "on 25 Oct 2026", false},
		{"growthPct", 10, "2026-10-01", false},
		{"growthPct", 12, "12/31/2026", false},
		{"pe", 27, "FY27 guidance", false},
		{"eps", 3, "Q3 EPS beat", false},
		{"growthPct", 26, "growth in 3Q26 of 5%", false},
		{"pe", 25, "an EV/EBITDA of 25x", false},
		{"pe", 4, "price-to-book of 4x", false},
		{"pe", 25, "sales multiple 25x", false},
		{"eps", 16, "EPS growth of 16%", false},
		{"level", 16, "EPS growth of 16%", false},
		{"growthPct", 16, "16 analysts", false},
		{"eventP", 0.25, "25 analysts", false},
		{"eps", 21.33, "21.33 units", false},
		{"unknown", 25, "25x", false},
		{"pe", 3, "S3 says nothing", false},
		{"level", 300, "at 3pm", false},

		{"pe", 22.5, "forward P/E of 22.5x", true},
		{"pe", 22.5, "22.5× forward earnings", true},
		{"pe", 25, "trades at 25 times earnings", true},
		{"pe", 25, "a price-to-earnings ratio near 25", true},
		{"eps", 21.33, "FY27 EPS $21.33", true},
		{"eps", 5.2, "earnings per share of $5.20", true},
		{"growthPct", 16, "EPS growth of 16%", true},
		{"cagrPct", 16, "a 16% CAGR", true},
		{"growthPct", 16, "grew 16 percent", true},
		{"movePct", -60, "the stock fell 60% in 2008", true},
		{"level", 1100, "price target $1,100", true},
		{"level", 1.2e12, "$1.2T market cap", true},
		{"level", 990, "revenue of NT$990 billion", true},
		{"level", 1100, "a target of 1,100", true},
		{"level", 2026, "a price of $2026", true},
		{"eventP", 0.25, "a 25% chance", true},
		{"eventP", 0.25, "a probability of 0.25", true},
		{"pe", 25, "Published Oct 25, 2026: a forward P/E of 25x", true},
	} {
		if got := NumberInField(c.field, c.value, c.text); got != c.want {
			t.Errorf("%s %v in %q: %v, want %v", c.field, c.value, c.text, got, c.want)
		}
	}
}

// An EPS a source gives only in another currency is that currency's: the
// simulator's TSMC consensus in NT$ per share, cited as the USD ADS card's EPS.
func TestEPSCurrency(t *testing.T) {
	const focus = "Market consensus compiled by Bloomberg now expects TSMC to post earnings of NT$108.6 per share in 2026 and NT$134.2 per share in 2027."
	for _, c := range []struct {
		value float64
		text  string
		want  string
	}{
		{134.2, focus, "NT$"},
		{108.6, focus, "NT$"},
		{152.4, "Citi lifted its 2027 EPS forecast to NT$152.4, about 14% above consensus", "NT$"},
		{4950, "2026 EPS of ₩4,950", "₩"},
		{134.2, "EPS of 134.2 TWD in 2027", "TWD"},
		{134.2, "EPS of HK$134.2", "HK$"},
		{134.2, "EPS of NT$ 134.2 per share in 2027", "NT$"},
		{21.25, "FY2027 EPS of US$ 21.25 per ADS", ""},
		{19.85, "raised its FY2027 earnings per share estimates for TSM to $19.85", ""},
		{21.25, "FY2027 EPS US$21.25 per ADS (NT$134.2 per share)", ""},
		{21.33, "FY27 EPS 21.33", ""},                                // unmarked: not another currency
		{134.2, "EPS NT$134.2; the ADR's EPS is $134.2", ""},         // stated once in dollars
		{2365, "TSMC shares closed at NT$2,365 in Taipei", ""},       // a price, not an EPS
		{17.6, "about 17.6 times 2027 consensus earnings", ""},       // a P/E
		{134.2, "Revenue rose 134.2% on NT$ strength; EPS beat", ""}, // a percentage
	} {
		if got := EPSCurrency(c.value, c.text); got != c.want {
			t.Errorf("EPSCurrency(%v, %q) = %q, want %q", c.value, c.text, got, c.want)
		}
	}
}

// A price a source gives only in another currency is that currency's: Citi's
// NT$ target for the TWSE share, cited as a level on TSM's USD ADS card.
func TestLevelCurrency(t *testing.T) {
	const citi = "Citi raised its target price for TSMC (2330) to NT$3,200 from NT$2,800 and lifted its 2027 EPS forecast to NT$152.4."
	for _, c := range []struct {
		value float64
		text  string
		want  string
	}{
		{3200, citi, "NT$"},
		{2800, citi, "NT$"},
		{2365, "TSMC shares closed at NT$2,365 in Taipei", "NT$"},
		{120000, "a 12-month target of ₩120,000 for Samsung", "₩"},
		{45.5, "DBS: target price S$45.50, Buy", "S$"},
		{3200, "price target 3,200 TWD", "TWD"},
		{640, "the high target is $640.00 and the low $385.00", ""},
		{521.4, "an average 12-month price target of US$521.40", ""},
		{3200, "a price target of 3,200", ""},                         // unmarked
		{3200, "NT$3,200 for the TWSE share, $3,200 for nothing", ""}, // stated once in dollars
		{17.6, "about 17.6 times 2027 consensus earnings", ""},        // a P/E, not a price
	} {
		if got := LevelCurrency(c.value, c.text); got != c.want {
			t.Errorf("LevelCurrency(%v, %q) = %q, want %q", c.value, c.text, got, c.want)
		}
	}
}
