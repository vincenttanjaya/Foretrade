package main

// The integration suite. Each TestIntegration* test boots the real server —
// run, called the way main calls it after config.Load — on 127.0.0.1:0
// against a fresh Postgres schema that the app migrates itself, with local
// fakes for every upstream: Bedrock (llmtest, reached by the real AWS SDK
// through AWS_ENDPOINT_URL_BEDROCK_RUNTIME), Binance (markettest), Brave
// Search and crawl4ai (fakeWeb). Tests drive real HTTP with a cookie jar and
// check status codes, the contract's JSON shapes and the rows left in Postgres.
//
// They skip without INTEGRATION_DATABASE_URL. The "Integration tests"
// workflow sets it and fails on any skipped or missing required test
// (ci/check_go_results.py lists them). Tests set the environment, so they run
// one at a time.

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

func TestMain(m *testing.M) {
	time.Local = time.UTC // as main does, before any goroutine reads it
	os.Exit(m.Run())
}

const (
	testPassword = "integration-pass-1"
	demoPassword = "demo-pass-1"
	braveKey     = "brave-test-key"
	crawlToken   = "crawl-test-token"
	// pageHost serves every fake article: a public IP literal, so the SSRF
	// guard passes it without DNS. crawl4ai answers every read, so the
	// direct-fetch fallback never dials it.
	pageHost = "https://93.184.215.14"
)

type options struct {
	runtime *runOptions                         // private lifecycle observation, with production defaults otherwise
	credits int                                 // AI_DAILY_CREDITS; 0 → the default 500
	demo    bool                                // DEMO_EMAIL/DEMO_PASSWORD set: the demo user is upserted at boot
	brave   bool                                // BRAVE_API_KEY and CRAWL_TOKEN set: search on, the news wire ingests at boot
	model   func(llmtest.Request) llmtest.Reply // Bedrock's answers besides the market brief; nil → a call fails the test
	gamma   http.HandlerFunc                    // POLYMARKET_BASE (Polymarket's Gamma API); nil → every search finds no events
	live    bool                                // LIVE_ARENA on: the shared live desk steps at boot (its model calls go to model)
	yahoo   func(*yahootest.Fake)               // Yahoo data served from boot: the screener's boot run reads the first 3 constituents
	deep    bool                                // FORECAST_RESEARCH_MODE=deep; off pins quick, so forecasts keep the one-shot research plan
	methods bool                                // FORECAST_METHODS on; off pins the event-frame roles and prompts for every question
	signals bool                                // the signals board's job runs: it analyses every instrument at boot, so only tests that read a computed board opt in
	rt      *realtimeOpts                       // real-time sources (KRX's alone by default) against a fake host, on the market's own clock; nil: every source off
}

// realtimeOpts switches real-time sources on against base, with the market
// client's clock and scheduler step.
type realtimeOpts struct {
	base   string
	now    func() time.Time
	tick   time.Duration
	venues string // QUOTES_REALTIME; "" → "krx"
}

// server is one booted API and its fakes.
type server struct {
	cancel  context.CancelFunc
	done    chan error
	url     string        // http://127.0.0.1:port
	db      *pgxpool.Pool // the server's schema, for side-effect checks
	binance *markettest.Binance
	yahoo   *yahootest.Fake // YAHOO_BASE: empty, so every symbol is not found until a test Sets it
	bedrock *llmtest.Server
	web     *fakeWeb // nil unless options.brave
	logs    *logBuffer
}

// boot starts the API like main does and stops it when the test ends.
// queryWriter is the system prompt of the forecast desk's Polymarket query
// writer; noMarketQueries answers it with none.
const queryWriter = "Write up to 2 short Polymarket search queries"

var noMarketQueries = llmtest.Reply{Text: `{"queries": []}`}

// repairWriter is the system prompt of the forecast desk's specialist repair:
// a fake that returns an invalid specialist distribution on purpose must route
// it (a 400 drops the specialist, a valid answer keeps it).
const repairWriter = "A forecaster's answer gave probabilities"

func boot(t *testing.T, o options) *server {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("INTEGRATION_DATABASE_URL not set")
	}
	s := &server{binance: markettest.New(t), yahoo: yahootest.New()}
	if o.yahoo != nil {
		o.yahoo(s.yahoo)
	}
	dsn, s.db = freshSchema(t, dsn)
	_, s.bedrock = llmtest.New(t, func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "You are the markets editor"): // the wire ingestion's brief
			return llmtest.Reply{Text: briefReply}
		case strings.HasPrefix(r.System, "You write one-line descriptions for a momentum screen"): // the signals board at boot
			return llmtest.Reply{Text: `{"picks":[]}`}
		case o.model != nil:
			return o.model(r)
		}
		t.Errorf("unexpected Bedrock call: %.80q", r.System)
		return llmtest.Reply{Status: http.StatusBadRequest}
	})
	isolateAWS(t, *s.bedrock.Config().BaseEndpoint)
	env := map[string]string{
		"DATABASE_URL": dsn, "PORT": "0", "ALLOWED_ORIGINS": origin + "," + adminOrigin, "ADMIN_ORIGINS": adminOrigin,
		"AI_PROVIDER": "bedrock", "AI_MODEL": llmtest.Model, "AI_BASE_URL": "", "AI_API_KEY": "", "AI_EFFORT": "", "SECRET_KEY": "",
		"COOKIE_SECURE": "false", "AI_DAILY_CREDITS": strconv.Itoa(cmp.Or(o.credits, 500)), "MODEL_KNOWLEDGE_CUTOFF": "2026-06-30",
		"BINANCE_BASE": s.binance.URL, "HYPERLIQUID_BASE": s.binance.URL /* markettest answers Hyperliquid's /info */, "DEMO_EMAIL": "", "DEMO_PASSWORD": "", "DEMO_NAME": "",
		"BRAVE_API_KEY": "", "BRAVE_BASE": "", "BRAVE_DAILY_LIMIT": "", "CRAWL_BASE": "http://127.0.0.1:1", "CRAWL_TOKEN": "",
		"LIVE_ARENA": "off", // the live desk would call the model at boot; TestIntegrationLiveArena turns it on
		"YAHOO_BASE": s.yahoo.Serve(t), "SCREENER_MAX_TICKERS": "3",
		"QUOTES_REALTIME": "", "QUOTES_REALTIME_BASE": "", // no real-time source reaches out; TestIntegrationRealtimeQuotes points one at a fake
		"FORECAST_RESEARCH_MODE": "quick", "FORECAST_RESEARCH_SEARCHES": "", // deep research only where a test asks for it
		"FORECAST_METHODS": "off", // the desk's per-frame methods only where a test asks for them
	}
	if o.deep {
		env["FORECAST_RESEARCH_MODE"] = "deep"
	}
	if o.methods {
		env["FORECAST_METHODS"] = "on"
	}
	if o.live {
		env["LIVE_ARENA"] = "on"
	}
	if o.rt != nil {
		env["QUOTES_REALTIME"], env["QUOTES_REALTIME_BASE"] = cmp.Or(o.rt.venues, "krx"), o.rt.base
	}
	if o.demo {
		env["DEMO_EMAIL"], env["DEMO_PASSWORD"], env["DEMO_NAME"] = " Demo@Example.Test ", demoPassword, "Demo Investor"
	}
	if o.brave {
		s.web = newFakeWeb(t)
		env["BRAVE_API_KEY"], env["BRAVE_BASE"], env["CRAWL_BASE"], env["CRAWL_TOKEN"] = braveKey, s.web.braveURL, s.web.crawlURL, crawlToken
	}
	gamma := o.gamma
	if gamma == nil {
		gamma = func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"events":[]}`) }
	}
	gammaSrv := httptest.NewServer(gamma)
	t.Cleanup(gammaSrv.Close)
	env["POLYMARKET_BASE"] = gammaSrv.URL
	for k, v := range env {
		t.Setenv(k, v)
	}

	s.logs = captureLogs(t) // before the server starts: its cleanup must run after the server stops

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.url = "http://" + ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	s.cancel, s.done = cancel, done
	var ro runOptions
	if o.runtime != nil {
		ro = *o.runtime
	}
	if !o.signals { // the board's boot analysis doubles a boot's CPU on a loaded runner
		inner := ro.worker
		ro.worker = func(name string, work func()) {
			switch {
			case name == "signals":
			case inner != nil:
				inner(name, work)
			default:
				work()
			}
		}
	}
	// The Yahoo fake is local, so its client skips the pacing kept for Yahoo
	// itself, which would add seconds to every boot's quote-feed refresh.
	fake, then := yahoo.New(yahoo.Config{Base: cfg.YahooBase, Unpaced: true}), ro.deps
	ro.deps = func(d *app.Deps) {
		d.Yahoo, d.Market.Yahoo = fake, fake
		if o.rt != nil {
			d.Market.Now, d.Market.RealtimeTick = o.rt.now, o.rt.tick
		}
		if then != nil {
			then(d)
		}
	}
	go func() { done <- runWithOptions(ctx, cfg, ln, ro) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("server did not stop within 30 s")
		}
	})

	// Ready once /healthz answers: boot migrates and may seed the demo user first.
	ping := &http.Client{Timeout: 5 * time.Second}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		select {
		case err := <-done:
			done <- err // for the cleanup
			t.Fatalf("server stopped while booting: %v", err)
		default:
		}
		if resp, err := ping.Get(s.url + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("server not healthy within 30 s")
		}
	}
}

// freshSchema creates a schema of the test's own on the database at dsn and
// returns dsn with search_path set to it, plus a pool on it. Cleanup closes
// the pool and drops the schema.
func freshSchema(t *testing.T, dsn string) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	admin, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "it_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "create schema "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "drop schema "+name+" cascade")
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	pool, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // before the drop: cleanups run last in, first out
	return u.String(), pool
}

// isolateAWS points the AWS SDK's Bedrock client at endpoint with static test
// credentials, away from the developer's AWS config, IMDS and retries.
func isolateAWS(t *testing.T, endpoint string) {
	dir := t.TempDir()
	for k, v := range map[string]string{
		"AWS_ENDPOINT_URL_BEDROCK_RUNTIME": endpoint, "AWS_ENDPOINT_URL": "", "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS": "",
		"AWS_REGION": "us-east-1", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "",
		"AWS_PROFILE": "", "AWS_CONFIG_FILE": dir + "/config", "AWS_SHARED_CREDENTIALS_FILE": dir + "/credentials",
		"AWS_EC2_METADATA_DISABLED": "true", "AWS_MAX_ATTEMPTS": "1",
	} {
		t.Setenv(k, v)
	}
}

// captureLogs sends the server's log to a buffer of the test's, shown when it
// fails. Afterwards stragglers log nowhere, so no output lands on another test.
func captureLogs(t *testing.T) *logBuffer {
	logs := &logBuffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(slog.New(slog.DiscardHandler))
		if t.Failed() {
			t.Logf("server log:\n%s", logs)
		}
	})
	return logs
}

// logBuffer collects one server's log lines from all its goroutines.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// waitLog polls the server log for a line containing every part.
func (s *server) waitLog(t *testing.T, parts ...string) {
	t.Helper()
	eventually(t, 10*time.Second, "log line "+strings.Join(parts, " "), func() bool {
		return slices.ContainsFunc(strings.Split(s.logs.String(), "\n"), func(line string) bool {
			return !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(line, p) })
		})
	})
}

// eventually polls cond every 50 ms until it holds or within passes.
func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !cond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
	}
}

// count runs a count(*) style query on the server's schema.
func (s *server) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// client is a browser session: a cookie jar, the allowed Origin, JSON bodies.
type client struct {
	t         *testing.T
	s         *server
	http      *http.Client
	id, email string // the signed-in user
}

func (s *server) client(t *testing.T) *client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, s: s, http: &http.Client{Jar: jar, Timeout: time.Minute}}
}

// signIn creates a user and logs in over HTTP. Its hash uses bcrypt's minimum
// cost so logins stay fast under -race; the demo user (cost 12) logs in once,
// in TestIntegrationAuth.
func (s *server) signIn(t *testing.T) *client {
	t.Helper()
	email := "it-" + strings.ToLower(rand.Text()) + "@example.test"
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := s.db.QueryRow(context.Background(), "insert into users (email, name, password_hash) values ($1, 'Integration', $2) returning id",
		email, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	c := s.client(t)
	got := c.call("POST", "/api/auth/login", map[string]string{"email": email, "password": testPassword}, 200)
	if str(t, got, "user.id") != id {
		t.Fatalf("login as %s: %v", id, got)
	}
	c.id, c.email = id, email
	return c
}

// withCookie is a client presenting tsid=value.
func (s *server) withCookie(t *testing.T, value string) *client {
	c := s.client(t)
	u, err := url.Parse(s.url)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Jar.SetCookies(u, []*http.Cookie{{Name: "tsid", Value: value}})
	return c
}

// request is what the web app sends: credentials (the jar), the allowed
// Origin and, on writes, Content-Type JSON. A string body goes as is,
// anything else JSON-encoded.
func (c *client) request(method, path string, body any) *http.Request {
	c.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			c.t.Fatal(err)
		}
		r = bytes.NewReader(j)
	}
	req, err := http.NewRequest(method, c.s.url+path, r)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// send sends req and returns the response with its body read.
func (c *client) send(req *http.Request) (*http.Response, []byte) {
	c.t.Helper()
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("%s %s: read body: %v", req.Method, req.URL.Path, err)
	}
	return resp, b
}

// call sends one request, requires status want and decodes the JSON body
// (nil when there is none).
func (c *client) call(method, path string, body any, want int) any {
	c.t.Helper()
	resp, b := c.send(c.request(method, path, body))
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	if len(b) == 0 {
		return nil
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		c.t.Fatalf("%s %s: Content-Type %q: %s", method, path, ct, b)
	}
	return decode(c.t, b)
}

// fail sends one request and requires the contract error: status and code.
func (c *client) fail(method, path string, body any, status int, code string) {
	c.t.Helper()
	got := c.call(method, path, body, status)
	shape(c.t, got, "error", "code message")
	if str(c.t, got, "error.code") != code || str(c.t, got, "error.message") == "" {
		c.t.Fatalf("%s %s: error %v, want code %q", method, path, got, code)
	}
}

type event struct {
	name string
	data any
}

// stream posts a chat message and reads its SSE frames to the end.
func (c *client) stream(body any) []event {
	c.t.Helper()
	resp, err := c.http.Do(c.request("POST", "/api/chat/stream", body))
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache" {
		b, _ := io.ReadAll(resp.Body)
		c.t.Fatalf("chat stream: %d %v %s", resp.StatusCode, resp.Header, b)
	}
	var events []event
	name := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		switch line := sc.Text(); {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			events = append(events, event{name, decode(c.t, []byte(strings.TrimPrefix(line, "data: ")))})
		case line == "", strings.HasPrefix(line, ":"): // frame end, keepalive
		default:
			c.t.Fatalf("chat stream: stray line %q", line)
		}
	}
	if err := sc.Err(); err != nil {
		c.t.Fatal(err)
	}
	return events
}

// JSON access. Responses decode to any (objects are map[string]any, numbers
// float64); a dotted path walks object keys and array indexes, e.g.
// "portfolio.positions.0.qty". A missing step fails the test.

func decode(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return v
}

func at(t *testing.T, v any, path string) any {
	t.Helper()
	if path == "" {
		return v
	}
	for _, k := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			if v, ok = x[k]; !ok {
				t.Fatalf("%s: no key %q in %v", path, k, x)
			}
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) {
				t.Fatalf("%s: no index %q in a list of %d", path, k, len(x))
			}
			v = x[i]
		default:
			t.Fatalf("%s: %q is not inside an object or list: %v", path, k, v)
		}
	}
	return v
}

func num(t *testing.T, v any, path string) float64 {
	t.Helper()
	f, ok := at(t, v, path).(float64)
	if !ok {
		t.Fatalf("%s is not a number: %v", path, at(t, v, path))
	}
	return f
}

func str(t *testing.T, v any, path string) string {
	t.Helper()
	s, ok := at(t, v, path).(string)
	if !ok {
		t.Fatalf("%s is not a string: %v", path, at(t, v, path))
	}
	return s
}

func list(t *testing.T, v any, path string) []any {
	t.Helper()
	l, ok := at(t, v, path).([]any)
	if !ok {
		t.Fatalf("%s is not a list: %v", path, at(t, v, path))
	}
	return l
}

func isNull(t *testing.T, v any, path string) bool {
	t.Helper()
	return at(t, v, path) == nil
}

// shape requires the object at path to have exactly the space-separated keys:
// a field the contract lacks or one it names but the response misses fails.
func shape(t *testing.T, v any, path, keys string) {
	t.Helper()
	m, ok := at(t, v, path).(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %v", path, at(t, v, path))
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	want := strings.Fields(keys)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s keys %v, contract %v", cmp.Or(path, "body"), got, want)
	}
}

// shapeAll applies shape to every element of the list at path.
func shapeAll(t *testing.T, v any, path, keys string) {
	t.Helper()
	for i := range list(t, v, path) {
		shape(t, v, strings.TrimPrefix(path+"."+strconv.Itoa(i), "."), keys)
	}
}

// near compares money and ratios to 1e-9 relative.
func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*max(1, math.Abs(a), math.Abs(b)) }

// isTime requires an RFC 3339 UTC timestamp ("…Z"), as the contract serializes every time.
func isTime(t *testing.T, v any, path string) time.Time {
	t.Helper()
	s := str(t, v, path)
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || !strings.HasSuffix(s, "Z") {
		t.Fatalf("%s: %q is not a UTC timestamp", path, s)
	}
	return ts
}

// Contract §3 field sets, for shape.
const (
	userKeys       = "id email name role profile"
	creditsKeys    = "used limit resetsAt"
	assetKeys      = "symbol pair name price change24hPct high24h low24h volume24hUsd range24hSource class venue session sessionChangeAt region sessionVenue underlying localCurrency"
	candleKeys     = "t o h l c v"
	analysisKeys   = "symbol interval price return1dPct return7dPct return30dPct realizedVolAnnPct rsi14 sma20 sma50 sma200 ema20 atr14 support resistance trend"
	outlookKeys    = "symbol spot horizonDays volAnnPct buckets p5 p50 p95 volBasis" // live Client.Outlook; no longHorizon to 365 days
	volBasisKeys   = "window days since"
	bucketKeys     = "label lo hi p"
	tradeKeys      = "id symbol side qty price notional fee realizedPnl source note createdAt ref"
	positionKeys   = "symbol qty avgPrice markPrice marketValue unrealizedPnl unrealizedPnlPct stopLoss takeProfit"
	portfolioKeys  = "revision cash equity startingBalance unrealizedPnl realizedPnl totalPnl totalPnlPct grossExposure buyingPower positions predictionStakes"
	planKeys       = "symbol style direction entry stopLoss takeProfit riskReward confidence horizon rationale invalidation catalysts suggestedNotional interval higherTrend desk generatedAt"
	newsKeys       = "id title url source description publishedAt symbols topic regions hasContent"
	briefKeys      = "id headline summary bullets sentiment articleCount window createdAt"
	specKeys       = "name symbol interval bars longEntry longExit stopLossPct positionPct feePct"
	metricsKeys    = "totalReturnPct annualizedReturnPct buyHoldReturnPct sharpe sortino volatilityAnnPct maxDrawdownPct calmar winRatePct trades profitFactor exposurePct feesPct startT endT bars"
	strategyKeys   = "id name description author prompt spec metrics equity benchmark trades explanation shortSample createdAt community forward"
	stratSumKeys   = "id name author symbol interval metrics sparkline shortSample createdAt community forward"
	forwardKeys    = "since basis metrics early asOf stale reason equity benchmark trades"
	fwdSumKeys     = "since basis metrics early asOf stale reason"
	btTradeKeys    = "side entryT exitT entry exit pnlPct reason"
	aiPortfolioKey = "id name thesis prompt holdings backtest createdAt invested overlap"
	draftKeys      = "shape question outcomes resolutionCriteria resolutionDate category priceRule context"
	forecastKeys   = draftKeys + " id status archivedFromStatus error result createdAt completedAt resolution replay parentId history progress materials"
	resultKeys     = "probabilities ensemble specialists summary drivers evidence flags whatWouldChange confidence knowledgeNote sources research market findings reference"
	chatMsgKeys    = "id role content cards attachments createdAt"
	runKeys        = "id name status error cadence start end steps stepsDone universe startingCash model knowledgeCutoff contaminated credits agents benchmarks createdAt completedAt"
	agentKeys      = "index name personaId instruction status rank errors equity metrics lastReasoning"
	arenaMetrics   = "totalReturnPct annualizedReturnPct sharpe sortino calmar maxDrawdownPct volatilityAnnPct winRatePct fills turnoverPct exposurePct feesUsd vsBtcPct vsBasketPct"
	stepKeys       = "step t equity cash positions targets fills tools reasoning error"
	runSumKeys     = "id name status cadence start end steps stepsDone contaminated leader btcReturnPct createdAt"
)

// fakeWeb is Brave Search and crawl4ai. Brave answers the wire ingestion
// (freshness "pd", per the contract) from wireNews and every other search
// with one result named after its query and endpoint (a search_lang=zh-hans
// search with one Chinese-titled result); crawl4ai completes every task with
// a short article.
type fakeWeb struct {
	braveURL, crawlURL string

	mu       sync.Mutex
	searches []search
	params   []url.Values // every search's full query, in order
	crawled  []string     // URLs submitted to crawl4ai
}

type search struct{ endpoint, q, freshness, count string }

func newFakeWeb(t *testing.T) *fakeWeb {
	f := &fakeWeb{}
	brave := httptest.NewServer(http.HandlerFunc(f.brave))
	crawl := httptest.NewServer(http.HandlerFunc(f.crawl))
	t.Cleanup(brave.Close)
	t.Cleanup(crawl.Close)
	f.braveURL, f.crawlURL = brave.URL+"/res/v1", crawl.URL
	return f
}

// wireNews are the stories behind the BTC and Fed wire queries: hours ago,
// title, path, description, publisher.
var wireNews = map[string][]struct {
	ago                       time.Duration
	title, path, desc, source string
}{
	"Bitcoin BTC crypto": {
		{1 * time.Hour, "Bitcoin ETF inflows top $1bn", "/btc-etf", "Ethereum funds also gained.", "Desk"},
		{3 * time.Hour, "Bitcoin miners sell fewer coins", "/btc-miners", "Hashprice recovered this week.", "Wire"},
	},
	"Federal Reserve interest rates": {
		{2 * time.Hour, "Fed holds rates steady", "/fed", "Powell sees no hurry to move.", "Wire"},
	},
}

// briefReply is the model's market brief over wireNews; its sources number
// the 24 h headlines newest first (btc-etf, fed, btc-miners). The unknown
// symbol and source, and DOGE (no news), must be dropped; BTC's label follows
// its score.
const briefReply = `{"headline":"ETF demand lifts bitcoin","summary":"Spot ETF inflows lifted BTC while the Fed held rates.",
	"bullets":[{"text":"Bitcoin ETF inflows topped $1bn.","symbols":["BTC"],"sources":[1]},
		{"text":"The Fed held rates steady.","symbols":[],"sources":[2]},
		{"text":"Miners sold fewer coins.","symbols":["btc","NOPE"],"sources":[3,9]}],
	"sentiment":[{"symbol":"BTC","score":0.5,"label":"neutral","reason":"ETF inflows"},
		{"symbol":"DOGE","score":-0.4,"label":"bearish","reason":"No DOGE news"}]}`

func (f *fakeWeb) brave(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Subscription-Token") != braveKey {
		http.Error(w, "bad key", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	endpoint := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/res/v1/"), "/search")
	f.mu.Lock()
	f.searches = append(f.searches, search{endpoint, q.Get("q"), q.Get("freshness"), q.Get("count")})
	f.params = append(f.params, q)
	f.mu.Unlock()
	type result struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
		PageAge     string `json:"page_age"`
		Profile     struct {
			Name string `json:"name"`
		} `json:"profile"`
	}
	item := func(ago time.Duration, title, path, desc, source string) result {
		r := result{Title: title, URL: pageHost + path, Description: desc, PageAge: time.Now().UTC().Add(-ago).Format("2006-01-02T15:04:05")}
		r.Profile.Name = source
		return r
	}
	results := []result{}
	if q.Get("freshness") == "pd" {
		for _, n := range wireNews[q.Get("q")] {
			results = append(results, item(n.ago, n.title, n.path, n.desc, n.source))
		}
	} else if q.Get("search_lang") == "zh-hans" {
		results = append(results, item(5*time.Hour, "本地报道："+q.Get("q"), "/zh/"+url.PathEscape(q.Get("q")), "新交所 上市 首日 表现", "Zaobao"))
	} else {
		title := map[string]string{"news": "Coverage: ", "web": "Background: "}[endpoint] + q.Get("q")
		results = append(results, item(6*time.Hour, title, "/"+endpoint+"/"+url.PathEscape(q.Get("q")), "Analysts weigh "+q.Get("q")+".", "Wire"))
	}
	if endpoint == "web" {
		json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": results}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"results": results})
}

func (f *fakeWeb) crawl(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+crawlToken {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodPost && r.URL.Path == "/crawl" {
		var body struct{ URLs []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.crawled = append(f.crawled, body.URLs...)
		fmt.Fprintf(w, `{"task_id":"%d"}`, len(f.crawled))
		return
	}
	i, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/task/"))
	if err != nil || i < 1 || i > len(f.crawled) {
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"status": "completed", "results": []any{map[string]any{
		"markdown": "# Story\n\nFull story from " + f.crawled[i-1] + ": analysts said demand stayed firm through the week while volumes rose. " +
			strings.Repeat("Desks reported steady two-way flow and tighter spreads across the session. ", 5), // ≥ 400 characters: shorter pages are stubs
		"metadata": map[string]any{"title": "Story"}}}})
}

// queries is every search's full query string so far, in order.
func (f *fakeWeb) queries() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.params)
}

func (f *fakeWeb) seen() ([]search, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.searches), slices.Clone(f.crawled)
}
