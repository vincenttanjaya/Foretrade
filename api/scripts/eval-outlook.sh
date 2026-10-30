#!/usr/bin/env bash
# Manual live eval of the chat's price_outlook horizon rules, not CI:
# sends the eight phrasings of the contract to a dev API's agent chat and prints,
# for each, the horizon fields of every price_outlook call the model made and the
# card's horizon basis and view kind. Each message spends the chat's credits.
#
#   EMAIL=dev@example.test PASSWORD=... scripts/eval-outlook.sh   # API defaults to http://localhost:8080
set -euo pipefail

API=${API:-http://localhost:8080}
: "${EMAIL:?set EMAIL}" "${PASSWORD:?set PASSWORD}"
command -v python3 >/dev/null || {
	echo "eval-outlook: python3 is required" >&2
	exit 2
}

jar=$(mktemp)
out=$(mktemp)
trap 'rm -f "$jar" "$out"' EXIT
curl -fsS -c "$jar" -H 'Content-Type: application/json' \
	-d "$(python3 -c 'import json,os; print(json.dumps({"email": os.environ["EMAIL"], "password": os.environ["PASSWORD"]}))')" \
	"$API/api/auth/login" >/dev/null

phrasings=(
	"can TSMC 10x, if yes by when"
	"where will TSM be in 5 years?"
	"TSM price by end of 2031"
	"how long until NVDA doubles?"
	"BTC next 3 months"
	"is SOL a good long-term hold?"
	"ETH this week"
	"could AMD be a 5-bagger?"
)
for q in "${phrasings[@]}"; do
	curl -fsS -N -b "$jar" -H 'Content-Type: application/json' \
		-d "$(python3 -c 'import json,sys; print(json.dumps({"message": sys.argv[1]}))' "$q")" \
		"$API/api/chat/stream" >"$out"
	python3 - "$q" "$out" <<'EOF'
import json, sys
q, path = sys.argv[1], sys.argv[2]
event, calls, cards = None, [], []
for line in open(path, encoding="utf-8"):
    line = line.rstrip("\n")
    if line.startswith("event: "):
        event = line[7:]
    elif line.startswith("data: "):
        data = json.loads(line[6:])
        if event == "tool_call" and data.get("name") == "price_outlook":
            i = data.get("input") or {}
            if isinstance(i, str):
                i = json.loads(i or "{}")
            calls.append({k: i.get(k) for k in ("symbol", "horizon_days", "horizon_reason", "target_multiple")} | {"scenarios": len(i.get("scenarios") or [])})
        elif event == "card" and data.get("type") == "price_outlook":
            d = data["data"]
            cards.append({"horizon": d.get("horizon"), "view": (d.get("view") or {}).get("kind"), "timeToTarget": d.get("timeToTarget") is not None})
print(json.dumps({"question": q, "calls": calls, "cards": cards}, indent=1))
EOF
done
