package forecast

import (
	"fmt"
	"regexp"
	"strings"
)

// ViewHeadline is how chat, the bell and the draft card state an ordered
// report: its view, by the rule the forecast list row uses (the web's
// summaryViewOf and Headline). ok only when the view leads (Headline "view")
// and either states a median (an anchored basis) or is flat: "median $611,
// 62% chance at or above today's $485", or, with no basis for a centre,
// "80% range ≈$242–≈$1,480". Otherwise (binary, categorical, a bucket
// headline, no view) ok is false and the caller keeps the top outcome.
func ViewHeadline(v *DeskView) (text string, ok bool) {
	if text, ok = ViewTitle(v); ok && v.Basis.anchored() && v.PAbove != nil && v.Reference != nil {
		text += fmt.Sprintf(", %s chance at or above today's %s", chanceWords(*v.PAbove), todayLevel(v.Reference.Value, v.Unit))
	}
	return text, ok
}

// ViewTitle is ViewHeadline without the chance, for a title (the bell's
// "Forecast ready · median $611"): "median $611" or "80% range …".
func ViewTitle(v *DeskView) (string, bool) {
	switch {
	case v == nil || v.Headline != "view":
		return "", false
	case v.Basis.anchored():
		return "median " + fmtQ(v.Median, v.Unit), true
	case v.Flat:
		return fmt.Sprintf("80%% range %s–%s", fmtQ(v.P10, v.Unit), fmtQ(v.P90, v.Unit)), true
	}
	return "", false
}

// ViewLean is the side a view ViewHeadline states leans, for chat's lean and
// its trade rule: "up: 62% chance at or above today" from 53%, "down: …" to
// 47%, "split: …" between, and "no lean: the desk states no median" on a view
// with no basis. "" when ViewHeadline is not ok.
func ViewLean(v *DeskView) string {
	if _, ok := ViewHeadline(v); !ok {
		return ""
	}
	if !v.Basis.anchored() {
		return "no lean: the desk states no median"
	}
	if v.PAbove == nil {
		return "split: the desk states no chance against today's level"
	}
	side := "split"
	switch p := v.PAbove.Value; {
	case p >= 0.53:
		side = "up"
	case p <= 0.47:
		side = "down"
	}
	return fmt.Sprintf("%s: %s chance at or above today", side, chanceWords(*v.PAbove))
}

// Brief is the view's list-row slice (ForecastSummary.view), for the chat
// cards that carry it.
func (v *DeskView) Brief() *ViewBrief { return v.brief() }

// viewChange is an update's change read off the previous and the new stored
// views (gap round G20): "Median $570 → $611 (+7.2%) · chance at or above
// today 58% → 62%" when both state a median, else "80% range $356–$917 →
// $380–$950". "" without both views.
func viewChange(prev, next *DeskView) string {
	if prev == nil || next == nil {
		return ""
	}
	u := next.Unit
	if !prev.Basis.anchored() || !next.Basis.anchored() {
		return fmt.Sprintf("80%% range %s–%s → %s–%s", fmtQ(prev.P10, prev.Unit), fmtQ(prev.P90, prev.Unit), fmtQ(next.P10, u), fmtQ(next.P90, u))
	}
	s := fmt.Sprintf("Median %s → %s", fmtQ(prev.Median, prev.Unit), fmtQ(next.Median, u))
	switch a, b := prev.Median.Value, next.Median.Value; {
	case next.Scale == "log" && a > 0:
		s += " (" + signedPct((b/a-1)*100) + ")"
	case next.Scale != "log":
		s += " (" + signedLevel(b-a, u) + ")"
	}
	if prev.PAbove != nil && next.PAbove != nil {
		s += fmt.Sprintf(" · chance at or above today %s → %s", chanceWords(*prev.PAbove), chanceWords(*next.PAbove))
	}
	return s
}

var (
	pctFigure = regexp.MustCompile(`(?i)\d\s*(?:%|percent\b|pct\b|bps?\b|basis points?\b)`)
	anyLevel  = regexp.MustCompile(`\d`)
)

// changeWords is a numeric update's changeNote with every sentence that
// states a number dropped (gap round G20): a level in the question's unit
// ("$650", "S$62", "2.5%"), a percentage or a bp figure. The model writes
// changeNote before the server's checks, so its numbers can be ones the
// stored view no longer says; viewChange states them. "" when none remain.
func changeWords(note string, u Unit) string {
	var kept []string
	for _, s := range sentences(note) {
		if pctFigure.MatchString(s) || u.Prefix != "" && strings.Contains(s, u.Prefix) && anyLevel.MatchString(s[strings.Index(s, u.Prefix):]) ||
			u.Suffix != "" && strings.Contains(s, u.Suffix) && anyLevel.MatchString(s) {
			continue
		}
		kept = append(kept, s)
	}
	return strings.Join(kept, " ")
}
