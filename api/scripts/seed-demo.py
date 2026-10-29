#!/usr/bin/env python3
"""Fill an account with demo content so the app doesn't open empty.

    EMAIL=demo@example.com PASSWORD=… scripts/seed-demo.py    # BASE defaults to http://localhost:8080

Resets the paper portfolio, then creates 3 forecasts, 1 strategy, 1 AI portfolio (+ a $20k
paper investment), a BTC position with stop/target, one agent chat that ends with an
unconfirmed trade card, and one AI backtest arena run (Trend Rider, Mean Reverter and News
Desk over the default daily window). Costs ~120 AI credits: ~30 (~25 model calls) plus 90
for the arena run (3 agents × 60 steps, ~400 model calls and ≤ 120 Brave calls over ~15 min,
left running: the script prints its id and does not wait). Stdlib only.
"""
import http.cookiejar, json, os, sys, time, urllib.error, urllib.request

BASE = os.environ.get("BASE", "http://localhost:8080")
jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def call(method, path, body=None, timeout=180):
    data = None if body is None and method == "GET" else json.dumps(body or {}).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with opener.open(req, timeout=timeout) as r:
            raw = r.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        sys.exit(f"{method} {path} → {e.code} {e.read().decode()[:300]}")


def chat(message):
    req = urllib.request.Request(BASE + "/api/chat/stream", method="POST",
                                 data=json.dumps({"message": message}).encode(),
                                 headers={"Content-Type": "application/json"})
    event, cards = None, 0
    with opener.open(req, timeout=300) as r:
        for line in r:
            line = line.decode().rstrip("\n")
            if line.startswith("event: "):
                event = line[7:]
            elif line.startswith("data: ") and event in ("card", "error", "done"):
                if event == "error":
                    sys.exit(f"chat error: {line[6:]}")
                cards += event == "card"
                if event == "done":
                    return cards


def forecast(idea):
    draft = call("POST", "/api/forecasts/draft", {"idea": idea})["draft"]
    f = call("POST", "/api/forecasts", draft)["forecast"]
    print(f"  forecast running: {draft['question'][:90]}")
    return f["id"]


call("POST", "/api/auth/login", {"email": os.environ["EMAIL"], "password": os.environ["PASSWORD"]})
call("POST", "/api/portfolio/reset")

print("forecasts")
ids = [forecast(i) for i in (
    "Will bitcoin close above $100k on Dec 31 2026?",
    "Where will ETH close on Dec 31 2026?",
    "Which company will be the largest by market cap at the end of 2026: Nvidia, Microsoft, Apple, Alphabet or another?",
)]

print("strategy")
s = call("POST", "/api/strategies/generate", {
    "prompt": "Buy SOL when EMA 20 crosses above EMA 50 on the 4h chart, exit on the reverse cross, 4% stop loss",
    "symbol": "SOL", "interval": "4h"})["strategy"]
print(f"  {s['name']}: total {s['metrics']['totalReturnPct']:.1f}%, sharpe {s['metrics']['sharpe']:.2f}")

print("AI portfolio")
p = call("POST", "/api/ai-portfolios/build", {"prompt": "Layer-1s with less drawdown than BTC"})["portfolio"]
inv = call("POST", f"/api/ai-portfolios/{p['id']}/invest", {"amount": 20000})
print(f"  {p['name']}: {len(inv['trades'])} paper fills")

print("BTC position")
price = next(a["price"] for a in call("GET", "/api/market/assets")["assets"] if a["symbol"] == "BTC")
call("POST", "/api/orders", {"symbol": "BTC", "side": "buy", "notional": 15000, "source": "manual",
                             "stopLoss": round(price * 0.92), "takeProfit": round(price * 1.15)})

print("agent chat")
cards = chat("Compare BTC, ETH and SOL momentum in a table, then give me one trade idea with a $2,000 size.")
print(f"  {cards} card(s)")

print("arena run")
daily = next(c for c in call("GET", "/api/arena/options")["cadences"] if c["cadence"] == "1d")
run = call("POST", "/api/arena/runs", {"cadence": "1d", "start": daily["defaultStart"], "end": daily["defaultEnd"],
                                       "agents": [{"personaId": p} for p in ("momentum", "contrarian", "macro")]})["run"]
print(f"  {run['id']}: {run['name']}, {run['steps']} steps, {run['credits']} credits, running server-side")

print("waiting for forecasts")
for fid in ids:
    for _ in range(60):
        st = call("GET", f"/api/forecasts/{fid}")["forecast"]
        if st["status"] != "running":
            break
        time.sleep(5)
    top = max(st["result"]["probabilities"], key=lambda x: x["p"]) if st.get("result") else None
    print(f"  {st['status']}: {st['question'][:70]} → {top}")

print("credits", call("GET", "/api/me")["credits"])
