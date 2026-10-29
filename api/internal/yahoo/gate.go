package yahoo

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// gate paces, bounds and breaks every request of the clients sharing it.
type gate struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
	slots  chan struct{} // in-flight bound

	threshold int           // consecutive 429s that open the breaker
	pause     time.Duration // how long it stays open
	strikes   int
	openUntil time.Time

	now func() time.Time
}

// shared is the process-wide gate of every Client built with default pacing.
var shared = newGate(2, 4, 4, 5, 3*time.Minute)

func newGate(rate float64, burst, inFlight, threshold int, pause time.Duration) *gate {
	return &gate{rate: rate, burst: float64(burst), tokens: float64(burst), slots: make(chan struct{}, inFlight),
		threshold: threshold, pause: pause, now: time.Now}
}

// open is the breaker's error while it is open, else nil.
func (g *gate) open() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := g.openUntil; g.now().Before(until) {
		err := unavailable("", "rate limited by Yahoo; paused until %s UTC", until.UTC().Format("15:04"))
		err.Paused = true
		return err
	}
	return nil
}

// wait takes one token, sleeping until it is due; a cancelled wait gives it back.
func (g *gate) wait(ctx context.Context) error {
	g.mu.Lock()
	now := g.now()
	if !g.last.IsZero() {
		g.tokens = min(g.burst, g.tokens+now.Sub(g.last).Seconds()*g.rate)
	}
	g.last = now
	g.tokens--
	var d time.Duration
	if g.tokens < 0 {
		d = time.Duration(-g.tokens / g.rate * float64(time.Second))
	}
	g.mu.Unlock()
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		g.tokens++
		g.mu.Unlock()
		return ctx.Err()
	}
}

// acquire takes an in-flight slot; release returns it.
func (g *gate) acquire(ctx context.Context) error {
	select {
	case g.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gate) release() { <-g.slots }

// limited records a 429; the threshold-th in a row opens the breaker. After a
// pause one more 429 reopens it at once.
func (g *gate) limited() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.strikes++
	if g.strikes >= g.threshold && !g.now().Before(g.openUntil) {
		g.openUntil = g.now().Add(g.pause)
		g.strikes = g.threshold - 1
		slog.Warn("yahoo rate limited; pausing every call", "until", g.openUntil.UTC().Format(time.RFC3339))
	}
}

// ok records an answer that was not a 429.
func (g *gate) ok() {
	g.mu.Lock()
	g.strikes = 0
	g.mu.Unlock()
}
