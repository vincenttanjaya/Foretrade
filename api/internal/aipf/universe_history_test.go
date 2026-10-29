package aipf

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

func TestPortfolioHistoryBatchDeliveryAndPrimaryFailure(t *testing.T) {
	primary := errors.New("primary history failure")
	got, errs := historyBatch(context.Background(), []string{"bad", "healthy"}, func(ctx context.Context, s string) ([]market.Candle, error) {
		if s == "bad" {
			return nil, primary
		}
		return []market.Candle{{C: 100}}, nil
	})
	if !errors.Is(errs[0], primary) || errs[1] != nil || len(got[1]) != 1 {
		t.Fatalf("per-symbol primary failure/delivery lost: %v %v", got, errs)
	}
}

func TestPortfolioHistoryBatchBoundAndJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered := make(chan struct{}, 3)
		done := make(chan struct{})
		var active, peak, started atomic.Int32
		go func() {
			historyBatch(ctx, []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, func(ctx context.Context, s string) ([]market.Candle, error) {
				n := active.Add(1)
				started.Add(1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				entered <- struct{}{}
				defer active.Add(-1)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			close(done)
		}()
		synctest.Wait()
		if peak.Load() != 3 || started.Load() != 3 {
			t.Fatalf("admission before owned workers: peak=%d started=%d", peak.Load(), started.Load())
		}
		cancel()
		synctest.Wait()
		<-done
		if active.Load() != 0 {
			t.Fatalf("operation returned with %d abandoned histories", active.Load())
		}
		// Every cancellation path released ownership; a later batch can complete.
		got, errs := historyBatch(context.Background(), []string{"later"}, func(context.Context, string) ([]market.Candle, error) { return []market.Candle{{C: 1}}, nil })
		if len(got[0]) != 1 || errs[0] != nil {
			t.Fatalf("capacity not reusable: %v %v", got, errs)
		}
	})
}
