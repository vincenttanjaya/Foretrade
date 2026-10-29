# api

The Go backend of **Foretrade**, a self-hosted AI paper-trading workspace: the agent chat and trade copilot, the
strategy studio with backtests and autopilots, the forecast desk, AI portfolios, the backtest and live arenas, alerts,
the signals board and the Asia screener, plus the admin console's API. The same process can also serve the built web
app and admin console (see "Serving" in the contract).

**Paper trading only.** Orders fill in a simulated Postgres ledger against public market data (Binance, Hyperliquid,
Yahoo Finance and optional real-time exchange feeds) and never reach a real venue.

- HTTP contract: [`docs/API-CONTRACT.md`](../docs/API-CONTRACT.md)
- Package layout and internal Go API: [`docs/BACKEND-LAYOUT.md`](../docs/BACKEND-LAYOUT.md)
- Stack: Go 1.27 (`go.mod`), stdlib `net/http`, pgx v5, bcrypt, Postgres. AI goes through `internal/llm`: 13 provider
  presets (OpenAI, a ChatGPT plan, Anthropic, Amazon Bedrock, OpenRouter, Gemini, Groq, DeepSeek, xAI, Mistral,
  Ollama, LM Studio, any OpenAI-compatible server). `internal/aiconfig` stores the admin console's choice, with
  secrets encrypted under `SECRET_KEY`. Setting `AI_PROVIDER` in the environment overrides it.

## Layout

```
cmd/api              the server: config → pool → migrations → demo user + bootstrap admin → routes; serve.go serves the SPAs
cmd/api/integration* integration suite: boots the server on real Postgres with local upstream fakes
cmd/genlisted        regenerates internal/market/listed.json from the screener's constituents
cmd/forecast-replay  regenerates the forecast desk's replay seed (hand-run: makes real model calls)
internal/…           feature packages (auth, market, paper, research, chat, forecast, strategy, arena, alerts,
                     screener, admin, llm, aiconfig, …); docs/BACKEND-LAYOUT.md describes each
scripts/             smoke.sh (end-to-end checks), seed-demo.py (demo content), eval-outlook.sh (manual chat eval)
ci/                  Go test-result checker that fails on skipped integration tests, with its own tests
```

## Run locally

The quickest path is Docker from the repository root (`scripts/setup.sh`, then `docker compose up -d --build`). To run the
API on its own you need Go 1.27 and Postgres (Docker and CI use 18). One way to get a database that matches
`.env.example` here:

```sh
docker run -d --name foretrade-db -p 127.0.0.1:55432:5432 \
  -e POSTGRES_USER=trade -e POSTGRES_DB=trade -e POSTGRES_HOST_AUTH_METHOD=trust \
  postgres:18-alpine
```

Then, from `api/`:

```sh
cp .env.example .env      # set SECRET_KEY (openssl rand -hex 32); check DATABASE_URL and the accounts
set -a; . ./.env; set +a  # the API reads plain environment variables, not .env itself
go run ./cmd/api          # listens on :8080
```

`api/.env.example` is the short local version. The Docker settings, with comments, are in the root
[`.env.example`](../.env.example); every variable the API reads is in the contract's §5.

At boot the API runs its migrations and ensures the bootstrap admin (`ADMIN_EMAIL`, `ADMIN_PASSWORD`, 12+ characters)
and, when `DEMO_EMAIL` and `DEMO_PASSWORD` are set, the demo user. AI stays off until a provider is configured, in
the admin console's Settings page or through the `AI_*` variables. News and web search need a Brave Search key;
without one the AI answers without them. Keep `LIVE_ARENA=off` unless you want the shared live arena making model
calls every four hours.

## Tests

From `api/`, with the database above:

```sh
gofmt -l . && go vet ./...
go test ./...                                                     # pure logic; database tests skip
docker exec foretrade-db createdb -U trade trade_test
export DATABASE_URL_TEST='postgres://trade@localhost:55432/trade_test?sslmode=disable'
go test -race ./...                                               # everything except the integration suite
export INTEGRATION_DATABASE_URL=$DATABASE_URL_TEST
go test -race -count=1 ./cmd/api/                                 # + the integration suite (a few minutes)
go test -race -json -count=1 ./cmd/api/ | python3 ci/check_go_results.py -   # fails on any skipped test
python3 -m unittest discover -s ci -p 'test_*.py'                 # the checker's own tests
```

Tests make no network calls: the model, market data, search and crawling are local fakes, and each database test
runs in a throwaway schema. The one live model test, in `internal/llm`, skips unless `LIVE_BEDROCK=1`. A new
`TestIntegration*` test must be added to `REQUIRED_TESTS` in `ci/check_go_results.py` (a checker test enforces it).

## Scripts

```sh
EMAIL=demo@example.com PASSWORD='…' scripts/smoke.sh          # end-to-end checks against BASE (default :8080), no AI calls
AI=1 EMAIL=demo@example.com PASSWORD='…' scripts/smoke.sh     # + the AI endpoints (spends AI credits)
EMAIL=demo@example.com PASSWORD='…' scripts/seed-demo.py      # fill an account with demo content
go run ./cmd/genlisted build                                  # rebuild listed.json offline from its own measurements
```

`smoke.sh` resets the account's paper portfolio and expects a non-admin account. `ORIGIN` and `ADMIN_ORIGIN`
(defaults `http://localhost:5173` and `:5174`) must be in the server's `ALLOWED_ORIGINS`, and `ADMIN_ORIGIN` in
`ADMIN_ORIGINS` too.
