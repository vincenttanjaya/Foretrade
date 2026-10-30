package market

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// NumberInField reports whether text states value as field's kind of number:
// NumberIn's 0.5% match, but only for a number written in
// that field's form, after dates, times, years and period labels are taken
// out of the text. A P/E is "25x", "25×", "25 times earnings" or a number
// near "P/E" (not one near sales, revenue, EBITDA, book or EV/); an EPS is a
// number near "EPS" or "per share"; a growth, CAGR or move is a percentage; a
// level has a currency mark or code before it or "target"/"price" near it;
// a stress event's probability (0–1) is its percentage, or the share near
// "chance", "probability", "odds" or "likelihood". "Oct 25", "35 analysts"
// and "up 20 times since 2006" state no P/E. An unknown field is never
// stated.
func NumberInField(field string, value float64, text string) bool {
	if !fieldForms[field] || math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	text = stripNonMeasures(text)
	v := math.Abs(value)
	for _, loc := range numberRe.FindAllStringSubmatchIndex(text, -1) {
		n, err := strconv.ParseFloat(strings.ReplaceAll(text[loc[2]:loc[3]], ",", ""), 64)
		if err != nil || startsInsideNumber(text, loc[2]) {
			continue
		}
		scaled := n
		if loc[4] >= 0 {
			scaled = n * cmpScale(text[loc[4]:loc[5]])
		}
		near := func(c float64) bool { return math.Abs(v-c) <= 0.005*c }
		before, after := text[:loc[2]], text[loc[3]:]
		if loc[4] >= 0 {
			after = text[loc[5]:]
		}
		pct := pctSuffix.MatchString(text[loc[3]:])
		switch field {
		case "pe":
			if near(n) && !pct && !currencyBefore(before) && !citeNear(before, 20, otherMultiple) &&
				(timesSuffix.MatchString(text[loc[3]:]) || timesEarnings.MatchString(text[loc[3]:]) ||
					citeNear(before, 40, peWords) || citeAfter(after, 40, peWords)) {
				return true
			}
		case "eps":
			if near(n) && !pct && (citeNear(before, 40, epsWords) || citeAfter(after, 40, epsWords)) {
				return true
			}
		case "growthPct", "cagrPct", "movePct":
			if near(n) && pct {
				return true
			}
		case "level":
			if (near(n) || near(scaled)) && !pct && (currencyBefore(before) || codeAfter.MatchString(after) ||
				citeNear(before, 20, levelWords) || citeAfter(after, 20, levelWords)) {
				return true
			}
		case "eventP":
			if pct && near(n/100) || !pct && near(n) && (citeNear(before, 40, chanceWords) || citeAfter(after, 40, chanceWords)) {
				return true
			}
		}
	}
	return false
}

// EPSCurrency is the currency mark or code text writes value with wherever it
// states it as an EPS (NumberInField's form): "NT$" for "NT$134.2 per share",
// "TWD" for "134.2 TWD". "" when one such statement is in US dollars ("$",
// "US$", "USD") or carries no mark, or there is none: only a figure the text
// gives in another currency alone is one.
func EPSCurrency(value float64, text string) string {
	return statedCurrency(value, text, func(before, after string) bool {
		return citeNear(before, 40, epsWords) || citeAfter(after, 40, epsWords)
	})
}

// LevelCurrency is EPSCurrency for a price (NumberInField's level form): "NT$"
// for Citi's "target price ... NT$3,200" (a TWSE share) cited as a level on
// TSM's USD ADS card, where it is about 6.5× the per-ADS dollar price.
func LevelCurrency(value float64, text string) string {
	return statedCurrency(value, text, func(before, after string) bool {
		return currencyBefore(before) || codeAfter.MatchString(after) || citeNear(before, 20, levelWords) || citeAfter(after, 20, levelWords)
	})
}

// statedCurrency is EPSCurrency and LevelCurrency over the statements of
// value that form (the text before and after the number) accepts.
func statedCurrency(value float64, text string, form func(before, after string) bool) string {
	text = stripNonMeasures(text)
	v, mark := math.Abs(value), ""
	for _, loc := range numberRe.FindAllStringSubmatchIndex(text, -1) {
		n, err := strconv.ParseFloat(strings.ReplaceAll(text[loc[2]:loc[3]], ",", ""), 64)
		if err != nil || startsInsideNumber(text, loc[2]) || !(math.Abs(v-n) <= 0.005*n) || pctSuffix.MatchString(text[loc[3]:]) {
			continue
		}
		before, after := text[:loc[2]], text[loc[3]:]
		if !form(before, after) {
			continue
		}
		m := strings.TrimSpace(currencyMark.FindString(strings.TrimRight(before, " "))) // "NT$ 134.2" too
		if m == "" {
			m = strings.TrimSpace(codeAfter.FindString(after))
		}
		if m == "" || m == "$" || m == "US$" || m == "USD" {
			return ""
		}
		mark = m
	}
	return mark
}

// fieldForms are the fields NumberInField knows: scenario_model's inputs
// (and the chat outlook's), the level tool's, and stress_mix's.
var fieldForms = map[string]bool{"pe": true, "eps": true, "growthPct": true, "cagrPct": true, "movePct": true, "level": true, "eventP": true}

var (
	pctSuffix     = regexp.MustCompile(`^\s?(?:%|(?i:percent|per cent|pct)\b)`)
	timesSuffix   = regexp.MustCompile(`^\s?(?:[xX×])(?:$|[^A-Za-z])`)
	timesEarnings = regexp.MustCompile(`(?i)^\s*times\s+(?:(?:its\s+)?(?:forward|trailing|next[- ]year(?:'s)?|current)\s+)?earnings`)
	peWords       = regexp.MustCompile(`(?i)\bp/e\b|\bpe ratio|price[- ]to[- ]earnings|price/earnings|earnings multiple`)
	otherMultiple = regexp.MustCompile(`(?i)sales|revenue|ebitda|book|\bev/`)
	epsWords      = regexp.MustCompile(`(?i)\beps\b|earnings per share|per share`)
	levelWords    = regexp.MustCompile(`(?i)target|price`)
	chanceWords   = regexp.MustCompile(`(?i)chance|probabilit|odds|likelihood`)
	codeAfter     = regexp.MustCompile(`^\s?(?:USD|SGD|HKD|TWD|KRW|JPY|CNY|RMB|EUR|GBP|IDR|MYR|THB|VND|INR)\b`)
	currencyMark  = regexp.MustCompile(`(?:US\$|HK\$|NT\$|S\$|A\$|C\$|\$|€|£|¥|₩|(?:USD|SGD|HKD|TWD|KRW|JPY|CNY|RMB|EUR|GBP|IDR|MYR|THB|VND|INR)\s?)$`)
)

// citeNear reports whether re matches within the last n bytes of s.
func citeNear(s string, n int, re *regexp.Regexp) bool { return re.MatchString(s[max(len(s)-n, 0):]) }

// citeAfter reports whether re matches within the first n bytes of s.
func citeAfter(s string, n int, re *regexp.Regexp) bool { return re.MatchString(s[:min(n, len(s))]) }

// currencyBefore reports a currency mark or code directly before a number.
func currencyBefore(before string) bool { return currencyMark.MatchString(before) }

// startsInsideNumber reports a match that continues a longer token ("S3",
// "A1"): a letter or digit right before it.
func startsInsideNumber(text string, i int) bool {
	if i == 0 {
		return false
	}
	c := text[i-1]
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

const monthName = `(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`

// nonMeasures are the numbers in a text that are not measurements: dates,
// times and period labels.
var nonMeasures = []*regexp.Regexp{
	regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b`),
	regexp.MustCompile(`(?i)\b` + monthName + `\.?\s+\d{1,2}(?:st|nd|rd|th)?\b(?:,?\s+\d{4}\b)?`),
	regexp.MustCompile(`(?i)\b\d{1,2}(?:st|nd|rd|th)?\s+(?:of\s+)?` + monthName + `\b\.?(?:,?\s+\d{4}\b)?`),
	regexp.MustCompile(`\b\d{1,2}/\d{1,2}/\d{2,4}\b|\b\d{1,2}-\d{1,2}-\d{4}\b|\b\d{1,2}\.\d{1,2}\.\d{4}\b`),
	regexp.MustCompile(`(?i)\b\d{1,2}:\d{2}(?::\d{2})?(?:\s*[ap]\.?m\.?)?(?:\s*(?:UTC|GMT|[ECMP][SD]?T|SGT|HKT|JST|KST|ICT|WIB|CET|CEST|BST))?\b`),
	regexp.MustCompile(`(?i)\b\d{1,2}\s*[ap]\.?m\.?(?:\s|$|[,;)])`),
	regexp.MustCompile(`(?i)\b(?:FY|CY)\s?'?\d{2,4}\b|\bQ[1-4](?:\s?'?\d{2,4})?\b|\b[1-4]Q\s?'?\d{2,4}\b|\bH[12](?:\s?'?\d{2,4})?\b|\b[12]H\s?'?\d{2,4}\b`),
}

var yearRe = regexp.MustCompile(`\b(?:19|20)\d{2}\b`)

// stripNonMeasures blanks dates, times, period labels and standalone years
// (1900–2099 not after a currency mark, not before %, x or ×, and not part of
// a longer number) with spaces of the same length, so the distances the
// field forms measure stay as written.
func stripNonMeasures(text string) string {
	blank := func(s string, locs [][]int) string {
		b := []byte(s)
		for _, l := range locs {
			for i := l[0]; i < l[1]; i++ {
				b[i] = ' '
			}
		}
		return string(b)
	}
	for _, re := range nonMeasures {
		text = blank(text, re.FindAllStringIndex(text, -1))
	}
	var years [][]int
	for _, l := range yearRe.FindAllStringIndex(text, -1) {
		before, after := text[:l[0]], text[l[1]:]
		// Not a year: "$2026", "2026%", "2026x", or part of a longer number ("12,2026", "0.2026", "2026.5", "2,026").
		switch {
		case currencyBefore(before), pctSuffix.MatchString(after), timesSuffix.MatchString(after),
			strings.HasSuffix(before, ","), strings.HasSuffix(before, "."),
			len(after) > 1 && (after[0] == '.' || after[0] == ',') && after[1] >= '0' && after[1] <= '9':
		default:
			years = append(years, l)
		}
	}
	return blank(text, years)
}
