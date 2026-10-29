package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
)

// Round 2 quality: the signals board's track record (steady-v1: its pick ledger), the studio's stop/target
// optimizer, and an admin disable that stops the account's autopilot.
// Round 3: a per-asset news read hides a multi-coin roundup whose title names
// another asset and lists title matches first; the levels page titled BTC
// stays for BTC, once and last.
func TestIntegrationQuality(t *testing.T) {
	s := boot(t, options{signals: true})
	c := s.signIn(t)

	if _, err := s.db.Exec(t.Context(), `insert into news_items (url, title, source, description, published_at, symbols, topic) values
		('https://cryptoticker.io/en/bnb-37th-burn', 'BNB Price Prediction: 44% Below Record, 37th Burn Nears', 'cryptoticker.io',
		 'Ethereum holds firm above $2,300 while Bitcoin leads the rally. As of February 4, 2026, Solana (SOL) has decisively ...', now(), '{ADA,BNB,BTC,ETH,SOL,TON}', 'asset'),
		('https://pandabull.io/support_resistance/BTC', 'Pandabull | Support Resistance Levels - BTC', 'pandabull.io',
		 'Support and resistance for Bitcoin (BTC), Ethereum (ETH), Ripple (XRP), Solana (SOL), Binance Coin (BNB) and more.', now(), '{BNB,BTC,ETH,SOL,XRP}', 'asset'),
		('https://news.example/fed', 'Fed holds rates', 'Wire', 'Bitcoin and solana dipped', now() - interval '1 hour', '{BTC,SOL}', 'macro'),
		('https://news.example/sol', 'Solana DEX volumes hit a record', 'Desk', '', now() - interval '2 hours', '{SOL}', 'asset'),
		('https://news.example/btc', 'Bitcoin miners sell', 'Desk', '', now() - interval '3 hours', '{BTC}', 'asset')`); err != nil {
		t.Fatal(err)
	}
	titles := func(symbol string) (out []string) {
		for _, n := range list(t, c.call("GET", "/api/news?symbol="+symbol, nil, 200), "news") {
			out = append(out, str(t, n, "title"))
		}
		return out
	}
	if got := titles("SOL"); fmt.Sprint(got) != "[Solana DEX volumes hit a record Fed holds rates]" {
		t.Errorf("SOL: %q", got)
	}
	if got := titles("BTC"); fmt.Sprint(got) != "[Bitcoin miners sell Fed holds rates Pandabull | Support Resistance Levels - BTC]" {
		t.Errorf("BTC: %q", got)
	}

	// The board's exits and track record come from its pick ledger, not candles: SOL left at its invalidation
	// and was scored at 72 h against a random pick from the same list. Boards stored before steady-v1 are still
	// retained (the switch was 84 h ago): how their picks left is listed after, marked legacy, never scored. SOL
	// dropped off the bullish side of one, its score under the threshold as its refresh recorded it (gap round
	// G22), and BTC left at the switch, which recorded no reason.
	eventually(t, 30*time.Second, "the boot board", func() bool { return s.count(t, "select count(*) from signal_boards") > 0 })
	picked := time.Now().Add(-80 * time.Hour).UTC().Truncate(time.Second)
	if _, err := s.db.Exec(t.Context(), `insert into signal_picks (symbol, side, picked_at, price_at_pick, target, invalidation, valid_until, score_at_pick,
		universe, exit_reason, exit_at, exit_price, return_pct, removed_at, scored_at, horizon_h, result_pct, random_pct, random_up_share, universe_n)
		values ('SOL', 'bullish', $1, 190, 201.4, 178.6, $1::timestamptz + interval '72 hours', 40, '[{"s":"SOL","p":190},{"s":"BTC","p":95000}]',
		'invalidated', $1::timestamptz + interval '5 hours', 178.6, -6, $1::timestamptz + interval '8 hours', $1::timestamptz + interval '72 hours', 72,
		-4.567, -1.2, 0.4, 2)`, picked); err != nil {
		t.Fatal(err)
	}
	legacyPicked := time.Now().Add(-93 * time.Hour).UTC().Truncate(time.Second)
	pick := func(symbol string, price, target float64) map[string]any {
		return map[string]any{"symbol": symbol, "side": "bullish", "score": 40, "priceAtPick": price, "pickedAt": legacyPicked, "target": target,
			"validUntil": legacyPicked.Add(72 * time.Hour), "volTier": "medium", "rationale": "Go.", "headlines": []any{},
			"factors": map[string]any{"return7dPct": 1, "return30dPct": 9, "rsi14": 60, "trend": "uptrend", "volAnnPct": 50, "drawdownPct": 3, "sentiment": nil}}
	}
	older, _ := json.Marshal(map[string]any{"bullish": []any{pick("BTC", 95_000, 120_000), pick("SOL", 190, 250)}, "bearish": []any{}})
	newer, _ := json.Marshal(map[string]any{"bullish": []any{pick("BTC", 95_000, 120_000)}, "bearish": []any{},
		"_dropped": []any{map[string]any{"symbol": "SOL", "side": "bullish", "reason": "score_fell", "score": 12}}})
	steady, _ := json.Marshal(map[string]any{"bullish": []any{}, "bearish": []any{},
		"_ledger": map[string]any{"version": 1, "rule": "steady-v1", "universe": []any{}, "exits": []any{}}})
	if _, err := s.db.Exec(t.Context(), "delete from signal_boards"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(t.Context(), `insert into signal_boards (board, created_at) values ($1, now() - interval '92 hours'),
		($2, now() - interval '88 hours'), ($3, now() - interval '84 hours'), ($3, now() - interval '5 minutes')`,
		researchtest.Board(t, s.db, older), researchtest.Board(t, s.db, newer), researchtest.Board(t, s.db, steady)); err != nil {
		t.Fatal(err)
	}
	got := c.call("GET", "/api/signals", nil, 200)
	shape(t, got, "", "board history historyUnavailable record refreshing refresh asOf firstBoard")
	shape(t, got, "history.0", "symbol side pickedAt removedAt priceAtPick target invalidation exitAt exitPrice returnPct result72Pct random72Pct scoredAt reason")
	for _, i := range []string{"1", "2"} { // a legacy pick has no 72 h score: none of its keys
		shape(t, got, "history."+i, "symbol side pickedAt removedAt priceAtPick target invalidation exitAt exitPrice returnPct reason legacy")
	}
	shape(t, got, "record", "rule since horizonHours costPct minPicks minBlocks confidence open unscored all bullish bearish replay")
	for _, side := range []string{"all", "bullish", "bearish"} {
		shape(t, got, "record."+side, "scored blocks movedItsWayPct randomMovedItsWayPct avgResultPct avgResultNetPct avgRandomPct excessPct excessLo99 excessHi99 "+
			"targetHit invalidated expired verdict")
	}
	shape(t, got, "record.replay", "from to picks days blocks movedItsWayPct randomMovedItsWayPct avgResultPct avgResultNetPct avgRandomPct excessPct excessLo95 excessHi95 bullish bearish")
	shape(t, got, "record.replay.bearish", "picks targetFirstPct invalidatedFirstPct expiredPct randomTargetFirstPct randomInvalidatedFirstPct excessPct excessLo95 excessHi95")
	if h := list(t, got, "history"); len(h) != 3 || str(t, h[0], "symbol") != "SOL" || str(t, h[0], "reason") != "invalidated" || num(t, h[0], "exitPrice") != 178.6 ||
		num(t, h[0], "returnPct") != -6 || num(t, h[0], "result72Pct") != -4.57 || num(t, h[0], "random72Pct") != -1.2 || num(t, h[0], "invalidation") != 178.6 ||
		!isTime(t, h[0], "pickedAt").Equal(picked) || !isTime(t, h[0], "exitAt").Equal(picked.Add(5*time.Hour)) || !isTime(t, h[0], "removedAt").Equal(picked.Add(8*time.Hour)) ||
		!isTime(t, h[0], "scoredAt").Equal(picked.Add(72*time.Hour)) || at(t, got, "historyUnavailable") != false {
		t.Fatalf("history: %v", h)
	}
	// The fake's 1h candles all closed before the legacy picks: each exit is the asset's last close (SOL 200).
	if h := list(t, got, "history"); str(t, h[1], "symbol") != "BTC" || str(t, h[1], "reason") != "left" || at(t, h[1], "legacy") != true ||
		!isTime(t, h[1], "removedAt").Equal(isTime(t, got, "asOf").Add(-84*time.Hour+5*time.Minute)) || num(t, h[1], "invalidation") != 0 ||
		num(t, h[1], "exitPrice") != markettest.Price("BTCUSDT") || !isTime(t, h[1], "pickedAt").Equal(legacyPicked) ||
		str(t, h[2], "symbol") != "SOL" || str(t, h[2], "reason") != "score_fell" || at(t, h[2], "legacy") != true ||
		num(t, h[2], "exitPrice") != 200 || num(t, h[2], "returnPct") != 5.26 {
		t.Fatalf("legacy history: %v", h)
	}
	if str(t, got, "record.rule") != "steady-v1" || !isTime(t, got, "record.since").Equal(picked) || num(t, got, "record.all.scored") != 1 ||
		num(t, got, "record.bullish.avgResultPct") != -4.57 || num(t, got, "record.bullish.avgResultNetPct") != -4.67 || num(t, got, "record.all.invalidated") != 1 ||
		str(t, got, "record.all.verdict") != "insufficient" || !isNull(t, got, "record.all.excessPct") || num(t, got, "record.replay.picks") != 864 ||
		num(t, got, "record.replay.excessPct") != -0.03 || num(t, got, "record.confidence") != 0.99 {
		t.Fatalf("record: %v", at(t, got, "record"))
	}

	// Optimize: 25 free backtests on one candle fetch.
	opt := c.call("POST", "/api/strategies/optimize", map[string]any{"spec": json.RawMessage(alwaysSpec)}, 200)
	shape(t, opt, "", "grid best current")
	shapeAll(t, opt, "grid", "stopLossPct takeProfitPct sharpe totalReturnPct maxDrawdownPct trades")
	if len(list(t, opt, "grid")) != 25 || len(list(t, opt, "best")) > 3 || num(t, opt, "current.stopLossPct") != 5 || !isNull(t, opt, "current.takeProfitPct") ||
		num(t, opt, "grid.0.stopLossPct") != 1 || num(t, opt, "grid.24.takeProfitPct") != 15 {
		t.Fatalf("optimize: %v", opt)
	}
	c.fail("POST", "/api/strategies/optimize", map[string]any{"spec": map[string]any{"symbol": "SHIB"}}, 400, "bad_request")
	if n := s.count(t, "select coalesce(sum(credits), 0) from ai_usage"); n != 0 {
		t.Fatalf("optimize spent %d credits", n)
	}

	// An admin disables the account: its running autopilot stops in the same change, position kept.
	top := list(t, c.call("GET", "/api/strategies?scope=top", nil, 200), "strategies")
	c.call("POST", "/api/autopilots", map[string]any{"strategyId": str(t, top[0], "id"), "allocationUsd": 5000, "maxDrawdownPct": 10,
		"stopLossPct": 5, "takeProfitPct": 10}, 201) // every autopilot entry carries a stop and a target
	admin := s.signIn(t)
	if _, err := s.db.Exec(t.Context(), "update users set role = 'admin' where id = $1", admin.id); err != nil {
		t.Fatal(err)
	}
	asAdmin := func(method, path string, body any) any {
		t.Helper()
		req := admin.request(method, path, body)
		req.Header.Set("Origin", adminOrigin)
		resp, b := admin.send(req)
		if resp.StatusCode != 200 {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
		}
		return decode(t, b)
	}
	asAdmin("PATCH", "/api/admin/users/"+c.id, map[string]bool{"disabled": true})
	if audit := asAdmin("GET", "/api/admin/audit", nil); str(t, audit, "entries.0.action") != "user.disable" ||
		num(t, audit, "entries.0.detail.autopilotsStopped") != 1 {
		t.Fatalf("audit: %v", audit)
	}
	c.fail("GET", "/api/autopilots", nil, 401, "unauthorized")
	asAdmin("PATCH", "/api/admin/users/"+c.id, map[string]bool{"disabled": false})
	c.call("POST", "/api/auth/login", map[string]string{"email": c.email, "password": testPassword}, 200)
	pilots := c.call("GET", "/api/autopilots", nil, 200)
	if str(t, pilots, "autopilots.0.status") != "stopped" || str(t, pilots, "autopilots.0.stopReason") != "account_disabled" ||
		str(t, pilots, "autopilots.0.log.0.text") != "Account disabled by an admin; position kept." {
		t.Fatalf("autopilots after the disable: %v", pilots)
	}
}
