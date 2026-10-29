# Foretrade

[![CI](https://github.com/vincenttanjaya/Foretrade/actions/workflows/ci.yml/badge.svg)](https://github.com/vincenttanjaya/Foretrade/actions/workflows/ci.yml)

A self-hosted AI desk for trading and forecasting markets, on paper money.

Foretrade is a web app you run on your own machine or server. You chat with an AI that researches the market and
proposes paper trades, then you confirm them. Around the chat sit a trade terminal, a forecast desk, a strategy
studio with backtests, AI-run arenas, AI portfolios and automations. Every order fills in a simulated ledger in
Postgres. You choose the AI: a cloud API key, a local model, or your own ChatGPT plan.

What is in the app:

- **Agent chat.** The AI reads live prices, news and your paper portfolio. You can attach files and images. Replies
  end in cards you confirm: a paper order, a price alert, a stop-loss or take-profit, a forecast, a strategy
  backtest or a portfolio.
- **Trade terminal.** Charts, an order ticket, positions and open orders, and an AI copilot that drafts a trade plan.
  Go computes the analysis and checks the levels; the model picks the direction and writes the plan.
- **Forecast desk.** A rough question becomes a resolvable one. Three specialist agents and an aggregator turn it
  into a probability with an audit trail, set next to Polymarket odds when a matching market exists.
- **Strategy studio.** Describe a strategy in plain words. The AI writes it as a JSON rule spec (never code), Go
  backtests it on candles, tracks how it does going forward, and can run it on your paper account (autopilot).
  Strategies can be shared with other users.
- **Arena.** AI personas trade a past window bar by bar, seeing only bars that had closed, and are scored against
  benchmarks. An optional live arena runs the personas on a shared paper desk, and users can copy them.
- **AI portfolios.** Describe a portfolio in one sentence. The AI picks a weighted basket, Go backtests it against
  BTC or the S&P 500, and you can invest it on paper.
- **Automations.** Price, prediction-market odds and drawdown triggers; daily or weekly briefs and recurring buys;
  scheduled research. Notifications go to an in-app bell, and optionally to email or a Lark bot.
- **Market views.** A news wire with an AI market brief, a momentum signals board, and an Asia stock screener.
- **Admin console.** Accounts, daily AI credits, usage, system health, an audit log, and the AI provider settings.

There is no public sign-up. The admin creates accounts in the admin console.

## Disclaimer

- **Paper trading only.** Foretrade never connects to a broker or exchange account and never places a real order.
  All fills are simulated.
- **Not financial advice.** The AI can be wrong. Nothing the app shows is a recommendation to buy or sell anything.
- **Market data may be delayed or wrong.** Prices come from free public sources (Binance, Hyperliquid, Yahoo Finance,
  Polymarket). Many stock quotes are delayed.

## Quickstart (Docker)

You need git and Docker with the Compose plugin. On Windows, run the commands in WSL.

```bash
git clone https://github.com/vincenttanjaya/Foretrade.git
cd Foretrade
./scripts/setup.sh
docker compose up -d --build --wait
```

`scripts/setup.sh` copies `.env.example` to `.env` and fills in the required values: a Postgres password, a
`SECRET_KEY`, and the admin login (`admin@example.com` and a random password). It prints the URLs and the admin
login when it is done. Running it again never changes a value that is already set.

From a source checkout, always pass `--build`. Without it, compose reuses an image it already has, which may not
match your code.

Then:

| What | URL | Who can reach it |
|---|---|---|
| Web app | http://localhost:8080 | every network interface of the host (`WEB_PORT`) |
| Admin console | http://localhost:8081 | this machine only: published on 127.0.0.1 (`ADMIN_PORT`) |

You can sign in only at an origin listed in `ALLOWED_ORIGINS`, which defaults to localhost and 127.0.0.1. To open
the web app from another machine, set `ALLOWED_ORIGINS` in `.env` to the URL you use and keep the localhost entries
for the console, e.g. `ALLOWED_ORIGINS=http://203.0.113.5:8080,http://localhost:8081,http://127.0.0.1:8081`. Set
`APP_URL` to that URL too, then run `docker compose up -d --build`.

1. Open the admin console and sign in with the login `setup.sh` printed (it is also in `.env`).
2. Go to **Settings** and pick an AI provider. Enter a key or a local server URL, choose a model, then **Test
   connection** and **Save**.
3. Open the web app and sign in with the same account, or create accounts for other people under **Users**.

On a remote server, reach the console through an SSH tunnel:

```bash
ssh -L 8081:127.0.0.1:8081 your-server   # then open http://localhost:8081
```

Day to day:

```bash
docker compose up -d          # apply changes to .env
docker compose logs -f app    # follow the server log
docker compose down           # stop (the data volume stays)
docker compose down -v        # stop and delete all data
```

## AI providers

Pick one in the admin console. The presets are defined in `api/internal/llm/settings.go`.

| Provider | ID | What you need | API it speaks |
|---|---|---|---|
| OpenAI (API key) | `openai` | API key | Responses |
| ChatGPT plan (sign in with ChatGPT) | `chatgpt` | Sign-in from the admin console | Responses |
| Anthropic Claude (API key) | `anthropic` | API key | Messages |
| Amazon Bedrock (AWS credentials) | `bedrock` | AWS credentials and a region on the server | Converse |
| OpenRouter (many models, one key) | `openrouter` | API key | Chat Completions |
| Google Gemini (API key) | `gemini` | API key | Chat Completions |
| Groq | `groq` | API key | Chat Completions |
| DeepSeek | `deepseek` | API key | Chat Completions |
| xAI Grok | `xai` | API key | Chat Completions |
| Mistral AI | `mistral` | API key | Chat Completions |
| Ollama (local models) | `ollama` | A running Ollama server | Chat Completions |
| LM Studio (local models) | `lmstudio` | LM Studio's local server | Chat Completions |
| Other OpenAI-compatible server | `custom` | A base URL (vLLM, llama.cpp, LiteLLM, ...); a key if it needs one | Chat Completions |

The console's **Load models** button lists the provider's models. You can also type a model id. The agent uses tool
calling, so pick a model that supports it.

### Two ways to configure

- **Admin console (default).** The **AI provider** card under Settings. Saving runs a test call first and only
  saves if it works. API keys are stored in Postgres, encrypted with AES-256-GCM under `SECRET_KEY`. The API never
  returns a stored key; the console shows at most its last four characters. Changes apply at once, without a restart.
- **Environment.** Set `AI_PROVIDER` and `AI_MODEL` (and `AI_API_KEY`, `AI_BASE_URL`, `AI_EFFORT`,
  `MODEL_KNOWLEDGE_CUTOFF` as needed) in `.env`. `AI_MODEL` is required unless the provider has a default model
  (`anthropic`, `bedrock`, `deepseek`, `mistral`). These variables are read only when `AI_PROVIDER` is set. The
  environment then controls the whole AI config, and the console shows it read-only. Instead of `AI_API_KEY` you
  can use the provider's own variable, such as `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` (the full list is in
  `.env.example`).

### ChatGPT plan

Foretrade uses OpenAI's official "Sign in with ChatGPT" token sharing for open-source and self-hosted apps (OAuth 2
with PKCE). In Settings > AI provider, choose **ChatGPT plan (sign in with ChatGPT)**, click **Sign in with
ChatGPT** and approve in the OpenAI tab. Back in the console, click **Load models**, pick a model and click **Save**.
AI calls then run on the signed-in person's ChatGPT plan.

- **Owner-only mode.** A ChatGPT plan belongs to one person. While this provider is active, only admins can use AI
  features; other users get an "owner-only mode" message. Background AI (the news brief, signal rationales and, if
  `LIVE_ARENA=on`, the live arena) still runs on the plan, and every user sees its output. To share AI with other
  users, use an API key provider.
- **Open the console on a loopback address.** The sign-in returns to `http://127.0.0.1:<admin port>/auth/callback`,
  so the console must be open at `http://localhost:<port>` or `http://127.0.0.1:<port>`. From another machine, use
  the SSH tunnel above with the same port on both sides.
- **Tokens stay on the server.** They are stored encrypted under `SECRET_KEY`, refreshed by the server, and revoked
  when you sign out. Signing in needs `SECRET_KEY` to be set.

### Local models

Ollama and LM Studio run on the host, outside the container. Their default URLs in the presets,
`http://host.docker.internal:11434/v1` and `http://host.docker.internal:1234/v1`, reach the host from Docker.
Without Docker, use `http://localhost:11434/v1` or `http://localhost:1234/v1`.

On Linux, the server must listen beyond 127.0.0.1 for the container to reach it. Start Ollama with
`OLLAMA_HOST=0.0.0.0`; in LM Studio, turn on serving on the local network. Firewall that port if the machine is
reachable from the internet.

## Configuration

`.env` holds the settings for Docker Compose. These are the main ones; `.env.example` documents them, and
[docs/API-CONTRACT.md](docs/API-CONTRACT.md) §5 lists every variable the API reads.

| Variable | Default (compose) | Notes |
|---|---|---|
| `POSTGRES_PASSWORD` | set by `setup.sh` | Fixed when the database volume is first created. |
| `SECRET_KEY` | set by `setup.sh` | 64 hex characters. Encrypts stored keys and the ChatGPT sign-in. If it changes, re-enter them. |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | `admin@example.com` / random | The bootstrap admin. The password must be 12 characters or more. |
| `AI_PROVIDER`, `AI_MODEL`, `AI_API_KEY`, ... | unset | Set to configure AI from the environment (see above). |
| `WEB_PORT` | `8080` | Host port of the web app, on every interface. |
| `ADMIN_PORT` | `8081` | Host port of the admin console, on 127.0.0.1 only. |
| `ALLOWED_ORIGINS` | localhost and 127.0.0.1 on `WEB_PORT` and `ADMIN_PORT` | Browser origins the API accepts. Set it when you open the app at any other host than localhost. |
| `ADMIN_ORIGINS` | localhost and 127.0.0.1 on `ADMIN_PORT` | The only origins the admin API answers. |
| `APP_URL` | `http://localhost:<WEB_PORT>` | Public URL of the web app. Built into the admin image: run `docker compose up -d --build` after changing it or `WEB_PORT`. |
| `COOKIE_SECURE` | `false` | Set `true` once the app is served over HTTPS. |
| `AI_DAILY_CREDITS` | `500` | AI credits per user per day. The admin can override it per user. |
| `BRAVE_API_KEY` | unset | Brave Search key for news and web search. Without it, search is off. |
| `CRAWL_BASE` / `CRAWL_TOKEN` | unset | A crawl4ai server and its API token for reading web pages; both are required. Without them, pages are fetched directly. |
| `LIVE_ARENA` | `off` | `on` runs the live arena, which calls the model on a schedule. |
| `QUOTES_REALTIME` | unset (none) | Real-time quotes for Asian exchanges: any of `krx,hkex,hose,sgx,twse`. |
| `DEMO_EMAIL` / `DEMO_PASSWORD` | unset | A demo account, created at boot. |
| `NOTIFY_EMAIL_FROM` | unset (off) | Sender for notification emails through Amazon SES. Needs AWS credentials. |

The search settings can also be set in the admin console (Settings > Search). A value saved there wins over the
environment.

`QUOTES_REALTIME` is off by default. Each source reads public exchange or portal feeds, and their terms may not
allow automated use. Check them and decide for yourself before you turn one on. Without it, those stocks use
delayed Yahoo Finance quotes.

Outside Docker, a few defaults differ (see `api/internal/config/config.go`): `ALLOWED_ORIGINS` defaults to the two
dev servers (`http://localhost:5173,http://localhost:5174`), `ADMIN_ORIGINS` to `http://localhost:5174`, and
`LIVE_ARENA` is on unless set to `off`.

## Local development

Prerequisites:

- Go 1.27 (`api/go.mod`)
- Node 20.20.2 (`web/.node-version`, `admin/.node-version`)
- Postgres (compose and CI use 18). The commands below run it in Docker.

**1. Postgres** on 127.0.0.1:55432, the address `api/.env.example` uses. Trust auth is fine here because the port
is bound to loopback.

```bash
docker run -d --name foretrade-db -p 127.0.0.1:55432:5432 \
  -e POSTGRES_USER=trade -e POSTGRES_DB=trade -e POSTGRES_HOST_AUTH_METHOD=trust \
  postgres:18-alpine
```

**2. API** on http://localhost:8080. It runs its migrations at boot and creates the demo user and the bootstrap
admin from `.env`.

```bash
cd api
cp .env.example .env        # set SECRET_KEY: openssl rand -hex 32
set -a; . ./.env; set +a    # the API reads environment variables, not .env itself
go run ./cmd/api
```

**3. Web app and admin console**, each in its own terminal:

```bash
cd web && npm ci && npm run dev      # http://localhost:5173, sign in as the demo user
cd admin && npm ci && npm run dev    # http://localhost:5174, sign in as the admin
```

Both dev servers call the API at `http://localhost:8080` by default (`VITE_API_URL`). The admin dev server proxies
`/auth` to the API, so ChatGPT sign-in works in development too.

### Tests

```bash
cd api
gofmt -l . && go vet ./...
go test ./...                                    # database tests skip without DATABASE_URL_TEST

docker exec foretrade-db createdb -U trade trade_test
export DATABASE_URL_TEST='postgres://trade@localhost:55432/trade_test?sslmode=disable'
go test -race ./...                              # everything except the integration suite
INTEGRATION_DATABASE_URL=$DATABASE_URL_TEST go test -race -count=1 ./cmd/api/   # the integration suite
```

Tests make no network calls: the model, market data, search and crawling are local fakes, and each database test
runs in its own throwaway schema.

```bash
cd web    # or admin
npm run typecheck
npm test                         # Vitest
npm run test:coverage            # with the coverage thresholds CI enforces
npx playwright install chromium  # once
npm run e2e                      # Playwright against e2e/mock-api.ts
```

## Architecture

- **API.** Go 1.27 on the standard library's `net/http`, with pgx v5 and Postgres. SQL migrations are embedded and
  run at boot. Sessions use an HttpOnly cookie; passwords are hashed with bcrypt.
- **Frontends.** Two single-page apps, `web` (the trading app) and `admin` (the console): React 19, Vite,
  TypeScript, Tailwind CSS v4 and react-router 7.
- **One image, two ports.** The Go server serves both SPAs and the API. Port 8080 serves the web app and the API,
  and answers 404 for `/api/admin/*` and `/auth/*`. Port 8081 serves the admin console, the whole API and the
  ChatGPT sign-in callback. Without `ADMIN_PORT`, `PORT` serves the whole API and the web app (`WEB_DIR`); the
  admin console's files are not served (`api/cmd/api/serve.go`).
- **`internal/llm`.** One client the rest of the code calls, with four wires behind it: three written by hand on
  `net/http` with a shared SSE reader (OpenAI Chat Completions for most presets, Anthropic Messages, and OpenAI
  Responses for OpenAI keys and ChatGPT sign-in), and Bedrock Converse through the AWS SDK. A settings change
  swaps the provider atomically; calls already running finish on the old one.
- **`internal/aiconfig`.** The stored AI and search settings, their encryption, the environment's precedence, the
  ChatGPT sign-in, and the admin settings endpoints.
- **Background jobs.** Goroutines in the API process: the paper stop-loss and take-profit watcher, alerts and
  automations, the news wire, scheduled research, the signals board, forecast and arena recovery, the live arena,
  autopilots, the Asia screener, and quote feeds. While no provider is configured, the signals board, scheduled
  research and the live arena skip their model calls, and the news wire refreshes without AI.

```text
api/
  cmd/api/              server: config, migrations, routes, background jobs, SPA serving
  cmd/forecast-replay/  tool: regenerates the forecast desk's replay seed
  cmd/genlisted/        tool: regenerates the listed-stock universe
  internal/llm/         AI client, provider presets, agent loop
  internal/aiconfig/    AI and search settings, ChatGPT sign-in
  internal/...          features: paper, market, research, chat, copilot, forecast, strategy,
                        arena, aipf, autopilot, alerts, signals, screener, admin, auth, usage
  internal/store/       Postgres pool and embedded migrations
web/                    the trading app
admin/                  the admin console
docs/                   API contract and code layout
scripts/setup.sh        writes .env for Docker Compose
Dockerfile, compose.yaml
.github/workflows/      CI, and a release workflow that publishes ghcr.io/vincenttanjaya/foretrade on v* tags
```

More detail:

- [docs/API-CONTRACT.md](docs/API-CONTRACT.md): the HTTP API, the data model and every environment variable
- [docs/BACKEND-LAYOUT.md](docs/BACKEND-LAYOUT.md): the Go packages
- [docs/FRONTEND-LAYOUT.md](docs/FRONTEND-LAYOUT.md): the SPAs' layout and design tokens

## Security

- The admin console is published on 127.0.0.1 only. Keep it that way, or put it behind a VPN or an authenticating
  proxy.
- Keep `.env` private. It holds `SECRET_KEY`, the admin password and any provider keys.
- Set `COOKIE_SECURE=true` once you serve the app over HTTPS.
- Stored keys and the ChatGPT sign-in are encrypted under `SECRET_KEY`. The API never returns them.

See [SECURITY.md](SECURITY.md) for deployment guidance and how to report a vulnerability.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, the checks CI runs, and the
code style.

## License

[MIT](LICENSE) © 2026 Vincent Tanjaya
