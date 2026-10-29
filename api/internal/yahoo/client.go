package yahoo

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBase = "https://query1.finance.yahoo.com"
	userAgent   = "Mozilla/5.0"
	maxBody     = 16 << 20 // a lifetime of daily bars is well under 1 MB
	sparkBatch  = 20       // symbols per v7 spark call
	fanout      = 4        // per-symbol chart fallbacks at once (the gate bounds them again)

	quoteTTL    = 60 * time.Second
	dailyTTL    = time.Hour
	longTTL     = 12 * time.Hour // weekly/monthly bars and max-range history
	notFoundTTL = 15 * time.Minute
	flightTTL   = 2 * time.Minute // one shared fetch, pacing waits included
	sparkPause  = time.Hour       // spark answered in a shape we do not read: charts only meanwhile
)

// Config configures New. The zero value is production: the real endpoint and
// the process-wide gate.
type Config struct {
	Base string       // "" → https://query1.finance.yahoo.com (YAHOO_BASE; tests point it at yahootest)
	HTTP *http.Client // nil → a plain client (each attempt has its own 10 s deadline)
	// Unpaced gives the client a gate of its own with the in-flight bound and
	// the 429 breaker but no politeness pacing: for a local fake such as
	// yahootest, never for Yahoo itself.
	Unpaced bool
}

// Client is the real Source. Its methods are safe for concurrent use and
// answer unavailable on a nil *Client.
type Client struct {
	base    string
	hc      *http.Client
	gate    *gate
	timeout time.Duration // per attempt
	backoff time.Duration // before retry n: backoff << n
	now     func() time.Time

	mu       sync.Mutex
	cache    map[string]entry
	flights  map[string]*flight
	bars     int // bars held by cached charts
	maxBars  int // memory bound: ~80 bytes a bar
	maxKeys  int
	sparkOff time.Time
}

type entry struct {
	val  any
	err  error
	exp  time.Time
	bars int
}

type flight struct {
	done    chan struct{}
	val     any
	err     error
	waiters int
	cancel  context.CancelFunc
}

func New(cfg Config) *Client {
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	g := shared
	if cfg.Unpaced {
		g = newGate(1000, 1000, 4, 5, 3*time.Minute)
	}
	return &Client{base: strings.TrimRight(cmp.Or(cfg.Base, defaultBase), "/"), hc: hc, gate: g,
		timeout: 10 * time.Second, backoff: time.Second, now: time.Now,
		cache: map[string]entry{}, flights: map[string]*flight{}, maxBars: 1_000_000, maxKeys: 10_000}
}

// Chart implements Source.
func (c *Client) Chart(ctx context.Context, symbol, rng, interval string) (Chart, error) {
	if c == nil {
		return Chart{}, off(symbol)
	}
	sym, ok := Normalize(symbol)
	if !ok {
		return Chart{}, &UnavailableError{Symbol: strings.TrimSpace(symbol), Reason: "not a valid Yahoo symbol", NotFound: true}
	}
	if !ranges[rng] || !intervals[interval] {
		return Chart{}, fmt.Errorf("yahoo: invalid range %q or interval %q", rng, interval)
	}
	ttl := chartTTL(rng, interval)
	if ctx.Value(noCache{}) != nil {
		ttl = 0
	}
	v, err := c.do(ctx, "chart|"+sym+"|"+rng+"|"+interval, sym, ttl, func(ctx context.Context) (any, error) {
		return c.fetchChart(ctx, sym, rng, interval)
	})
	if err != nil {
		return Chart{}, err
	}
	ch := v.(Chart)
	ch.Bars = slices.Clone(ch.Bars) // the cached copy stays intact
	return ch, nil
}

func chartTTL(rng, interval string) time.Duration {
	switch {
	case rng == "max" || longInterval(interval):
		return longTTL
	case interval == "1d":
		return dailyTTL
	}
	return quoteTTL // intraday
}

func longInterval(interval string) bool {
	return interval == "5d" || interval == "1wk" || interval == "1mo" || interval == "3mo"
}

func (c *Client) fetchChart(ctx context.Context, sym, rng, interval string) (Chart, error) {
	q := url.Values{"interval": {interval}, "includePrePost": {"false"}}
	if rng == "max" {
		// range=max serves monthly bars whatever the interval (1d and 1wk both
		// came back with dataGranularity 1mo, 2026-10-06); an explicit lifetime
		// window keeps the interval asked for.
		q.Set("period1", "0")
		q.Set("period2", strconv.FormatInt(c.now().Unix(), 10))
	} else {
		q.Set("range", rng)
	}
	body, status, err := c.get(ctx, "/v8/finance/chart/"+url.PathEscape(sym), q)
	if err != nil {
		return Chart{}, withSymbol(err, sym)
	}
	return parseChart(sym, rng, interval, status, body)
}

// Quotes implements Source: cached quotes first, then v7 spark batches of 20
// (verified keyless and cookieless 2026-10-06), and per-symbol 5-day daily
// charts when spark answers in a shape this client does not read.
func (c *Client) Quotes(ctx context.Context, symbols []string) (map[string]Quote, error) {
	if c == nil {
		return nil, off("")
	}
	out := map[string]Quote{}
	var want []string
	seen := map[string]bool{}
	for _, s := range symbols {
		sym, ok := Normalize(s)
		if !ok || seen[sym] {
			continue
		}
		seen[sym] = true
		if v, err, hit := c.lookup("q|" + sym); hit {
			if err == nil {
				out[sym] = v.(Quote)
			}
			continue
		}
		want = append(want, sym)
	}
	var first error
	note := func(err error) {
		if first == nil && err != nil && !IsNotFound(err) {
			first = err
		}
	}
	if c.sparkOn() {
		var rest []string
		for chunk := range slices.Chunk(want, sparkBatch) {
			key := "spark|" + strings.Join(chunk, ",")
			v, err := c.do(ctx, key, "", 0, func(ctx context.Context) (any, error) { return c.fetchSpark(ctx, chunk) })
			switch {
			case errors.Is(err, errSparkShape):
				rest = append(rest, chunk...)
			case ctx.Err() != nil:
				return out, ctx.Err()
			case err != nil:
				note(err)
			default:
				maps.Copy(out, v.(map[string]Quote))
			}
		}
		want = rest
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, fanout)
	for _, sym := range want {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			v, err := c.do(ctx, "q|"+sym, sym, quoteTTL, func(ctx context.Context) (any, error) {
				ch, err := c.fetchChart(ctx, sym, "5d", "1d")
				if err != nil {
					return nil, err
				}
				return quoteFrom(sym, ch.Meta, "chart")
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				out[sym] = v.(Quote)
			}
			note(err)
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, first
}

// errSparkShape: spark answered, but not as a v7 spark document (or with a
// status other than 200/404); the batch falls back to charts.
var errSparkShape = errors.New("yahoo: unexpected spark answer")

func (c *Client) fetchSpark(ctx context.Context, chunk []string) (any, error) {
	q := url.Values{"symbols": {strings.Join(chunk, ",")}, "range": {"1d"}, "interval": {"1d"}}
	body, status, err := c.get(ctx, "/v7/finance/spark", q)
	if err != nil {
		return nil, err
	}
	r, ok := parseSpark(body)
	if !ok || status != http.StatusOK && status != http.StatusNotFound {
		c.mu.Lock()
		c.sparkOff = c.now().Add(sparkPause)
		c.mu.Unlock()
		slog.Warn("yahoo spark unreadable; per-symbol charts for an hour", "status", status)
		return nil, errSparkShape
	}
	got := map[string]Quote{}
	for _, res := range r.Spark.Result {
		sym, ok := Normalize(res.Symbol)
		if !ok || !slices.Contains(chunk, sym) || len(res.Response) == 0 {
			continue
		}
		q, err := quoteFrom(sym, res.Response[0].Meta.meta(sym), "spark")
		if err != nil {
			c.store("q|"+sym, nil, err, quoteTTL)
			continue
		}
		got[sym] = q
		c.store("q|"+sym, q, nil, quoteTTL)
	}
	for _, sym := range chunk {
		if _, ok := got[sym]; !ok {
			if _, _, hit := c.lookup("q|" + sym); !hit {
				c.store("q|"+sym, nil, notFound(sym), notFoundTTL) // spark leaves out what Yahoo does not serve
			}
		}
	}
	return got, nil
}

func quoteFrom(sym string, m Meta, via string) (Quote, error) {
	if m.Price == nil || m.PriceTime == nil || m.Currency == "" {
		return Quote{}, unavailable(sym, "Yahoo sent no regular-market price")
	}
	return Quote{Symbol: sym, Price: *m.Price, Time: *m.PriceTime, Currency: m.Currency, Meta: m, Via: via}, nil
}

func (c *Client) sparkOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.now().Before(c.sparkOff)
}

// Batched reports whether Quotes reads in spark batches of 20 now; false for
// an hour after spark answered in a shape this client does not read, while
// every quote costs its own chart read (Batcher).
func (c *Client) Batched() bool { return c != nil && c.sparkOn() }

// FX implements Source.
func (c *Client) FX(ctx context.Context, ccy string) (Rate, error) {
	if c == nil {
		return Rate{}, off(ccy)
	}
	pair, div, ok := fxPair(ccy)
	if !ok {
		return Rate{}, &UnavailableError{Symbol: ccy, Reason: "not a currency code", NotFound: true}
	}
	if pair == "" {
		return Rate{Ccy: "USD", USD: 1}, nil
	}
	// The pair's meta price is rounded to 4 decimals (IDRUSD=X reads 0.0001);
	// its bar closes are not, and the newest bar is the live rate.
	v, err := c.do(ctx, "fx|"+pair, pair, quoteTTL, func(ctx context.Context) (any, error) {
		ch, err := c.fetchChart(ctx, pair, "5d", "1d")
		switch {
		case err != nil:
			return nil, err
		case ch.Meta.Currency != "USD":
			return nil, unavailable(pair, "FX quote is not in USD")
		case len(ch.Bars) == 0:
			return nil, unavailable(pair, "Yahoo sent no FX rate")
		}
		last := ch.Bars[len(ch.Bars)-1]
		return Point{T: last.T, V: last.C}, nil
	})
	if err != nil {
		return Rate{}, err
	}
	p := v.(Point)
	return Rate{Ccy: strings.TrimSpace(ccy), USD: p.V / div, Time: p.T, Symbol: pair}, nil
}

// Quote implements Source.
func (c *Client) Quote(ctx context.Context, symbol string) (Quote, error) {
	if c == nil {
		return Quote{}, off(symbol)
	}
	sym, ok := Normalize(symbol)
	if !ok {
		return Quote{}, &UnavailableError{Symbol: strings.TrimSpace(symbol), Reason: "not a valid Yahoo symbol", NotFound: true}
	}
	qs, err := c.Quotes(ctx, []string{sym})
	if q, ok := qs[sym]; ok {
		return q, nil
	}
	if _, cached, hit := c.lookup("q|" + sym); hit && cached != nil {
		return Quote{}, cached // the symbol's own reason, e.g. no regular-market price
	}
	if err != nil {
		return Quote{}, withSymbol(err, sym)
	}
	return Quote{}, notFound(sym)
}

// FXHistory implements Source.
func (c *Client) FXHistory(ctx context.Context, ccy, rng string) ([]Point, error) {
	if c == nil {
		return nil, off(ccy)
	}
	pair, div, ok := fxPair(ccy)
	if !ok {
		return nil, &UnavailableError{Symbol: ccy, Reason: "not a currency code", NotFound: true}
	}
	if pair == "" {
		return nil, nil
	}
	ch, err := c.Chart(ctx, pair, rng, "1d")
	if err != nil {
		return nil, err
	}
	if ch.Meta.Currency != "USD" {
		return nil, unavailable(pair, "FX history is not in USD")
	}
	if len(ch.Bars) == 0 {
		return nil, unavailable(pair, "Yahoo sent no FX history")
	}
	out := make([]Point, len(ch.Bars))
	for i, b := range ch.Bars {
		out[i] = Point{T: b.T, V: b.C / div}
	}
	return out, nil
}

// minorUnits are the sub-unit currencies Yahoo prices some listings in.
var minorUnits = map[string]struct {
	ccy string
	div float64
}{"GBp": {"GBP", 100}, "GBX": {"GBP", 100}, "ZAc": {"ZAR", 100}, "ILA": {"ILS", 100}}

// fxPair is ccy's Yahoo FX symbol and divisor ("SGD" → "SGDUSD=X", 1;
// "GBp" → "GBPUSD=X", 100); USD has no pair. ccy is case-sensitive for the
// minor units, as Yahoo labels them.
func fxPair(ccy string) (pair string, div float64, ok bool) {
	ccy = strings.TrimSpace(ccy)
	code, div := strings.ToUpper(ccy), 1.0
	if m, ok := minorUnits[ccy]; ok {
		code, div = m.ccy, m.div
	}
	if len(code) != 3 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return "", 0, false
	}
	if code == "USD" {
		return "", 1, true
	}
	return code + "USD=X", div, true
}

// get fetches base+path?q through the gate and returns any answer except a
// 429 or 5xx that outlived two retries, or a transport failure.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, int, error) {
	u := c.base + path + "?" + q.Encode()
	for attempt := 0; ; attempt++ {
		if err := c.gate.open(); err != nil {
			return nil, 0, err
		}
		if err := c.gate.wait(ctx); err != nil {
			return nil, 0, err
		}
		body, status, err := c.once(ctx, u)
		if err != nil {
			return nil, 0, err
		}
		if status == http.StatusTooManyRequests {
			c.gate.limited()
		} else {
			c.gate.ok()
		}
		if status != http.StatusTooManyRequests && status < 500 {
			return body, status, nil
		}
		if attempt == 2 {
			if status == http.StatusTooManyRequests {
				return nil, status, unavailable("", "rate limited by Yahoo")
			}
			return nil, status, unavailable("", "Yahoo answered HTTP %d", status)
		}
		t := time.NewTimer(c.backoff << attempt)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, 0, ctx.Err()
		}
	}
}

// once is one attempt in an in-flight slot. The body is never logged.
func (c *Client) once(ctx context.Context, u string) ([]byte, int, error) {
	if err := c.gate.acquire(ctx); err != nil {
		return nil, 0, err
	}
	defer c.gate.release()
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	// Browser-like but short: from the deploy host Yahoo once answered 429 to
	// a full Chrome User-Agent and 200 to "Mozilla/5.0" (2026-10-06).
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	CountCall(ctx)
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if actx.Err() != nil {
			return nil, 0, unavailable("", "Yahoo timed out")
		}
		return nil, 0, unavailable("", "Yahoo unreachable")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	switch {
	case ctx.Err() != nil:
		return nil, 0, ctx.Err()
	case err != nil:
		return nil, 0, unavailable("", "Yahoo answer cut off")
	case len(body) > maxBody:
		return nil, 0, unavailable("", "Yahoo answer too large")
	}
	return body, resp.StatusCode, nil
}

func withSymbol(err error, sym string) error {
	var u *UnavailableError
	if errors.As(err, &u) && u.Symbol == "" {
		cp := *u
		cp.Symbol = sym
		return &cp
	}
	return err
}

// do answers key from the cache or from one shared fetch. Successes are kept
// for ttl and not-found answers for notFoundTTL (ttl 0 keeps nothing); other
// errors are not cached. The fetch runs detached from any one caller and is
// cancelled when every waiter has gone.
func (c *Client) do(ctx context.Context, key, sym string, ttl time.Duration, fetch func(context.Context) (any, error)) (any, error) {
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && c.now().Before(e.exp) {
		c.mu.Unlock()
		return e.val, e.err
	}
	f, ok := c.flights[key]
	if !ok {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flightTTL)
		f = &flight{done: make(chan struct{}), cancel: cancel}
		c.flights[key] = f
		go func() {
			defer cancel()
			val, err := fetch(fctx)
			if errors.Is(err, context.DeadlineExceeded) && fctx.Err() != nil {
				err = withSymbol(unavailable("", "Yahoo request timed out"), sym)
			}
			c.mu.Lock()
			f.val, f.err = val, err
			if c.flights[key] == f {
				delete(c.flights, key)
				if err == nil {
					c.storeLocked(key, val, nil, ttl)
				} else if IsNotFound(err) && ttl > 0 {
					c.storeLocked(key, nil, err, min(ttl, notFoundTTL))
				}
			}
			c.mu.Unlock()
			close(f.done)
		}()
	}
	f.waiters++
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		c.mu.Lock()
		if f.waiters--; f.waiters == 0 {
			f.cancel()
			if c.flights[key] == f {
				delete(c.flights, key) // a later caller starts afresh
			}
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *Client) lookup(key string) (any, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.cache[key]; ok && c.now().Before(e.exp) {
		return e.val, e.err, true
	}
	return nil, nil, false
}

func (c *Client) store(key string, val any, err error, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storeLocked(key, val, err, ttl)
}

func (c *Client) storeLocked(key string, val any, err error, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.dropLocked(key)
	n := 0
	if ch, ok := val.(Chart); ok {
		n = len(ch.Bars)
	}
	c.cache[key] = entry{val: val, err: err, exp: c.now().Add(ttl), bars: n}
	c.bars += n
	c.evictLocked()
}

func (c *Client) dropLocked(key string) {
	if e, ok := c.cache[key]; ok {
		c.bars -= e.bars
		delete(c.cache, key)
	}
}

// evictLocked keeps the cache under its key and bar bounds: expired entries
// first, then those closest to expiry.
func (c *Client) evictLocked() {
	over := func(frac int) bool { return len(c.cache) > c.maxKeys*frac/10 || c.bars > c.maxBars*frac/10 }
	if !over(10) {
		return
	}
	now := c.now()
	for k, e := range c.cache {
		if !now.Before(e.exp) {
			c.dropLocked(k)
		}
	}
	if !over(10) {
		return
	}
	keys := slices.SortedFunc(maps.Keys(c.cache), func(a, b string) int { return c.cache[a].exp.Compare(c.cache[b].exp) })
	for _, k := range keys {
		if !over(9) {
			break
		}
		c.dropLocked(k)
	}
}
