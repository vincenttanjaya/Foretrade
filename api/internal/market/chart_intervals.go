package market

import (
	"context"
	"slices"
	"time"
)

// Long chart intervals do not expand the intervals accepted by analysis,
// strategies or financial history callers.
func validChartInterval(interval string) bool {
	return ValidInterval(interval) || calendarInterval(interval)
}

func calendarInterval(interval string) bool { return interval == "1w" || interval == "1M" }

// Calendar charts use Monday weeks and actual calendar months, in UTC.
func chartPeriodStart(at time.Time, interval string) time.Time {
	u := at.UTC()
	y, m, d := u.Date()
	switch interval {
	case "1M":
		return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	case "1w":
		return time.Date(y, m, d-(int(u.Weekday())+6)%7, 0, 0, 0, 0, time.UTC)
	}
	return u.Truncate(time.Duration(BarSeconds(interval)) * time.Second)
}

func nextChartBoundary(at time.Time, interval string) time.Time {
	start := chartPeriodStart(at, interval)
	if interval == "1M" {
		return start.AddDate(0, 1, 0)
	}
	if interval == "1w" {
		return start.AddDate(0, 0, 7)
	}
	return start.Add(time.Duration(BarSeconds(interval)) * time.Second)
}

func calendarCandles(bars []Candle, interval string) []Candle {
	out := make([]Candle, 0, len(bars))
	for _, b := range bars {
		b.T = chartPeriodStart(time.Unix(b.T, 0), interval).Unix()
		if n := len(out); n > 0 && out[n-1].T == b.T {
			prev := &out[n-1]
			prev.H, prev.L, prev.C, prev.V = max(prev.H, b.H), min(prev.L, b.L), b.C, prev.V+b.V
		} else {
			out = append(out, b)
		}
	}
	return out
}

// Hyperliquid's native long bars use epoch-aligned weeks and 30-day months.
// Aggregate its already split-adjusted daily bars to obtain calendar periods.
func (c *Client) perpCalendarCandles(ctx context.Context, coin, interval string, limit int, endTime int64) ([]Candle, error) {
	const dailyRetention = 5000
	cursor := endTime
	// The venue retains its latest daily window, anchored at now, regardless
	// of the requested cursor. Aggregate that same snapshot before selecting
	// older period opens, so paging cannot restore an incomplete retention
	// bucket or clip an already-open historical candle at the cursor's day.
	bars, err := c.fetchPerpCandles(ctx, coin, "1d", dailyRetention, 0)
	if err != nil {
		return nil, err
	}
	out := calendarCandles(bars, interval)
	// A retention/query window may begin in the middle of a period. Do not
	// present that truncated bucket as a complete historical candle. A shorter
	// result starting after the requested window reaches listing inception.
	end := time.Now().UnixMilli()
	start := max(int64(0), end-dailyRetention*86400_000)
	truncated := len(bars) == dailyRetention || len(bars) > 0 && bars[0].T*1000 <= start+86400_000
	if truncated && len(out) > 0 && bars[0].T != out[0].T {
		out = out[1:]
	}
	if s, ok := splits[coin]; ok {
		// If the existing split repair omitted an unrecoverable split-day
		// candle, its enclosing week/month is incomplete too.
		splitDay := s.at.UTC().Truncate(24 * time.Hour).Unix()
		found := false
		for _, b := range bars {
			found = found || b.T == splitDay
		}
		if !found {
			period := chartPeriodStart(s.at, interval).Unix()
			for i, b := range out {
				if b.T == period {
					out = append(out[:i], out[i+1:]...)
					break
				}
			}
		}
	}
	if cursor > 0 {
		out = slices.DeleteFunc(out, func(b Candle) bool { return b.T*1000 > cursor })
	}
	return out[max(len(out)-limit, 0):], nil
}
