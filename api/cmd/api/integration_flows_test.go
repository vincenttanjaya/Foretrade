package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
)

const someID = "6f1c2c1e-0000-4000-8000-000000000000"

// tomorrow is when today's AI credits reset: 00:00 UTC.
func tomorrow() time.Time { return time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1) }

func TestIntegrationAuth(t *testing.T) {
	s := boot(t, options{demo: true})

	// Boot upserted the demo user: email normalized, bcrypt at the production cost.
	var hash, name string
	if err := s.db.QueryRow(context.Background(), "select password_hash, name from users where email = 'demo@example.test'").Scan(&hash, &name); err != nil {
		t.Fatalf("demo user: %v", err)
	}
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != 12 || name != "Demo Investor" {
		t.Fatalf("demo user: cost %d %v, name %q", cost, err, name)
	}

	anon := s.client(t)
	if resp, b := anon.send(anon.request("GET", "/healthz", nil)); resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("healthz: %d %q", resp.StatusCode, b)
	}
	anon.fail("GET", "/api/me", nil, 401, "unauthorized")

	// Login: the email is case- and space-insensitive; the session is a
	// host-only HttpOnly Lax cookie for 7 days whose SHA-256 alone is stored.
	c := s.client(t)
	resp, b := c.send(c.request("POST", "/api/auth/login", map[string]string{"email": "  DEMO@example.test", "password": demoPassword}))
	if resp.StatusCode != 200 {
		t.Fatalf("demo login: %d %s", resp.StatusCode, b)
	}
	login := decode(t, b)
	shape(t, login, "", "user")
	shape(t, login, "user", userKeys)
	shape(t, login, "user.profile", "enabled riskProfile style focusSymbols customPrompt memories revision updatedAt")
	if profileObject(t, profileObject(t, login, "user"), "profile")["enabled"] != false || num(t, login, "user.profile.revision") != 0 {
		t.Fatalf("managed demo profile: %v", login)
	}
	if str(t, login, "user.email") != "demo@example.test" || str(t, login, "user.name") != "Demo Investor" {
		t.Fatalf("login user: %v", login)
	}
	var cookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "tsid" {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 7*24*3600 ||
		cookie.Secure || cookie.Domain != "" {
		t.Fatalf("session cookie: %+v", cookie)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	sum := sha256.Sum256(raw)
	if err != nil || len(raw) != 32 || s.count(t, `select count(*) from sessions where token_hash = $1 and user_id = $2
		and expires_at > now() + interval '6 days 23 hours'`, sum[:], str(t, login, "user.id")) != 1 {
		t.Fatalf("stored session: %v, %d token bytes", err, len(raw))
	}
	me := c.call("GET", "/api/me", nil, 200)
	shape(t, me, "", "user credits")
	shape(t, me, "user", userKeys)
	shape(t, me, "credits", creditsKeys)
	if num(t, me, "credits.used") != 0 || num(t, me, "credits.limit") != 500 || !isTime(t, me, "credits.resetsAt").Equal(tomorrow()) {
		t.Fatalf("me: %v", me)
	}

	// Logout deletes the session and the cookie; the old cookie stays dead.
	resp, b = c.send(c.request("POST", "/api/auth/logout", "{}"))
	if resp.StatusCode != 204 || len(b) != 0 || !slices.ContainsFunc(resp.Cookies(), func(ck *http.Cookie) bool { return ck.Name == "tsid" && ck.MaxAge < 0 }) {
		t.Fatalf("logout: %d %v %q", resp.StatusCode, resp.Cookies(), b)
	}
	if n := s.count(t, "select count(*) from sessions where token_hash = $1", sum[:]); n != 0 {
		t.Fatalf("session rows after logout: %d", n)
	}
	c.fail("GET", "/api/me", nil, 401, "unauthorized")
	s.withCookie(t, cookie.Value).fail("GET", "/api/me", nil, 401, "unauthorized")

	// Wrong password and malformed logins.
	u := s.signIn(t)
	anon.fail("POST", "/api/auth/login", map[string]string{"email": u.email, "password": "wrong-password"}, 401, "invalid_credentials")
	anon.fail("POST", "/api/auth/login", map[string]string{"email": u.email}, 400, "bad_request")
	anon.fail("POST", "/api/auth/login", `{"email":`, 400, "bad_request")

	// CSRF: a write must be JSON, and an Origin, when sent, must be allowed.
	order := `{"symbol":"BTC","side":"buy","notional":100}`
	req := u.request("POST", "/api/orders", order)
	req.Header.Set("Content-Type", "text/plain")
	if resp, b := u.send(req); resp.StatusCode != 403 || !strings.Contains(string(b), `"code":"forbidden"`) {
		t.Fatalf("form-like post: %d %s", resp.StatusCode, b)
	}
	req = u.request("POST", "/api/orders", order)
	req.Header.Set("Origin", "https://evil.example")
	if resp, b := u.send(req); resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("cross-site post: %d %v %s", resp.StatusCode, resp.Header, b)
	}
	if n := s.count(t, "select count(*) from trades"); n != 0 {
		t.Fatalf("a rejected write reached the ledger: %d trades", n)
	}
	req = u.request("POST", "/api/orders", order)
	req.Header.Del("Origin") // a non-browser client sends none
	if resp, b := u.send(req); resp.StatusCode != 200 || s.count(t, "select count(*) from trades where user_id = $1", u.id) != 1 {
		t.Fatalf("same-origin JSON post without Origin: %d %s", resp.StatusCode, b)
	}

	// CORS: the allowed origin is echoed with credentials; others get no grant.
	resp, _ = u.send(u.request("GET", "/api/me", nil))
	if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != origin ||
		resp.Header.Get("Access-Control-Allow-Credentials") != "true" || !slices.Contains(resp.Header.Values("Vary"), "Origin") {
		t.Fatalf("CORS on the allowed origin: %d %v", resp.StatusCode, resp.Header)
	}
	req = u.request("GET", "/api/me", nil)
	req.Header.Set("Origin", "https://evil.example")
	if resp, _ := u.send(req); resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("CORS on another origin: %d %v", resp.StatusCode, resp.Header)
	}
	for from, grant := range map[string]bool{origin: true, "https://evil.example": false} {
		req := anon.request("OPTIONS", "/api/orders", nil)
		req.Header.Set("Origin", from)
		req.Header.Set("Access-Control-Request-Method", "POST")
		resp, _ := anon.send(req)
		h := resp.Header
		if resp.StatusCode != 204 || (h.Get("Access-Control-Allow-Origin") == from) != grant ||
			grant && (h.Get("Access-Control-Allow-Credentials") != "true" || !strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
				h.Get("Access-Control-Allow-Headers") != "Content-Type") {
			t.Fatalf("preflight from %s: %d %v", from, resp.StatusCode, h)
		}
	}

	// Login rate limit: 10 attempts per 10 minutes per client IP. The peer is
	// loopback (the local proxy's place), so the IP is X-Forwarded-For's last hop,
	// the one the proxy appended; the client wrote the hop before it.
	attempt := func(ip string, body any) int {
		req := anon.request("POST", "/api/auth/login", body)
		req.Header.Set("X-Forwarded-For", "10.0.0.1, "+ip)
		resp, _ := anon.send(req)
		return resp.StatusCode
	}
	for i := range 10 {
		if code := attempt("198.51.100.7", "{}"); code != 400 {
			t.Fatalf("attempt %d: %d", i+1, code)
		}
	}
	good := map[string]string{"email": u.email, "password": testPassword}
	if code := attempt("198.51.100.7", good); code != 429 {
		t.Fatalf("11th attempt from one IP: %d", code)
	}
	if code := attempt("198.51.100.8", good); code != 200 {
		t.Fatalf("another IP: %d", code)
	}
}

// contractRoutes are the /api routes API-CONTRACT.md documents, with {id}
// filled in and query strings dropped.
func contractRoutes(t *testing.T) []string {
	b, err := os.ReadFile("../../../docs/API-CONTRACT.md") // the monorepo root's docs/
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile("`(GET|POST|PUT|PATCH|DELETE) (/api/[^` ?]*)").FindAllStringSubmatch(string(b), -1) {
		if r := m[1] + " " + strings.ReplaceAll(m[2], "{id}", someID); !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func TestIntegrationRoutesRequireSession(t *testing.T) {
	s := boot(t, options{})
	routes := contractRoutes(t)
	if len(routes) != 106 || !slices.Contains(routes, "GET /api/notifications/channels") || !slices.Contains(routes, "PUT /api/notifications/channels") || !slices.Contains(routes, "POST /api/notifications/channels/test") || !slices.Contains(routes, "GET /api/market/quote-sources") || !slices.Contains(routes, "GET /api/screener/asia") || !slices.Contains(routes, "GET /api/screener/asia/quotes") || !slices.Contains(routes, "GET /api/market/instruments") || !slices.Contains(routes, "GET /api/alerts/ath") || !slices.Contains(routes, "GET /api/alerts/"+someID+"/runs") || !slices.Contains(routes, "POST /api/alerts/"+someID+"/resume") || !slices.Contains(routes, "GET /api/portfolio/review") || !slices.Contains(routes, "GET /api/arena/live") || !slices.Contains(routes, "GET /api/predictions/markets") || !slices.Contains(routes, "GET /api/predictions/markets/"+someID+"/draft") || !slices.Contains(routes, "POST /api/copy-plans/"+someID+"/stop") || !slices.Contains(routes, "POST /api/strategies/"+someID+"/publish") || !slices.Contains(routes, "POST /api/strategies/"+someID+"/star") || !slices.Contains(routes, "POST /api/strategies/"+someID+"/copy") || !slices.Contains(routes, "POST /api/forecasts/"+someID+"/restore") || !slices.Contains(routes, "PATCH /api/portfolio/positions/{symbol}") || !slices.Contains(routes, "GET /api/market/derivatives") || !slices.Contains(routes, "GET /api/signals") || !slices.Contains(routes, "POST /api/autopilots/"+someID+"/stop") || !slices.Contains(routes, "GET /api/portfolio/equity") ||
		!slices.Contains(routes, "GET /api/portfolio/attribution") || !slices.Contains(routes, "POST /api/auth/login") || !slices.Contains(routes, "GET /api/arena/runs/"+someID+"/steps") ||
		!slices.Contains(routes, "POST /api/forecasts/"+someID+"/resolve") || !slices.Contains(routes, "GET /api/forecasts/track-record") {
		t.Fatalf("contract routes: %d %q", len(routes), routes)
	}
	expired := s.signIn(t)
	if _, err := s.db.Exec(context.Background(), "update sessions set expires_at = now() - interval '1 minute' where user_id = $1", expired.id); err != nil {
		t.Fatal(err)
	}
	callers := map[string]*client{"no cookie": s.client(t), "forged cookie": s.withCookie(t, base64.RawURLEncoding.EncodeToString(make([]byte, 32))),
		"malformed cookie": s.withCookie(t, "not-a-token"), "expired session": expired}
	body := func(method string) any { // writes carry an empty JSON object, as the web app sends
		if method == "GET" {
			return nil
		}
		return "{}"
	}

	for _, route := range routes {
		if route == "POST /api/auth/login" {
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		for who, c := range callers {
			req := c.request(method, path, body(method))
			if strings.HasPrefix(path, "/api/admin/") {
				// The admin API refuses the web app's origin outright, session or not.
				resp, b := c.send(req)
				if resp.StatusCode != 403 || !strings.Contains(string(b), `"code":"forbidden"`) {
					t.Errorf("%s from the web origin with %s: %d %s", route, who, resp.StatusCode, b)
				}
				req = c.request(method, path, body(method))
				req.Header.Set("Origin", adminOrigin)
			}
			resp, b := c.send(req)
			if want := req.Header.Get("Origin"); resp.StatusCode != 401 || !strings.Contains(string(b), `"code":"unauthorized"`) ||
				resp.Header.Get("Access-Control-Allow-Origin") != want {
				t.Errorf("%s with %s: %d %s", route, who, resp.StatusCode, b)
			}
		}
	}

	// Signed in, every route is mounted: a JSON answer (an empty {} body is a
	// 400, a random id a 404; POST /api/notifications/seen a bodiless 204), never
	// the session error or the mux's plain 404/405.
	c := s.signIn(t)
	for _, route := range routes {
		if route == "POST /api/auth/login" || route == "POST /api/auth/logout" {
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		resp, b := c.send(c.request(method, path, body(method)))
		if resp.StatusCode == 401 || resp.StatusCode == 405 || resp.StatusCode != 204 && resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s signed in: %d %s %s", route, resp.StatusCode, resp.Header.Get("Content-Type"), b)
		}
	}
	if resp, _ := c.send(c.request("GET", "/api/nope", nil)); resp.StatusCode != 404 {
		t.Errorf("unknown route: %d", resp.StatusCode)
	}
	if resp, _ := c.send(c.request("PUT", "/api/orders", "{}")); resp.StatusCode != 405 {
		t.Errorf("wrong method: %d", resp.StatusCode)
	}
	c.call("POST", "/api/auth/logout", "{}", 204)
	c.fail("GET", "/api/portfolio", nil, 401, "unauthorized")
}

func TestIntegrationTrading(t *testing.T) {
	s := boot(t, options{})
	c := s.signIn(t)

	assets := c.call("GET", "/api/market/assets", nil, 200)
	shapeAll(t, assets, "assets", assetKeys)
	if n := len(list(t, assets, "assets")); n != 107 || str(t, assets, "assets.0.symbol") != "BTC" || str(t, assets, "assets.11.symbol") != "PAXG" || str(t, assets, "assets.39.symbol") != "STX" ||
		num(t, assets, "assets.0.price") != markettest.Price("BTCUSDT") {
		t.Fatalf("assets: %v", assets)
	}
	candles := c.call("GET", "/api/market/candles?symbol=btc&interval=1h&limit=50", nil, 200)
	shapeAll(t, candles, "candles", candleKeys)
	if n := len(list(t, candles, "candles")); n != 50 || num(t, candles, "candles.49.t") != float64(markettest.End.Unix()) ||
		num(t, candles, "candles.49.t")-num(t, candles, "candles.48.t") != 3600 {
		t.Fatalf("candles: %d, last %v", n, at(t, candles, "candles.49"))
	}
	for _, q := range []string{"symbol=SHIB&interval=1h", "symbol=BTC&interval=2h", "symbol=BTC&interval=1h&limit=1001"} {
		c.fail("GET", "/api/market/candles?"+q, nil, 400, "bad_request")
	}
	an := c.call("GET", "/api/market/analysis?symbol=BTC&interval=4h", nil, 200)
	shape(t, an, "analysis", analysisKeys)
	if str(t, an, "analysis.interval") != "4h" || num(t, an, "analysis.price") != 100_000 ||
		!slices.Contains([]string{"uptrend", "downtrend", "range"}, str(t, an, "analysis.trend")) {
		t.Fatalf("analysis: %v", an)
	}

	pf := c.call("GET", "/api/portfolio", nil, 200)
	shape(t, pf, "portfolio", portfolioKeys)
	if num(t, pf, "portfolio.cash") != 100_000 || num(t, pf, "portfolio.equity") != 100_000 || num(t, pf, "portfolio.buyingPower") != 200_000 ||
		len(list(t, pf, "portfolio.positions")) != 0 {
		t.Fatalf("fresh portfolio: %v", pf)
	}

	// A long with a stop and a target: 2 bps slippage, a 0.1% fee, avg-cost books.
	o := c.call("POST", "/api/orders", map[string]any{"symbol": "btc", "side": "buy", "notional": 10_000, "stopLoss": 95_000, "takeProfit": 101_000}, 200)
	shape(t, o, "", "trade portfolio")
	shape(t, o, "trade", tradeKeys)
	shape(t, o, "portfolio", portfolioKeys)
	shapeAll(t, o, "portfolio.positions", positionKeys)
	buyPx := 100_000 * 1.0002
	if str(t, o, "trade.symbol") != "BTC" || str(t, o, "trade.side") != "buy" || !near(num(t, o, "trade.price"), buyPx) ||
		!near(num(t, o, "trade.qty"), 10_000/buyPx) || !near(num(t, o, "trade.notional"), 10_000) || !near(num(t, o, "trade.fee"), 10) ||
		!near(num(t, o, "trade.realizedPnl"), -10) || str(t, o, "trade.source") != "manual" || str(t, o, "trade.note") != "" {
		t.Fatalf("buy: %v", at(t, o, "trade"))
	}
	isTime(t, o, "trade.createdAt")
	if !near(num(t, o, "portfolio.cash"), 89_990) || str(t, o, "portfolio.positions.0.symbol") != "BTC" ||
		!near(num(t, o, "portfolio.positions.0.avgPrice"), buyPx) || num(t, o, "portfolio.positions.0.markPrice") != 100_000 ||
		num(t, o, "portfolio.positions.0.stopLoss") != 95_000 || num(t, o, "portfolio.positions.0.takeProfit") != 101_000 {
		t.Fatalf("portfolio after the buy: %v", at(t, o, "portfolio"))
	}
	// Shorts: SOL with a stop above the price, ETH by quantity.
	c.call("POST", "/api/orders", map[string]any{"symbol": "SOL", "side": "sell", "notional": 1000, "stopLoss": 210}, 200)
	c.call("POST", "/api/orders", map[string]any{"symbol": "ETH", "side": "sell", "qty": 1}, 200)

	for body, code := range map[string]string{
		`{"symbol":"BTC","side":"buy","notional":100,"takeProfit":99000}`: "bad_request", // target below a long
		`{"symbol":"BTC","side":"buy","notional":100,"qty":1}`:            "bad_request",
		`{"symbol":"BTC","side":"buy","notional":5}`:                      "bad_request",
		`{"symbol":"SHIB","side":"buy","notional":100}`:                   "bad_request",
		`{"symbol":"BTC","side":"hold","notional":100}`:                   "bad_request",
		`{"symbol":"BTC","side":"buy","notional":1000000}`:                "insufficient_buying_power",
	} {
		c.fail("POST", "/api/orders", body, 400, code)
	}

	closed := c.call("POST", "/api/portfolio/close", map[string]string{"symbol": "ETH"}, 200)
	ethBuy, ethSell := 4000*1.0002, 4000*0.9998
	if str(t, closed, "trade.side") != "buy" || num(t, closed, "trade.qty") != 1 || !near(num(t, closed, "trade.price"), ethBuy) ||
		!near(num(t, closed, "trade.realizedPnl"), ethSell-ethBuy-ethBuy*0.001) {
		t.Fatalf("close ETH: %v", at(t, closed, "trade"))
	}
	c.fail("POST", "/api/portfolio/close", map[string]string{"symbol": "ETH"}, 404, "not_found")

	// History and portfolio agree with each other and with Postgres.
	trades := c.call("GET", "/api/portfolio/trades?limit=50", nil, 200)
	shapeAll(t, trades, "trades", tradeKeys)
	c.fail("GET", "/api/portfolio/trades?limit=201", nil, 400, "bad_request")
	pf = c.call("GET", "/api/portfolio", nil, 200)
	checkBooks(t, s, c, pf, trades, []string{"ETH:buy", "ETH:sell", "SOL:sell", "BTC:buy"})
	if n := s.count(t, "select count(*) from positions where user_id = $1 and (symbol, stop_loss, take_profit) in (('BTC', 95000, 101000))", c.id); n != 1 {
		t.Fatalf("BTC stop and target rows: %d", n)
	}

	// The watcher (every 15 s) closes BTC at its target and SOL at its stop.
	s.binance.SetPrice("BTCUSDT", 101_500)
	s.binance.SetPrice("SOLUSDT", 215)
	eventually(t, 45*time.Second, "the stop/target watcher", func() bool {
		return len(list(t, c.call("GET", "/api/portfolio", nil, 200), "portfolio.positions")) == 0
	})
	trades = c.call("GET", "/api/portfolio/trades", nil, 200)
	fills := map[string]any{}
	for _, tr := range list(t, trades, "trades")[:2] {
		fills[str(t, tr, "source")] = tr
	}
	tp, sl := fills["take_profit"], fills["stop_loss"]
	if tp == nil || sl == nil || str(t, tp, "symbol") != "BTC" || str(t, tp, "side") != "sell" || !near(num(t, tp, "price"), 101_500*0.9998) ||
		str(t, tp, "note") != "Take profit triggered" || num(t, tp, "realizedPnl") <= 0 ||
		str(t, sl, "symbol") != "SOL" || str(t, sl, "side") != "buy" || !near(num(t, sl, "price"), 215*1.0002) ||
		str(t, sl, "note") != "Stop loss triggered" || num(t, sl, "realizedPnl") >= 0 {
		t.Fatalf("watcher fills: %v", list(t, trades, "trades")[:2])
	}
	checkBooks(t, s, c, c.call("GET", "/api/portfolio", nil, 200), trades, nil)

	reset := c.call("POST", "/api/portfolio/reset", "{}", 200)
	shape(t, reset, "portfolio", portfolioKeys)
	if num(t, reset, "portfolio.cash") != 100_000 || num(t, reset, "portfolio.realizedPnl") != 0 || len(list(t, reset, "portfolio.positions")) != 0 ||
		len(list(t, c.call("GET", "/api/portfolio/trades", nil, 200), "trades")) != 0 ||
		s.count(t, "select count(*) from trades where user_id = $1", c.id)+s.count(t, "select count(*) from positions where user_id = $1", c.id) != 0 {
		t.Fatalf("reset: %v", reset)
	}
}

// checkBooks requires the portfolio to follow from the trade history (cash,
// realized P&L, totals, exposure) and to match the account row. order, when
// given, is the expected "SYMBOL:side" history, newest first.
func checkBooks(t *testing.T, s *server, c *client, pf, trades any, order []string) {
	t.Helper()
	cash, realized, got := 100_000.0, 0.0, []string{}
	for _, tr := range list(t, trades, "trades") {
		sign := 1.0
		if str(t, tr, "side") == "buy" {
			sign = -1
		}
		cash += sign*num(t, tr, "notional") - num(t, tr, "fee")
		realized += num(t, tr, "realizedPnl")
		got = append(got, str(t, tr, "symbol")+":"+str(t, tr, "side"))
	}
	if order != nil && !slices.Equal(got, order) {
		t.Fatalf("trade history %v, want %v", got, order)
	}
	value, gross, unrealized := 0.0, 0.0, 0.0
	for _, p := range list(t, pf, "portfolio.positions") {
		value += num(t, p, "marketValue")
		gross += abs(num(t, p, "marketValue"))
		unrealized += num(t, p, "unrealizedPnl")
	}
	equity := num(t, pf, "portfolio.equity")
	var stored float64
	if err := s.db.QueryRow(context.Background(), "select cash from accounts where user_id = $1", c.id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !near(num(t, pf, "portfolio.cash"), cash) || !near(stored, cash) || !near(num(t, pf, "portfolio.realizedPnl"), realized) ||
		!near(equity, cash+value) || !near(num(t, pf, "portfolio.unrealizedPnl"), unrealized) ||
		!near(num(t, pf, "portfolio.totalPnl"), realized+unrealized) || !near(num(t, pf, "portfolio.grossExposure"), gross) ||
		!near(num(t, pf, "portfolio.buyingPower"), max(0, 2*equity-gross)) ||
		s.count(t, "select count(*) from trades where user_id = $1", c.id) != len(got) {
		t.Fatalf("books: cash %v (stored %v) realized %v from %d trades; portfolio %v", cash, stored, realized, len(got), at(t, pf, "portfolio"))
	}
}

func abs(x float64) float64 { return max(x, -x) }

const emaSpec = `{"name":"BTC EMA Cross","symbol":"BTC","interval":"4h","bars":1000,
	"longEntry":{"all":[{"left":{"kind":"ema","period":20},"op":"crosses_above","right":{"kind":"ema","period":50}}]},
	"longExit":{"all":[{"left":{"kind":"ema","period":20},"op":"crosses_below","right":{"kind":"ema","period":50}}]},
	"stopLossPct":4,"positionPct":100,"feePct":0.05}`

func TestIntegrationStrategies(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, "You design trading strategies"):
			return llmtest.Reply{Text: `{"name":"BTC EMA Cross","description":"Rides 4h EMA crosses.","spec":` + emaSpec + `}`}
		case strings.HasPrefix(r.System, "You explain a strategy backtest"):
			return llmtest.Reply{Text: "It rides 4h trends."}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)

	gen := c.call("POST", "/api/strategies/generate", map[string]string{"prompt": "Buy BTC when the 20 EMA crosses above the 50 EMA",
		"symbol": "BTC", "interval": "4h"}, 201)
	shape(t, gen, "", "strategy credits")
	shape(t, gen, "strategy", strategyKeys)
	shape(t, gen, "strategy.spec", specKeys)
	shape(t, gen, "strategy.metrics", metricsKeys)
	shapeAll(t, gen, "strategy.trades", btTradeKeys)
	shape(t, gen, "credits", creditsKeys)
	id := str(t, gen, "strategy.id")
	if str(t, gen, "strategy.author") != "Integration" || str(t, gen, "strategy.explanation") != "It rides 4h trends." ||
		num(t, gen, "strategy.equity.0.v") != 100 || len(list(t, gen, "strategy.equity")) > 400 || num(t, gen, "strategy.metrics.trades") == 0 ||
		num(t, gen, "strategy.metrics.bars") > 1000 || num(t, gen, "credits.used") != 2 {
		t.Fatalf("generate: author %v, explanation %v, %d equity points from %v, metrics %v, credits %v", at(t, gen, "strategy.author"),
			at(t, gen, "strategy.explanation"), len(list(t, gen, "strategy.equity")), at(t, gen, "strategy.equity.0"), at(t, gen, "strategy.metrics"), at(t, gen, "credits"))
	}
	if s.count(t, "select count(*) from strategies where user_id = $1 and id = $2", c.id, id) != 1 ||
		s.count(t, "select credits from ai_usage where user_id = $1", c.id) != 2 {
		t.Fatal("generate: strategy row or credits not stored")
	}

	// The same spec backtests to the same numbers, for free.
	bt := c.call("POST", "/api/strategies/backtest", map[string]any{"spec": json.RawMessage(emaSpec)}, 200)
	shape(t, bt, "", "metrics equity benchmark trades")
	shape(t, bt, "metrics", metricsKeys)
	for _, k := range []string{"totalReturnPct", "sharpe", "maxDrawdownPct", "trades"} {
		if num(t, bt, "metrics."+k) != num(t, gen, "strategy.metrics."+k) {
			t.Fatalf("backtest %s %v, generate %v", k, num(t, bt, "metrics."+k), num(t, gen, "strategy.metrics."+k))
		}
	}

	// Tune (the studio's stop/target/size card): rerun with new levels and size.
	var spec map[string]any
	json.Unmarshal([]byte(emaSpec), &spec)
	spec["stopLossPct"], spec["takeProfitPct"], spec["positionPct"] = 2, 6, 50
	tuned := c.call("POST", "/api/strategies/backtest", map[string]any{"spec": spec}, 200)
	reasons := map[string]bool{}
	for _, tr := range list(t, tuned, "trades") {
		reasons[str(t, tr, "reason")] = true
	}
	if num(t, tuned, "metrics.totalReturnPct") == num(t, bt, "metrics.totalReturnPct") || !reasons["take_profit"] && !reasons["stop_loss"] {
		t.Fatalf("tuned backtest: %v, exits %v", at(t, tuned, "metrics"), reasons)
	}
	spec["positionPct"] = 0
	c.fail("POST", "/api/strategies/backtest", map[string]any{"spec": spec}, 400, "bad_request")
	if s.count(t, "select count(*) from strategies where user_id = $1", c.id) != 1 || num(t, c.call("GET", "/api/me", nil, 200), "credits.used") != 2 {
		t.Fatal("backtests must not save or spend")
	}

	mine := c.call("GET", "/api/strategies?scope=mine", nil, 200)
	shapeAll(t, mine, "strategies", stratSumKeys)
	if l := list(t, mine, "strategies"); len(l) != 1 || str(t, l[0], "id") != id || len(list(t, l[0], "sparkline")) > 60 {
		t.Fatalf("mine: %v", mine)
	}
	if got := c.call("GET", "/api/strategies/"+id, nil, 200); num(t, got, "strategy.metrics.sharpe") != num(t, gen, "strategy.metrics.sharpe") {
		t.Fatalf("get: %v", got)
	}
	top := c.call("GET", "/api/strategies?scope=top", nil, 200)
	shapeAll(t, top, "strategies", stratSumKeys)
	labs := list(t, top, "strategies")
	short := func(i int) bool { return labs[i].(map[string]any)["shortSample"] == true }
	for i := range labs { // full samples by Sharpe, then short samples by Sharpe
		if str(t, labs[i], "author") != "Foretrade Labs" || i > 0 && (short(i-1) && !short(i) ||
			short(i-1) == short(i) && num(t, labs[i], "metrics.sharpe") > num(t, labs[i-1], "metrics.sharpe")) {
			t.Fatalf("top %d: %v", i, labs[i])
		}
	}
	labsID := str(t, labs[0], "id")
	if len(labs) != 8 || str(t, c.call("GET", "/api/strategies/"+labsID, nil, 200), "strategy.author") != "Foretrade Labs" {
		t.Fatalf("labs: %d", len(labs))
	}
	c.fail("DELETE", "/api/strategies/"+labsID, nil, 404, "not_found")
	c.fail("GET", "/api/strategies?scope=all", nil, 400, "bad_request")

	other := s.signIn(t)
	other.fail("GET", "/api/strategies/"+id, nil, 404, "not_found")
	other.fail("DELETE", "/api/strategies/"+id, nil, 404, "not_found")
	c.call("DELETE", "/api/strategies/"+id, nil, 204)
	c.fail("GET", "/api/strategies/"+id, nil, 404, "not_found")
	if s.count(t, "select count(*) from strategies") != 0 {
		t.Fatal("strategy row left after delete")
	}
}

const basketReply = `{"name":"Core Majors","thesis":"A large-cap core with a growth sleeve.",
	"holdings":[{"symbol":"BTC","weight":0.55,"rationale":"The anchor."},{"symbol":"eth","weight":0.3,"rationale":"Smart-contract beta."},
		{"symbol":"SOL","weight":0.15,"rationale":"Higher-growth sleeve."}]}`

func TestIntegrationAIPortfolio(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, "You build mixed crypto and stock/index perp portfolios") {
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		}
		return llmtest.Reply{Text: basketReply}
	}})
	c := s.signIn(t)

	b := c.call("POST", "/api/ai-portfolios/build", map[string]string{"prompt": "Blue chips with a little SOL"}, 201)
	shape(t, b, "", "portfolio credits")
	shape(t, b, "portfolio", aiPortfolioKey)
	shapeAll(t, b, "portfolio.holdings", "symbol weight rationale driver")
	shape(t, b, "portfolio.backtest", "series benchmark returnPct benchmarkReturnPct maxDrawdownPct volatilityAnnPct benchmarkSymbol benchmarkLabel windowStart windowEnd bars")
	id := str(t, b, "portfolio.id")
	weights := map[string]float64{}
	for _, h := range list(t, b, "portfolio.holdings") {
		weights[str(t, h, "symbol")] = num(t, h, "weight")
	}
	series := list(t, b, "portfolio.backtest.series")
	if str(t, b, "portfolio.name") != "Core Majors" || !near(weights["BTC"], 0.55) || !near(weights["ETH"], 0.3) || !near(weights["SOL"], 0.15) ||
		num(t, b, "portfolio.backtest.series.0.v") != 100 || num(t, b, "portfolio.backtest.benchmark.0.v") != 100 ||
		len(series) < 170 || len(series) != len(list(t, b, "portfolio.backtest.benchmark")) || num(t, b, "credits.used") != 2 {
		t.Fatalf("build: %v", b)
	}
	if r := s.bedrock.Requests()[0]; r.Effort != "medium" || !strings.Contains(r.Prompt, "Blue chips with a little SOL") {
		t.Fatalf("model request: %q / %.80q", r.Effort, r.Prompt)
	}
	if l := list(t, c.call("GET", "/api/ai-portfolios", nil, 200), "portfolios"); len(l) != 1 || str(t, l[0], "id") != id {
		t.Fatalf("list: %v", l)
	}
	c.call("GET", "/api/ai-portfolios/"+id, nil, 200)

	c.fail("POST", "/api/ai-portfolios/"+id+"/invest", map[string]float64{"amount": 49}, 400, "bad_request")
	c.fail("POST", "/api/ai-portfolios/"+id+"/invest", map[string]float64{"amount": 1e7}, 400, "insufficient_buying_power")
	inv := c.call("POST", "/api/ai-portfolios/"+id+"/invest", map[string]float64{"amount": 1000}, 200)
	shape(t, inv, "", "trades portfolio skipped")
	shapeAll(t, inv, "trades", tradeKeys)
	shape(t, inv, "portfolio", portfolioKeys)
	for i, want := range []struct {
		sym      string
		notional float64
	}{{"BTC", 550}, {"ETH", 300}, {"SOL", 150}} {
		tr := at(t, inv, "trades."+strconv.Itoa(i))
		if str(t, tr, "symbol") != want.sym || !near(num(t, tr, "notional"), want.notional) || str(t, tr, "source") != "portfolio" || str(t, tr, "note") != "Core Majors" {
			t.Fatalf("invest trade %d: %v", i, tr)
		}
	}
	if len(list(t, inv, "trades")) != 3 || len(list(t, inv, "skipped")) != 0 {
		t.Fatalf("invest 1000: %v", inv)
	}
	small := c.call("POST", "/api/ai-portfolios/"+id+"/invest", map[string]float64{"amount": 50}, 200) // SOL's $7.50 is under the $10 minimum
	shapeAll(t, small, "skipped", "symbol reason")
	if len(list(t, small, "trades")) != 2 || str(t, small, "skipped.0.symbol") != "SOL" || str(t, small, "skipped.0.reason") != "$7.50 is below the $10 minimum order" {
		t.Fatalf("invest 50: %v", small)
	}
	pf := c.call("GET", "/api/portfolio", nil, 200)
	checkBooks(t, s, c, pf, c.call("GET", "/api/portfolio/trades", nil, 200), nil)
	if len(list(t, pf, "portfolio.positions")) != 3 || s.count(t, "select count(*) from trades where user_id = $1 and source = 'portfolio'", c.id) != 5 {
		t.Fatalf("positions after investing: %v", pf)
	}

	other := s.signIn(t)
	other.fail("GET", "/api/ai-portfolios/"+id, nil, 404, "not_found")
	other.fail("POST", "/api/ai-portfolios/"+id+"/invest", map[string]float64{"amount": 100}, 404, "not_found")
}

const copilotReply = `{"direction":"long","entry":100000,"stopLoss":98000,"takeProfit":104000,"confidence":70,"horizon":"1-2 weeks",
	"rationale":["Price holds above the 20 EMA.","RSI is not stretched."],"invalidation":"A 4h close below 98,000.","catalysts":["ETF flows"]}`

func TestIntegrationCopilot(t *testing.T) {
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, "You are the Copilot") {
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		}
		return llmtest.Reply{Text: copilotReply}
	}})
	c := s.signIn(t)

	got := c.call("POST", "/api/copilot/plan", map[string]string{"symbol": "btc", "style": "swing", "bias": "auto"}, 200)
	shape(t, got, "", "plan analysis outlook headlines credits")
	shape(t, got, "plan", planKeys)
	shape(t, got, "analysis", analysisKeys)
	shape(t, got, "outlook", outlookKeys)
	shape(t, got, "outlook.volBasis", volBasisKeys)
	shapeAll(t, got, "outlook.buckets", bucketKeys)
	p := 0.0
	for _, b := range list(t, got, "outlook.buckets") {
		p += num(t, b, "p")
	}
	// No news is stored (search is off), so there are no catalysts or headlines.
	if str(t, got, "plan.symbol") != "BTC" || str(t, got, "plan.direction") != "long" || num(t, got, "plan.entry") != 100_000 ||
		num(t, got, "plan.stopLoss") != 98_000 || num(t, got, "plan.takeProfit") != 104_000 || num(t, got, "plan.riskReward") != 2 ||
		num(t, got, "plan.confidence") != 70 || len(list(t, got, "plan.rationale")) != 2 || len(list(t, got, "plan.catalysts")) != 0 ||
		num(t, got, "plan.suggestedNotional") <= 0 || str(t, got, "analysis.interval") != "4h" || num(t, got, "outlook.horizonDays") != 14 ||
		len(list(t, got, "outlook.buckets")) != 7 || !near(p, 1) || str(t, got, "outlook.volBasis.window") != "90d" || num(t, got, "outlook.volBasis.days") != 90 ||
		len(list(t, got, "headlines")) != 0 || num(t, got, "credits.used") != 1 {
		t.Fatalf("plan: %v", got)
	}
	isTime(t, got, "plan.generatedAt")
	if r := s.bedrock.Requests(); len(r) != 1 || r[0].Effort != "low" || !strings.Contains(r[0].Prompt, "BTC") {
		t.Fatalf("model requests: %d", len(r))
	}
	for _, body := range []string{`{"symbol":"SHIB","style":"swing"}`, `{"symbol":"BTC","style":"hodl"}`, `{"symbol":"BTC","style":"day","bias":"up"}`} {
		c.fail("POST", "/api/copilot/plan", body, 400, "bad_request")
	}
	if s.count(t, "select credits from ai_usage where user_id = $1", c.id) != 1 {
		t.Fatal("copilot credits not stored")
	}
}

// opinion is a specialist's JSON with P(Yes) = yes.
func opinion(yes float64) string {
	return fmt.Sprintf(`{"probabilities":[{"outcome":"Yes","p":%v},{"outcome":"No","p":%v}],"reasoning":"Base rates and S1.","keyFactors":["ETF flows"],"confidence":"medium"}`,
		yes, 1-yes)
}

const draftReply = `{"shape":"binary","question":"Will the Binance BTC/USDT price be above $120,000 at 23:59 UTC on 2099-12-31?",
	"outcomes":["Yes","No"],"resolutionCriteria":"Binance BTC/USDT last trade price at 23:59 UTC on 2099-12-31.","resolutionDate":"2099-12-31","category":"Crypto"}`

const aggregatorReply = `{"probabilities":[{"outcome":"Yes","p":0.27},{"outcome":"No","p":0.73}],
	"summary":"The panel leans No: $120k needs a large rally.","drivers":[{"factor":"ETF inflows","direction":"up","weight":"medium"}],
	"evidence":[{"claim":"ETF flows stayed positive.","sourceId":"S1","source":"Wire","stance":"supports","asOf":"2026-10-01"},
		{"claim":"The lognormal outlook puts $120k far out.","sourceId":null,"source":"Lognormal outlook from 90-day realized volatility","stance":"opposes","asOf":"live"}],
	"flags":[],"whatWouldChange":["A sustained ETF bid"],"confidence":"medium"}`

func TestIntegrationForecast(t *testing.T) {
	s := boot(t, options{brave: true, model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, queryWriter): // the Polymarket query writer: no queries, so the question's keywords are searched
			return noMarketQueries
		case strings.HasPrefix(r.System, "You are the question editor"):
			return llmtest.Reply{Text: draftReply}
		case strings.HasPrefix(r.System, "You are the research assistant"):
			return llmtest.Reply{Text: `{"queries":["bitcoin ETF flows","bitcoin price outlook"]}`}
		case strings.HasPrefix(r.System, "You are the Base-rate statistician"):
			return llmtest.Reply{Text: opinion(0.3)}
		case strings.HasPrefix(r.System, "You are the Narrative & macro skeptic"):
			return llmtest.Reply{Text: opinion(0.25)}
		case strings.HasPrefix(r.System, "You are the Market & quant analyst") && len(r.Messages) == 1:
			return llmtest.Reply{Reasoning: "quant plan", Tools: []llmtest.ToolUse{{ID: "q1", Name: "price_outlook", Input: `{"symbol":"BTC","horizon_days":90,"thresholds":[120000]}`}}}
		case strings.HasPrefix(r.System, "You are the Market & quant analyst"):
			return llmtest.Reply{Text: opinion(0.2)}
		case strings.HasPrefix(r.System, "You are the aggregator"):
			return llmtest.Reply{Text: aggregatorReply}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)

	d := c.call("POST", "/api/forecasts/draft", map[string]string{"idea": "Will bitcoin hit 120k?"}, 200)
	shape(t, d, "", "draft credits")
	shape(t, d, "draft", draftKeys)
	if str(t, d, "draft.category") != "crypto" || str(t, d, "draft.resolutionDate") != "2099-12-31" || num(t, d, "credits.used") != 1 {
		t.Fatalf("draft: %v", d)
	}
	created := c.call("POST", "/api/forecasts", at(t, d, "draft"), 201)
	shape(t, created, "forecast", forecastKeys)
	id := str(t, created, "forecast.id")
	if str(t, created, "forecast.status") != "running" || !isNull(t, created, "forecast.result") || num(t, created, "credits.used") != 6 {
		t.Fatalf("create: %v", created)
	}

	var f any
	eventually(t, 30*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	shape(t, f, "forecast", forecastKeys)
	shape(t, f, "forecast.result", resultKeys)
	shapeAll(t, f, "forecast.result.specialists", "role name probabilities reasoning keyFactors confidence")
	shapeAll(t, f, "forecast.result.evidence", "claim source stance asOf url")
	shapeAll(t, f, "forecast.result.sources", "id title url source publishedAt read")
	shapeAll(t, f, "forecast.result.drivers", "factor direction weight")
	shape(t, f, "forecast.result.research", "queries status depth searches")
	r := "forecast.result."
	if str(t, f, r+"research.depth") != "quick" || num(t, f, r+"research.searches") != 3 || len(list(t, f, r+"findings")) != 0 {
		t.Fatalf("quick research: %v findings %v", at(t, f, r+"research"), at(t, f, r+"findings"))
	}
	sources := list(t, f, r+"sources")
	if str(t, f, "forecast.status") != "completed" || !isNull(t, f, "forecast.error") || num(t, f, r+"probabilities.0.p") != 0.27 ||
		!near(num(t, f, r+"ensemble.0.p"), 0.25) || str(t, f, r+"specialists.1.role") != "quant" || len(sources) != 3 ||
		str(t, f, r+"sources.0.id") != "S1" || str(t, f, r+"evidence.0.url") != str(t, f, r+"sources.0.url") || !isNull(t, f, r+"evidence.1.url") ||
		!strings.HasPrefix(str(t, f, r+"knowledgeNote"), "Live web research via Brave Search (3 sources, 3 articles read)") ||
		!slices.Equal(list(t, f, r+"research.queries"), []any{"bitcoin ETF flows", "bitcoin price outlook"}) {
		t.Fatalf("forecast: status %v error %v, p %v ensemble %v, specialist 1 %v, %d sources, evidence %v, note %v, queries %v",
			at(t, f, "forecast.status"), at(t, f, "forecast.error"), at(t, f, r+"probabilities"), at(t, f, r+"ensemble"), at(t, f, r+"specialists.1.role"),
			len(sources), at(t, f, r+"evidence"), at(t, f, r+"knowledgeNote"), at(t, f, r+"research"))
	}
	isTime(t, f, "forecast.completedAt")

	// The research stage searched Brave (a month back: resolution is years
	// away) and read every source through crawl4ai.
	searches, crawled := s.web.seen()
	research := slices.DeleteFunc(searches, func(q search) bool { return q.freshness == "pd" }) // the boot ingestion's
	slices.SortFunc(research, func(a, b search) int { return strings.Compare(a.endpoint+a.q, b.endpoint+b.q) })
	if !slices.Equal(research, []search{{"news", "bitcoin ETF flows", "pm", "6"}, {"news", "bitcoin price outlook", "pm", "6"}, {"web", "bitcoin ETF flows", "", "4"}}) {
		t.Fatalf("research searches: %v", research)
	}
	for _, src := range sources {
		if !slices.Contains(crawled, str(t, src, "url")) || at(t, src, "read") != true {
			t.Fatalf("source %v not read (crawled %v)", src, crawled)
		}
	}
	quantTool := slices.ContainsFunc(s.bedrock.Requests(), func(q llmtest.Request) bool {
		b := string(q.Body)
		return strings.HasPrefix(q.System, "You are the Market & quant analyst") && len(q.Messages) == 3 && q.Effort == "high" &&
			strings.Contains(b, `"toolUse"`) && strings.Contains(b, `"toolResult"`) &&
			strings.Contains(b, `"signature":"sig-quant plan"`) && !strings.Contains(q.Prompt, "<run_transcript>")
	})
	if !quantTool {
		t.Fatal("the quant's price_outlook result and signed reasoning must return in default block mode")
	}

	sums := c.call("GET", "/api/forecasts", nil, 200)
	shapeAll(t, sums, "forecasts", "id shape question category status archivedFromStatus resolutionDate createdAt completedAt headline resolvedOutcome replay parentId")
	if l := list(t, sums, "forecasts"); len(l) != 1 || str(t, l[0], "headline.outcome") != "Yes" || num(t, l[0], "headline.p") != 0.27 {
		t.Fatalf("list: %v", sums)
	}
	for q, n := range map[string]int{"?status=completed": 1, "?status=running": 0, "?shape=ordered": 0} {
		if l := list(t, c.call("GET", "/api/forecasts"+q, nil, 200), "forecasts"); len(l) != n {
			t.Errorf("list %s: %d", q, len(l))
		}
	}
	c.fail("GET", "/api/forecasts?status=done", nil, 400, "bad_request")
	if s.count(t, "select count(*) from forecasts where id = $1 and status = 'completed' and result is not null", id) != 1 ||
		s.count(t, "select credits from ai_usage where user_id = $1", c.id) != 6 {
		t.Fatal("forecast row or credits not stored")
	}

	s.signIn(t).fail("GET", "/api/forecasts/"+id, nil, 404, "not_found")
	c.call("DELETE", "/api/forecasts/"+id, nil, 204)
	archived := c.call("GET", "/api/forecasts/"+id, nil, 200)
	if str(t, archived, "forecast.status") != "archived" || str(t, archived, "forecast.archivedFromStatus") != "completed" ||
		num(t, archived, r+"probabilities.0.p") != 0.27 {
		t.Fatalf("archived report: %v", archived)
	}
	if s.count(t, "select count(*) from forecasts where id = $1 and status = 'archived'", id) != 1 ||
		len(list(t, c.call("GET", "/api/forecasts", nil, 200), "forecasts")) != 0 {
		t.Fatal("archive")
	}
	archives := list(t, c.call("GET", "/api/forecasts?status=archived", nil, 200), "forecasts")
	if len(archives) != 1 || str(t, archives[0], "id") != id {
		t.Fatalf("archive list: %v", archives)
	}
	c.fail("POST", "/api/forecasts/"+id+"/update", map[string]any{}, 409, "conflict")
	c.fail("POST", "/api/forecasts/"+id+"/resolve", map[string]string{"outcome": "Yes"}, 404, "not_found")
	s.signIn(t).fail("POST", "/api/forecasts/"+id+"/restore", map[string]any{}, 404, "not_found")
	restored := c.call("POST", "/api/forecasts/"+id+"/restore", map[string]any{}, 200)
	shape(t, restored, "forecast", forecastKeys)
	if str(t, restored, "forecast.status") != "completed" || !isNull(t, restored, "forecast.archivedFromStatus") ||
		num(t, restored, r+"probabilities.0.p") != 0.27 || s.count(t, "select credits from ai_usage where user_id = $1", c.id) != 6 {
		t.Fatalf("restored report: %v", restored)
	}
	c.fail("POST", "/api/forecasts/"+id+"/restore", map[string]any{}, 409, "conflict")
}

func TestIntegrationChat(t *testing.T) {
	s := boot(t, options{brave: true, model: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, "You are the trading desk analyst") || !r.Stream {
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		}
		switch {
		case r.Prompt == "Should I buy BTC here?":
			return llmtest.Reply{Text: "Checking the tape.", Tools: []llmtest.ToolUse{
				{ID: "t1", Name: "search_news", Input: `{"query":"bitcoin ETF flows","freshness":"week"}`},
				{ID: "t2", Name: "propose_trade", Input: `{"symbol":"BTC","side":"buy","notional_usd":2000,"stop_loss":97000,"take_profit":106000,"rationale":"ETF demand supports a swing long."}`},
			}}
		case r.AfterTools: // the tool results came back
			return llmtest.Reply{Text: "ETF inflows stay strong [1]. **View:** bullish above $97k."}
		case r.Prompt == "And ETH?":
			return llmtest.Reply{Text: "ETH tracks BTC. **View:** neutral."}
		}
		t.Errorf("unexpected chat turn %q", r.Prompt)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)

	ev := c.stream(map[string]any{"message": "Should I buy BTC here?", "context": map[string]string{"symbol": "btc"}})
	var names []string
	var answer strings.Builder
	cards := map[string]any{}
	for _, e := range ev {
		if e.name != "delta" || len(names) == 0 || names[len(names)-1] != "delta" {
			names = append(names, e.name)
		}
		switch e.name {
		case "delta":
			shape(t, e.data, "", "text")
			answer.WriteString(str(t, e.data, "text"))
		case "tool_call":
			shape(t, e.data, "", "id name label input")
		case "tool_result":
			shape(t, e.data, "", "id name ok summary")
			if at(t, e.data, "ok") != true {
				t.Fatalf("tool failed: %v", e.data)
			}
		case "card":
			shape(t, e.data, "", "type data")
			cards[str(t, e.data, "type")] = at(t, e.data, "data")
		}
	}
	if want := []string{"chat", "delta", "tool_call", "tool_result", "tool_call", "tool_result", "card", "delta", "card", "done"}; !slices.Equal(names, want) {
		t.Fatalf("events %v, want %v", names, want)
	}
	shape(t, ev[0].data, "", "chatId title")
	chatID := str(t, ev[0].data, "chatId")
	done := ev[len(ev)-1].data
	shape(t, done, "", "messageId credits")
	trade, srcs := cards["trade_proposal"], cards["sources"]
	shape(t, trade, "", "proposalId symbol side notional price stopLoss takeProfit rationale forecast status trade")
	shape(t, srcs, "", "items")
	shapeAll(t, srcs, "items", "n title url source publishedAt kind")
	if str(t, ev[0].data, "title") != "Should I buy BTC here?" || str(t, trade, "symbol") != "BTC" || num(t, trade, "price") != 100_000 ||
		num(t, trade, "notional") != 2000 || num(t, trade, "stopLoss") != 97_000 || num(t, trade, "takeProfit") != 106_000 ||
		len(list(t, srcs, "items")) != 1 || num(t, srcs, "items.0.n") != 1 || str(t, srcs, "items.0.url") != pageHost+"/news/bitcoin%20ETF%20flows" ||
		num(t, done, "credits.used") != 2 || answer.String() != "Checking the tape.\n\nETF inflows stay strong [1]. **View:** bullish above $97k." {
		t.Fatalf("stream: %v", ev)
	}
	first := s.bedrock.Requests()
	first = slices.DeleteFunc(first, func(r llmtest.Request) bool { return !r.Stream })
	if r := first[0]; r.Effort != "medium" || !strings.Contains(r.System, "The user is looking at BTC right now") ||
		!slices.Contains(r.Tools, "propose_trade") || !slices.Contains(r.Tools, "search_news") {
		t.Fatalf("chat request: effort %q tools %v", r.Effort, r.Tools)
	}
	searches, _ := s.web.seen()
	if !slices.Contains(searches, search{"news", "bitcoin ETF flows", "pw", "6"}) {
		t.Fatalf("search_news never reached Brave: %v", searches)
	}

	// Both turns are stored; the assistant's holds the streamed text and cards.
	chats := c.call("GET", "/api/chats", nil, 200)
	shapeAll(t, chats, "chats", "id title updatedAt")
	if l := list(t, chats, "chats"); len(l) != 1 || str(t, l[0], "id") != chatID {
		t.Fatalf("chats: %v", chats)
	}
	got := c.call("GET", "/api/chats/"+chatID, nil, 200)
	shape(t, got, "chat", "id title messages pending")
	shapeAll(t, got, "chat.messages", chatMsgKeys)
	m := "chat.messages."
	if len(list(t, got, "chat.messages")) != 2 || str(t, got, m+"0.role") != "user" || str(t, got, m+"0.content") != "Should I buy BTC here?" ||
		len(list(t, got, m+"0.cards")) != 0 || str(t, got, m+"1.id") != str(t, done, "messageId") || str(t, got, m+"1.content") != answer.String() ||
		str(t, got, m+"1.cards.0.type") != "trade_proposal" || str(t, got, m+"1.cards.1.type") != "sources" {
		t.Fatalf("stored chat: %v", got)
	}

	// Confirm on the trade card places the order as the agent's.
	o := c.call("POST", "/api/orders", map[string]any{"symbol": at(t, trade, "symbol"), "side": at(t, trade, "side"), "notional": at(t, trade, "notional"),
		"stopLoss": at(t, trade, "stopLoss"), "takeProfit": at(t, trade, "takeProfit"), "source": "agent", "note": at(t, trade, "rationale")}, 200)
	if str(t, o, "trade.source") != "agent" || num(t, o, "portfolio.positions.0.takeProfit") != 106_000 ||
		s.count(t, "select count(*) from trades where user_id = $1 and source = 'agent'", c.id) != 1 {
		t.Fatalf("confirm: %v", o)
	}

	// A follow-up in the same chat sends the stored history.
	ev = c.stream(map[string]any{"chatId": chatID, "message": "And ETH?"})
	if ev[0].name != "chat" || str(t, ev[0].data, "chatId") != chatID || ev[len(ev)-1].name != "done" || num(t, ev[len(ev)-1].data, "credits.used") != 4 {
		t.Fatalf("follow-up: %v", ev)
	}
	reqs := s.bedrock.Requests()
	i := slices.IndexFunc(reqs, func(r llmtest.Request) bool { return r.Prompt == "And ETH?" })
	if i < 0 {
		t.Fatal("the follow-up never reached the model")
	}
	h := reqs[i].Messages
	if len(h) != 3 || h[0].Role != "user" || h[1].Role != "assistant" || h[1].Content[0]["text"] != answer.String() || h[2].Content[0]["text"] != "And ETH?" {
		t.Fatalf("history sent: %v", h)
	}
	if n := s.count(t, "select count(*) from chat_messages where chat_id = $1", chatID); n != 4 {
		t.Fatalf("messages stored: %d", n)
	}

	other := s.signIn(t)
	other.fail("GET", "/api/chats/"+chatID, nil, 404, "not_found")
	other.fail("DELETE", "/api/chats/"+chatID, nil, 404, "not_found")
	other.fail("POST", "/api/chat/stream", map[string]string{"chatId": chatID, "message": "hi"}, 404, "not_found")
	c.fail("POST", "/api/chat/stream", map[string]string{"message": " "}, 400, "bad_request")
	c.fail("POST", "/api/chat/stream", map[string]any{"message": "hi", "context": map[string]string{"symbol": "SHIB"}}, 400, "bad_request")
	c.fail("POST", "/api/chat/stream", map[string]string{"chatId": "nope", "message": "hi"}, 404, "not_found")
	c.call("DELETE", "/api/chats/"+chatID, nil, 204)
	c.fail("GET", "/api/chats/"+chatID, nil, 404, "not_found")
	if n := s.count(t, "select count(*) from chat_messages"); n != 0 {
		t.Fatalf("messages left after delete: %d", n)
	}
}

func TestIntegrationNews(t *testing.T) {
	s := boot(t, options{brave: true})
	c := s.signIn(t)

	// Boot ingests at once (nothing stored yet); the brief is its last step.
	var brief any
	eventually(t, 30*time.Second, "the boot ingestion's market brief", func() bool {
		brief = c.call("GET", "/api/news/brief", nil, 200)
		return !isNull(t, brief, "brief")
	})
	news := c.call("GET", "/api/news", nil, 200)
	shapeAll(t, news, "news", newsKeys)
	items := list(t, news, "news")
	if len(items) != 3 {
		t.Fatalf("news: %v", news)
	}
	for i, want := range []struct {
		path, topic      string
		symbols, regions []any
		read             bool
	}{{"/btc-etf", "asset", []any{"BTC", "ETH"}, []any{"crypto"}, true}, {"/fed", "macro", []any{}, []any{"us"}, true},
		{"/btc-miners", "asset", []any{"BTC"}, []any{"crypto"}, true}} {
		it := items[i]
		if str(t, it, "url") != pageHost+want.path || str(t, it, "topic") != want.topic || !slices.Equal(list(t, it, "symbols"), want.symbols) ||
			!slices.Equal(list(t, it, "regions"), want.regions) || at(t, it, "hasContent") != want.read {
			t.Fatalf("news %d: %v", i, it)
		}
		isTime(t, it, "publishedAt")
	}
	ids := []any{str(t, items[0], "id"), str(t, items[1], "id"), str(t, items[2], "id")}
	for q, want := range map[string][]any{"?symbol=eth": ids[:1], "?topic=macro": ids[1:2], "?symbol=btc&topic=asset&limit=1": ids[:1],
		"?region=us": ids[1:2], "?region=crypto&limit=1": ids[:1], "?region=east_asia": nil} {
		var got []any
		for _, it := range list(t, c.call("GET", "/api/news"+q, nil, 200), "news") {
			got = append(got, str(t, it, "id"))
		}
		if !slices.Equal(got, want) {
			t.Errorf("news%s: %v, want %v", q, got, want)
		}
	}
	for _, q := range []string{"?symbol=NOPE", "?topic=rumor", "?limit=0", "?limit=101", "?region=asia", "?region=US"} {
		c.fail("GET", "/api/news"+q, nil, 400, "bad_request")
	}

	shape(t, brief, "brief", briefKeys+" sources") // GET /api/news/brief adds the cited rows
	shapeAll(t, brief, "brief.bullets", "text symbols sources")
	shapeAll(t, brief, "brief.sources", "id title url source publishedAt")
	if got := list(t, brief, "brief.sources"); len(got) != 3 || str(t, got[0], "id") != ids[0] {
		t.Errorf("brief sources: %v", got)
	}
	shapeAll(t, brief, "brief.sentiment", "symbol score label reason")
	shape(t, brief, "brief.window", "from to")
	b := "brief."
	if str(t, brief, b+"headline") != "ETF demand lifts bitcoin" || num(t, brief, b+"articleCount") != 3 ||
		!slices.Equal(list(t, brief, b+"bullets.0.sources"), ids[:1]) || !slices.Equal(list(t, brief, b+"bullets.1.sources"), ids[1:2]) ||
		!slices.Equal(list(t, brief, b+"bullets.2.sources"), ids[2:]) || !slices.Equal(list(t, brief, b+"bullets.2.symbols"), []any{"BTC"}) ||
		len(list(t, brief, b+"sentiment")) != 1 || str(t, brief, b+"sentiment.0.symbol") != "BTC" || str(t, brief, b+"sentiment.0.label") != "bullish" {
		t.Fatalf("brief: %v", brief)
	}
	if s.count(t, "select count(*) from news_items") != 3 || s.count(t, "select count(*) from news_items where content is not null and crawled_at is not null") != 3 ||
		s.count(t, "select count(*) from market_briefs") != 1 {
		t.Fatal("news rows")
	}

	// One run: 12 asset, 6 macro and (the boot run is a regional one) 18
	// regional Brave News searches over the past day, the two unread BTC
	// articles and the US region's one read through crawl4ai, one low-effort brief.
	searches, crawled := s.web.seen()
	queries := []string{}
	for _, q := range searches {
		if q.endpoint != "news" || q.freshness != "pd" || q.count != "10" {
			t.Fatalf("ingest search %+v", q)
		}
		queries = append(queries, q.q)
	}
	slices.Sort(crawled)
	if len(queries) != 36 || !slices.Contains(queries, "gold price") || !slices.Contains(queries, "US dollar treasury yields") ||
		!slices.Contains(queries, "Nikkei Japan stocks") || !slices.Contains(queries, "Vietnam stocks VN-Index") ||
		!slices.Equal(crawled, []string{pageHost + "/btc-etf", pageHost + "/btc-miners", pageHost + "/fed"}) {
		t.Fatalf("searches %q, crawled %q", queries, crawled)
	}
	if r := s.bedrock.Requests(); len(r) != 1 || r[0].Effort != "low" || r[0].Stream {
		t.Fatalf("brief model calls: %d", len(r))
	}
}

var stepRE = regexp.MustCompile(`Step (\d+) of \d+`)

// arenaStep is the 1-based step of an arena agent call, from its first message.
func arenaStep(r llmtest.Request) int {
	if len(r.Messages) > 0 && len(r.Messages[0].Content) > 0 {
		text, _ := r.Messages[0].Content[0]["text"].(string)
		if m := stepRE.FindStringSubmatch(text); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return 0
}

func TestIntegrationArena(t *testing.T) {
	// The first run's Trend Rider buys BTC at step 1 and then holds; the
	// second run's agents hold and wait at step 3 until the test opens gate.
	gate, reached := make(chan struct{}), make(chan struct{})
	var opened, arrived sync.Once
	open := func() { opened.Do(func() { close(gate) }) }
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		step := arenaStep(r)
		switch {
		case !strings.Contains(r.System, "historical trading simulation") || step == 0:
			t.Errorf("unexpected model call: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		case !strings.HasPrefix(r.System, "You are Trend Rider"):
			if step == 3 {
				arrived.Do(func() { close(reached) })
				<-gate
			}
			return llmtest.Reply{Text: "Holding cash."}
		case step == 1 && len(r.Messages) == 1:
			return llmtest.Reply{Reasoning: "arena plan", Tools: []llmtest.ToolUse{{ID: "o1", Name: "place_orders", Input: `{"targets":[{"symbol":"BTC","weightPct":50}]}`}}}
		case step == 1:
			b := string(r.Body)
			if !strings.Contains(b, `"toolUse"`) || !strings.Contains(b, `"toolResult"`) ||
				!strings.Contains(b, `"signature":"sig-arena plan"`) || strings.Contains(r.Prompt, "<run_transcript>") {
				t.Errorf("arena must preserve tool blocks and signed reasoning: %s", b)
			}
			return llmtest.Reply{Text: "Bought BTC at half weight on the uptrend."}
		}
		return llmtest.Reply{Text: "Holding: the trend is intact."}
	}})
	t.Cleanup(open) // runs first: a blocked fake call must return before the fakes close
	c := s.signIn(t)

	opts := c.call("GET", "/api/arena/options", nil, 200)
	shape(t, opts, "", "personas knowledgeCutoff latestEnd cadences maxAgents maxSteps creditsPerAgentStep")
	shapeAll(t, opts, "personas", "id name description")
	shapeAll(t, opts, "cadences", "cadence minDays maxDays defaultStart defaultEnd")

	window := map[string]any{"cadence": "1d", "start": "2026-09-01", "end": "2026-09-30"}
	body := func(agents ...map[string]string) map[string]any {
		b := map[string]any{"agents": agents}
		for k, v := range window {
			b[k] = v
		}
		return b
	}
	created := c.call("POST", "/api/arena/runs", body(map[string]string{"personaId": "momentum"}), 201)
	shape(t, created, "", "run warnings credits")
	shape(t, created, "run", runKeys)
	id := str(t, created, "run.id")
	if str(t, created, "run.status") != "running" || num(t, created, "run.steps") != 30 || num(t, created, "run.credits") != 15 ||
		at(t, created, "run.contaminated") != false || len(list(t, created, "warnings")) != 0 || len(list(t, created, "run.universe")) != 12 ||
		num(t, created, "credits.used") != 15 || str(t, created, "run.model") != llmtest.Model {
		t.Fatalf("create: %v", created)
	}

	var run any
	eventually(t, 60*time.Second, "the first arena run", func() bool {
		run = c.call("GET", "/api/arena/runs/"+id, nil, 200)
		return str(t, run, "run.status") != "running"
	})
	shape(t, run, "run", runKeys)
	shapeAll(t, run, "run.agents", agentKeys)
	shape(t, run, "run.agents.0.metrics", arenaMetrics)
	shapeAll(t, run, "run.benchmarks", "id name equity metrics")
	a := "run.agents.0."
	if str(t, run, "run.status") != "completed" || num(t, run, "run.stepsDone") != 30 || num(t, run, "run.credits") != 15 ||
		len(list(t, run, a+"equity")) != 31 || num(t, run, a+"metrics.fills") != 1 || num(t, run, a+"rank") != 1 || num(t, run, a+"errors") != 0 ||
		str(t, run, a+"lastReasoning") != "Holding: the trend is intact." || len(list(t, run, "run.benchmarks.0.equity")) != 31 ||
		!isNull(t, run, a+"metrics.annualizedReturnPct") || !isNull(t, run, a+"metrics.calmar") || !isNull(t, run, a+"metrics.winRatePct") {
		t.Fatalf("finished run: %v", run) // 30 days: nothing annualized; one buy and no sell: no win rate
	}
	isTime(t, run, "run.completedAt")

	// Steps page by "after"; step 0 holds the BTC order and its fill.
	page := c.call("GET", "/api/arena/runs/"+id+"/steps?agent=0&limit=10", nil, 200)
	shapeAll(t, page, "steps", stepKeys)
	steps := list(t, page, "steps")
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix()
	if len(steps) != 10 || num(t, steps[0], "step") != 0 || num(t, steps[9], "step") != 9 || num(t, steps[9], "t") != float64(start+9*86400) ||
		str(t, steps[0], "targets.0.symbol") != "BTC" || num(t, steps[0], "targets.0.weightPct") != 50 || len(list(t, steps[0], "fills")) != 1 ||
		str(t, steps[0], "fills.0.side") != "buy" || str(t, steps[0], "tools.0.name") != "place_orders" || at(t, steps[0], "tools.0.ok") != true ||
		!isNull(t, steps[1], "targets") || len(list(t, steps[1], "fills")) != 0 {
		t.Fatalf("first page: %v", steps)
	}
	shapeAll(t, steps[0], "fills", "symbol side qty price notional fee realizedPnl")
	shapeAll(t, steps[0], "tools", "name input ok summary")
	shapeAll(t, steps[0], "positions", "symbol qty avgPrice mark weightPct")
	rest := list(t, c.call("GET", "/api/arena/runs/"+id+"/steps?agent=0&after=9", nil, 200), "steps")
	if len(rest) != 20 || num(t, rest[0], "step") != 10 || num(t, rest[19], "step") != 29 ||
		len(list(t, c.call("GET", "/api/arena/runs/"+id+"/steps?agent=0&after=29", nil, 200), "steps")) != 0 {
		t.Fatalf("later pages: %d steps", len(rest))
	}
	for _, q := range []string{"", "?agent=1", "?agent=0&limit=0", "?agent=0&after=-2"} {
		c.fail("GET", "/api/arena/runs/"+id+"/steps"+q, nil, 400, "bad_request")
	}
	runs := c.call("GET", "/api/arena/runs", nil, 200)
	shapeAll(t, runs, "runs", runSumKeys)
	if str(t, runs, "runs.0.leader.name") != "Trend Rider" || s.count(t, "select count(*) from arena_steps where run_id = $1", id) != 30 {
		t.Fatalf("runs: %v", runs)
	}

	// A second run: two agents, cancelled while step 3 is in flight. Cancel
	// bills the finished steps plus the one in flight and refunds the rest.
	second := c.call("POST", "/api/arena/runs", body(map[string]string{"personaId": "contrarian"},
		map[string]string{"personaId": "custom", "name": "Patient Pat", "instruction": "Hold cash unless BTC drops 10% in a week."}), 201)
	id2 := str(t, second, "run.id")
	if num(t, second, "run.credits") != 30 || num(t, second, "credits.used") != 45 || str(t, second, "run.agents.1.name") != "Patient Pat" {
		t.Fatalf("second run: %v", second)
	}
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the second run never reached step 3")
	}
	if done := num(t, c.call("GET", "/api/arena/runs/"+id2, nil, 200), "run.stepsDone"); done != 2 {
		t.Fatalf("steps done before the gate: %v", done)
	}
	c.fail("POST", "/api/arena/runs", body(map[string]string{"personaId": "macro"}), 409, "conflict")
	c.fail("DELETE", "/api/arena/runs/"+id2, nil, 409, "conflict")
	cancelled := c.call("POST", "/api/arena/runs/"+id2+"/cancel", "{}", 200)
	shape(t, cancelled, "", "run credits")
	if str(t, cancelled, "run.status") != "cancelled" || num(t, cancelled, "run.credits") != 3 || num(t, cancelled, "credits.used") != 18 {
		t.Fatalf("cancel: %v", cancelled)
	}
	open()
	s.waitLog(t, "arena run stopped", id2)
	c.fail("POST", "/api/arena/runs/"+id2+"/cancel", "{}", 409, "conflict")
	if s.count(t, "select count(*) from arena_steps where run_id = $1", id2) != 4 ||
		s.count(t, "select credits from ai_usage where user_id = $1", c.id) != 18 {
		t.Fatal("cancelled run: steps or credits")
	}
	c.call("DELETE", "/api/arena/runs/"+id2, nil, 204)
	c.fail("GET", "/api/arena/runs/"+id2, nil, 404, "not_found")
	s.signIn(t).fail("GET", "/api/arena/runs/"+id, nil, 404, "not_found")
	if l := list(t, c.call("GET", "/api/arena/runs", nil, 200), "runs"); len(l) != 1 || s.count(t, "select count(*) from arena_steps where run_id = $1", id2) != 0 {
		t.Fatalf("after delete: %v", l)
	}
}

func TestIntegrationCredits(t *testing.T) {
	var fail atomic.Bool
	s := boot(t, options{credits: 3, model: func(r llmtest.Request) llmtest.Reply {
		if !strings.HasPrefix(r.System, "You are the Copilot") {
			t.Errorf("model called past the quota: %.60q", r.System)
			return llmtest.Reply{Status: 400}
		}
		if fail.Load() {
			return llmtest.Reply{Status: 400}
		}
		return llmtest.Reply{Text: copilotReply}
	}})
	c := s.signIn(t)
	used := func(c *client) float64 {
		me := c.call("GET", "/api/me", nil, 200)
		if num(t, me, "credits.limit") != 3 || !isTime(t, me, "credits.resetsAt").Equal(tomorrow()) {
			t.Fatalf("credits: %v", me)
		}
		return num(t, me, "credits.used")
	}
	if used(c) != 0 {
		t.Fatal("fresh user has spent credits")
	}
	plan := map[string]string{"symbol": "BTC", "style": "day", "bias": "long"}
	for i := 1; i <= 3; i++ {
		if got := c.call("POST", "/api/copilot/plan", plan, 200); num(t, got, "credits.used") != float64(i) {
			t.Fatalf("plan %d: %v", i, at(t, got, "credits"))
		}
	}

	// Exhausted: every AI endpoint answers 429 ai_quota before calling Bedrock.
	c.fail("POST", "/api/copilot/plan", plan, 429, "ai_quota")
	for path, body := range map[string]any{
		"/api/chat/stream":     map[string]string{"message": "Should I buy BTC?"},
		"/api/forecasts/draft": map[string]string{"idea": "Will BTC rally?"},
		"/api/forecasts": map[string]any{"shape": "binary", "question": "Will BTC close above $120k in 2099?", "outcomes": []string{"Yes", "No"},
			"resolutionDate": "2099-12-31", "category": "crypto"},
		"/api/strategies/generate": map[string]string{"prompt": "Buy BTC dips"},
		"/api/ai-portfolios/build": map[string]string{"prompt": "Blue chips"},
		"/api/arena/runs":          map[string]any{"cadence": "1d", "start": "2026-09-01", "end": "2026-09-30", "agents": []any{map[string]string{"personaId": "momentum"}}},
	} {
		c.fail("POST", path, body, 429, "ai_quota")
	}
	if n := len(s.bedrock.Requests()); n != 3 {
		t.Fatalf("Bedrock calls past the quota: %d", n)
	}
	for _, table := range []string{"chats", "forecasts", "strategies", "ai_portfolios", "arena_runs"} {
		if n := s.count(t, "select count(*) from "+table+" where user_id is not null"); n != 0 { // not the boot's shared replay forecasts
			t.Fatalf("%s rows written past the quota: %d", table, n)
		}
	}
	c.call("GET", "/api/portfolio", nil, 200) // the rest of the app still works

	// 00:00 UTC: today's usage becomes yesterday's and the limit is fresh.
	if _, err := s.db.Exec(context.Background(), "update ai_usage set day = day - 1 where user_id = $1", c.id); err != nil {
		t.Fatal(err)
	}
	if used(c) != 0 {
		t.Fatal("credits did not reset with the day")
	}
	c.call("POST", "/api/copilot/plan", plan, 200)
	rows, _ := s.db.Query(context.Background(), "select credits from ai_usage where user_id = $1 order by day", c.id)
	var perDay []int
	for rows.Next() {
		var n int
		rows.Scan(&n)
		perDay = append(perDay, n)
	}
	if rows.Err() != nil || !slices.Equal(perDay, []int{3, 1}) {
		t.Fatalf("usage per day: %v %v", perDay, rows.Err())
	}

	// A failed model call gives its credit back; credits are per user.
	fail.Store(true)
	c.fail("POST", "/api/copilot/plan", plan, 502, "upstream")
	if used(c) != 1 || used(s.signIn(t)) != 0 {
		t.Fatal("refund or per-user credits")
	}
}
