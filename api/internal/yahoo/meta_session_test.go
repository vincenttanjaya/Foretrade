package yahoo

import (
	"net/http"
	"testing"
)

// The session fields quote feeds price with: the 1-day chart previous close
// and the session's high, low and volume; absent or non-positive ones are nil.
func TestMetaSessionFields(t *testing.T) {
	body := []byte(`{"chart":{"result":[{"meta":{"symbol":"0700.HK","currency":"HKD","regularMarketPrice":428.2,
		"regularMarketTime":1791274081,"chartPreviousClose":423.0,"regularMarketDayHigh":429.2,"regularMarketDayLow":425.6,
		"regularMarketVolume":10030081},"timestamp":[1791274081],"indicators":{"quote":[{"close":[428.2]}]}}],"error":null}}`)
	c, err := parseChart("0700.HK", "1d", "1d", http.StatusOK, body)
	m := c.Meta
	if err != nil || *m.ChartPreviousClose != 423 || *m.DayHigh != 429.2 || *m.DayLow != 425.6 || *m.DayVolume != 10030081 || m.PreviousClose != nil {
		t.Fatalf("%+v %v", m, err)
	}
	c, err = parseChart("X", "1d", "1d", http.StatusOK, []byte(`{"chart":{"result":[{"meta":{"symbol":"X","regularMarketVolume":0}}],"error":null}}`))
	if err != nil || c.Meta.DayVolume != nil || c.Meta.ChartPreviousClose != nil || c.Meta.DayHigh != nil {
		t.Fatalf("%+v %v", c.Meta, err)
	}
}
