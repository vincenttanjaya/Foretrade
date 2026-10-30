# admin

Operator console for **Foretrade**. Manage accounts (create, edit, disable, passwords, sessions, credit limits,
paper portfolios, delete), choose the AI provider and search keys, watch usage and system health, and read the audit
log of every admin change.

Only accounts with `role = "admin"` can enter. Anyone else who signs in is signed out at once and sees an
"Access restricted" screen. The console talks to the same Go API as the trading app, with the same `tsid` session
cookie, so signing out here also signs that browser out of the app.

## Pages

| Route | Purpose |
|---|---|
| `/login` | Admin sign-in (no registration). A non-admin account is refused inline and signed out |
| `/` | Overview: KPIs, AI credits per day, feature usage, top users, paper totals, live paper desk and system status. Refreshes every minute |
| `/users` | Accounts table: search, filters, sorting, paging (all in the URL). **New user** shows a generated password once |
| `/users/:id` | One account: edit, disable / enable, set password, revoke sessions, reset credits or portfolio, delete; usage, portfolio, activity and audit entries |
| `/audit` | The admin audit log, 50 per page, newest first |
| `/settings` | AI provider card: provider, model (with **Load models**), API key, base URL, reasoning effort, knowledge cutoff, AWS region for Bedrock; **Test connection**, and **Save**, which tests first. **Sign in with ChatGPT** uses a ChatGPT plan instead of an API key and puts AI in owner-only mode. Read-only when the server's `AI_PROVIDER` is set. Search card: Brave Search API key, crawl4ai base URL and token |

Destructive actions ask for confirmation. You can't disable, demote or delete your own account, and the bootstrap
accounts (`managed`) take their passwords from the server config. The API enforces all of this too.

Stack: Vite, React 19, TypeScript (strict), Tailwind CSS v4, react-router 7; the same tokens and fonts as `web`.
API contract: [`docs/API-CONTRACT.md`](../docs/API-CONTRACT.md) §3 "Admin console"; layout:
[`docs/FRONTEND-LAYOUT.md`](../docs/FRONTEND-LAYOUT.md).

## Develop

Node 24 (`.node-version`).

```sh
npm ci
cp .env.example .env.local   # optional, the defaults point at localhost
npm run dev                  # http://localhost:5174
```

Run the API on port 8080 (`go run ./cmd/api` in `../api`) with `ADMIN_EMAIL` / `ADMIN_PASSWORD` set, then sign in as
that bootstrap admin. The API's default `ALLOWED_ORIGINS` and `ADMIN_ORIGINS` already include `http://localhost:5174`.
Set `SECRET_KEY` (64 hex characters, `openssl rand -hex 32`) to save API keys or a ChatGPT sign-in from the Settings
page.

The dev server (`vite.config.ts`) keeps the console on `localhost`: a request to `127.0.0.1:5174` is redirected to
`localhost:5174`, except `/auth/*`, which it proxies to `VITE_API_URL`. That is how ChatGPT sign-in works in dev: the
API sends OpenAI a redirect to `http://127.0.0.1:5174/auth/callback`, and the session cookie stays on `localhost`.

| var | default | notes |
|---|---|---|
| `VITE_API_URL` | `http://localhost:8080` | API origin, no trailing slash; `/` when the API is on the same origin (the Docker image). Read at build time; the dev server proxies `/auth` to it |
| `VITE_APP_URL` | `http://localhost:5173` | the trading app, for the "Open Foretrade" link. The Docker image takes it from the `APP_URL` build arg |

In Docker (`scripts/setup.sh`, then `docker compose up -d --build` from the repo root) the Go server serves the console on
http://localhost:8081, published on 127.0.0.1 only. From another machine, use an SSH tunnel
(`ssh -L 8081:127.0.0.1:8081 <server>`): ChatGPT sign-in needs the console on a loopback address.

## Test

| script | does |
|---|---|
| `npm run dev` | dev server on port 5174 (fails if the port is taken) |
| `npm run build` | type-check, then build static files into `dist/` |
| `npm run preview` | serve `dist/` locally |
| `npm run typecheck` | `tsc -b` (app, config and e2e) |
| `npm test` | Vitest unit and page tests (jsdom + Testing Library, API mocked), plus `e2e/*.test.ts` |
| `npm run test:coverage` | same, with v8 coverage; fails under 70% lines overall or 80% in `src/lib` and `src/components` |
| `npm run e2e:build` | production build against the mock API (`VITE_API_URL=http://localhost:4011`) into `dist-e2e/` |
| `npm run e2e` | `e2e:build`, then run the Playwright suite headless (first time: `npx playwright install chromium`) |
| `npm run e2e:ui` | same build, then Playwright's UI mode |

Unit tests sit next to the code (`*.test.ts(x)`). `e2e/dev-server.test.ts` starts the real dev server and checks the
`127.0.0.1` redirect and the `/auth` proxy.

`npm run e2e` serves the build with `vite preview` on port 4181 and drives it with Playwright (Chromium). The backend is
`e2e/mock-api.ts` on port 4011, a small Node server that follows the API contract (cookie auth, CSRF rule, error
envelope, rate limit, 409 safety rules, audit rows, the settings endpoints). Nothing leaves localhost.
