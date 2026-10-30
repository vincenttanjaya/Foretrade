# Contributing

Thanks for helping. Bug reports, fixes and small improvements are welcome. For a larger change or a new feature,
open an issue first so we can agree on the approach before you write it.

Security problems go through [SECURITY.md](SECURITY.md), not public issues.

## Setup

Follow [Local development](README.md#local-development) in the README: Postgres, the API, and the two dev servers.
The layout and the HTTP contract are in [docs/](docs/).

## What CI checks

Every pull request runs `.github/workflows/ci.yml`. Run the same checks locally before you push.

**API (`api/`)**, unit job:

- `gofmt -l .` must print nothing
- `go vet ./...`
- `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`: no known vulnerability the code reaches, in a module or
  in the Go release that `go.mod`'s `toolchain` line pins
- `go test -race -count=1 ./...` with a Postgres service, and a coverage gate: total statement coverage must stay
  at or above 75%

**API**, integration job:

- `python3 -m unittest discover -s ci -p 'test_*.py'` (the results checker's own tests)
- the integration suite, `go test -race -count=1 ./cmd/api/` with `INTEGRATION_DATABASE_URL` set
- `ci/check_go_results.py`, which fails on any failed, skipped or missing required test. A new
  `TestIntegration*` test must be added to `REQUIRED_TESTS` in `api/ci/check_go_results.py`.

**Web app and admin console (`web/`, `admin/`)**, each:

- `npm ci`
- `npm run typecheck`
- `npm run test:coverage`: Vitest, failing under 70% line coverage overall or 80% in `src/lib` and
  `src/components`
- `npm run build`
- end to end: `npm run e2e:build`, then Playwright against `e2e/mock-api.ts` (web runs in two shards)

**Docker**: `scripts/setup.sh`, then `docker compose up -d --build --wait --wait-timeout 180`, then checks that both
ports serve their app, that the admin API answers on the admin port only, and that the bootstrap admin can sign in.

Exchange holiday calendars (`api/internal/market/session.go`) run out every year. The server logs a warning at boot
when one has fewer than 30 days left. To check them by hand, run
`CALENDAR_ALARMS=1 go test -run TestCalendarHorizons ./internal/market/`, then add the next year's dates with a
source line.

## Code style

**Go**

- `gofmt` and `go vet` clean.
- Prefer the standard library. Do not add a Go module without discussing it in an issue first.
- Short doc comments that say what a thing is or why it exists, matching the code around it.
- Deliberate simplifications carry a `// ponytail:` comment that names the limit and when to change it, for example
  `// ponytail: no compression; put a proxy in front or precompress when bandwidth matters`.
- Tests make no network calls. Use the existing fakes: `internal/llm/llmtest`, `internal/yahoo/yahootest`,
  `internal/market/rtquote/rtquotetest`, and `httptest` servers. Database tests read `DATABASE_URL_TEST` and run in
  a throwaway schema.

**TypeScript (`web/`, `admin/`)**

- Strict TypeScript (`strict`, `noUnusedLocals`, `noUnusedParameters`); `npm run typecheck` must pass.
- No ESLint or Prettier config is checked in. Match the existing style: 2-space indent, single quotes, no
  semicolons.
- Unit tests sit next to the code (`*.test.ts(x)`) and mock the API. Playwright specs live in `e2e/` and run against
  `e2e/mock-api.ts`, which is typed with the client's own signatures: when you change the API contract, update the
  mock too.

**The web app never names an AI vendor or model.** User-facing copy in `web/` says "AI", never "ChatGPT", "Claude",
"GPT", "Gemini" or any other vendor or model name, and errors shown to users stay generic. The admin console may
show provider and model names, since the operator chooses them there.

**API changes** go into [docs/API-CONTRACT.md](docs/API-CONTRACT.md) in the same pull request. New environment
variables go into its configuration table and into the root `.env.example`.

## Commits and pull requests

- Keep each pull request to one change. Split unrelated fixes into separate ones.
- Write commit messages in the imperative, with a short first line ("Fix the stop-loss check on short positions"),
  and a body that says why when it is not obvious.
- In the pull request, say what changed, why, and how you tested it. Include a screenshot for a visible UI change.
- Add or update tests for the behaviour you change.
- CI must be green before review.

By contributing, you agree that your contribution is licensed under the [MIT License](LICENSE).
