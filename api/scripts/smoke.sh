#!/usr/bin/env bash
# End-to-end smoke test against a running API.
#
#   EMAIL=demo@example.com PASSWORD=… scripts/smoke.sh          # no AI calls
#   AI=1 EMAIL=… PASSWORD=… scripts/smoke.sh                    # + AI endpoints (~11 model calls, 13 credits)
#
# BASE (default http://localhost:8080), ORIGIN (the web app, default
# http://localhost:5173, must be in the server's ALLOWED_ORIGINS) and ADMIN_ORIGIN
# (the console, default http://localhost:5174, must be in ALLOWED_ORIGINS and
# ADMIN_ORIGINS, and differ from ORIGIN) are optional. EMAIL must be a
# non-admin account (the demo user): one check expects the admin API to refuse it.
# Needs bash, curl and python3. Resets the account's paper portfolio. Prints
# PASS/FAIL per check and exits non-zero if any check failed. EXPECT_COMMIT
# (optional, the first 12 characters of a commit) checks /healthz's
# X-Api-Commit header: the deploy under test is the one serving.
set -uo pipefail

BASE=${BASE:-http://localhost:8080}
ORIGIN=${ORIGIN:-http://localhost:5173}
ADMIN_ORIGIN=${ADMIN_ORIGIN:-http://localhost:5174}
EMAIL=${EMAIL:?set EMAIL}
PASSWORD=${PASSWORD:?set PASSWORD}
AI=${AI:-0}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
jar=$tmp/cookies
fails=0
code=000

pass() { printf 'PASS  %s\n' "$1"; }
fail() { printf 'FAIL  %s: %s\n' "$1" "$2"; fails=$((fails + 1)); }

# req METHOD PATH [BODY [extra curl args…]]: response body in $tmp/body,
# headers in $tmp/headers, status in $code. A BODY is sent as application/json.
req() {
	local method=$1 path=$2 body=${3-}
	shift $(($# < 3 ? $# : 3))
	local args=(-sS --max-time 60 -o "$tmp/body" -D "$tmp/headers" -w '%{http_code}' -b "$jar" -c "$jar" -X "$method")
	[ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data-binary "$body")
	code=$(curl "${args[@]}" "$@" "$BASE$path") || code=000
}

# py EXPR: evaluates a Python expression with j = the JSON body (None if not
# JSON), t = the raw body, h = response headers (lower-case names).
py() {
	python3 - "$tmp/body" "$tmp/headers" "$1" <<'PY'
import json, sys
t = open(sys.argv[1]).read()
try:
    j = json.loads(t)
except ValueError:
    j = None
h = {}
for line in open(sys.argv[2]).read().splitlines():
    k, sep, v = line.partition(":")
    if sep:
        h[k.strip().lower()] = v.strip()
v = eval(sys.argv[3], {"j": j, "t": t, "h": h, "json": json})
print(v if not isinstance(v, (dict, list)) else json.dumps(v))
PY
}

# check NAME WANT_STATUS [EXPR]: PASS when the status matches and EXPR is truthy.
check() {
	local name=$1 want=$2 expr=${3:-True} got
	if [ "$code" != "$want" ]; then
		fail "$name" "status $code, want $want: $(head -c 300 "$tmp/body")"
		return 1
	fi
	got=$(py "bool($expr)" 2>&1)
	if [ "$got" != True ]; then
		fail "$name" "expected $expr; body: $(head -c 300 "$tmp/body") ${got:0:300}"
		return 1
	fi
	pass "$name"
}

# json K V [K V…]: a JSON object of string values, safely quoted.
json() { python3 -c 'import json, sys; a = sys.argv[1:]; print(json.dumps(dict(zip(a[::2], a[1::2]))))' "$@"; }

: >"$tmp/headers"

# --- health and auth --------------------------------------------------------
req GET /healthz
check "healthz" 200 't.strip() == "ok"'
if [ -n "${EXPECT_COMMIT:-}" ]; then
	check "healthz serves commit $EXPECT_COMMIT" 200 "h.get('x-api-commit') == '$EXPECT_COMMIT'"
fi

req POST /api/auth/login "$(json email "$EMAIL" password "not-the-password")"
check "login rejects a wrong password" 401 'j["error"]["code"] == "invalid_credentials"'

req POST /api/auth/login "$(json email "$EMAIL" password "$PASSWORD")"
check "login" 200 "j['user']['email'] == '$(printf %s "$EMAIL" | tr '[:upper:]' '[:lower:]')' and 'tsid=' in h.get('set-cookie', '') and 'HttpOnly' in h['set-cookie']"

req GET /api/me
check "me" 200 'j["user"]["id"] and j["user"]["role"] == "user" and j["credits"]["limit"] > 0 and j["credits"]["resetsAt"].endswith("Z")'

req GET /api/admin/overview
check "admin API refuses a non-admin" 403 'j["error"]["code"] == "forbidden" and h.get("cache-control") == "no-store"'

# --- CSRF and CORS ------------------------------------------------------------
req POST /api/orders '' -H 'Content-Type: text/plain' --data-binary '{"symbol":"BTC","side":"buy","notional":10}'
check "CSRF rejects a non-JSON content type" 403 'j["error"]["code"] == "forbidden"'

req POST /api/orders '{"symbol":"BTC","side":"buy","notional":10}' -H 'Origin: https://evil.example'
check "CSRF rejects a foreign Origin" 403 'j["error"]["code"] == "forbidden"'

req OPTIONS /api/orders '' -H "Origin: $ORIGIN" -H 'Access-Control-Request-Method: POST' -H 'Access-Control-Request-Headers: content-type'
check "CORS preflight" 204 "h.get('access-control-allow-origin') == '$ORIGIN' and h.get('access-control-allow-credentials') == 'true' and 'POST' in h.get('access-control-allow-methods', '')"

req OPTIONS /api/orders '' -H 'Origin: https://evil.example' -H 'Access-Control-Request-Method: POST'
check "CORS ignores a foreign Origin" 204 '"access-control-allow-origin" not in h'

req OPTIONS /api/admin/overview '' -H "Origin: $ADMIN_ORIGIN" -H 'Access-Control-Request-Method: GET'
check "CORS preflight from the admin console" 204 "h.get('access-control-allow-origin') == '$ADMIN_ORIGIN' and h.get('access-control-allow-credentials') == 'true'"

req GET /api/admin/overview '' -H "Origin: $ORIGIN"
check "admin API refuses the web app's origin" 403 'j["error"]["message"] == "Cross-site request rejected."'

# --- market data --------------------------------------------------------------
# Every listed pair is priced and listed once, BTC included (its price is used below). A core pair may be missing
# only while Binance has it halted (no trade for over an hour; the API leaves it out): its newest hourly bar is that old.
req GET /api/market/assets
check "market assets" 200 'all(a["price"] > 0 for a in j["assets"]) and len({a["symbol"] for a in j["assets"]}) == len(j["assets"]) and any(a["symbol"] == "BTC" for a in j["assets"])' &&
	btc=$(py 'next(a["price"] for a in j["assets"] if a["symbol"] == "BTC")') &&
	unlisted=$(py '" ".join(s for s in "BTC ETH SOL BNB XRP DOGE ADA AVAX LINK SUI TON PAXG".split() if s not in {a["symbol"] for a in j["assets"]})')
for sym in ${unlisted:-}; do
	req GET "/api/market/candles?symbol=$sym&interval=1h&limit=1"
	check "market assets: core $sym left out as halted" 200 'j["candles"][-1]["t"] < __import__("time").time() - 3600'
done

req GET '/api/market/candles?symbol=BTC&interval=1h&limit=50'
check "market candles" 200 'len(j["candles"]) == 50 and set(j["candles"][0]) == {"t", "o", "h", "l", "c", "v"} and j["candles"][0]["t"] < j["candles"][-1]["t"]'

req GET '/api/market/candles?symbol=NOPE&interval=1h'
check "market candles rejects an unknown symbol" 400 'j["error"]["code"] == "bad_request"'

req GET '/api/market/analysis?symbol=ETH&interval=4h'
check "market analysis" 200 'j["analysis"]["symbol"] == "ETH" and j["analysis"]["trend"] in ("uptrend", "downtrend", "range") and j["analysis"]["support"] <= j["analysis"]["resistance"]'

# --- paper trading ------------------------------------------------------------
req POST /api/portfolio/reset '{}'
check "portfolio reset" 200 'j["portfolio"]["cash"] == j["portfolio"]["startingBalance"] == 100000 and j["portfolio"]["positions"] == []'

btc=${btc:-0}
sl=$(python3 -c "print(round($btc * 0.9, 2))")
tp=$(python3 -c "print(round($btc * 1.1, 2))")
req POST /api/orders "{\"symbol\":\"BTC\",\"side\":\"buy\",\"notional\":2000,\"stopLoss\":$sl,\"takeProfit\":$tp,\"note\":\"smoke\"}"
check "buy with stop and target" 200 "j['trade']['side'] == 'buy' and abs(j['trade']['notional'] - 2000) < 0.01 and abs(j['trade']['realizedPnl'] + j['trade']['fee']) < 1e-9 and j['trade']['createdAt'].endswith('Z') and j['portfolio']['positions'][0]['stopLoss'] == $sl and j['portfolio']['positions'][0]['takeProfit'] == $tp" &&
	qty=$(py 'j["portfolio"]["positions"][0]["qty"]')

half=$(python3 -c "print(${qty:-0} / 2)")
req POST /api/orders "{\"symbol\":\"BTC\",\"side\":\"sell\",\"qty\":$half}"
check "partial sell" 200 "j['trade']['side'] == 'sell' and abs(j['portfolio']['positions'][0]['qty'] - $half) < 1e-9 and j['portfolio']['positions'][0]['stopLoss'] == $sl"

req GET '/api/portfolio/trades?limit=10'
check "trades" 200 'len(j["trades"]) == 2 and [t["side"] for t in j["trades"]] == ["sell", "buy"] and all(t["createdAt"].endswith("Z") for t in j["trades"])'

req GET /api/portfolio/review
check "portfolio health" 200 'j["positions"] == 1 and j["holdings"][0]["symbol"] == "BTC" and j["holdings"][0]["beta"] == 1 and j["concentration"]["level"] == "high" and j["concentration"]["top1Pct"] == 100 and j["stops"]["covered"] == 1 and j["var"]["usd"] >= 0 and len(j["stress"]) == 3 and j["notes"]'

req POST /api/portfolio/close '{"symbol":"BTC"}'
check "close position" 200 'j["trade"]["side"] == "sell" and j["portfolio"]["positions"] == [] and j["portfolio"]["grossExposure"] == 0'

req POST /api/portfolio/close '{"symbol":"BTC"}'
check "close without a position is 404" 404 'j["error"]["code"] == "not_found"'

# --- strategy studio ----------------------------------------------------------
# Full samples by Sharpe desc, then short samples by Sharpe desc (contract).
req GET '/api/strategies?scope=top'
check "strategies top list" 200 'len(j["strategies"]) == 8 and all(s["author"] == "Foretrade Labs" for s in j["strategies"]) and [(s["shortSample"], -s["metrics"]["sharpe"]) for s in j["strategies"]] == sorted((s["shortSample"], -s["metrics"]["sharpe"]) for s in j["strategies"])' &&
	lab=$(py 'j["strategies"][0]["id"]')

req GET "/api/strategies/${lab:-none}"
check "built-in strategy detail" 200 'j["strategy"]["author"] == "Foretrade Labs" and 0 < len(j["strategy"]["equity"]) <= 400 and len(j["strategy"]["trades"]) <= 100 and j["strategy"]["metrics"]["maxDrawdownPct"] >= 0'

spec='{"spec":{"name":"Smoke SMA cross","symbol":"BTC","interval":"4h","bars":500,"positionPct":100,"feePct":0.05,"stopLossPct":5,
  "longEntry":{"all":[{"left":{"kind":"price"},"op":"crosses_above","right":{"kind":"sma","period":50}}]},
  "longExit":{"all":[{"left":{"kind":"price"},"op":"crosses_below","right":{"kind":"sma","period":50}}]}}}'
req POST /api/strategies/backtest "$spec"
check "strategy backtest (hand-written spec)" 200 'j["equity"][0]["v"] == 100 and len(j["equity"]) <= 400 and len(j["benchmark"]) == len(j["equity"]) and j["metrics"]["bars"] > 0 and j["metrics"]["maxDrawdownPct"] >= 0 and 0 <= j["metrics"]["profitFactor"] <= 100'

req POST /api/strategies/backtest '{"spec":{"name":"bad","symbol":"BTC","interval":"4h","positionPct":100}}'
check "strategy backtest rejects a spec without entries" 400 'j["error"]["code"] == "bad_request"'

# --- lists ------------------------------------------------------------------
req GET /api/ai-portfolios
check "ai-portfolios list" 200 'isinstance(j["portfolios"], list)'

req GET /api/forecasts
check "forecasts list" 200 'isinstance(j["forecasts"], list)'

req GET /api/chats
check "chats list" 200 'isinstance(j["chats"], list)'

# --- AI backtest arena (read-only: never starts a run) ------------------------
req GET /api/arena/options
check "arena options" 200 '[p["id"] for p in j["personas"]] == ["momentum", "contrarian", "macro", "custom"] and [c["cadence"] for c in j["cadences"]] == ["1d", "4h"] and all(c["defaultStart"] <= c["defaultEnd"] == j["latestEnd"] for c in j["cadences"]) and j["maxAgents"] == 4 and j["maxSteps"] == 90 and len(j["knowledgeCutoff"]) == 10'

req GET /api/arena/runs
check "arena runs list" 200 'isinstance(j["runs"], list) and all(r["status"] in ("running", "completed", "failed", "cancelled") for r in j["runs"])'

req DELETE /api/chats/00000000-0000-4000-8000-000000000000 '{}'
check "delete unknown chat is 404" 404 'j["error"]["code"] == "not_found"'

# --- AI endpoints (AI=1) ------------------------------------------------------
if [ "$AI" = 1 ]; then
	# Agent chat over SSE.
	sse=$(curl -sS -N --max-time 240 -b "$jar" -o "$tmp/body" -D "$tmp/headers" -w '%{http_code}' \
		-H 'Content-Type: application/json' --data-binary '{"message":"In one short paragraph, what can you help me with on this desk?"}' \
		"$BASE/api/chat/stream") || sse=000
	code=$sse
	events='[(b.split("\n")[0][7:], json.loads(b.split("\n")[1][6:])) for b in t.split("\n\n") if b.startswith("event: ")]'
	check "chat stream (chat, delta, done)" 200 "(lambda ev: ev[0][0] == 'chat' and any(e == 'delta' for e, _ in ev) and ev[-1][0] == 'done' and ev[-1][1]['messageId'] and ev[-1][1]['credits']['used'] > 0)($events)" &&
		chat=$(py "$events[0][1]['chatId']")
	req GET "/api/chats/${chat:-none}"
	check "chat history saved" 200 '[m["role"] for m in j["chat"]["messages"]] == ["user", "assistant"] and j["chat"]["messages"][1]["content"] and j["chat"]["messages"][0]["createdAt"].endswith("Z")'
	req DELETE "/api/chats/${chat:-none}" '{}'
	check "delete chat" 204

	req POST /api/copilot/plan '{"symbol":"BTC","style":"swing","bias":"auto"}'
	check "copilot plan" 200 '(lambda p: p["direction"] in ("long", "short", "neutral") and p["riskReward"] > 0 and p["rationale"] and ((p["stopLoss"] < p["entry"] < p["takeProfit"]) or (p["takeProfit"] < p["entry"] < p["stopLoss"])) and p["generatedAt"].endswith("Z"))(j["plan"]) and len(j["outlook"]["buckets"]) == 7'

	draft='{}'
	req POST /api/forecasts/draft '{"idea":"Will the US Federal Reserve cut rates at its December 2026 meeting?"}'
	check "forecast draft" 200 'j["draft"]["shape"] in ("binary", "ordered", "categorical") and len(j["draft"]["outcomes"]) >= 2 and j["draft"]["resolutionDate"]' &&
		draft=$(py 'j["draft"]')
	req POST /api/forecasts "$draft"
	check "forecast create" 201 'j["forecast"]["status"] == "running" and j["forecast"]["createdAt"].endswith("Z")' &&
		fid=$(py 'j["forecast"]["id"]')
	status=running
	for _ in $(seq 1 108); do # 9 minutes: a deep run times out at 8
		req GET "/api/forecasts/${fid:-none}"
		status=$(py 'j["forecast"]["status"]' 2>/dev/null || echo error)
		[ "$status" = running ] || break
		sleep 5
	done
	check "forecast completes" 200 'j["forecast"]["status"] == "completed" and abs(sum(p["p"] for p in j["forecast"]["result"]["probabilities"]) - 1) < 1e-6 and j["forecast"]["result"]["specialists"] and j["forecast"]["completedAt"].endswith("Z") and j["forecast"]["result"]["knowledgeNote"].endswith("no live news feed.") and ("live Binance" in j["forecast"]["result"]["knowledgeNote"] or not any(w in j["forecast"]["question"].lower() for w in ("btc", "bitcoin")))'

	req POST /api/strategies/generate '{"prompt":"Buy BTC when RSI(14) drops below 30 and sell when it rises above 60, with a 4% stop.","symbol":"BTC","interval":"4h"}'
	check "strategy generate" 201 'j["strategy"]["id"] and j["strategy"]["spec"]["symbol"] == "BTC" and j["strategy"]["explanation"] and j["strategy"]["createdAt"].endswith("Z")' &&
		sid=$(py 'j["strategy"]["id"]')
	req DELETE "/api/strategies/${sid:-none}" '{}'
	check "delete strategy" 204

	req POST /api/ai-portfolios/build '{"prompt":"A defensive basket that holds up when crypto sells off"}'
	check "ai-portfolio build" 201 '3 <= len(j["portfolio"]["holdings"]) <= 6 and abs(sum(x["weight"] for x in j["portfolio"]["holdings"]) - 1) < 1e-6 and max(x["weight"] for x in j["portfolio"]["holdings"]) <= 0.6 + 1e-9 and j["portfolio"]["backtest"]["maxDrawdownPct"] >= 0' &&
		pid=$(py 'j["portfolio"]["id"]')
	req POST "/api/ai-portfolios/${pid:-none}/invest" '{"amount":1000}'
	check "ai-portfolio invest" 200 'len(j["trades"]) + len(j["skipped"]) >= 3 and all(t["source"] == "portfolio" for t in j["trades"]) and len(j["portfolio"]["positions"]) == len(j["trades"])'
	req POST /api/portfolio/reset '{}'
	check "portfolio reset after invest" 200 'j["portfolio"]["positions"] == []'
fi

# --- logout -------------------------------------------------------------------
req POST /api/auth/logout '{}'
check "logout" 204

req GET /api/me
check "401 after logout" 401 'j["error"]["code"] == "unauthorized"'

if [ "$fails" -gt 0 ]; then
	echo "$fails check(s) failed"
	exit 1
fi
echo "all checks passed"
