# Security

## Reporting a vulnerability

Please report security problems privately. Do not open a public issue or pull request.

Use GitHub's private vulnerability reporting: on
[vincenttanjaya/Foretrade](https://github.com/vincenttanjaya/Foretrade), open the **Security** tab and click
**Report a vulnerability** (or go straight to
<https://github.com/vincenttanjaya/Foretrade/security/advisories/new>).

Include:

- what an attacker can do, and what they need first (an account, admin access, network position)
- the steps or a request that shows it
- the commit or release you tested

I will confirm I received it, work on a fix in a private advisory, and credit you when the fix is published unless
you ask me not to. Fixes go to `main` and the next release. Only the latest release and `main` are supported.

## Scope

In scope: the code in this repository.

- the Go API (`api/`), including authentication, sessions, the admin API, the settings store and the ChatGPT sign-in
- the web app (`web/`) and the admin console (`admin/`)
- the `Dockerfile`, `compose.yaml` and `scripts/setup.sh` defaults

Out of scope:

- bugs in AI providers, market data sources or other third-party services
- attacks that need control of the host, the database, `.env` or an admin account
- a deployment that ignores the guidance below, such as an admin console published to the internet with a weak
  password
- AI output being wrong, or prompt injection that only changes what the AI says to the user who asked. Report it if
  it lets someone act as another user, read another user's data, or reach secrets.
- denial of service through heavy but valid use, such as spending one's own AI credits

Foretrade only trades on paper. No code path can move real money, so a finding cannot lead to a real trade.

## Deploying safely

What the code does, and what you should do.

**Keep the admin console private.** `compose.yaml` publishes the admin port (8081) on 127.0.0.1 only. The admin API
(`/api/admin/*`) and the ChatGPT callback (`/auth/*`) answer only on that port: the public port returns 404 for them.
The admin API also requires an admin session, and rejects a request whose `Origin` header is present but not listed
in `ADMIN_ORIGINS`. Keep the port on loopback
and reach it through an SSH tunnel (`ssh -L 8081:127.0.0.1:8081 your-server`). If you must expose it, put it behind
a VPN or an authenticating reverse proxy.

**Use strong secrets.** `scripts/setup.sh` generates `POSTGRES_PASSWORD`, `SECRET_KEY` (32 random bytes as hex) and
`ADMIN_PASSWORD`, and writes `.env` readable by its owner only. If you set them yourself:

- `SECRET_KEY` must be 64 hex characters (`openssl rand -hex 32`). Without a valid key, the API still starts but
  refuses to save provider keys or the ChatGPT sign-in.
- `ADMIN_PASSWORD` must be 12 to 72 characters (bcrypt's limit is 72 bytes); the API refuses to start otherwise. Use
  a long random one.
- Never commit `.env`. It is in `.gitignore`.

**Serve over HTTPS on a public host.** Put a TLS-terminating reverse proxy in front of the web port, then set
`COOKIE_SECURE=true` so the session cookie is only sent over HTTPS. The cookie is already `HttpOnly` and
`SameSite=Lax`. Set `ALLOWED_ORIGINS` and `APP_URL` to your real origin. State-changing requests must be JSON and,
when they carry an `Origin`, come from an allowed origin. Set `TRUSTED_PROXIES` to the proxy's address as the API sees
it (`172.16.0.0/12` for a proxy on the Docker host), or every sign-in shares one rate limit.

**Provider keys are encrypted and never returned.** Keys and tokens saved in the admin console (AI provider key,
Brave key, crawl4ai token, ChatGPT sign-in) are stored in Postgres encrypted with AES-256-GCM under `SECRET_KEY`.
The settings API returns only whether a key is set and a hint: the last 4 characters of a key at least 16
characters long. The audit log records which fields changed, never their values. If `SECRET_KEY` changes, the stored
secrets can no longer be decrypted and must be entered again.

**Keys in the environment.** When `AI_PROVIDER` is set, the AI key comes from `.env`, and the console shows it
read-only, with the same hint. Anyone who can read `.env` or the container's environment can read those keys.

**ChatGPT tokens stay on the server.** The sign-in uses OAuth 2 with PKCE and a single-use `state`, and checks the
`id_token` signature against OpenAI's published keys. The access and refresh tokens are encrypted in the database,
refreshed by the server, and revoked on sign-out. The browser never receives them. While ChatGPT is the provider,
only admins can use AI features, because the plan belongs to the person who signed in. Background AI (the news brief,
signal rationales and, if `LIVE_ARENA=on`, the live arena) still runs on the plan, and every user sees its output.

**Local model servers.** On Linux, Ollama or LM Studio must listen beyond 127.0.0.1 for the container to reach them
(`OLLAMA_HOST=0.0.0.0`). Firewall that port if the machine is reachable from the internet: those servers have no
authentication.

**Page fetching.** When the AI reads a web page without crawl4ai, the server refuses private, loopback and other
non-public addresses. A `CRAWL_TOKEN` from the environment is sent only to `CRAWL_BASE`.

**Database.** `compose.yaml` does not publish the Postgres port. Keep it that way, and back up the `db` volume
together with `.env`: the backup is useless for stored secrets without `SECRET_KEY`.

**Rate limits.** Sign-in is limited to 10 attempts per client IP per 10 minutes, and admin changes to 60 per admin
per 10 minutes. The counters live in memory, per process. The client IP is the TCP peer; behind a trusted proxy
(loopback or `TRUSTED_PROXIES`) it is the rightmost `X-Forwarded-For` address that is not a trusted proxy, so a
client cannot pick its own by sending the header.
