package rtquotetest_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote/rtquotetest"
)

// sample is two listings per source, as our tickers.
var sample = map[string][]rtquote.Wanted{
	"naver":    {{Ticker: "005930.KS", Currency: "KRW"}, {Ticker: "035420.KS", Currency: "KRW"}},
	"tencent":  {{Ticker: "0700.HK", Currency: "HKD"}, {Ticker: "9988.HK", Currency: "HKD"}},
	"vietcap":  {{Ticker: "VCB.VN", Currency: "VND"}, {Ticker: "VIC.VN", Currency: "VND"}},
	"sgx":      {{Ticker: "D05.SI", Currency: "SGD"}, {Ticker: "O39.SI", Currency: "SGD"}},
	"mis":      {{Ticker: "2330.TW", Currency: "TWD"}, {Ticker: "2454.TW", Currency: "TWD"}},
	"yahoo-tw": {{Ticker: "2330.TW", Currency: "TWD"}, {Ticker: "2454.TW", Currency: "TWD"}},
}

func want(t *testing.T, a rtquote.Adapter) rtquote.Want {
	t.Helper()
	w := rtquote.Want{}
	for _, l := range sample[a.Info().Name] {
		sym, ok := a.Symbol(l.Ticker)
		if !ok {
			t.Fatalf("%s: %s does not map", a.Info().Name, l.Ticker)
		}
		w[sym] = l
	}
	return w
}

// read sends a's request for its sample to the board (as a server at base,
// or as a transport when base is the adapter's own host) and parses it.
func read(t *testing.T, a rtquote.Adapter, client *http.Client, base string) (rtquote.Batch, int, error) {
	t.Helper()
	req, err := a.Request(context.Background(), base, want(t, a))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if b, ok := a.(rtquote.Blocker); ok {
			if why, blocked := b.Blocked(resp.StatusCode, body); blocked {
				return rtquote.Batch{}, resp.StatusCode, rtquote.Refused(why)
			}
		}
		return rtquote.Batch{}, resp.StatusCode, nil
	}
	b, err := a.Parse(body, want(t, a))
	return b, resp.StatusCode, err
}

// TestBoardSpeaksEachSource: every adapter's request, sent to the board
// either way, parses into a row for each listing asked with no rejection;
// while trading, each answer shows a trade a second back and a volume that
// grew; once the venue stops, the sources with a session say closed; a
// refusal is each source's own.
func TestBoardSpeaksEachSource(t *testing.T) {
	now := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	trading := true
	b := &rtquotetest.Board{Now: func() time.Time { return now }, Open: func(string, time.Time) bool { return trading }, SGX: []string{"D05", "O39", "U11"}}
	srv := httptest.NewServer(b)
	defer srv.Close()
	for _, a := range rtquote.All {
		in := a.Info()
		for _, mode := range []struct {
			name, base string
			client     *http.Client
		}{{"transport", in.Host, &http.Client{Transport: b}}, {"server", srv.URL, srv.Client()}} {
			trading = true
			batch, status, err := read(t, a, mode.client, mode.base)
			if err != nil || status != http.StatusOK || len(batch.Rows) != 2 || len(batch.Rejected) != 0 || batch.Closed {
				t.Fatalf("%s via %s: %d %v %+v", in.Name, mode.name, status, err, batch)
			}
			for _, r := range batch.Rows {
				if in.Times == rtquote.TimesTrade && !r.TradeAt.Equal(now.Add(-time.Second)) || r.Price < 50 || r.Price >= 950 || r.Volume == nil {
					t.Fatalf("%s via %s: %+v", in.Name, mode.name, r)
				}
			}
			vol := *batch.Rows[0].Volume
			now = now.Add(time.Minute)
			again, _, _ := read(t, a, mode.client, mode.base)
			if *again.Rows[0].Volume <= vol {
				t.Fatalf("%s via %s: volume %v then %v", in.Name, mode.name, vol, *again.Rows[0].Volume)
			}
			trading = false
			now = now.Add(time.Minute)
			closed, _, _ := read(t, a, mode.client, mode.base)
			if sessioned := in.Name == "naver" || in.Name == "vietcap" || in.Name == "yahoo-tw"; closed.Closed != sessioned ||
				*closed.Rows[0].Volume != *again.Rows[0].Volume {
				t.Fatalf("%s via %s, stopped: %+v", in.Name, mode.name, closed)
			}
		}
		b.Refuse(in.Name, true)
		_, status, err := read(t, a, &http.Client{Transport: b}, in.Host)
		switch {
		case in.Name == "mis" && (status != http.StatusBadGateway || !errors.Is(err, rtquote.ErrRefused) || !strings.Contains(err.Error(), "support ID")):
			t.Fatalf("mis refused: %d %v", status, err)
		case in.Name != "mis" && status != http.StatusForbidden:
			t.Fatalf("%s refused: %d", in.Name, status)
		}
		b.Refuse(in.Name, false)
	}
	reqs := b.Requests()
	if len(reqs) != len(rtquote.All)*7 {
		t.Fatalf("%d requests", len(reqs))
	}
	for _, r := range reqs {
		if r.Source == "" || r.Symbols == 0 && r.Status == http.StatusOK || r.At.IsZero() {
			t.Fatalf("%+v", r)
		}
	}
	b.SetPrice("naver", "005930", 250000)
	batch, _, _ := read(t, rtquote.Naver{}, &http.Client{Transport: b}, rtquote.Naver{}.Info().Host)
	if batch.Rows[0].Price != 250000 {
		t.Fatalf("set price %+v", batch.Rows[0])
	}
	// a path no source has
	resp, err := http.Get(srv.URL + "/elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatal(resp.StatusCode)
	}
}
