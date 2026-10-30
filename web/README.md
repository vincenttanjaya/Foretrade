# foretrade-web

The user-facing app of **Foretrade**, a self-hosted AI trading desk: an agent chat that ends in a confirmable trade
card, a trade terminal with an AI copilot, an Asia stock screener, a probability forecast desk, a strategy studio
with backtests, a live paper arena with copy plans, and one-sentence AI portfolios. **Paper trading only**: every
fill is simulated by the Go API (`../api`), and nothing executes on a real venue.

Vite + React 19 + TypeScript + Tailwind CSS v4 + react-router 7 + lightweight-charts 5 + react-markdown. The HTTP
contract is in [`docs/API-CONTRACT.md`](../docs/API-CONTRACT.md); layout and design tokens in
[`docs/FRONTEND-LAYOUT.md`](../docs/FRONTEND-LAYOUT.md).

## Run locally

Node 24 (`.node-version`).

```sh
npm ci
cp .env.example .env.local   # optional: the default already points at http://localhost:8080
npm run dev                  # http://localhost:5173
```

Start the API on port 8080 (`go run ./cmd/api` in `../api`, with `DATABASE_URL` set). Its default `ALLOWED_ORIGINS`
already includes `http://localhost:5173`. Sign in with the demo user it seeds when `DEMO_EMAIL` and `DEMO_PASSWORD` are
set. AI features need a provider: an admin picks one on the admin console's Settings page, or the server's
`AI_PROVIDER` sets it.

To run the whole stack instead, run `scripts/setup.sh` and then `docker compose up -d --build` from the repo root: the Go server
serves this app (built with `VITE_API_URL=/`) and its API on http://localhost:8080.

| var | default | notes |
|---|---|---|
| `VITE_API_URL` | `http://localhost:8080` | API origin, no trailing slash; `/` when the API is on the same origin (the Docker image). Read at build time. |

Requests use the `tsid` session cookie (`credentials: "include"`), so the API must list this site's origin in
`ALLOWED_ORIGINS`.

## Scripts

| script | does |
|---|---|
| `npm run dev` | dev server on port 5173 (fails if the port is taken) |
| `npm run build` | type-check, then build static files into `dist/` |
| `npm run preview` | serve `dist/` locally |
| `npm run typecheck` | `tsc -b` only |
| `npm test` | Vitest unit suite (jsdom + Testing Library), single run |
| `npm run test:coverage` | same, with v8 coverage; fails under 70% lines overall or 80% in `src/lib` and `src/components` |
| `npm run e2e:build` | production build against the mock API (`VITE_API_URL=http://localhost:4010`) into `dist-e2e/` |
| `npm run e2e` | `e2e:build`, then run the Playwright suite headless |
| `npm run e2e:ui` | same build, then Playwright's UI mode |

## Tests

Unit tests sit next to the code (`*.test.ts(x)`) and run against a mocked API (`vi.spyOn(api, …)`); no test touches
the network.

`npm run e2e` builds the app as production does (into `dist-e2e/`, with `VITE_API_URL=http://localhost:4010`), serves
it with `vite preview` on port 4180 and drives it with Playwright (Chromium) at 1440 px; tests tagged `@mobile` run
again at 375 px. First time: `npx playwright install chromium`. The backend is `e2e/mock-api.ts`, a Node stand-in for
the API (about 3k lines, with its own paper engine) that follows the API contract and is typed with the client's own
signatures, so a contract change the mock does not follow fails `npm run typecheck`. Every test fails on a console error and blocks requests that leave localhost.

## Layout

```
src/lib/          contract types, typed API client + chat SSE stream, formatters, providers (auth, portfolio, assets, theme)
src/components/   UI kit (ui/), app shell (layout/AppShell.tsx), toasts
src/pages/        one folder per area: agent, trade, screener, signals, news, account, automations, forecasts, strategies, arena, portfolios
src/assets/       bundled coin and stock artwork; each folder's LICENSE.md records sources and licences
src/test/         Vitest setup, fixtures, render helpers and fakes
e2e/              Playwright journeys (*.spec.ts), their fixtures, and the mock API
```

Company logos under `src/assets/stocks` are trademarks of their owners, used only to identify the listed security.
