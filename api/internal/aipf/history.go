package aipf

import (
	"context"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// historyBatch owns at most three workers for one portfolio operation. The
// unbuffered job channel bounds admission before work begins; this caller
// alone closes it and joins every worker before returning. No fetch holds
// shared cache state. Per-symbol errors remain intact, including a primary
// upstream failure when another symbol is canceled.
func historyBatch(ctx context.Context, symbols []string, fetch func(context.Context, string) ([]market.Candle, error)) ([][]market.Candle, []error) {
	got, errs := make([][]market.Candle, len(symbols)), make([]error, len(symbols))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(3, len(symbols)) {
		wg.Go(func() {
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					errs[i] = err
					continue
				}
				got[i], errs[i] = fetch(ctx, symbols[i])
			}
		})
	}
	next := 0
admission:
	for ; next < len(symbols); next++ {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- next:
		case <-ctx.Done():
			break admission
		}
	}
	close(jobs)
	wg.Wait()
	for ; next < len(symbols); next++ {
		errs[next] = ctx.Err()
	}
	return got, errs
}

func finitePositive(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

// closedDailyWindow keeps the newest consecutive UTC daily sample, not a
// tail-index approximation. Conflicting duplicate or invalid closes create a
// missing day. A lagging newest bar is retained if it is actually closed.
func closedDailyWindow(rows []market.Candle, now time.Time) []market.Candle {
	byTime := map[int64]market.Candle{}
	bad := map[int64]bool{}
	for _, c := range rows {
		if c.T%86400 != 0 || c.T > now.Unix()-86400 || !finitePositive(c.C) {
			bad[c.T] = true
			continue
		}
		if old, ok := byTime[c.T]; ok && old.C != c.C {
			bad[c.T] = true
		}
		byTime[c.T] = c
	}
	out := make([]market.Candle, 0, len(byTime))
	for at, c := range byTime {
		if !bad[at] {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b market.Candle) int {
		if a.T < b.T {
			return -1
		}
		if a.T > b.T {
			return 1
		}
		return 0
	})
	start := len(out) - 1
	for start > 0 && out[start].T-out[start-1].T == 86400 {
		start--
	}
	if start < 0 {
		return nil
	}
	return out[max(start, len(out)-days):]
}
