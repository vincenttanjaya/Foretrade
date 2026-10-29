package alerts

import (
	"testing"
	"time"
)

func TestNextRun(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04 Mon", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		every       string
		minute, wd  int
		after, want string
	}{
		{"day", 8 * 60, 0, "2026-10-03 07:59 Sat", "2026-10-03 08:00 Sat"}, // before the time: today
		{"day", 8 * 60, 0, "2026-10-03 08:00 Sat", "2026-10-04 08:00 Sun"}, // exactly at it: strictly after, so tomorrow
		{"day", 8 * 60, 0, "2026-10-03 08:01 Sat", "2026-10-04 08:00 Sun"}, // after: tomorrow
		{"day", 0, 0, "2026-10-31 23:59 Sat", "2026-11-01 00:00 Sun"},      // month rollover
		{"day", 1439, 0, "2026-12-31 23:59 Thu", "2027-01-01 23:59 Fri"},   // year rollover
		{"week", 9 * 60, 1, "2026-10-03 10:00 Sat", "2026-10-05 09:00 Mon"},
		{"week", 9 * 60, 0, "2026-10-03 10:00 Sat", "2026-10-04 09:00 Sun"},     // Sat → Sun wraps the week; Sunday = 0
		{"week", 9 * 60, 6, "2026-10-03 08:59 Sat", "2026-10-03 09:00 Sat"},     // today, still ahead
		{"week", 9 * 60, 6, "2026-10-03 09:00 Sat", "2026-10-10 09:00 Sat"},     // today, at the time: next week
		{"week", 9 * 60, 5, "2026-12-26 12:00 Sat", "2027-01-01 09:00 Fri"},     // year rollover
		{"weekdays", 8 * 60, 0, "2026-10-02 07:59 Fri", "2026-10-02 08:00 Fri"}, // Friday, still ahead
		{"weekdays", 8 * 60, 0, "2026-10-02 08:00 Fri", "2026-10-05 08:00 Mon"}, // Friday after the slot: Monday
		{"weekdays", 8 * 60, 0, "2026-10-03 07:00 Sat", "2026-10-05 08:00 Mon"}, // Saturday: Monday
		{"weekdays", 8 * 60, 0, "2026-10-04 23:59 Sun", "2026-10-05 08:00 Mon"},
		{"weekdays", 8 * 60, 0, "2026-10-05 09:00 Mon", "2026-10-06 08:00 Tue"},
	} {
		if got := nextRun(c.every, c.minute, c.wd, at(c.after)); !got.Equal(at(c.want)) {
			t.Errorf("%s %d wd%d after %s: %s, want %s", c.every, c.minute, c.wd, c.after, got.Format("2006-01-02 15:04 Mon"), c.want)
		}
	}
	// A non-UTC `after` is the same instant.
	if got := nextRun("day", 8*60, 0, at("2026-10-03 07:30 Sat").In(time.FixedZone("ICT", 7*3600))); !got.Equal(at("2026-10-03 08:00 Sat")) {
		t.Errorf("zone: %s", got)
	}
	if got := ScheduleLabel("week", "09:00", new(1)) + " / " + ScheduleLabel("day", "08:00", nil) + " / " + ScheduleLabel("weekdays", "08:00", nil); got != "Mondays 09:00 UTC / daily 08:00 UTC / weekdays 08:00 UTC" {
		t.Errorf("labels %q", got)
	}
}
