# Frontend layout & design system (`web`)

Vite + React 19 + TypeScript (strict) + Tailwind v4 (`@tailwindcss/vite`) + react-router 7 (`react-router`
package, data-less `<BrowserRouter>`), `lightweight-charts` v5, `react-markdown` + `remark-gfm`.
Node 24 (`.node-version` pins 24.22.2).
API base: `import.meta.env.VITE_API_URL` (dev default `http://localhost:8080`; the Docker image builds with `/`, since
the Go server serves the app and the API on one origin).
HTTP contract: [API-CONTRACT.md](API-CONTRACT.md).

## Product framing
"Foretrade" — one workspace with an AI **Agent** (research chat that ends in a confirmable trade card),
a **Trade** terminal with **Copilot** (Ask Long / Ask Short → plan card → pre-filled paper ticket), a
**Forecast desk** (rough idea → AI-drafted question → probability estimates from three specialist
agents + aggregator), a **Strategy studio** (one sentence → rule spec → backtest, plus a "Top strategies"
leaderboard), **AI portfolios** (one sentence → weighted basket + actual-window backtest → Invest), and a paper
**Account**. The web app's copy never names other products or AI vendors/models: it says "AI".
Persistent trust cues: a "PAPER TRADING" badge in the top bar and a small "Simulated · not financial advice"
line on every AI card.

The forecast specialists are personas on one configured model, not separate models. Outcome resolution,
Brier scoring and empirical calibration are not implemented; describe the output as probability estimates.

## Visual direction
Editorial-fintech: warm paper light theme by default (dark theme toggle), serif display headlines, mono
uppercase micro-labels with letter-spacing (`CREDIT BALANCE · DAILY`), tabular numbers, generous whitespace,
cards with 1px borders and soft radius, one gold accent, green/red only for gains/losses (always paired with ▲/▼).

Tokens (CSS variables on `:root` / `[data-theme="dark"]`, exposed to Tailwind via `@theme inline`):

| token | light | dark | use |
|---|---|---|---|
| `--bg` | `#F7F5F0` | `#0B0D12` | page |
| `--surface` | `#FFFFFF` | `#12151C` | cards |
| `--surface-2` | `#F1EEE6` | `#181C25` | inset, hover, table header |
| `--border` | `#E6E1D6` | `#252A35` | hairlines |
| `--text` | `#14161B` | `#ECEDEF` | primary text |
| `--muted` | `#62666F` | `#8B91A0` | secondary text |
| `--ink` | `#0F1B2D` | `#ECEDEF` | primary button bg / hero card bg |
| `--ink-fg` | `#FFFFFF` | `#0B0D12` | text on ink |
| `--accent` | `#C8962E` | `#E0B252` | gold accent fills |
| `--accent-ink` | `#8A6516` | `#E0B252` | gold text, links, focus ring (AA on light surfaces) |
| `--accent-soft` | `#F6ECD6` | `#2A2414` | accent backgrounds |
| `--up` | `#15803D` | `#22C55E` | gains |
| `--down` | `#DC2626` | `#EF4444` | losses |

Tailwind utility names: `bg-bg bg-surface bg-surface-2 border-border text-text text-muted bg-ink text-ink-fg
text-accent bg-accent-soft text-up text-down`. Fonts (Google Fonts in `index.html`): `font-display` =
"Newsreader" (headlines, big numbers on hero cards), `font-sans` = "Inter" (UI), `font-mono` =
"JetBrains Mono" (micro-labels, tickers, prices; use `tabular-nums`). Theme is stored in localStorage
(try/catch) and applied as `data-theme` on `<html>`; default follows `prefers-color-scheme`.

## App shell
- Left icon rail (76 px, labels under icons, active item highlighted): Agent `/`, Trade `/trade/BTC`, Screener
  `/screener` (title "Asia screener"), News `/news`, Signals `/signals`, Automations `/automations`, Forecasts
  `/forecasts`, Strategies `/strategies`, Portfolios `/portfolios`, Account `/account`.
  Logo mark at top (three gold candlesticks in a gold-bordered navy square, the favicon's mark; "FORETRADE" under it on
  wide screens). The top bar reads "Foretrade / <page title>"; `document.title` is "<page title> · Foretrade".
  On < 768 px the rail becomes a bottom tab bar of eight tabs (`grid-cols-8`; labels go screen-reader-only under
  360 px): Signals and Automations are rail-only (Signals is linked from News there, Automations from the bell).
- Top bar (56 px): page title slot (left), then right side: `PAPER TRADING` badge, equity pill
  (`$100,000.00` + ▲/▼ total P&L %, links to /account), credits pill (`● 482 cr`, tooltip "Daily AI credits,
  resets 00:00 UTC"), theme toggle, user menu (name/email, Log out).
- Content area scrolls; pages set their own max-width.
- Toasts: a minimal `toast(msg, kind)` provider for success/error.
- The top bar's **notifications bell**
  (`NotificationsBell.tsx`, desktop and mobile) polls `GET /api/notifications` every 30 s and on focus, swallows errors
  (no badge, no toast), badge = unread (99+); its dropdown lists 20 items (kind icon, title, body, time, link; an
  `alert` item adds "Ask the agent"), POSTs `/seen` on open and links "All automations →" (the mobile way in).

## Shared code
```
src/main.tsx, src/App.tsx            routes (below), providers, auth guard (redirect to /login)
src/index.css                        tailwind import, tokens, base styles, .card/.label helpers
src/lib/types.ts                     ALL contract types (User, Credits, Asset, Candle, Analysis, PriceOutlook, Position,
                                     Portfolio, Trade, Card, ChatMessage, Plan, ForecastDraft, Forecast, ForecastSummary,
                                     StrategySpec, Strategy, StrategySummary, Metrics, Point, AIPortfolio, …)
src/lib/api.ts                       ApiError {status, code, message}; one typed function per endpoint, e.g.
                                     api.login, api.logout, api.me, api.assets, api.candles, api.analysis, api.portfolio,
                                     api.trades, api.placeOrder, api.closePosition, api.resetPortfolio, api.chats, api.chat,
                                     api.deleteChat, api.copilotPlan, api.draftForecast, api.createForecast, api.forecasts,
                                     api.forecast, api.deleteForecast, api.generateStrategy, api.backtest, api.strategies,
                                     api.strategy, api.deleteStrategy, api.buildPortfolio, api.aiPortfolios, api.aiPortfolio,
                                     api.investPortfolio;
                                     streamChat(body, onEvent: (ev: ChatStreamEvent) => void, signal?) — fetch POST +
                                     ReadableStream SSE parser (handles chunk boundaries); ChatStreamEvent is a
                                     discriminated union {event:'chat'|'tool_call'|'tool_result'|'delta'|'card'|'done'|'error', data}
                                     401 anywhere → AuthProvider sends the user to /login.
src/lib/format.ts                    fmtUsd(n, opts?), fmtPrice(n) (precision by magnitude: ≥1000 → 2dp with commas, ≥1 → 2–4dp,
                                     <1 → 4–6 significant), fmtPct(n, {sign}), fmtCompact(n), fmtProb(p) ("62%"), timeAgo(iso),
                                     fmtDate(iso)
src/lib/auth.tsx                     AuthProvider/useAuth(): { user, credits, setCredits, refresh, login, logout }
src/lib/portfolio.tsx                PortfolioProvider/usePortfolio(): { portfolio, refresh } (polls 30 s)
src/lib/assets.tsx                   AssetsProvider/useAssets(): { assets, bySymbol } (polls /api/market/assets every 10 s)
src/lib/theme.tsx                    ThemeProvider/useTheme()
src/components/ui/Button.tsx         variants: primary (ink), secondary (outline), ghost, accent (gold), danger; sizes sm/md/lg; loading
src/components/ui/Card.tsx           Card, CardHeader (title + optional mono label + actions)
src/components/ui/Badge.tsx          Badge (neutral/accent/up/down/outline)
src/components/ui/Tabs.tsx           segmented Tabs<T extends string>({value, onChange, items})
src/components/ui/Input.tsx          Input, Textarea (auto-grow), Select, Label (mono micro-label)
src/components/ui/Spinner.tsx        Spinner, Skeleton
src/components/ui/EmptyState.tsx
src/components/ui/Stat.tsx           Stat({label, value, sub, tone})
src/components/ui/PriceChange.tsx    ▲ 2.31% / ▼ 1.02% coloured
src/components/ui/ProbabilityBars.tsx  horizontal outcome bars: label · bar · % (highlight top outcome in accent);
                                     props {items: {label, p}[], compact?}
src/components/ui/Sparkline.tsx      tiny SVG line, coloured by first→last
src/components/ui/LineChart.tsx      lightweight-charts wrapper: series [{name, points: Point[], color?}], theme-aware, resizes
src/components/ui/AssetIcon.tsx      bundled logo per symbol from src/assets/{coins,stocks} (provenance in each LICENSE.md),
                                     else a monogram from the name
src/components/ui/Disclaimer.tsx     "Simulated · not financial advice"
src/components/ui/Markdown.tsx       react-markdown + remark-gfm with styled tables/lists/code
src/components/layout/AppShell.tsx   rail + top bar + <Outlet/>; PageHeader({title, subtitle, actions}) export
src/components/toast.tsx             ToastProvider/useToast()
src/pages/LoginPage.tsx              split layout: left editorial hero ("Trade with an AI desk." + three feature bullets),
                                     right login card (email, password, Sign in); errors inline
```

## Routes
| route | component (file) |
|---|---|
| `/login` | `LoginPage` (`src/pages/LoginPage.tsx`) |
| `/`, `/chat/:chatId` | `AgentPage` (`src/pages/agent/AgentPage.tsx`) |
| `/trade` → redirect `/trade/BTC`, `/trade/:symbol` | `TradePage` (`src/pages/trade/TradePage.tsx`) |
| `/account` | `AccountPage` (`src/pages/account/AccountPage.tsx`) |
| `/forecasts`, `/forecasts/new`, `/forecasts/:id` | `ForecastsPage`, `NewForecastPage`, `ForecastDetailPage` (`src/pages/forecasts/*.tsx`) |
| `/strategies`, `/strategies/:id` | `StrategiesPage`, `StrategyDetailPage` (`src/pages/strategies/*.tsx`) |
| `/portfolios`, `/portfolios/:id` | `PortfoliosPage`, `PortfolioDetailPage` (`src/pages/portfolios/*.tsx`) |
| `/automations` (`?symbol=SOL` preselects; `?create=drawdown&symbol=SOL` opens the Drawdown form) | `AutomationsPage` (`src/pages/automations/AutomationsPage.tsx`) |
| `/screener` (`?filter=&market=&q=&sort=`) | `ScreenerPage` (`src/pages/screener/ScreenerPage.tsx`) |
| `/news` (the market wire and brief, by region) | `NewsPage` (`src/pages/news/NewsPage.tsx`) |
| `/signals` (the shared signals board, a momentum screen: its exits and track record) | `SignalsPage` (`src/pages/signals/SignalsPage.tsx`) |
| `/strategies/arena/:id` (one AI backtest arena run) | `ArenaRunPage` (`src/pages/arena/ArenaRunPage.tsx`) |

Each area keeps its files in its own `src/pages/<area>/` folder and imports the shared code above.
After AI calls, pages call `setCredits(res.credits)`; after orders, `usePortfolio().refresh()`.

## Page specs (summary)
- **Agent** — Empty state: serif headline "Which market is on your mind?", composer (textarea, ⌘/Ctrl+Enter
  to send; Answer mode and Answer lens selects, Lens: Auto / Investor / Trader, kept in localStorage and sent as
  `mode` and `lens` with every message), prompt chips by tab (Trending / Crypto / Macro / Trade ideas). Left panel: chat history list +
  New chat. Conversation: user bubbles right, assistant markdown left; live tool-call chips ("Analyzing BTC (4h)" →
  ✓ summary); cards rendered inline (`trade_proposal` with Confirm (paper) → api.placeOrder source "agent" and
  Edit amount; `price_outlook` with ProbabilityBars + p5/p50/p95). Stop button aborts the stream.
  `ActionCards.tsx`: `forecast_draft` (Run the desk · 5 cr → running, polled every 3 s → headline + Open
  report; Open Forecast desk carries the draft) and `build_action` (Build portfolio / Generate & backtest · 2 cr →
  result inline with Open & invest / Open in Studio); trade cards show the desk's stance badge (agrees / leans against
  / split); the sources header names the provenance (market wire / live search / both); a Portfolio prompt tab and a
  "Market now" movers row on the empty state (`src/pages/agent/Welcome.tsx`).
- **Trade** — Asset header (icon, name, price, 24h change/high/low/volume), interval tabs, candlestick chart
  (lightweight-charts; crypto live last candle from Binance WS `wss://data-stream.binance.vision/ws/<pair>@kline_<iv>`,
  graceful if blocked; stock/index candles use abortable visible 30s API polling, with at most one flight). Header/order
  price is the venue mark, distinct from a candle close. Stock/index groups show Perp and known US session or unknown;
  unavailable range bounds render unavailable using range24hSource. Market list includes the full 245 configured assets. Quote-feed stocks (venue `yahoo`; rows in `src/pages/strategies/quoteFeeds.ts`, spread into `SYMBOLS` after the perps) show a Quote feed badge, their listing's session, "vs prev. close" and Session high/low/volume, and on the header and ticket a note that fills use the last close outside the listing's hours, plus the Yahoo FX rate for a non-USD listing; HSDT groups under US, the ADRs and local listings under Asia. Right panel tabs **Copilot** (style Scalp/Day/Swing, Ask Long / Ask
  Short / Auto → plan card: direction, entry/SL/TP, R:R, confidence meter, rationale bullets, invalidation,
  price-outlook ProbabilityBars → "Use plan" pre-fills the ticket) and **Manual** (buy/sell, USD amount with
  25/50/100% of buying power, optional SL/TP, Confirm (paper)). Bottom: Positions (close button) / Trade history.
- **Automations** — create form (asset, rises above / falls below, price with its % from live, Notify me / Buy /
  Sell with amount and optional SL/TP, note), Active / History tabs, each alert a node graph Trigger → Action → status
  chip (Active / Fired … → Filled / Failed: … / Cancelled), Cancel on active ones; empty state chips prefill the form
  from live prices. The agent's `price_alert` card (`AlertCard.tsx`) sets one with `source: "agent"`; the Trade
  ticket's Limit mode places buy/sell alerts (`source: "ticket"`) listed under it as **Open orders** with Cancel.
  A **Drawdown** trigger tab (% below the all-time high from `GET /api/alerts/ath`: the high, its date, source
  label and current drawdown, the threshold; Notify me / Notify + sell 1–100% of the paper long) reviewed once in a
  confirmation that warns when the price is already past it; rows show the high, threshold, current drawdown, source
  and outcome. The trade page header links "Set drawdown alert"; the agent's `set_drawdown_alert` card is the same
  `price_alert` card with `trigger: "drawdown"`.
- **Asia screener** — `GET /api/screener/asia` as a mungbeans.io-style table: filter tabs All / Below / Deep value /
  Distressed / Approaching / Oversold with counts, a market select, debounced search and sortable column headers, all in
  the URL; columns stock, market · index, local price + USD (FX pair in the tooltip), % vs 200WMA, direction ↓/↑, RSI 14w,
  drawdown from the ATH with its week, updated (Pending / No data feed / Stale badges). "Load more" follows `nextCursor`.
  A row with `tradableSymbol` offers **Set drawdown alert** (`/automations?create=drawdown&symbol=X`) and **Trade**
  ("via ADR" when through the US listing); others **View**, which opens the agent with an analysis prompt of the row's
  measured facts. Footer: not-financial-advice note, data as-of, last/next run, price and FX sources, each index list's
  source and as-of. The table scrolls sideways inside its card on phones (sticky first column).
- **Account** — equity hero, replayed performance and P&L by source, open cash-asset positions,
  prediction positions/stakes, active and recent copy plans, every simulated fill and confirmed Reset portfolio.
  Account revisions refresh fill history without redundant close/reset reloads; copy fills and Stop remain visible.
  **Portfolio health** (`src/pages/account/PortfolioHealth.tsx`) under the hero reads the free,
  caller-owned `GET /api/portfolio/review` on account revisions and every 60 seconds while visible. Loading,
  refreshing, retained stale facts, error/Retry and recovery are explicit; request generations, aborts and revision/
  request-time ordering reject older results. Concentration remains useful for one holding; prediction-only books
  retain stake facts and agent access. Known gross/net exposure, concentration and stop facts remain visible when
  whole-book beta/volatility/VaR/stress is unavailable, with server coverage, missing support and actual shared-day
  counts. Labels distinguish requested daily returns from observed samples, the limited BTC reference, current-book
  hypothetical worst day from observed account drawdown, and outcome stakes excluded from daily asset risk.
  Supplied directional groups and raw pair correlations never imply independence from missing measurements.
  Stop labels expose valid/dormant/crossed/missing/unknown status and covered-only distance before fees/slippage.
  "Review with the agent" sends the existing plain router prompt; the server reads fresh `review_portfolio` facts,
  separating measurements from interpretation and offering at most one warranted action with ordinary confirmation.
  AI portfolio detail adds private **Overlap with what you hold** near allocation, with caller gross weights,
  basket weights, direction, supported pair correlations and coverage/revision/request time. Revisions, investment
  success/partial failure and visible 60-second polling refresh it; Account outages preserve saved static allocation/
  backtest and detail error/Retry while withholding unqualified overlap. Caller facts stay response-local.
- **Forecasts** — dashboard like a research desk: hero "Your forecasts." + counts (In flight / Completed / Total),
  filter chips (status + shape), list rows with question, shape badge, headline probability, status, dates.
  **New**: big serif "Point it at the future.", rough-idea textarea (0/400) → Draft with AI → editable review
  form (shape cards Binary/Ordered/Categorical, question, outcomes list editable/reorderable, resolution criteria,
  resolution date) → Run forecast (5 cr). Example idea cards (Markets / Crypto / Macro / Tech).
  **Detail**: question as serif headline, status (running → animated "Specialists at work" with 3 specialist
  rows; poll every 3 s), final distribution (binary: huge "62%" YES with bar; ordered/categorical: ProbabilityBars),
  summary, specialists side by side with their own bars + reasoning, ensemble vs final, drivers (▲/▼ chips),
  evidence list with stance badges, flags, "What would change our mind", resolution criteria + date, knowledge note.
  `src/pages/forecasts/DeskView.tsx` (numeric hero, RangeStrip, MedianDots), `Scenarios.tsx` (What has to
  happen, input chips, id links), `MeasuredHistory.tsx` (H1, `#measured`); readers and the ported bucket maths in `forecast.ts`.
- **Strategies** — tabs **Studio** (headline "Turn ideas into strategies.", prompt box + symbol/interval selects,
  example prompts, recent "My strategies") and **Top strategies** ("Measured honestly." multi-line cumulative return
  chart of the top 8 + table: rank, name, author, symbol·interval, Ann. return, Total, Sharpe, Max DD, Trades).
  **Detail**: metrics row, equity vs buy-and-hold LineChart, spec rendered as readable rules ("Enter long when
  EMA(20) crosses above EMA(50)…"), editable params (stop/take-profit/position %) with Re-run backtest (free),
  trades table, explanation.
- **Portfolios** — prompt ("Describe a portfolio… e.g. 'Layer-1s with less drawdown than BTC'"), example chips;
  result/detail card: name, thesis, prompt as a quote bubble, donut or stacked bar of weights with AssetIcons,
  holdings with rationale, backtest LineChart vs saved BTC/SP500 with 1M/3M/6M tabs (slice client-side only within
  measured history), actual window/count and return / max DD / nullable vol stats; invested generic benchmark fields retain
  the saved choice through top-ups, missing comparisons show unavailable and legacy rows use BTC. Invest (paper) with amount → api.investPortfolio. List of previous portfolios as cards.

## Admin console (`admin`)
The operator console lives in `admin/` of this monorepo: a second SPA on the same Go API and session cookie, for
admins only. In Docker the Go server serves it on `ADMIN_PORT` (8081, published on 127.0.0.1 only) from `ADMIN_DIR`,
while `PORT` (8080) serves this app and answers 404 for `/api/admin/*` and `/auth/*`. Dev: `http://localhost:5174`
(`vite --port 5174 --strictPort`).
HTTP contract: [API-CONTRACT.md → Admin console](API-CONTRACT.md#admin-console-admin-role-required) plus `POST /api/auth/login`,
`POST /api/auth/logout` and `GET /api/me`.

**Stack** = this app's, minus the chart and markdown libraries: Vite + React 19 + TypeScript strict + Tailwind v4 +
react-router 7; Vitest + Testing Library (+ v8 coverage thresholds as in `vite.config.ts` here); Playwright e2e against a
local mock API (this app uses port 4010; the admin uses 4011). Node 24 (`.node-version`). Charts are plain
HTML/SVG: bar strips whose bars are focusable with an `aria-label` ("Oct 2: 42 credits"), and a tiny SVG sparkline.
Env: `VITE_API_URL` (dev `http://localhost:8080`; the Docker image builds with `/`) and `VITE_APP_URL` (the trade app,
dev `http://localhost:5173`; the Docker image takes it from the `APP_URL` build arg, default `http://localhost:8080`).
The dev server proxies `/auth` to `VITE_API_URL` and redirects every other request on `127.0.0.1` to `localhost`:
ChatGPT sign-in returns to `http://127.0.0.1:5174/auth/callback`, while the session cookie lives on `localhost`.

**Look.** Same tokens, fonts and `.card`/`.label` helpers as this app (copy `src/index.css` tokens and `@theme inline`
block verbatim; light/dark toggle the same way), so it feels like the same product, but it is clearly labelled ADMIN:
an `ADMIN` badge (ink background, mono uppercase, letter-spaced) in the top bar where this app shows `PAPER TRADING`,
the logo lockup reads "Foretrade · Admin", `<title>` "Foretrade Admin" ("<page> · Foretrade Admin" per page). Unlike
the web app, the console names AI providers and models (it is where they are configured).

**Auth gate.** `AuthProvider` calls `GET /api/me` once on load:
- 401 → `/login`.
- `user.role !== "admin"` → `POST /api/auth/logout` `{}`, then a full-screen **Access restricted** wall: "This console
  is for Foretrade administrators. Your session has been signed out." + "Back to sign in". A `403 forbidden`
  from `/api/admin/*` re-checks `/api/me` and shows the same wall only if the role is no longer admin (a demotion
  mid-session); otherwise (an origin missing from `ADMIN_ORIGINS`) the page shows the API's message.
- Network failure → "Can't reach the API" + Retry.
- `/login`: email + password. A successful login whose `user.role` is not admin logs out at once and shows
  "Access restricted — this console is for administrators only." inline; 401 → "Wrong email or password."; 429 → the
  server's message; 403 `forbidden` (this origin is not in `ALLOWED_ORIGINS`) → "Sign-in isn't allowed from this
  address. Add it to ALLOWED_ORIGINS and ADMIN_ORIGINS in .env."
- The session cookie is shared with the trade app (one cookie per host; cookies ignore the port), so signing out here
  signs that browser out of the trade app too. Say so under the Log out item.

**Shell.** The same 76 px left icon rail as this app (bottom tab bar under 768 px): Overview `/`, Users `/users`,
Audit `/audit`, Settings `/settings`. Top bar: page title, `ADMIN` badge, signed-in admin email, "Open Foretrade ↗"
(`VITE_APP_URL`, new tab),
theme toggle, Log out. Toast provider as here. Content max-width ~1200 px.

| route | page | content |
|---|---|---|
| `/login` | `LoginPage` | split layout like this app's login; hero "Operator console." + one line on what it does |
| `/` | `OverviewPage` | `asOf` + Refresh (auto-refresh every 60 s). KPI cards: Users (total · admins · disabled), Active today / 7d / 30d, New users 30d, AI credits today. AI credits 30-day bar strip. Feature usage table: feature · today · 7d · 30d · 30-day sparkline. Top users by credits (7d) → `/users/:id`. Paper totals (accounts, open positions, equity, total P&L, 30d notional). System status card: version, uptime, DB latency, model + knowledge cutoff ("AI is off · choose a provider" → `/settings` when none), news (last ingest, items, brief age; amber when the ingest is > 3 h or the brief > 6 h old, or news is disabled), Brave used/limit meter, crawler dot (reachable / unreachable / not configured), running forecasts and arena runs, and the live paper desk's status and limits. `null` values render "—" |
| `/users` | `UsersPage` | Filters live in the URL query (`q` debounced 300 ms, `status`, `role`, `sort`, `order`, `page`). Table: name + email, role badge, status badge, managed badge, created, last login, last active (time ago), credits today / limit ("12 / 500", limit muted when it is the default), equity, sessions; sortable headers map to `sort`/`order`; pager. Row → `/users/:id`. **New user** modal: email, name, role, credit limit (blank = default), password (blank = generate), "Start with showcase content" (off by default; when on, the "Copy from" email, blank by default, is sent as `seedFrom`) → when the response has `password`, show it once with Copy and "I've saved it" |
| `/users/:id` | `UserDetailPage` | Header: name, email, role / status / managed badges. Profile card: created, last login, last active, sessions, credit limit. Actions: Edit (name, role, credit limit, with "Use default"), Disable / Enable, Set password (type one or generate; disabled with a tooltip on managed accounts), Revoke sessions, Reset today's credits, Reset portfolio (confirm), and a Danger zone **Delete** whose button enables only after typing the user's exact email (on a managed account its copy says the account is recreated with the config password at the next API restart, instead of "can't be undone"). On your own account, Disable, demote and Delete are disabled with "You can't … your own account." Then: 30-day usage bars against the limit, lifetime counts, portfolio summary (equity, P&L, positions table), recent trades, activity lists (chats, forecasts, strategies, AI portfolios, arena runs), and this user's audit entries |
| `/audit` | `AuditPage` | Paged table (50 per page): time, admin email, action as a label ("Disabled user", "Generated password", …), target email (→ `/users/:id`; a 404 there reads "Account deleted"), detail summary (`from → to` for updates) |
| `/settings` | `SettingsPage` | **AI provider** card: status badge (off / ready / error); provider select grouped Cloud APIs / Local / ChatGPT plan / AWS; per preset an API key (show/hide, saved "…a1b2" hint, Replace / Clear), base URL (preset default as placeholder), model combo box filled by **Load models** that also takes free text, reasoning-effort toggle, knowledge cutoff date and, for Bedrock, AWS region; **Test connection** (latency or the provider's error) and **Save**, which tests first and saves nothing on failure. When `AI_PROVIDER` is set in the server's environment the card is read-only ("Set by environment"). **Sign in with ChatGPT** opens OpenAI's sign-in in a new tab; the API's `/auth/callback` returns to `/settings?chatgpt=ok\|error&message=…`, which the page toasts once; signed in, it shows the email and **Sign out**, plus a note that the plan belongs to the signed-in person, so AI runs in owner-only mode (admins only). **Search** card: Brave Search API key (link to brave.com/search/api), crawl4ai base URL and token, each with a source badge (saved here or from the environment). A warning explains `SECRET_KEY` when the server has none |

Every mutation shows a toast; the returned `user` replaces local state; `409`/`400` messages are shown verbatim.
Generated passwords live only in component state for the open modal and are cleared on close: never in the URL,
storage, logs or analytics.

```
src/lib/api.ts          ApiError {status, code, message}; login, logout, me, overview, users(params), createUser,
                        user(id), updateUser, setPassword, revokeSessions, resetPortfolio, resetCredits, seedUser,
                        deleteUser, audit, settings, updateSettings, testSettings, models, chatgptStart, chatgptSignOut
src/lib/types.ts        User (with role), Credits, Portfolio, Position, Trade + every type of the contract's Admin console
src/lib/format.ts       copied from this app (fmtUsd, fmtPct, fmtCompact, timeAgo, fmtDate)
src/lib/auth.tsx        AuthProvider/useAuth(): { user, state: "loading"|"ok"|"restricted"|"error", login, logout }
src/lib/theme.tsx       as here
src/components/ui/      Button, Card, Badge, Input/Select, Spinner/Skeleton, EmptyState, Stat, Modal, Bars, Sparkline,
                        Logo, PasswordReveal, icons
src/components/layout/AdminShell.tsx   rail + top bar + <Outlet/>; PageHeader
src/pages/{LoginPage,OverviewPage,UsersPage,UserDetailPage,AuditPage,SettingsPage}.tsx
```
