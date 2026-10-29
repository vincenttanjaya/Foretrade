package chat

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// The price_outlook card's horizon follows the user's words, never a default:
// an explicit timeframe in the message overrides the model's horizon_days, and
// "can it 10x, by when" questions get the time-to-target view.

var (
	numWord  = `(\d+(?:\.\d+)?|a|an|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|fifteen|twenty)`
	unitWord = `(day|week|month|quarter|year|yr|decade)s?`
	enumUnit = `(day|week|month|quarter|year|yr|y|decade)s?`
	// "3-5 years", "12–18 months", "3 to 5 years": a range runs to its far end
	rangeFrom = `(?:\d+(?:\.\d+)?\s*(?:-|–|to)\s*)?`
	// "in 5 years", "within 3 months", "over the next 10 years", "next 2 weeks", "within a decade"; not "the last 5 years" or "5 years ago".
	spanRe = regexp.MustCompile(`(?i)\b(?:in|within|over|next|for)\s+(?:the\s+)?(?:next\s+|coming\s+)?` + rangeFrom + numWord + `\s*-?\s*` + unitWord + `\b(\s+ago)?`)
	// "5 years from now", "a decade from today", "two years out"
	fromNowRe = regexp.MustCompile(`(?i)\b` + numWord + `\s*-?\s*` + unitWord + `\s+(?:from\s+(?:now|today)|out|ahead|hence|later)\b`)
	// "over the coming year", "the next decade": one unit, no number
	comingRe = regexp.MustCompile(`(?i)\b(?:(?:the\s+)?coming\s+(year|month|week|quarter|decade)|(?:the\s+)?next\s+(decade))\b`)
	// "a 5-year outlook", "10-year view"; not "the 200-day moving average", "its 52-week high" or "the 10-year yield"
	hyphenRe    = regexp.MustCompile(`(?i)\b(\d+)-` + unitWord + `\b`)
	notHorizonA = regexp.MustCompile(`(?i)^[\s-]*(?:high|low|highs|lows|moving|average|avg|ma|sma|ema|rsi|yield|yields|treasury|treasuries|note|notes|bond|bonds|bill|bills|gilt|bund|jgb|rate|rates|range|chart|return|returns|cagr|change|performance|history|track|record|old|ago|lookback|window|realized|vol|volatility|beta|correlation|mortgage|mortgages|loan|loans|tips|swap|swaps)\b`)
	notHorizonB = regexp.MustCompile(`(?i)\b(?:last|past|trailing|prior|previous)\s+$`)
	// "by 2031", "by the end of 2031", "end of 2027", "end 2027", "before 2028" (the end of 2027), "through 2028", "till 2030"
	endYearRe = regexp.MustCompile(`(?i)\b(?:(?:by|before|in|until|till|til|through|thru|up\s+to|at)\s+(?:the\s+)?(?:end\s+(?:of\s+)?)?|(?:the\s+)?end\s+(?:of\s+)?)(20[2-9]\d)\b`)
	// "2026-2028", "2026–28", "from 2027 to 2030", "over 2027-2030": a range of years runs to the far year's end
	yearRangeRe = regexp.MustCompile(`(?i)\b(?:(?:from|over|in|for|between|during)\s+)?(20[2-9]\d)\s*(?:-|–|—|to|through|thru)\s*(20[2-9]\d|[2-9]\d)\b`)
	rangeAfter  = regexp.MustCompile(`^\s*(?:-|–|—|to|through|thru)\s*(?:20)?[2-9]\d\b`)
	// "H1 2027" (June 30), "in H2 '27" (December 31); "FY2028" is left alone: fiscal years end differently by issuer
	halfRe = regexp.MustCompile(`(?i)\b(?:(by|before|in|until|till|through|end\s+of)\s+(?:the\s+)?)?(?:h([12])|([12])h)\s*(?:of\s+)?(?:'([2-9]\d)|(20[2-9]\d))\b`)
	// "5 years vs 10 years", "in 5 and 10 years", "5yr and 10yr": one phrase per number (a shared unit is the second's)
	enumRe = regexp.MustCompile(`(?i)\b(?:(?:in|within|over|for|next)\s+(?:the\s+)?(?:next\s+|coming\s+)?)?` + numWord + `(?:\s*-?\s*` + enumUnit + `)?` +
		`\s*,?\s+(?:vs\.?|versus|and|or|&)\s+(?:(?:in|within|over|for)\s+)?` + numWord + `\s*-?\s*` + enumUnit + `\b`)
	// "NVDA 10 years outlook", "10 year outlook", "10yr view", "ten year forecast", "5y outlook", "TSM 5 years":
	// a bare span of years followed by an outlook word or ending the message.
	bareYearsRe = regexp.MustCompile(`(?i)\b` + rangeFrom + numWord + `\s*-?\s*(years?|yrs?|y|months?)\b`)
	outlookWord = regexp.MustCompile(`(?i)^\s*-?\s*(outlook|view|forecast|horizon|plan|target|projection|prediction|price|call|thesis|scenario|picture|hold|holding|investment)s?\b`)
	endOfText   = regexp.MustCompile(`^[\s?.!]*$`)
	// "compounding 15% a year", "once a year": a rate or a frequency, not a span ahead
	rateBefore = regexp.MustCompile(`(?i)(?:%|\bpercent|\bpct|\bpts?|\bpoints?|\bbps|\bonce|\btwice|\btimes|\bper|\beach|\bevery)\s*$`)
	// a forward word in the clause: "the expected return over 5 years" is ahead, "the return over 5 years" is behind
	forwardRe = regexp.MustCompile(`(?i)\b(?:will|would|could|can|might|may|should|expected|expect|expecting|forecast|forecasts|projected|project|future|going\s+to|if)\b`)
	// a span the user names as the period behind a statistic, not ahead: "CAGR 10 years", "returns over 5 years"
	notHorizonC = regexp.MustCompile(`(?i)\b(?:cagr|return|returns|performance|volatility|vol|change|history|chart|high|low|drawdown|average|avg|yield|record)\s+(?:of\s+|over\s+|for\s+|in\s+)?$`)
	// "over the next 5 years", "the coming year": ahead by construction, whatever verb precedes it
	forwardSpan = regexp.MustCompile(`(?i)\b(?:next|coming)\b`)
	// spanRe's prepositions: a span after one is spanRe's to read (or skip, as the past)
	spanPrep = regexp.MustCompile(`(?i)\b(?:in|within|over|next|for|coming)\s+(?:the\s+)?$`)
	// "it doubled in 5 years", "TSM went up 300% over 10 years": a past move over a span, not a horizon
	pastRe = regexp.MustCompile(`(?i)\b(?:risen|fallen|grown|been|gone|sunk|shrunk|dipped|lagged|doubled|tripled|quadrupled|halved|rose|fell|grew|gained|lost|returned|rallied|surged|dropped|crashed|tanked|plunged|sank|slid|slumped|soared|jumped|climbed|went|ran|made|did|done|performed|traded|held|owned|increased|decreased|declined|compounded|delivered|rebounded|recovered|moved|multiplied|appreciated|outperformed|underperformed)\b(?:\s+\S+){0,3}\s*$`)
	// "TSM's up 20% in 5 days", "NVDA is up 200%": a move with its subject; "what's up with TSM" is not one
	upDownRe = regexp.MustCompile(`(?i)\b(\w+)(?:\s+(?:is|are|was|were)|'s|’s)\s+(?:up|down)\b(?:\s+\S+){0,3}\s*$`)
	// "If I bought TSM today and held it for 10 years": a hypothetical holding period, not a past move
	holdRe       = regexp.MustCompile(`(?i)\b(?:held|hold|holding|kept|keep|owned|own)\b(?:\s+\S+){0,3}\s*$`)
	ifWouldRe    = regexp.MustCompile(`(?i)\bif\b|\bwould\b|\bwere\s+to\b`)
	entryAndRe   = regexp.MustCompile(`(?i)\b(?:buy|buying|invest|investing|put|putting|enter|entering)\b.*\band\b|\b(?:bought|invested|put|entered)\b.*\b(?:today|now)\b.*\band\b`)
	entryTodayRe = regexp.MustCompile(`(?i)\b(?:bought|buy|buying|invest|invested|investing|put|putting|enter|entered|entering)\b(?:\s+\S+){0,5}\s*$`)
	// "TSM 2027", "TSM price 2027": a bare year after a ticker or a price word; not "TSM 2027 EPS"
	bareYearRe = regexp.MustCompile(`\b(20[2-9]\d)\b`)
	priceWord  = regexp.MustCompile(`(?i)^(?:price|prices|target|outlook|forecast|view|projection|prediction|level|close|valuation)$`)
	tickerTok  = regexp.MustCompile(`^[A-Z][A-Z0-9.&]{1,7}$`)
	notTicker  = map[string]bool{"EPS": true, "FY": true, "GDP": true, "CPI": true, "PE": true, "AI": true, "IPO": true, "ETF": true, "USD": true,
		"CAGR": true, "YOY": true, "ATH": true, "EOY": true, "EOM": true, "EOQ": true, "H1": true, "H2": true, "Q1": true, "Q2": true, "Q3": true, "Q4": true, "THE": true, "IN": true, "BY": true}
	notHorizonYear = regexp.MustCompile(`(?i)^\s*(?:fy|fiscal|eps|earnings|revenue|revenues|sales|guidance|capex|margin|margins|dividend|dividends|results|consensus|estimate|estimates|profit|profits|income|ebitda|cash|budget|numbers|report|growth|shipments|roadmap|node|nodes|chip|chips|production|launch|ipo|election|q[1-4]|h[12])\b`)
	midYearRe      = regexp.MustCompile(`(?i)\bmid[- ]?(20[2-9]\d)\b`)
	quarterRe      = regexp.MustCompile(`(?i)\b(?:by\s+|in\s+|before\s+|until\s+|end\s+of\s+)?(?:the\s+)?q([1-4])\s*(?:of\s+)?(?:'([2-9]\d)|(20[2-9]\d))\b`)
	monthRe        = regexp.MustCompile(`(?i)\b(?:by|before|in|until|on|end\s+of)\s+(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?(?:\s+(\d{1,2})(?:st|nd|rd|th)?)?,?\s+(20[2-9]\d)\b`)
	isoRe          = regexp.MustCompile(`\b(20[2-9]\d)-(\d\d)-(\d\d)\b`)
	periodRe       = regexp.MustCompile(`(?i)\b(?:by\s+)?(?:the\s+)?(end\s+of\s+(?:the\s+|this\s+)?year|year[- ]end|next\s+year|this\s+year|this\s+quarter|end\s+of\s+(?:the\s+|this\s+)?quarter|next\s+quarter|this\s+week|next\s+week|end\s+of\s+(?:the\s+|this\s+)?month|this\s+month|next\s+month|today|tomorrow)\b`)

	// gap round G18: what the user wrote, read to its end
	// "within a year or two", "a month or two", "in 2 years or 3": a span's far end (a unit after M is enumRe's)
	orMoreRe = regexp.MustCompile(`(?i)^\s+or\s+` + numWord + `\b`)
	unitNext = regexp.MustCompile(`(?i)^\s*-?\s*` + enumUnit + `\b`)
	// "in 5 years or 10% a year", "in a year or 2x": M is a rate, a multiple or an amount, not the span's far end
	notFarEnd = regexp.MustCompile(`(?i)^\s*(?:%|percent\b|pct\b|bps?\b|x\b|×|[$€£¥₩]|[.,]\d)`)
	// "a year and a half", "in 2 and a half years", "one and a half years": N.5 units
	andHalfRe = regexp.MustCompile(`(?i)\b(?:(?:in|within|over|for|next)\s+(?:the\s+)?(?:next\s+|coming\s+)?)?(?:` + numWord + `\s+` + unitWord +
		`\s+and\s+a\s+half|` + numWord + `\s+and\s+a\s+half\s+` + unitWord + `)\b`)
	// "over the next year", "in the next year", "the next year", "this time next year": 12 months ahead; bare "next year" is the calendar year
	nextYearSpanRe = regexp.MustCompile(`(?i)\b(?:(?:(?:over|in|within|for|during|through)\s+)?the\s+next\s+year|this\s+time\s+next\s+year)\b`)
	// "1, 3 and 5 year outlook", "1y, 3y, 5y", "in 1 month, 6 months and 1 year": one phrase per number, the last unit shared
	listNum  = `(\d+(?:\.\d+)?|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|fifteen|twenty)`
	listItem = listNum + `(?:\s*-?\s*` + enumUnit + `)?`
	listRe   = regexp.MustCompile(`(?i)\b(?:(?:in|within|over|for|next)\s+(?:the\s+)?)?` + listItem + `(?:\s*,\s*(?:in\s+)?` + listItem + `)+` +
		`(?:\s*,?\s*(?:and|or|&)\s+(?:in\s+)?` + listItem + `)?`)
	thousands = regexp.MustCompile(`\d,\d{3}\b`) // "1,000 years" is one number
	itemRe    = regexp.MustCompile(`(?i)\b` + listNum + `(?:\s*-?\s*` + enumUnit + `\b)?`)
	monthAlt  = `jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?`
	// "by December", "by Dec 31", "Dec 31", "end of March": a month or a day in it, its next end (a year after it is that year)
	monthNoYearRe = regexp.MustCompile(`(?i)\b(?:(by|before|in|until|till|on|through|end\s+of)\s+)?(` + monthAlt + `)\.?(?:\s+(\d{1,2})(?:st|nd|rd|th)?)?\b`)
	yearAfterRe   = regexp.MustCompile(`^,?\s+(20[2-9]\d)\b`)
	// "31 Dec 2026", "by 31 December", "the 31st of December"
	dayFirstRe = regexp.MustCompile(`(?i)\b(?:(by|before|until|till|on|through)\s+(?:the\s+)?)?(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?(` + monthAlt + `)\b\.?(?:,?\s+(20[2-9]\d)\b)?`)
	// "by Q4", "in Q1", "TSM Q4 outlook": the next such quarter end
	quarterNoYearRe = regexp.MustCompile(`(?i)\b(?:(by|before|in|until|till|through|end\s+of)\s+(?:the\s+)?)?(?:q([1-4])|([1-4])q)\b`)
	quarterYearNext = regexp.MustCompile(`(?i)^\s*(?:of\s+)?(?:'?\d{2}\b|20[2-9]\d)`)
	// "EOY", "by EOY 2027", "EOM", "EOQ"
	eoRe = regexp.MustCompile(`(?i)\b(?:by\s+)?(eoy|eom|eoq)\b(?:\s+'?(20[2-9]\d|[2-9]\d)\b)?`)
	// "by Friday", "by next Friday": the coming weekday
	weekdayRe = regexp.MustCompile(`(?i)\b(?:by|before|until|till|on)\s+(?:(?:next|this|the\s+coming)\s+)?(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)
	// "by the end of the decade", "this decade": Dec 31 of the decade's last year
	decadeRe = regexp.MustCompile(`(?i)\b(?:by\s+)?(?:the\s+)?(?:end\s+of\s+(?:the|this)\s+decade|this\s+decade)\b`)
	// "12/31/2026", "31/12/2026": only when one part is over 12 (03/04/2027 is ambiguous)
	numDateRe = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(20[2-9]\d)\b`)
	weekdays  = map[string]time.Weekday{"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6}

	multWord   = `(\d+(?:\.\d+)?|two|three|four|five|six|seven|eight|nine|ten|twenty|fifty|hundred)`
	multipleRe = regexp.MustCompile(`(?i)\b` + multWord + `\s*-?\s*(?:x\b|×|fold\b|times\b)`)
	// "trades at 25x earnings", "a 30x P/E", "3x leverage": a valuation or leverage multiple, not a price target
	notTargetA = regexp.MustCompile(`(?i)^\s*(?:forward\s+|trailing\s+|fwd\s+)?(?:earnings|sales|revenue|revenues|ebitda|ebit|book|fcf|free\s+cash|cash\s+flow|eps|p/?e\b|pe\b|p/?s\b|p/?b\b|ev\b|multiple|leverage|leveraged|margin|levered|a\s+(?:day|week|month|year)|per\s|each\s)`)
	notTargetB = regexp.MustCompile(`(?i)(?:p/?e|multiple|ev/ebitda|valued|valuation|trades|trading|traded)\s+(?:ratio\s+)?(?:of\s+|at\s+|is\s+|near\s+|around\s+)?(?:about\s+|roughly\s+|~)?$`)
	baggerRe   = regexp.MustCompile(`(?i)\b(\d+|ten|five|two|three)-?\s?bagger`)
	doubleRe   = regexp.MustCompile(`(?i)\b(doubl|tripl|quadrupl)(?:e|es|ed|ing)\b`)
	byWhenRe   = regexp.MustCompile(`(?i)\bby when\b|\bhow long\b|\bwhen (?:will|could|can|would|might|does|do|is|are|should)\b|\bhow many years\b|\bhow soon\b|\bhow fast\b|\b(?:what|which) year\b|\btime ?line\b|\btime ?frame\b|\bwhen\s*(?:[?!.,;]|$)`)
	// "TSMC 10x when", "10x, and when": the multiple asked by when
	// ("can it 2x when rates fall" is a condition, not a by-when question)
	multipleWhen = regexp.MustCompile(`(?i)^\s*[,?]?\s*(?:(?:and|if so|if yes)\s*,?\s*when\b|(?:(?:and|if so|if yes)\s*,?\s*)?by\s+when\b|when\s*(?:[?!.,;]|$))`)
)

var words = map[string]float64{"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7,
	"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "fifteen": 15, "twenty": 20, "fifty": 50, "hundred": 100}

var months = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

// horizonPhrase is one timeframe the user named: where it starts in the text,
// the days from today to the end of the named period, and the phrase. dated
// is a phrase that names a date ("by 2030", "Q4 2027", "this year"), not a
// span of time; capped holds a span's words ("30 years") when it ran past
// MaxOutlookDays, and days is then the longest outlook.
type horizonPhrase struct {
	at     int
	days   int
	phrase string
	dated  bool
	capped string
}

// explicitHorizon is the timeframe the user named, in days from today to the
// end of the named period, with the phrase it came from; the earliest phrase
// in the text wins. Not ok without one, or outside 1..MaxOutlookDays.
func explicitHorizon(text string, today time.Time) (days int, phrase string, ok bool) {
	all := explicitHorizons(text, today)
	if len(all) == 0 {
		return 0, "", false
	}
	return all[0].days, all[0].phrase, true
}

// explicitHorizons is every timeframe the user named, earliest first (one per
// position), each within 1..MaxOutlookDays; a span past it is capped there.
func explicitHorizons(text string, today time.Time) []horizonPhrase {
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	var out []horizonPhrase
	add := func(at int, p string, end time.Time, dated bool, capped string) {
		d := int(end.Sub(today).Hours() / 24)
		if d > market.MaxOutlookDays && d <= market.MaxOutlookDays+7 {
			d = market.MaxOutlookDays // "20 years" spans leap days: the longest view
		}
		if capped != "" {
			d = market.MaxOutlookDays
		}
		if d < 1 || d > market.MaxOutlookDays || slices.ContainsFunc(out, func(h horizonPhrase) bool { return h.at == at }) {
			return
		}
		out = append(out, horizonPhrase{at, d, strings.TrimSpace(p), dated, capped})
	}
	try := func(at int, p string, end time.Time) { add(at, p, end, true, "") }
	// a span ("in 30 years") past the longest outlook is that outlook, said so
	trySpan := func(at int, p string, n float64, unit string) {
		end, capped := addSpan(today, n, unit), ""
		if int(end.Sub(today).Hours()/24) > market.MaxOutlookDays+7 {
			capped = spanWords(n, unit)
		}
		add(at, p, end, false, capped)
	}
	// a span's "or M" runs to its far end: "within a year or two" is 2 years
	farEnd := func(n float64, end int) (float64, int) {
		if o := orMoreRe.FindStringSubmatchIndex(text[end:]); o != nil && !unitNext.MatchString(text[end+o[1]:]) && !notFarEnd.MatchString(text[end+o[1]:]) {
			if far := amount(text[end+o[2] : end+o[3]]); far > n {
				return far, end + o[1]
			}
		}
		return n, end
	}
	for _, m := range andHalfRe.FindAllStringSubmatchIndex(text, -1) {
		if past(text[:m[0]]) && !forwardSpan.MatchString(text[m[0]:m[1]]) || lookback(text[:m[0]], text[m[0]:m[1]]) {
			continue
		}
		g := 2 // "a year and a half", else "one and a half years"
		if m[2] < 0 {
			g = 6
		}
		trySpan(m[0], text[m[0]:m[1]], amount(text[m[g]:m[g+1]])+0.5, strings.ToLower(text[m[g+2]:m[g+3]]))
	}
	for _, m := range listRe.FindAllStringIndex(text, -1) {
		before, after := text[:m[0]], text[m[1]:]
		if thousands.MatchString(text[m[0]:m[1]]) || past(before) || notHorizonA.MatchString(after) || notHorizonB.MatchString(before) || lookback(before, text[m[0]:m[1]]) ||
			rateBefore.MatchString(before) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(after)), "ago") {
			continue // "the 20, 50 and 200 day moving averages", "1, 3 and 5 years ago"
		}
		items := itemRe.FindAllStringSubmatchIndex(text[m[0]:m[1]], -1)
		if len(items) < 2 || items[len(items)-1][4] < 0 {
			continue // the last number names the unit: "1, 3 and 5 year"
		}
		unit := ""
		for i := len(items) - 1; i >= 0; i-- {
			it := items[i]
			if it[4] >= 0 {
				unit = strings.ToLower(text[m[0]+it[4] : m[0]+it[5]])
			}
			at, p := m[0]+it[0], text[m[0]+it[0]:m[0]+it[1]]
			if it[4] < 0 {
				p += " " + unit // "1" of "1, 3 and 5 year outlook" names 1 year
			}
			if i == 0 {
				at = m[0] // the list's own start: a span reading its first item ("in 1 month") is the same phrase
			}
			trySpan(at, p, amount(text[m[0]+it[2]:m[0]+it[3]]), unit)
		}
	}
	for _, m := range nextYearSpanRe.FindAllStringIndex(text, -1) {
		if past(text[:m[0]]) {
			continue
		}
		n, end := farEnd(1, m[1])
		trySpan(m[0], text[m[0]:end], n, "year")
	}
	for _, m := range spanRe.FindAllStringSubmatchIndex(text, -1) {
		// "… ago", "it doubled in 5 years": the past; "if it ran like NVDA over the next 5 years" is ahead
		if m[6] >= 0 || past(text[:m[0]]) && !forwardSpan.MatchString(text[m[0]:m[1]]) {
			continue
		}
		if notHorizonA.MatchString(text[m[1]:]) || lookback(text[:m[0]], text[m[0]:m[1]]) {
			continue // "over the 200 day moving average", "TSM's CAGR over 10 years"
		}
		n, end := farEnd(amount(text[m[2]:m[3]]), m[1])
		trySpan(m[0], text[m[0]:end], n, strings.ToLower(text[m[4]:m[5]]))
	}
	for _, m := range fromNowRe.FindAllStringSubmatchIndex(text, -1) {
		if strings.HasSuffix(strings.ToLower(text[m[0]:m[1]]), "later") && (past(text[:m[0]]) || !forwardRe.MatchString(clauseOf(text[:m[0]]))) {
			continue // "what will it be worth 10 years later" is ahead; "it IPO'd and 10 years later it had tripled" is not
		}
		trySpan(m[0], text[m[0]:m[1]], amount(text[m[2]:m[3]]), strings.ToLower(text[m[4]:m[5]]))
	}
	for _, m := range comingRe.FindAllStringSubmatchIndex(text, -1) {
		g := 2 // "coming year", else "next decade"
		if m[2] < 0 {
			g = 4
		}
		n, end := farEnd(1, m[1])
		trySpan(m[0], text[m[0]:end], n, strings.ToLower(text[m[g]:m[g+1]]))
	}
	for _, m := range hyphenRe.FindAllStringSubmatchIndex(text, -1) {
		if notHorizonA.MatchString(text[m[1]:]) || notHorizonB.MatchString(text[:m[0]]) || past(text[:m[0]]) {
			continue // an indicator's lookback or a bond's tenor, not a horizon
		}
		trySpan(m[0], text[m[0]:m[1]], amount(text[m[2]:m[3]]), strings.ToLower(text[m[4]:m[5]]))
	}
	for _, m := range bareYearsRe.FindAllStringSubmatchIndex(text, -1) {
		before, after := text[:m[0]], text[m[1]:]
		end := m[1]
		if w := outlookWord.FindStringSubmatchIndex(after); w != nil {
			if strings.EqualFold(after[w[2]:w[3]], "price") && notHorizonA.MatchString(after[w[1]:]) {
				continue // "a 5 year price return"
			}
			end += w[1]
		} else if !endOfText.MatchString(after) {
			continue
		}
		if spanPrep.MatchString(before) || notHorizonB.MatchString(before) || notHorizonC.MatchString(before) || past(before) || strings.HasSuffix(before, "-") {
			continue
		}
		if n := strings.ToLower(text[m[2]:m[3]]); rateBefore.MatchString(before) || ((n == "a" || n == "an") && end == m[1]) {
			continue // "15% a year", "once a year"; a bare "a year" needs an outlook word ("a year view")
		}
		unit := "year"
		if strings.HasPrefix(strings.ToLower(text[m[4]:m[5]]), "month") {
			unit = "month"
		}
		trySpan(m[0], text[m[0]:end], amount(text[m[2]:m[3]]), unit)
	}
	for _, m := range enumRe.FindAllStringSubmatchIndex(text, -1) {
		before := text[:m[0]]
		if past(before) || notHorizonA.MatchString(text[m[1]:]) || notHorizonB.MatchString(before) || lookback(before, text[m[0]:m[1]]) || rateBefore.MatchString(before) {
			continue // "the 50 and 200 day moving averages", "it fell in 5 and 10 days"
		}
		u2 := strings.ToLower(text[m[8]:m[9]])
		unit1, p1 := u2, text[m[2]:m[3]]+" "+text[m[8]:m[1]] // a shared unit: "in 5 and 10 years" names 5 years
		if amount(text[m[2]:m[3]]) == 1 {
			p1 = text[m[2]:m[3]] + " " + text[m[8]:m[9]] // "in 1 or 2 days" names 1 day
		}
		if m[4] >= 0 {
			end := m[5]
			if end < len(text) && (text[end] == 's' || text[end] == 'S') {
				end++
			}
			unit1, p1 = strings.ToLower(text[m[4]:m[5]]), text[m[2]:end]
		}
		if conj := strings.ToLower(strings.Fields(text[m[2]:m[6]])[len(strings.Fields(text[m[2]:m[6]]))-1]); conj == "or" && m[4] < 0 &&
			amount(text[m[6]:m[7]]) > amount(text[m[2]:m[3]]) {
			// "in 2 or 3 years" runs to its far end, as a range does; the near end stays readable for a horizon_days that names it
			trySpan(m[0], text[m[0]:m[1]], amount(text[m[6]:m[7]]), u2)
			trySpan(m[0]+1, p1, amount(text[m[2]:m[3]]), unit1)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(strings.TrimRight(before, " ")), "between") { // "between 3 and 5 years" is a range: its far end
			trySpan(m[0], p1, amount(text[m[2]:m[3]]), unit1)
		}
		trySpan(m[6], text[m[6]:m[1]], amount(text[m[6]:m[7]]), u2)
	}
	for _, m := range bareYearRe.FindAllStringSubmatchIndex(text, -1) {
		before := strings.TrimRight(text[:m[0]], " \t")
		if after := text[m[1]:]; len(before) == len(text[:m[0]]) || notHorizonYear.MatchString(after) || strings.HasPrefix(after, "-") || strings.HasPrefix(after, "/") || rangeAfter.MatchString(after) {
			continue // "FY2027", "TSM 2027 EPS" (a fiscal year), a date's year or a range's near year
		}
		start := strings.LastIndexAny(before, " \t\n(") + 1
		tok := strings.TrimRight(before[start:], ":,")
		if !priceWord.MatchString(tok) && !isTicker(tok) || past(text[:start]) {
			continue
		}
		y, _ := strconv.Atoi(text[m[2]:m[3]])
		try(start, text[start:m[1]], time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC))
	}
	for _, m := range monthRe.FindAllStringSubmatchIndex(text, -1) {
		if past(text[:m[0]]) {
			continue
		}
		y, _ := strconv.Atoi(text[m[6]:m[7]])
		mo := months[strings.ToLower(text[m[2]:m[2]+3])]
		end := time.Date(y, mo+1, 0, 0, 0, 0, 0, time.UTC)
		if m[4] >= 0 {
			if d, _ := strconv.Atoi(text[m[4]:m[5]]); d >= 1 && d <= end.Day() {
				end = time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
			}
		}
		try(m[0], text[m[0]:m[1]], end)
	}
	for _, m := range monthNoYearRe.FindAllStringSubmatchIndex(text, -1) {
		mon, after := text[m[4]:m[5]], text[m[1]:]
		if m[2] < 0 && m[6] < 0 || strings.EqualFold(mon, "may") && mon != "May" && m[2] < 0 || past(text[:m[0]]) {
			continue // a month needs "by", "in" … or a day ("Dec 31"); "may" is a verb
		}
		mo := months[strings.ToLower(mon[:3])]
		y, fixed := today.Year(), false
		if ya := yearAfterRe.FindStringSubmatch(after); ya != nil {
			y, _ = strconv.Atoi(ya[1])
			fixed = true
		}
		if !fixed && ambiguousPrep(text, m[2], m[3]) && !ahead(text[:m[0]], after) {
			continue // "What happened to TSM in March", "Since Dec 31": behind, not next year's
		}
		end := time.Date(y, mo+1, 0, 0, 0, 0, 0, time.UTC)
		if m[6] >= 0 {
			d, _ := strconv.Atoi(text[m[6]:m[7]])
			if d < 1 || d > end.Day() {
				continue
			}
			end = time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
		}
		if !fixed && end.Before(today) { // this year's has passed: next year's
			end = time.Date(end.Year()+1, end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
			if m[6] < 0 {
				end = time.Date(y+1, mo+1, 0, 0, 0, 0, 0, time.UTC)
			}
		}
		try(m[0], text[m[0]:m[1]], end)
	}
	for _, m := range dayFirstRe.FindAllStringSubmatchIndex(text, -1) {
		if past(text[:m[0]]) || strings.EqualFold(text[m[6]:m[7]], "may") && text[m[6]:m[7]] != "May" ||
			m[8] < 0 && ambiguousPrep(text, m[2], m[3]) && !ahead(text[:m[0]], text[m[1]:]) {
			continue // "I bought ETH on 15 Oct": behind
		}
		d, _ := strconv.Atoi(text[m[4]:m[5]])
		mo := months[strings.ToLower(text[m[6]:m[6]+3])]
		y := today.Year()
		if m[8] >= 0 {
			y, _ = strconv.Atoi(text[m[8]:m[9]])
		}
		if d < 1 || d > time.Date(y, mo+1, 0, 0, 0, 0, 0, time.UTC).Day() {
			continue
		}
		end := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
		if m[8] < 0 && end.Before(today) {
			end = end.AddDate(1, 0, 0)
		}
		try(m[0], text[m[0]:m[1]], end)
	}
	for _, m := range numDateRe.FindAllStringSubmatchIndex(text, -1) {
		a, _ := strconv.Atoi(text[m[2]:m[3]])
		b, _ := strconv.Atoi(text[m[4]:m[5]])
		y, _ := strconv.Atoi(text[m[6]:m[7]])
		mo, d := a, b // 12/31/2026
		switch {
		case a > 12 && b <= 12: // 31/12/2026
			mo, d = b, a
		case b > 12 && a <= 12:
		default:
			continue // 03/04/2027: March 4 or 3 April
		}
		if end := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC); end.Day() == d && !past(text[:m[0]]) {
			try(m[0], text[m[0]:m[1]], end)
		}
	}
	for _, m := range quarterNoYearRe.FindAllStringSubmatchIndex(text, -1) {
		after := text[m[1]:]
		if quarterYearNext.MatchString(after) || notHorizonYear.MatchString(after) || past(text[:m[0]]) ||
			m[2] < 0 && !outlookWord.MatchString(after) && !endOfText.MatchString(after) ||
			ambiguousPrep(text, m[2], m[3]) && !ahead(text[:m[0]], after) {
			continue // "Q4 2027" is quarterRe's; "TSM Q4 30% growth", "Q4 earnings", "NVDA reports in Q4" name no horizon
		}
		g := 4 // "Q4", else "4Q"
		if m[4] < 0 {
			g = 6
		}
		q, _ := strconv.Atoi(text[m[g]:m[g+1]])
		end := time.Date(today.Year(), time.Month(3*q+1), 0, 0, 0, 0, 0, time.UTC)
		if end.Before(today) {
			end = time.Date(today.Year()+1, time.Month(3*q+1), 0, 0, 0, 0, 0, time.UTC)
		}
		try(m[0], text[m[0]:m[1]], end)
	}
	for _, m := range eoRe.FindAllStringSubmatchIndex(text, -1) {
		if past(text[:m[0]]) {
			continue
		}
		y, q := today.Year(), (int(today.Month())-1)/3
		if m[4] >= 0 {
			y, _ = strconv.Atoi(text[m[4]:m[5]])
			if y < 100 {
				y += 2000
			}
		}
		end := time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC) // EOY
		switch strings.ToLower(text[m[2]:m[3]]) {
		case "eom":
			end = time.Date(today.Year(), today.Month()+1, 0, 0, 0, 0, 0, time.UTC)
		case "eoq":
			end = time.Date(today.Year(), time.Month(3*q+4), 0, 0, 0, 0, 0, time.UTC)
		}
		try(m[0], text[m[0]:m[1]], end)
	}
	for _, m := range weekdayRe.FindAllStringSubmatchIndex(text, -1) {
		d := (int(weekdays[strings.ToLower(text[m[2]:m[3]])]) - int(today.Weekday()) + 7) % 7
		if d == 0 {
			d = 7 // "by Friday" on a Friday: the next one
		}
		on := strings.EqualFold(strings.Fields(text[m[0]:m[1]])[0], "on") // "What happened on Monday" is behind; "by Friday" is ahead
		if !past(text[:m[0]]) && !(on && !ahead(text[:m[0]], text[m[1]:])) {
			try(m[0], text[m[0]:m[1]], today.AddDate(0, 0, d))
		}
	}
	for _, m := range decadeRe.FindAllStringIndex(text, -1) {
		if !past(text[:m[0]]) {
			try(m[0], text[m[0]:m[1]], time.Date(today.Year()-today.Year()%10+9, 12, 31, 0, 0, 0, 0, time.UTC))
		}
	}
	for _, m := range quarterRe.FindAllStringSubmatchIndex(text, -1) {
		if past(text[:m[0]]) {
			continue
		}
		q, _ := strconv.Atoi(text[m[2]:m[3]])
		var y int
		if m[4] >= 0 { // "Q3 '27"
			y, _ = strconv.Atoi(text[m[4]:m[5]])
			y += 2000
		} else {
			y, _ = strconv.Atoi(text[m[6]:m[7]])
		}
		try(m[0], text[m[0]:m[1]], time.Date(y, time.Month(3*q+1), 0, 0, 0, 0, 0, time.UTC))
	}
	for _, m := range midYearRe.FindAllStringSubmatchIndex(text, -1) {
		if past(text[:m[0]]) {
			continue
		}
		y, _ := strconv.Atoi(text[m[2]:m[3]])
		try(m[0], text[m[0]:m[1]], time.Date(y, 6, 30, 0, 0, 0, 0, time.UTC))
	}
	for _, m := range endYearRe.FindAllStringSubmatchIndex(text, -1) {
		if past(text[:m[0]]) || rangeAfter.MatchString(text[m[1]:]) {
			continue // "in 2026-2028" is the range's
		}
		y, _ := strconv.Atoi(text[m[2]:m[3]])
		if p := strings.ToLower(text[m[0]:m[2]]); strings.HasPrefix(p, "before") && !strings.Contains(p, "end") {
			y-- // "before 2028" ends with 2027; "before the end of 2028" is 2028's
		}
		try(m[0], text[m[0]:m[1]], time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC))
	}
	for _, m := range yearRangeRe.FindAllStringSubmatchIndex(text, -1) {
		near, _ := strconv.Atoi(text[m[2]:m[3]])
		far, _ := strconv.Atoi(text[m[4]:m[5]])
		if far < 100 {
			far += 2000 // "2026-28"
		}
		if far <= near || past(text[:m[0]]) || strings.HasPrefix(text[m[1]:], "-") {
			continue // "2027-03-31" is a date
		}
		try(m[0], text[m[0]:m[1]], time.Date(far, 12, 31, 0, 0, 0, 0, time.UTC))
	}
	for _, m := range halfRe.FindAllStringSubmatchIndex(text, -1) {
		if past(text[:m[0]]) {
			continue
		}
		var y int
		if m[8] >= 0 { // "H2 '27"
			y, _ = strconv.Atoi(text[m[8]:m[9]])
			y += 2000
		} else {
			y, _ = strconv.Atoi(text[m[10]:m[11]])
		}
		g := 4 // "H2 2027", else "2H 2027"
		if m[4] < 0 {
			g = 6
		}
		half := text[m[g]:m[g+1]]
		end := time.Date(y, 6, 30, 0, 0, 0, 0, time.UTC)
		if half == "2" {
			end = time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC)
		}
		if m[2] >= 0 && strings.EqualFold(text[m[2]:m[3]], "before") {
			end = time.Date(y, time.Month(6*(int(half[0]-'0')-1)+1), 0, 0, 0, 0, 0, time.UTC) // "before H2 2027" ends June 30
		}
		try(m[0], text[m[0]:m[1]], end)
	}
	for _, m := range isoRe.FindAllStringSubmatchIndex(text, -1) {
		if t, err := time.Parse(time.DateOnly, text[m[0]:m[1]]); err == nil {
			try(m[0], text[m[0]:m[1]], t)
		}
	}
	for _, m := range periodRe.FindAllStringSubmatchIndex(text, -1) {
		p := strings.Join(strings.Fields(strings.ToLower(text[m[2]:m[3]])), " ")
		if past(text[:m[0]]) || (p == "today" || p == "tomorrow") && entryTodayRe.MatchString(clauseOf(text[:m[0]])) {
			continue // "NVDA ran 20% this week"; "if I bought TSM today" starts a holding period, not a horizon
		}
		if p == "next year" && (strings.Contains(strings.ToLower(text[m[0]:m[2]]), "the") || strings.HasSuffix(strings.ToLower(text[:m[0]]), "this time ")) {
			continue // "the next year", "this time next year": a 12-month span (nextYearSpanRe)
		}
		y, q := today.Year(), (int(today.Month())-1)/3
		var end time.Time
		switch {
		case p == "next year":
			end = time.Date(y+1, 12, 31, 0, 0, 0, 0, time.UTC)
		case strings.Contains(p, "year"):
			end = time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC)
		case p == "next quarter":
			end = time.Date(y, time.Month(3*q+7), 0, 0, 0, 0, 0, time.UTC)
		case strings.Contains(p, "quarter"):
			end = time.Date(y, time.Month(3*q+4), 0, 0, 0, 0, 0, time.UTC)
		case strings.Contains(p, "week"):
			end = today.AddDate(0, 0, map[bool]int{true: 14, false: 7}[p == "next week"])
		case p == "next month":
			end = time.Date(y, today.Month()+2, 0, 0, 0, 0, 0, time.UTC)
		case strings.Contains(p, "month"): // this month, the end of the month
			end = time.Date(y, today.Month()+1, 0, 0, 0, 0, 0, time.UTC)
		default: // today, tomorrow
			end = today.AddDate(0, 0, 1)
		}
		try(m[0], text[m[0]:m[1]], end)
	}
	slices.SortStableFunc(out, func(a, b horizonPhrase) int { return a.at - b.at })
	return out
}

// past reports whether the clause before a span ends in a past move ("it
// doubled in 5 years", "TSM went up 300% over 10 years"). A holding verb in a
// hypothetical ("if I bought TSM today and held it for 10 years", "I would
// hold it for 5 years") starts a holding period ahead, not a past move.
func past(before string) bool {
	clause := clauseOf(before)
	if holdRe.MatchString(clause) && (ifWouldRe.MatchString(clause) || entryAndRe.MatchString(clause)) {
		return false
	}
	if m := upDownRe.FindStringSubmatch(clause); m != nil && !questionWord[strings.ToLower(m[1])] {
		return true // "TSM's up 20% in 5 days"; not "what's up with TSM in 5 years"
	}
	return pastRe.MatchString(clause)
}

var questionWord = map[string]bool{"what": true, "how": true, "where": true, "who": true, "why": true, "when": true, "whats": true}

// spanWords is a span as words: "30 years", "3 decades".
func spanWords(n float64, unit string) string {
	if unit == "yr" || unit == "y" {
		unit = "year"
	}
	if n != 1 {
		unit += "s"
	}
	return strconv.FormatFloat(n, 'f', -1, 64) + " " + unit
}

// ambiguousPrep reports whether a date's preposition, text[i:j] (i < 0: none,
// as in "Dec 31"), names a day behind as readily as one ahead: "in" and "on"
// do, "by", "before", "until" and "through" look ahead.
func ambiguousPrep(text string, i, j int) bool {
	if i < 0 {
		return true
	}
	p := strings.ToLower(text[i:j])
	return p == "in" || p == "on"
}

// sentenceStop ends a sentence: ".", "!", "?" or ";" before a space (not a decimal point).
var sentenceStop = regexp.MustCompile(`[.!?;](?:\s|$)`)

// ahead reports whether a date written without a year after an ambiguous
// preposition is one ahead: a forward word earlier in its sentence ("Where
// will SOL be in March?", "Will SOL, ETH and BTC rally in March?"), or, with
// no past word there, an outlook word or the end of the text right after it
// ("SOL in March?", "TSM Q4 outlook"). Otherwise it is read as behind and
// names no horizon: "What happened to TSM in March", "I bought ETH on 15 Oct"
// and "Since Dec 31" would each read as the next such day, months ahead (gap
// round 2 review).
func ahead(before, after string) bool {
	if locs := sentenceStop.FindAllStringIndex(before, -1); len(locs) > 0 {
		before = before[locs[len(locs)-1][1]:]
	}
	if forwardRe.MatchString(before) {
		return true
	}
	return !behindRe.MatchString(before) && (outlookWord.MatchString(after) || endOfText.MatchString(after))
}

// behindRe is a past word in a sentence: its year-less date is behind.
var behindRe = regexp.MustCompile(`(?i)\b(?:happened|bought|sold|was|were|did|had|opened|closed|peaked|bottomed|started|began|reported|announced|listed|since|last)\b`)

// clauseOf is the clause the text ends in: everything after its last punctuation.
func clauseOf(before string) string {
	if i := strings.LastIndexAny(before, ",.;:!?"); i >= 0 {
		return before[i+1:]
	}
	return before
}

// lookback reports whether a span reads as the period behind a statistic
// ("TSM's CAGR over 10 years", "average return for 5 years") rather than ahead
// ("the expected return over the next 5 years", "what will the CAGR be in 5 years").
func lookback(before, span string) bool {
	before = clauseOf(before)
	return notHorizonC.MatchString(before) && !forwardRe.MatchString(before) && !forwardSpan.MatchString(span)
}

// isTicker reports whether tok reads as a ticker or a universe symbol: "TSM",
// "TSMC", "BRK.B", or a lower-case symbol of three letters or more ("nvda").
func isTicker(tok string) bool {
	if tickerTok.MatchString(tok) && !notTicker[tok] {
		return true
	}
	if _, ok := market.Lookup(tok); ok && len(tok) >= 3 && !commonWord[strings.ToLower(tok)] {
		return true
	}
	return false
}

// commonWord are universe symbols that are also English words.
var commonWord = map[string]bool{"near": true, "one": true, "sun": true, "gas": true, "ape": true, "ray": true, "pump": true, "move": true, "jto": true, "the": true}

func amount(s string) float64 {
	if n, ok := words[strings.ToLower(s)]; ok {
		return n
	}
	n, _ := strconv.ParseFloat(s, 64)
	return n
}

// addSpan is today plus n units (fractions of a year or month by days).
func addSpan(today time.Time, n float64, unit string) time.Time {
	switch unit {
	case "day":
		return today.AddDate(0, 0, int(n))
	case "week":
		return today.AddDate(0, 0, int(7*n))
	case "month":
		if n == float64(int(n)) {
			return today.AddDate(0, int(n), 0)
		}
		return today.AddDate(0, 0, int(n*30.44))
	case "quarter":
		return today.AddDate(0, int(3*n), 0)
	case "decade":
		n *= 10
	}
	if n == float64(int(n)) {
		return today.AddDate(int(n), 0, 0)
	}
	if 2*n == float64(int(2*n)) { // "a year and a half": a year and six months
		return today.AddDate(int(n), 6, 0)
	}
	return today.AddDate(0, 0, int(n*365.25))
}

// targetMultiple is a multiple of today's price the user asks about ("10x",
// "5-bagger", "tenfold", "double"), and whether they ask by when. A
// valuation or leverage multiple ("25x earnings", "a P/E of 30x", "3x
// leverage") is skipped. A multiple alone is not a target: the server uses it
// only with byWhen; otherwise the model decides whether to pass target_multiple.
func targetMultiple(text string) (x float64, byWhen bool, ok bool) {
	x, byWhen, ok, _ = readMultiple(text)
	return x, byWhen, ok
}

// readMultiple is targetMultiple, and whether that multiple is a move already
// made ("TSM doubled this year", "NVDA 10x'd since 2020", "it went up 5x"):
// a question that only mentions one asks no target of its own.
func readMultiple(text string) (x float64, byWhen, ok, pastMove bool) {
	byWhen = byWhenRe.MatchString(text)
	for _, m := range multipleRe.FindAllStringSubmatchIndex(text, -1) {
		if notTargetA.MatchString(text[m[1]:]) || notTargetB.MatchString(text[:m[0]]) {
			continue // "25x earnings", "a P/E of 30x": a valuation multiple
		}
		x = amount(text[m[2]:m[3]])
		return x, byWhen || multipleWhen.MatchString(text[m[1]:]), x > 1, madeAfter.MatchString(text[m[1]:]) || made(text[:m[0]])
	}
	if m := baggerRe.FindStringSubmatchIndex(text); m != nil {
		x = amount(text[m[2]:m[3]])
		return x, byWhen, x > 1, made(text[:m[0]])
	}
	if m := doubleRe.FindStringSubmatchIndex(text); m != nil {
		w := strings.ToLower(text[m[0]:m[1]])
		return map[string]float64{"doubl": 2, "tripl": 3, "quadrupl": 4}[strings.ToLower(text[m[2]:m[3]])], byWhen, true,
			strings.HasSuffix(w, "ed") || made(text[:m[0]])
	}
	return 0, byWhen, false, false
}

// made reports whether the words before a multiple make it a move already
// made: a past verb ("it went up 5x") or "was a 10 bagger", "has been a 5x".
func made(before string) bool {
	return past(before) || madeBefore.MatchString(clauseOf(before))
}

var (
	madeBefore = regexp.MustCompile(`(?i)\b(?:was|were|has\s+been|had\s+been|already)\s+(?:a\s+|an\s+)?$`)
	// madeAfter follows a multiple made already: "10x'd", "5x’d"
	madeAfter = regexp.MustCompile(`^(?:'d|’d)\b`)
)
