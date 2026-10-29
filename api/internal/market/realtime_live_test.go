//go:build rtlive

package market

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
)

// TestRealtimeLiveProbe makes ONE live read of each real-time source named
// in RT_PROBE (comma list of adapter names, e.g. "naver"), through the
// framework's own request (the honest User-Agent and nothing else), and
// logs what came back. Opt-in only: go test -tags rtlive -run
// TestRealtimeLiveProbe ./internal/market/ with RT_PROBE set; never in CI.
// Builders are allowed at most 20 such reads of their own source, at least
// 10 s apart.
func TestRealtimeLiveProbe(t *testing.T) {
	names := strings.Split(os.Getenv("RT_PROBE"), ",")
	if os.Getenv("RT_PROBE") == "" {
		t.Skip("RT_PROBE not set")
	}
	c := NewClient("")
	c.UseRealtime(rtquote.All, "")
	if dir := os.Getenv("RT_PROBE_SAVE"); dir != "" { // keep each answer (and what was sent) for the write-up
		c.RealtimeHTTP = &http.Client{Timeout: rtTimeout, Transport: saveTransport{dir}}
	}
	f := &c.feeds
	for _, v := range f.rtVenues() {
		for _, s := range v.chain {
			if !contains(names, s.info.Name) {
				continue
			}
			f.mu.Lock()
			want := f.rtWant(v, s)
			f.mu.Unlock()
			start := time.Now()
			b, status, err := c.rtFetch(context.Background(), rtPoll{v: v, s: s, want: want}, s.info.Host)
			t.Logf("%s %s: asked %d, HTTP %d in %s, err %v", v.venue, s.info.Name, len(want), status,
				time.Since(start).Round(time.Millisecond), err)
			if err != nil {
				continue
			}
			t.Logf("%d rows, %d rejected, stamp %s, closed %v", len(b.Rows), len(b.Rejected), b.Stamp.Format(time.RFC3339Nano), b.Closed)
			for _, r := range b.Rows {
				if contains(s.info.Canaries, r.Ticker) {
					vol := -1.0
					if r.Volume != nil {
						vol = *r.Volume
					}
					t.Logf("canary %s %g %s trade %s volume %g", r.Ticker, r.Price, r.Currency, r.TradeAt.Format(time.RFC3339), vol)
				}
			}
			for i, r := range b.Rejected {
				if i < 5 {
					t.Logf("rejected %+v", r)
				}
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// saveTransport writes each request's headers and its answer to dir.
type saveTransport struct{ dir string }

func (s saveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	name := filepath.Join(s.dir, time.Now().UTC().Format("20060102T150405")+"_"+req.URL.Host)
	var sent bytes.Buffer
	fmt.Fprintf(&sent, "%s %s\n", req.Method, req.URL)
	req.Header.Write(&sent)
	fmt.Fprintf(&sent, "\nHTTP %d\n", resp.StatusCode)
	resp.Header.Write(&sent)
	os.WriteFile(name+".hdr", sent.Bytes(), 0o644)
	os.WriteFile(name+".body", body, 0o644)
	return resp, nil
}
