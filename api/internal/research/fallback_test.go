package research

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// The test dialer routes a vetted public address to a local HTTP fixture.
// Production validation still runs, including checking the connected peer.
type publicPeerConn struct{ net.Conn }

func (c publicPeerConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 80}
}

func failedCrawler(t *testing.T) *crawler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/crawl" {
			fmt.Fprint(w, `{"task_id":"failed-task"}`)
			return
		}
		fmt.Fprint(w, `{"status":"failed","error":"maximum recursion depth exceeded"}`)
	}))
	t.Cleanup(srv.Close)
	c := newCrawler(srv.URL, "crawler-secret")
	c.pollDelay = time.Millisecond
	return c
}

func directForServer(t *testing.T, srv *httptest.Server) *directFetcher {
	t.Helper()
	return &directFetcher{
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "93.184.216.34:80" {
				t.Errorf("dial must use vetted IP, got %q", address)
			}
			conn, err := (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
			if err != nil {
				return nil, err
			}
			return publicPeerConn{conn}, nil
		},
	}
}

func TestFailedCrawlerRecoversVisibleHTMLWithoutCredentials(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy-user:proxy-secret@127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://proxy-user:proxy-secret@127.0.0.1:1")
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if r.Header.Get(h) != "" {
				t.Errorf("forwarded %s: %q", h, r.Header.Get(h))
			}
		}
		if r.URL.Path == "/" {
			w.Header().Set("Set-Cookie", "crawler-cookie=secret")
			http.Redirect(w, r, "/page", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title> Pickup   Coffee | Home </title><style>hidden-style</style><script>hidden-script</script></head><body><nav>hidden-nav</nav><h1>Pickup Coffee</h1><p>500+ <strong>stores</strong> &amp; fresh coffee.</p><noscript>hidden-noscript</noscript><svg><title>hidden-svg-title</title><text>hidden-svg</text></svg><footer>hidden-footer</footer></body></html>`)
	}))
	defer srv.Close()
	c := failedCrawler(t)
	c.direct = directForServer(t, srv)
	got, err := c.fetch(context.Background(), "http://public.example/")
	if err != nil {
		t.Fatalf("page recovery failed: %v", err)
	}
	if got.Via != "direct" || got.Title != "Pickup Coffee | Home" {
		t.Errorf("via %q, title %q", got.Via, got.Title)
	}
	for _, want := range []string{"Pickup Coffee", "500+ stores & fresh coffee."} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("missing %q in %q", want, got.Text)
		}
	}
	for _, hidden := range []string{"hidden-script", "hidden-style", "hidden-noscript", "hidden-svg", "hidden-nav", "hidden-footer", "Home"} {
		if strings.Contains(got.Text, hidden) {
			t.Errorf("included invisible content %q", hidden)
		}
	}
	if received.Load() != 2 {
		t.Errorf("wanted one fetch with one redirect, got %d requests", received.Load())
	}
}

func TestHealthyCrawlerMarkdownIsUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer crawler-secret" {
			t.Errorf("primary credential missing")
		}
		if r.URL.Path == "/crawl" {
			fmt.Fprint(w, `{"task_id":"ok"}`)
			return
		}
		fmt.Fprint(w, `{"status":"completed","results":[{"markdown":"# rendered JS content"}]}`)
	}))
	defer srv.Close()
	c := newCrawler(srv.URL, "crawler-secret")
	c.pollDelay = time.Millisecond
	c.direct = &directFetcher{lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
		t.Error("healthy primary invoked fallback")
		return nil, errors.New("unexpected")
	}}
	got, err := c.fetch(context.Background(), "https://public.example/")
	if err != nil || got.Text != "# rendered JS content" || got.Via != "crawl4ai" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDirectFallbackDoesNotForwardURLTokensInReferer(t *testing.T) {
	var redirected atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "public.example" {
			http.Redirect(w, r, "http://second.example/page", http.StatusFound)
			return
		}
		redirected.Store(true)
		if referer := r.Header.Get("Referer"); referer != "" {
			t.Errorf("forwarded source URL through Referer: %q", referer)
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<p>Public company page</p>")
	}))
	defer srv.Close()
	c := failedCrawler(t)
	c.direct = directForServer(t, srv)
	if _, err := c.fetch(context.Background(), "http://public.example/?token=private-query"); err != nil {
		t.Fatal(err)
	}
	if !redirected.Load() {
		t.Fatal("cross-origin redirect was not exercised")
	}
}

func TestDirectFallbackRejectsNonPublicDNSBeforeConnecting(t *testing.T) {
	for _, ip := range []string{"0.1.2.3", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1", "192.0.0.8", "192.0.2.2", "192.88.99.1", "192.168.1.1", "198.18.0.1", "198.51.100.2", "203.0.113.2", "224.0.0.1", "240.0.0.1", "255.255.255.255", "::", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "fec0::1", "ff02::1", "2001:db8::1", "2002:7f00:1::1"} {
		t.Run(ip, func(t *testing.T) {
			c := failedCrawler(t)
			c.direct = &directFetcher{
				lookupIP: func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil },
				dialContext: func(context.Context, string, string) (net.Conn, error) {
					t.Error("connected to nonpublic address")
					return nil, errors.New("unexpected")
				},
			}
			if _, err := c.fetch(context.Background(), "http://public.example/"); err == nil || !strings.Contains(err.Error(), "blocked_destination") {
				t.Fatalf("wanted blocked destination, got %v", err)
			}
		})
	}
}

func TestDirectFallbackRejectsMixedPublicAndPrivateDNS(t *testing.T) {
	c := failedCrawler(t)
	c.direct = &directFetcher{
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("10.0.0.1")}}, nil
		},
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Error("connected to mixed DNS")
			return nil, errors.New("unexpected")
		},
	}
	if _, err := c.fetch(context.Background(), "http://public.example/"); err == nil || !strings.Contains(err.Error(), "blocked_destination") {
		t.Fatalf("got %v", err)
	}
}

func TestDirectFallbackRevalidatesDNSWhenDialing(t *testing.T) {
	c := failedCrawler(t)
	var lookups atomic.Int32
	c.direct = &directFetcher{
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			if lookups.Add(1) == 1 {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Error("dialed after DNS rebinding")
			return nil, errors.New("unexpected")
		},
	}
	if _, err := c.fetch(context.Background(), "http://public.example/"); err == nil || !strings.Contains(err.Error(), "blocked_destination") {
		t.Fatalf("got %v", err)
	}
	if lookups.Load() < 2 {
		t.Fatal("DNS was not revalidated on actual dial")
	}
}

func TestDirectFallbackRejectsPrivateConnectedPeer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("sent request to private peer") }))
	defer srv.Close()
	c := failedCrawler(t)
	c.direct = directForServer(t, srv)
	c.direct.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	if _, err := c.fetch(context.Background(), "http://public.example/"); err == nil || !strings.Contains(err.Error(), "blocked_destination") {
		t.Fatalf("got %v", err)
	}
}

func TestDirectFallbackValidatesInitialURLAndEveryRedirect(t *testing.T) {
	for _, destination := range []string{"file:///etc/passwd", "ftp://public.example/", "http://user:password@public.example/", "http://public.example:8080/", "http://public.example:0/", "http://public.example:bad/", "http://public.example:/", "http://localhost/", "http://service.internal/", "http://service.local./", "http://127.0.0.1/", "http://[::ffff:127.0.0.1]/"} {
		t.Run(destination, func(t *testing.T) {
			for _, redirect := range []bool{false, true} {
				var received atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					received.Add(1)
					http.Redirect(w, r, destination, http.StatusFound)
				}))
				c := failedCrawler(t)
				c.direct = directForServer(t, srv)
				c.direct.lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
					if ip := net.ParseIP(host); ip != nil {
						return []net.IPAddr{{IP: ip}}, nil
					}
					return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
				}
				u := destination
				if redirect {
					u = "http://public.example/"
				}
				_, err := c.fetch(context.Background(), u)
				srv.Close()
				if err == nil {
					t.Fatalf("accepted %q, redirect=%t", destination, redirect)
				}
				wantRequests := int32(0)
				if redirect {
					wantRequests = 1
				}
				if received.Load() != wantRequests {
					t.Errorf("redirect=%t: got %d requests, want %d", redirect, received.Load(), wantRequests)
				}
			}
		})
	}
}

func TestPublicIPOnlyAcceptsPublicIPv4AndGlobalIPv6(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true}, {"93.184.216.34", true}, {"2001:4860:4860::8888", true}, {"2606:4700:4700::1111", true}, {"::ffff:8.8.8.8", true},
		{"4000::1", false}, {"::2", false}, {"64:ff9b::a00:1", false}, {"::ffff:192.0.2.1", false}, {"3fff::1", false},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			if got := publicIP(net.ParseIP(tc.ip)); got != tc.want {
				t.Errorf("publicIP(%s)=%t, want %t", tc.ip, got, tc.want)
			}
		})
	}
}

func TestDirectFallbackRejectsBadPageResponses(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, want string
		status                        int
	}{
		{"empty HTML", "text/html", "<html><script>ignored</script></html>", "empty_content", 200},
		{"empty plain", "text/plain", " \n\t ", "empty_content", 200},
		{"JSON", "application/json", `{"title":"not a page"}`, "unsupported_content_type", 200},
		{"PDF", "application/pdf", "%PDF", "unsupported_content_type", 200},
		{"malformed type", "text/html; bad", "hello", "unsupported_content_type", 200},
		{"status", "text/html", "<p>error page</p>", "http_status", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c := failedCrawler(t)
			c.direct = directForServer(t, srv)
			if got, err := c.fetch(context.Background(), "http://public.example/"); got.Text != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestDirectFallbackParsesTheFirstMiBOfALargePage(t *testing.T) {
	// 1 MiB + change, with the cut landing inside a two-byte rune and an unclosed <div>.
	body := "<html><head><title>Big page</title></head><body><p>Lead facts up top</p><div>" + strings.Repeat("é", 1<<19) + "</div></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c := failedCrawler(t)
	c.direct = directForServer(t, srv)
	got, err := c.fetch(context.Background(), "http://public.example/")
	if err != nil || got.Title != "Big page" || !strings.HasPrefix(got.Text, "Lead facts up top\n") {
		t.Fatalf("got %q / %.40q, %v", got.Title, got.Text, err)
	}
	if !utf8.ValidString(got.Text) || len(got.Text) > maxDirectBody {
		t.Fatalf("text must be valid UTF-8 within the cap, got %d bytes, valid=%t", len(got.Text), utf8.ValidString(got.Text))
	}
}

func TestDirectFallbackReadsPlainTextAndXHTML(t *testing.T) {
	for _, tc := range []struct{ contentType, body, want string }{{"text/plain; charset=utf-8", "Plain page facts\nMore facts", "Plain page facts\nMore facts"}, {"application/xhtml+xml", "<html><body><p>Visible XHTML</p></body></html>", "Visible XHTML"}} {
		t.Run(tc.contentType, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c := failedCrawler(t)
			c.direct = directForServer(t, srv)
			got, err := c.fetch(context.Background(), "http://public.example/")
			if err != nil || !strings.Contains(got.Text, tc.want) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestDirectFallbackBoundsRedirects(t *testing.T) {
	var received atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer srv.Close()
	c := failedCrawler(t)
	c.direct = directForServer(t, srv)
	if _, err := c.fetch(context.Background(), "http://public.example/"); err == nil || !strings.Contains(err.Error(), "too_many_redirects") {
		t.Fatalf("got %v", err)
	}
	if received.Load() != 11 {
		t.Fatalf("got %d requests, want first request plus 10 redirects", received.Load())
	}
}

func TestPrimaryTimeoutReservesCallerTimeForFallback(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer primary.Close()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Recovered page facts")
	}))
	defer direct.Close()
	c := newCrawler(primary.URL, "secret")
	c.direct = directForServer(t, direct)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	got, err := c.fetch(ctx, "http://public.example/")
	if err != nil || !strings.Contains(got.Text, "Recovered page facts") {
		t.Fatalf("got %q, %v", got, err)
	}
	if ctx.Err() != nil {
		t.Fatal("primary consumed entire caller deadline")
	}
}

func TestDirectFallbackTimeoutAndCallerCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	c := failedCrawler(t)
	c.direct = directForServer(t, srv)
	c.direct.timeout = 20 * time.Millisecond
	if _, err := c.fetch(context.Background(), "http://public.example/"); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wanted bounded direct timeout, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var lookups atomic.Int32
	c.direct.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		lookups.Add(1)
		return nil, errors.New("unexpected")
	}
	if _, err := c.fetch(ctx, "http://public.example/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if lookups.Load() != 0 {
		t.Fatal("canceled caller started fallback")
	}
}

func TestCallerCancellationDuringPrimarySkipsFallback(t *testing.T) {
	started := make(chan struct{})
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer primary.Close()
	c := newCrawler(primary.URL, "secret")
	c.direct = &directFetcher{lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
		t.Error("canceled primary invoked fallback")
		return nil, errors.New("unexpected")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	if _, err := c.fetch(ctx, "http://public.example/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestFallbackLogsOnlyHostAndSafeCategories(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Recovered")
	}))
	defer srv.Close()
	c := failedCrawler(t)
	c.direct = directForServer(t, srv)
	if _, err := c.fetch(context.Background(), "http://public.example/page?secret=private-query"); err != nil {
		t.Fatal(err)
	}
	c.direct.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("private-query crawler-secret")
	}
	_, _ = c.fetch(context.Background(), "http://public.example/page?secret=private-query")
	got := logs.String()
	for _, want := range []string{"recovered", "failed", "public.example", "dns"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in logs %q", want, got)
		}
	}
	for _, secret := range []string{"private-query", "crawler-secret", "http://public.example/page"} {
		if strings.Contains(got, secret) {
			t.Errorf("logged sensitive value %q", secret)
		}
	}
}
