package research

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
)

const (
	fngURL     = "https://api.alternative.me/fng/?limit=1"
	pulseEvery = time.Hour
)

// FearGreed is alternative.me's Crypto Fear & Greed Index (updated daily).
type FearGreed struct {
	Value  int       `json:"value"` // 0..100
	Label  string    `json:"label"` // "Extreme Fear" … "Extreme Greed"
	AsOf   time.Time `json:"asOf"`
	Source string    `json:"source"`
}

// pulse caches the index in memory: one upstream call per hour at most, the
// last good value kept through failures.
// ponytail: the mutex is held during the 5 s fetch, so concurrent readers wait for it once an hour.
type pulse struct {
	mu   sync.Mutex
	at   time.Time // last attempt
	last *FearGreed
}

// FearGreed is the cached index, refreshed when the last attempt is over an
// hour old; nil until a fetch has ever succeeded.
func (s *Service) FearGreed(ctx context.Context) *FearGreed {
	p := &s.pulse
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.at) < pulseEvery {
		return p.last
	}
	p.at = time.Now()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	fg, err := fetchFearGreed(fctx, s.fngURL)
	if err != nil {
		slog.Warn("fear & greed fetch failed", "err", err)
		return p.last
	}
	p.last = fg
	return fg
}

func fetchFearGreed(ctx context.Context, url string) (*FearGreed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			Value     string `json:"value"`
			Label     string `json:"value_classification"`
			Timestamp string `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return nil, err
	}
	if len(body.Data) == 0 {
		return nil, fmt.Errorf("no data")
	}
	d := body.Data[0]
	v, err1 := strconv.Atoi(d.Value)
	ts, err2 := strconv.ParseInt(d.Timestamp, 10, 64)
	if err1 != nil || err2 != nil || v < 0 || v > 100 || d.Label == "" {
		return nil, fmt.Errorf("unexpected entry %+v", d)
	}
	return &FearGreed{Value: v, Label: d.Label, AsOf: time.Unix(ts, 0).UTC(), Source: "alternative.me"}, nil
}

func (s *Service) handlePulse(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"fearGreed": s.FearGreed(r.Context())})
}
