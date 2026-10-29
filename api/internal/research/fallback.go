package research

// The direct fetch the crawler falls back to: SSRF-guarded, returns the page
// <title> too, skips nav/footer/aside/button, sends a User-Agent.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const maxDirectBody = 1 << 20

type directFetcher struct {
	lookupIP    func(context.Context, string) ([]net.IPAddr, error)
	dialContext func(context.Context, string, string) (net.Conn, error)
	timeout     time.Duration
}

func newDirectFetcher() *directFetcher {
	return &directFetcher{
		lookupIP:    net.DefaultResolver.LookupIPAddr,
		dialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		timeout:     30 * time.Second,
	}
}

// directFailure deliberately hides raw network errors, whose messages may
// contain a URL query or credentials. Unwrap preserves cancellation checks.
type directFailure struct {
	category string
	cause    error
}

func (e *directFailure) Error() string { return "direct fetch: " + e.category }
func (e *directFailure) Unwrap() error { return e.cause }

func directCategory(err error) string {
	var failure *directFailure
	if errors.As(err, &failure) {
		return failure.category
	}
	return "network"
}

// fetch returns the page title and its visible text.
func (f *directFetcher) fetch(ctx context.Context, rawURL string) (title, text string, err error) {
	if f == nil {
		f = newDirectFetcher()
	}
	budget := f.timeout
	if budget <= 0 || budget > 30*time.Second {
		budget = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", &directFailure{category: "invalid_url"}
	}
	if err := f.validateURL(ctx, u); err != nil {
		return "", "", err
	}
	// A fresh transport never uses environment proxies, the crawler's bearer,
	// its cookies, or any client-wide authentication state.
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           f.dialPublic,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Go synthesizes a Referer from the previous URL. Its query may
			// contain a private token, so never forward it to another page.
			req.Header.Del("Referer")
			if len(via) > 10 {
				return &directFailure{category: "too_many_redirects"}
			}
			return f.validateURL(req.Context(), req.URL)
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", &directFailure{category: "invalid_url"}
	}
	req.Header.Set("User-Agent", "Foretrade")
	req.Header.Set("Accept", "text/html, application/xhtml+xml, text/plain")
	resp, err := client.Do(req)
	if err != nil {
		var failure *directFailure
		if errors.As(err, &failure) {
			return "", "", failure
		}
		if ctx.Err() != nil {
			return "", "", &directFailure{category: "timeout", cause: ctx.Err()}
		}
		return "", "", &directFailure{category: "network", cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", &directFailure{category: "http_status"}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mediaType != "text/html" && mediaType != "application/xhtml+xml" && mediaType != "text/plain") {
		return "", "", &directFailure{category: "unsupported_content_type"}
	}
	// Only the first 1 MiB is read and parsed: news pages often run just past it
	// (scripts, inline JSON) while the article text sits near the top, and the
	// HTML parser closes a cut document cleanly.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDirectBody))
	if err != nil {
		if ctx.Err() != nil {
			return "", "", &directFailure{category: "timeout", cause: ctx.Err()}
		}
		return "", "", &directFailure{category: "response_read", cause: err}
	}
	// The cut can split a rune, and non-UTF-8 pages would fail the content save.
	body = bytes.ToValidUTF8(body, nil)
	text = strings.TrimSpace(string(body))
	if mediaType != "text/plain" {
		title, text, err = visibleHTML(body)
		if err != nil {
			return "", "", &directFailure{category: "html_parse", cause: err}
		}
	}
	if text == "" {
		return "", "", &directFailure{category: "empty_content"}
	}
	if ctx.Err() != nil {
		return "", "", &directFailure{category: "timeout", cause: ctx.Err()}
	}
	return title, text, nil
}

func (f *directFetcher) validateURL(ctx context.Context, u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.Opaque != "" || strings.HasSuffix(u.Host, ":") {
		return &directFailure{category: "invalid_url"}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") || strings.Contains(host, "%") {
		return &directFailure{category: "blocked_destination"}
	}
	port := u.Port()
	if port != "" && ((u.Scheme == "http" && port != "80") || (u.Scheme == "https" && port != "443")) {
		return &directFailure{category: "invalid_url"}
	}
	_, err := f.publicIPs(ctx, host)
	return err
}

func (f *directFetcher) publicIPs(ctx context.Context, host string) ([]net.IPAddr, error) {
	if err := ctx.Err(); err != nil {
		return nil, &directFailure{category: "timeout", cause: err}
	}
	var ips []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IPAddr{{IP: ip}}
	} else {
		lookup := f.lookupIP
		if lookup == nil {
			lookup = net.DefaultResolver.LookupIPAddr
		}
		var err error
		ips, err = lookup(ctx, host)
		if err != nil || len(ips) == 0 {
			if ctx.Err() != nil {
				return nil, &directFailure{category: "timeout", cause: ctx.Err()}
			}
			return nil, &directFailure{category: "dns", cause: err}
		}
	}
	for _, ip := range ips {
		if ip.Zone != "" || !publicIP(ip.IP) {
			return nil, &directFailure{category: "blocked_destination"}
		}
	}
	return ips, nil
}

func (f *directFetcher) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || (port != "80" && port != "443") {
		return nil, &directFailure{category: "invalid_url"}
	}
	// Resolve again at the actual connection boundary, then dial an IP literal
	// so the lower-level dialer cannot perform a second, unvalidated lookup.
	ips, err := f.publicIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	dial := f.dialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	}
	var lastErr error
	for _, ip := range ips {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err != nil {
			lastErr = err
			continue
		}
		peer, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok || !publicIP(peer.IP) {
			conn.Close()
			return nil, &directFailure{category: "blocked_destination"}
		}
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, &directFailure{category: "timeout", cause: ctx.Err()}
	}
	return nil, &directFailure{category: "network", cause: lastErr}
}

// IsGlobalUnicast alone admits documentation, benchmarking, CGNAT and
// other reserved ranges. IPv4-mapped IPv6 is unmapped before these checks.
var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fec0::/10"),
}

var globalIPv6 = netip.MustParsePrefix("2000::/3")

func publicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() {
		return false
	}
	if address.Is6() && !globalIPv6.Contains(address) {
		return false
	}
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

// visibleHTML returns the first <title> and the visible text, one line per block.
func visibleHTML(body []byte) (title, visible string, err error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "title":
				if title == "" && node.FirstChild != nil {
					title = strings.Join(strings.Fields(node.FirstChild.Data), " ")
				}
				return
			case "script", "style", "noscript", "svg", "template", "nav", "footer", "aside", "button":
				return
			}
		}
		block := node.Type == html.ElementNode && isTextBlock(node.Data)
		if block {
			text.WriteByte('\n')
		}
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
			text.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
		if block {
			text.WriteByte('\n')
		}
	}
	visit(doc)
	var lines []string
	for _, line := range strings.Split(text.String(), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return title, strings.Join(lines, "\n"), nil
}

func isTextBlock(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "br", "div", "dl", "dt", "dd", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav", "ol", "p", "pre", "section", "table", "tr", "td", "th", "ul":
		return true
	}
	return false
}
