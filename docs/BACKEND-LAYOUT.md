# Backend layout (`api`)

A map of the Go API for contributors: where each package lives, what it owns, and the internal Go API the packages
share. Paths below are relative to `api/`. Module `github.com/vincenttanjaya/Foretrade/api`, Go 1.27 (`api/go.mod`;
the root `.tool-versions` pins `golang 1.27.0`). HTTP contract: [API-CONTRACT.md](API-CONTRACT.md).

Dependencies: `github.com/jackc/pgx/v5` (Postgres), `github.com/aws/aws-sdk-go-v2` (`config`,
`service/bedrockruntime` for the Bedrock provider, `service/sesv2` for email notifications) with `smithy-go`,
`golang.org/x/crypto` (bcrypt), `golang.org/x/net` (HTML parsing in `research`) and `golang.org/x/sync` (the upload
budget in `attach`). Everything else is stdlib, including the HTTP AI wires and ChatGPT sign-in. Feature packages do
not add modules.

## Package map

```
cmd/api/main.go             wiring: config → pgx pool → migrate → demo user + bootstrap admin → llm client (off) →
                            research → aiconfig.Load (AI provider and search keys) → app.Deps → background tasks →
                            routes → server. Tasks: paper (TP/SL watcher), alerts, research (news loop),
                            scheduled_research, signals, forecast_recovery, arena_recovery, live_arena, autopilot,
                            screener, quote_feeds, realtime_quotes. main → start (config.Load; listens on PORT and,
                            when set, ADMIN_PORT) → runWithOptions; run(ctx, cfg, listener) is the seam the
                            integration suite boots. Mounts GET /healthz (X-Api-Commit, X-Quote-Sources) and
                            GET /auth/callback (ChatGPT sign-in), then every package's routes.
cmd/api/serve.go            serving: one process serves the API and the built SPAs. Without ADMIN_PORT, PORT serves
                            everything (plus WEB_DIR's SPA when set). With it, PORT serves the web app (WEB_DIR) and the
                            API but answers 404 for /api/admin/* and /auth/*; ADMIN_PORT serves the admin SPA
                            (ADMIN_DIR), the whole API and the OAuth callback. SPA fallback to index.html; hashed
                            /assets/* cached for a year, everything else no-cache. `api healthcheck` (the image's
                            HEALTHCHECK) probes /healthz on PORT.
cmd/api/integration*_test.go  integration suite: run() on 127.0.0.1:0, a fresh schema, every upstream faked
cmd/forecast-replay/        one-off tool: re-runs the forecast panel as of past dates and writes
                            internal/forecast/replay_seed.json (embedded; forecast.Recover inserts it at boot)
cmd/genlisted/              writes internal/market/listed.json: build (offline; TestListedUpToDate pins it) and
                            measure (hand-run, networked, never in CI)
ci/check_go_results.py      fails CI's integration job on a failed, skipped or missing required test

internal/config/            Load() reads the environment (its only source): AI_* (env mode for the AI settings),
                            SECRET_KEY, PORT / ADMIN_PORT, WEB_DIR / ADMIN_DIR, origins, feature switches.
                            BEDROCK_MODEL is a deprecated alias of AI_MODEL when AI_PROVIDER=bedrock
internal/store/             Open(ctx, url) → *pgxpool.Pool; Migrate(ctx, pool) applies the embedded migrations/*.sql
                            once each, in a transaction, tracked in schema_migrations. storetest: a migrated pool and
                            a throwaway user for DB tests
internal/httpx/             HTTP plumbing: JSON, Error, Decode, the request user, SSE, middleware (CORS, CSRF, Recover, Log)
internal/auth/              users, password login, cookie sessions (only the token's SHA-256 is stored), roles,
                            disabled accounts, login rate limit; /api/auth/*, /api/me; Require and RequireAdmin;
                            EnsureUser (boot bootstrap)
internal/usage/             AI credits per user per UTC day (users.credit_limit, else AI_DAILY_CREDITS), spent before
                            each model call; while OwnerOnly holds, non-admins get ErrOwnerOnly (403 ai_owner_only)
internal/app/               Deps (what feature packages receive), WriteError (package errors → contract error codes),
                            Tasks (background task admission and drain); apptest builds a full Deps for handler tests
internal/profile/           explicit user preferences and confirmed memories, /api/me/profile*; independent of app and auth
internal/attach/            validates files and images attached to a chat message or a forecast: bounded base64,
                            header sniffing, mediaType/extension agreement, one process-wide budget for upload-sized
                            bodies; attachtest builds synthetic fixtures
internal/notify/            delivers one bell item outside the app: email through Amazon SES (off unless
                            NOTIFY_EMAIL_FROM is set) or a post to the user's own Lark bot

internal/llm/               the AI provider client: Complete, CompleteJSON, CompleteWith / CompleteJSONWith (one turn
                            with an image or document), Agent (streaming tool loop). The provider is a preset on one
                            of four wires; Configure swaps it at runtime, and each call keeps the one it started with.
  llm.go                    the public types (Message, Image, Document, Tool, Event, AgentRequest, Effort), the
                            Complete* calls and the Agent loop
  settings.go               Settings, Providers (13 presets: openai, chatgpt, anthropic, bedrock, openrouter, gemini,
                            groq, deepseek, xai, mistral, ollama, lmstudio, custom), NewFromSettings, Configure,
                            Configured, Model, KnowledgeCutoff, Test, ListModels, TokenSource
  backend.go                the internal backend interface each wire implements; ProviderError
  chat.go                   wire "chat", OpenAI Chat Completions: openrouter, gemini, groq, deepseek, xai, mistral,
                            ollama, lmstudio, custom
  messages.go               wire "messages", Anthropic Messages: anthropic
  responses.go              wire "responses", OpenAI Responses: openai (API key) and chatgpt (signed-in plan)
  converse.go, stream.go    wire "converse", Bedrock Converse / ConverseStream through the AWS SDK: bedrock
  http.go                   the HTTP wires' shared client, retries, error parsing (API keys removed), SSE reader
  docs.go                   documents as text for wires without native support (docx/xlsx extraction, size-capped)
  flatten.go                the Flatten transcript: tool rounds replayed as text
  llmtest/                  a fake Bedrock runtime (Converse and ConverseStream) the real client runs against
internal/aiconfig/          the AI provider and search settings the admin console edits: the one ai_settings row, the
                            environment's precedence over it, the admin endpoints and ChatGPT sign-in. Every change
                            reconfigures the llm client and the research service's keys at once.
  aiconfig.go               Service: Load at boot, the merged view, env mode (AI_PROVIDER set → the AI settings are
                            read-only in the console); search keys: the row's value where set, else the environment's
  crypto.go                 AES-256-GCM sealing of stored secrets under SECRET_KEY; the "…a1b2" key hints
  http.go                   GET / PUT /api/admin/settings, POST /api/admin/settings/test, …/models,
                            …/chatgpt/start, …/chatgpt/signout (mounted by admin.Register behind its gate)
  chatgpt.go                "Sign in with ChatGPT" for open-source and self-hosted apps: OAuth 2 with PKCE and
                            dynamic client registration, a loopback redirect on the admin port, the RS256 id_token
                            checked against the issuer's JWKS, rotating refresh tokens renewed under the row lock;
                            GET /auth/callback; the llm.TokenSource. With provider chatgpt, AI is owner-only

internal/market/            the fixed universe and its market data: crypto from Binance public data, US and Asian
                            stocks and indices from their Hyperliquid perps (both keyless), Yahoo quote feeds for
                            named and listed stocks; short caches, indicators, Analyze, Outlook. Every number the AI
                            cites comes from here. universe_quotefeed.go (138 named quote feeds), universe_listed.go
                            + listed.json (604 listed Asian instruments), quotefeed.go (the quote-feed poller),
                            listingquote.go (the screener's watch tier), session.go (venue calendars), perps.go,
                            perf.go and returns.go (equity-curve and return statistics), derivatives.go (Hyperliquid
                            perp context), realtime*.go (the optional real-time sources); markettest fakes Binance
                            and Hyperliquid
internal/market/rtquote/    one adapter per real-time source, no I/O of its own: naver (KRX), tencent (HKEX), vietcap
                            (HOSE), sgx (SGX), mis and yahootw (TWSE); registry.go lists them. rtquotetest fakes all
                            six. Off unless QUOTES_REALTIME names their venues
internal/yahoo/             keyless Yahoo Finance client (charts, quotes, FX) behind one shared rate gate, plus pure
                            helpers over daily bars; yahootest fakes it
internal/paper/             the simulated ledger: market orders at the latest mark ± 2 bps with a 0.1% fee, signed
                            average-cost positions in Postgres, the TP/SL watcher, history and P&L by source,
                            prediction stakes, portfolio review, overlap. Nothing reaches a real venue
internal/research/          market intelligence: Brave Search, article reads (crawl4ai, else an SSRF-guarded direct
                            fetch), the stored news wire a background loop refreshes every 2 h, the AI market brief;
                            GET /api/news, /api/news/brief, /api/news/pulse; SetSearch takes new keys at runtime
internal/researchrun/       the neutral boundary between scheduled research (alerts owns admission and publication)
                            and its read-only runner (chat)

internal/chat/              agent chat: POST /api/chat/stream, /api/chats*. Researches with Go tools and ends in cards
                            the user confirms; it never places orders itself. Also the scheduled-research runner
internal/copilot/           trade plan: POST /api/copilot/plan, GET /api/copilot/desk. Go computes the analysis and
                            outlook; one low-effort model call picks direction, levels and wording; Go checks the levels
internal/forecast/          forecast desk: /api/forecasts*, /api/predictions/markets, /api/market-positions. A rough
                            idea becomes a resolvable question; three specialist agents and an aggregator turn it into
                            a probability estimate with an audit trail. Register + Recover
internal/strategy/          strategy studio: /api/strategies*. A strategy is a JSON rule spec (never generated code)
                            that Backtest runs; the model writes the spec and explains the numbers Go computed.
                            Exports Backtest, Decide, Find
internal/aipf/              AI portfolios: /api/ai-portfolios*. The model picks a weighted basket; Go cleans the
                            weights, backtests against a saved BTC/SP500 benchmark and invests through the paper engine
internal/arena/             AI backtest arena: /api/arena/*. Persona agents trade a past window step by step, each
                            seeing only closed bars. Also the live paper desk (live.go, LIVE_ARENA) and copy plans
                            (copy.go, /api/copy-plans*). Register, Recover, RunLive
internal/autopilot/         /api/autopilots*: runs a saved or built-in strategy on the paper engine each closed bar
                            with strategy.Decide; Go rules only, no model call, no credits
internal/alerts/            Automations: /api/alerts*, /api/notifications*. Price, schedule, odds and drawdown triggers
                            checked every 15 s (no model call, no credits), the notifications bell's feed, delivery
                            channels, and scheduled research
internal/signals/           GET /api/signals: the shared momentum board, rebuilt every 4 h; one low-effort model call
                            writes the new picks' descriptions (at most 12 a UTC day); no user credits. Register + Run
internal/screener/          Asia screener: GET /api/screener/asia, /api/screener/asia/quotes. A weekday job screens
                            index constituents against their 200-week moving average, 14-week RSI and drawdown from
                            the all-time high; each request prices the rows at live quotes. Register + Run
internal/admin/             /api/admin/*: overview, accounts, audit, and (mounted from aiconfig) settings. One gate
                            (adminOrigin + RequireAdmin) for the whole prefix; every mutation writes its admin_audit
                            row(s) in the transaction that makes the change
```

Feature packages import the core packages. A few import each other: chat (its tools reach aipf, alerts, autopilot,
forecast and signals), alerts → forecast (linked-market odds), autopilot → strategy (Find, Decide) and admin → arena
and autopilot (stopping a disabled user's copies and autopilots). Each package builds and vets alone:
`go build ./internal/<pkg>/ && go vet ./internal/<pkg>/`.

## Core signatures

The shared Go API the feature packages code against. `go doc` on a package is the source of truth; this is the
short version.

```go
// package app
type Deps struct {
    Tasks    *Tasks            // background task admission and drain; nil for standalone package callers
    DB       *pgxpool.Pool
    Market   *market.Client
    LLM      *llm.Client       // the configured provider; d.LLM.Model() and d.LLM.KnowledgeCutoff() (arena runs, admin overview)
    Usage    *usage.Meter
    Paper    *paper.Engine
    Auth     *auth.Service
    Research *research.Service // market intelligence; see package research below
    Yahoo    yahoo.Source      // keyless Yahoo feed; read via d.YahooSource() (yahoo.Disabled when nil)
    Settings *aiconfig.Service // the AI provider and search keys the admin console sets; nil in package tests
    // … plus Version, StartedAt, Managed, AdminOrigins, PolymarketBase, the forecast research switches,
    // LiveArena, Mailer, Lark and AppURL (see app.go)
}
func (d *Deps) AIOff() bool // no provider configured: background jobs that call the model skip their turn quietly
func WriteError(w http.ResponseWriter, err error) // usage.ErrOwnerOnly→403 ai_owner_only; usage.ErrQuota→429 ai_quota;
    // *paper.ValidationError→400 bad_request; *paper.HaltedError→409 conflict;
    // paper.ErrInsufficient→400 insufficient_buying_power; market.ErrUpstream / yahoo.ErrUnavailable / llm.ErrUpstream→502 upstream;
    // pgx.ErrNoRows / app.ErrNotFound→404 not_found; app.ErrForbidden→403 forbidden; *app.BadRequest→400 bad_request;
    // *app.Conflict→409 conflict; a cancelled request→499; else 500 internal (logged, generic message)
var ErrNotFound = errors.New("not found")
type BadRequest struct{ Msg string } // func (e *BadRequest) Error() string
type Conflict struct{ Msg string }   // func (e *Conflict) Error() string; Msg is shown to the user

// package httpx
func JSON(w http.ResponseWriter, status int, v any)
func Error(w http.ResponseWriter, status int, code, msg string)
func Decode(w http.ResponseWriter, r *http.Request, v any) error // 64 KiB MaxBytesReader; returns *app-agnostic error text on bad JSON
func UserID(ctx context.Context) string                         // set by auth.Service.Require
type SSE struct{ /* ResponseWriter + Flusher */ }
func NewSSE(w http.ResponseWriter) (*SSE, error)                 // sets text/event-stream, no-cache, X-Accel-Buffering: no; writes headers
func (s *SSE) Send(event string, data any) error                 // event: x\ndata: json\n\n + flush
func (s *SSE) Ping() error                                       // ": ping\n\n" + flush (keepalive)

// package auth
func (s *Service) Require(next http.Handler) http.Handler        // 401 unauthorized if no valid session
// usage in features: mux.Handle("POST /api/forecasts", d.Auth.Require(http.HandlerFunc(h.create)))

// package usage
type Credits struct { Used int `json:"used"`; Limit int `json:"limit"`; ResetsAt time.Time `json:"resetsAt"` }
var ErrQuota = errors.New("ai_quota")
var ErrOwnerOnly error // wraps ErrQuota: a non-admin while Meter.OwnerOnly() holds (main: provider "chatgpt")
const (CostChat = 2; CostCopilot = 1; CostDraft = 1; CostForecast = 5; CostMaterial = 1; CostStrategy = 2; CostPortfolio = 2)
func (m *Meter) Spend(ctx context.Context, userID string, cost int) (Credits, error) // atomic; ErrQuota if over limit
func (m *Meter) Refund(ctx context.Context, userID string, cost int)               // best effort, when the model call fails outright
func (m *Meter) Get(ctx context.Context, userID string) (Credits, error)

// package market
type AssetInfo struct{ Symbol, Pair, Name string; Class string; Venue string }
var Universe []AssetInfo // 245 configured; CryptoUniverse 69 / StockPerps 38 (21 US + 17 Asian) / QuoteFeeds 138 / Core12 preserved
var Listed []AssetInfo   // 604 listed Asian quote feeds (listed.json), outside Universe: Symbol = Pair = Yahoo ticker
func Instruments() []AssetInfo // Universe then Listed (849), a new slice; ByYahoo(ticker) finds one by its Yahoo listing
func (a AssetInfo) IsListed() bool // also Asset.IsListed; ListedExcluded(ticker) (reason, ok); ListedIndex(symbol); ListedPattern
func LookupCrypto(symbol string) (AssetInfo, bool) // forecast-only boundary
func CryptoSymbolList() string // 69; SymbolList() is the full named 245 (byte-identical: testdata/symbollist-245.golden)
func Lookup(symbol string) (AssetInfo, bool)       // case-insensitive, returns canonical upper-case Symbol: Universe, then Listed
func ValidInterval(iv string) bool                 // 15m 1h 4h 1d
var ErrUpstream = errors.New("market data unavailable")
type Asset struct { Symbol, Pair, Name, Class, Venue, Range24hSource string; Price, Change24hPct, High24h, Low24h, Volume24hUsd float64; Session *string; SessionChangeAt *time.Time; Halted bool } // camelCase json tags
    // Halted (json "halted", omitempty): ticker closeTime over 1 h old; Price is the frozen last trade (contract §2)
type Candle struct { T int64; O, H, L, C, V float64 }                                                   // json t,o,h,l,c,v
func NewClient(baseURL string) *Client             // "" → https://data-api.binance.vision; main passes BINANCE_BASE
func (c *Client) Assets(ctx context.Context) ([]Asset, error)          // cache 10s; halted crypto included, flagged; session cloned on read; FreshAssets(ctx) forces both venue marks fresh
func (c *Client) Price(ctx context.Context, symbol string) (float64, error)
func (c *Client) Quote(ctx context.Context, symbol string) (Asset, error) // a universe pair Assets left out → ErrUpstream (an unread quote feed is read first)
func (c *Client) FillAssets(ctx context.Context, symbols []string) (map[string]Asset, error) // quote feeds re-read when over 60 s old (the real-time source first); paper's marks overlay it
func (c *Client) UseRealtime(adapters []rtquote.Adapter, base string) // the real-time chains (rtquote.Enabled), base "" = each adapter's Host
func (c *Client) RunRealtime(ctx context.Context) // the realtime_quotes task: every RealtimeTick (1 s) the due venues' polls
func (c *Client) QuoteSources() string             // X-Quote-Sources: "krx=naver ok 2s; hkex=…"; QuoteSourcesDetail() []VenueSource the detail route
func QuoteLabel(class string, delaySeconds float64, lastTrade time.Time) string // "real-time · 04:03:12 UTC" | "about 2 min behind · 04:03 UTC" | "delayed · last trade 04:03 UTC"
func (c *Client) RunFeeds(ctx context.Context) // the quote_feeds task: every 15 s, Interest (≤ 1/min) then a refresh of what is due
func (c *Client) Touch(symbols ...string)      // hot for 10 min (charts, analysis, assets with=, Quote)
func (c *Client) Candles(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) // 1..3000 (paginates by 1000), cache 30 s
func (c *Client) CandlesBefore(ctx context.Context, symbol, interval string, end time.Time, limit int) ([]Candle, error)
    // the newest 1..3000 bars that opened before end, oldest first; pages back by endTime; uncached (history is fixed)
func (c *Client) Analyze(ctx context.Context, symbol, interval string) (Analysis, error)
func AnalyzeCandles(symbol, interval string, bars []Candle, daily []float64) Analysis        // pure Analyze over given
    // (e.g. closed-only) bars; bars non-empty, the last close is the price
func (c *Client) Outlook(ctx context.Context, symbol string, horizonDays int) (PriceOutlook, error)     // 1..365
func (c *Client) LongOutlook(ctx context.Context, symbol string, horizonDays int) (PriceOutlook, error) // 1..3650 (chat)
type Analysis struct{ /* contract §3 Analysis, json camelCase; Sma200 *float64 */ }
type Bucket struct { Label string; Lo, Hi *float64; P float64 }       // json label, lo, hi, p
type PriceOutlook struct { Symbol string; Spot float64; HorizonDays int; VolAnnPct float64; Buckets []Bucket; P5, P50, P95 float64
    VolBasis *VolBasis; LongHorizon *LongHorizon }                    // contract §PriceOutlook; long view past 365 days
func OutlookFrom(symbol string, spot, volAnn float64, horizonDays int) PriceOutlook // pure
func OutlookCandles(symbol string, daily []Candle, horizonDays int) PriceOutlook    // pure: 90d σ to 365 days, long-run past it
func RealizedVolAnn(dailyCloses []float64) float64                                  // pure, annualized with √365
// perf.go — pure equity-curve metrics, 365-day year, zero risk-free rate
func BarsPerYear(interval string) float64                                           // 15m 35040, 1h 8760, 4h 2190, 1d 365; else 0
type EquityStats struct{ Sharpe, Sortino, VolAnnPct, MaxDrawdownPct float64 }
func Stats(equity []float64, interval string) EquityStats // one mark per bar; Sortino uses √(Σ min(r,0)²/N), clamped ±100
func AnnualizedPct(first, last, days float64) *float64     // compounded %; nil under 30 days; −100 when last ≤ 0
// returns.go — pure return statistics over aligned daily series (NaN = no value that day); shared by the portfolio review
func Returns(closes []float64) []float64                        // simple returns, len−1; NaN around a NaN or non-positive close
func Correlation(a, b []float64, minN int) (rho float64, ok bool) // Pearson over the days both have; false under minN or a flat series
func Beta(x, bench []float64, minN int) (beta float64, ok bool)   // cov/var over the days both have; false under minN or a flat bench
// derivatives.go — Hyperliquid perp context (contract "Market data"); main sets c.HyperliquidBase = HYPERLIQUID_BASE
type Derivatives struct{ /* contract Derivatives */ }
func (c *Client) Derivatives(ctx context.Context, symbol string) (*Derivatives, error) // nil = no live perp; one
    // default-dex metaAndAssetCtxs answer/failure cached 60s for crypto; xyz answer 10s shared with Assets; overflow/null data unavailable; markettest answers both POST /info dex routes
// pure indicator series (len == len(input), math.NaN() during warm-up)
func Closes(c []Candle) []float64
func SMA(v []float64, n int) []float64
func EMA(v []float64, n int) []float64
func RSI(v []float64, n int) []float64              // Wilder
func ROC(v []float64, n int) []float64              // percent
func ATR(c []Candle, n int) []float64               // Wilder
func Bollinger(v []float64, n int, k float64) (upper, mid, lower []float64)
func MACD(v []float64, fast, slow, signal int) (macd, sig []float64)
func Highest(v []float64, n int) []float64          // max of the previous n values, excluding current
func Lowest(v []float64, n int) []float64           // min of the previous n values, excluding current

// package llm  (every wire: effort is sent only when the preset or settings ask for it; no temperature/top_p/thinking
//               budget/forced tool choice; assistant content, signed or encrypted reasoning included, is replayed unchanged
//               inside a tool loop (a Flatten run replays its turns as text) and never outside the run; consecutive
//               same-role messages are merged)
type Settings struct {
    Provider        string      // a Providers id; "" is off
    Model           string      // "" → the preset's default
    BaseURL         string      // "" → the preset's default (bedrock: the endpoint override)
    APIKey          string      // never logged; optional for local servers
    Effort          string      // "" → the preset's default | "on" | "off"
    AWSRegion       string      // bedrock
    KnowledgeCutoff time.Time   // zero → DefaultKnowledgeCutoff
    ChatGPT         TokenSource // chatgpt: the signed-in plan's access tokens (aiconfig implements it)
}
var Providers []ProviderInfo // the 13 presets; ProviderInfo{ID, Label, Wire chat|messages|responses|converse, DefaultBaseURL,
                             // DefaultModel, KeyEnv, NeedsKey, EffortDefault, KeyURL, Note}; ProviderByID(id)
var ErrNotConfigured error   // wraps ErrUpstream: every call's error while no provider is set
type ProviderError struct{ Status int; Code, Msg string } // an explicit rejection by the provider; wraps ErrUpstream
func NewFromSettings(s Settings) (*Client, error)      // main starts with Settings{} (off); aiconfig.Load configures it
func New(cfg aws.Config, model string) *Client          // a Bedrock client (llmtest and many tests use it)
func (c *Client) Configure(s Settings) error            // validates and swaps atomically; in-flight calls keep their provider
func (c *Client) Settings() Settings
func (c *Client) Configured() bool
func (c *Client) Model() string
func (c *Client) KnowledgeCutoff() time.Time
func (c *Client) Test(ctx context.Context) error        // one tiny completion, no retries; a max_tokens stop passes
func ListModels(ctx context.Context, s Settings) ([]ModelInfo, error) // s need not be saved
func DecodeJSON(text string, out any) error             // the first JSON object in a model reply
type Effort string
const (Low Effort = "low"; Medium Effort = "medium"; High Effort = "high")
var ErrUpstream = errors.New("ai provider unavailable")
type Tool struct {
    Name, Description string
    Schema map[string]any                                           // JSON Schema (type: object)
    Run    func(ctx context.Context, input json.RawMessage) (string, error) // result text for the model; error → sent as error tool result
}
type Message struct { Role, Text string; Images []Image; Documents []Document } // Role "user" | "assistant"
// The current user turn may carry transient Images []Image{Format png|jpeg|webp|gif; Bytes} and
// Documents []Document{Format pdf|csv|txt|md|html|docx|xlsx; Name (Converse-safe, unique); Bytes};
// historical turns are always sent as text only (internal/attach validates and names them). Wires without native
// docx/xlsx support get the extracted text (docs.go).
type Event struct {
    Kind      string          // "delta" | "tool_call" | "tool_result"
    Text      string          // delta
    ToolID    string
    ToolName  string
    ToolInput json.RawMessage
    Output    string          // tool_result
    IsError   bool            // tool_result
}
type AgentRequest struct { System string; Messages []Message; Tools []Tool; Effort Effort; MaxTurns int /*default 6*/; MaxTokens int /*default 8192*/
                           MaxRequests int; BeforeRequest func(context.Context) error; OnRequestDone func(RequestOutcome)
                           Flatten bool; Visible func() string; Nudge func() string; OnProviderEvent func(); NoRetries bool }
    // MaxRequests caps all provider calls of the run (0 = unbounded); BeforeRequest runs before each and can abort it;
    // OnRequestDone and OnProviderEvent observe actual provider outcomes and raw events; NoRetries: one attempt per turn.
    // Flatten (chat): never send tool call/result blocks — Bedrock withholds the whole streamed turn after a toolResult.
    // After a tool round the next call re-sends Messages with a <run_transcript> (the text the user saw, per Visible; each
    // tool call and its <tool_result>-wrapped output) appended to the last user message; tools stay declared, signed
    // reasoning is not replayed. Entry text cannot forge wrapper tags; remaining &, < and > are escaped.
    // Nudge: called when the model ends the run (also at Flatten's exhausted tool budget); a non-empty note buys
    // one more turn (once), without granting more tool rounds or consuming one before any tools run.
func (c *Client) Complete(ctx context.Context, system, prompt string, effort Effort) (string, error)
func (c *Client) CompleteWith(ctx context.Context, system string, m Message, effort Effort) (string, error) // one turn with an image or document
func (c *Client) CompleteJSON(ctx context.Context, system, prompt string, effort Effort, out any) error // parses first {...} object; one retry with the parse error
func (c *Client) CompleteJSONWith(ctx context.Context, system string, m Message, effort Effort, out any) error
func (c *Client) Agent(ctx context.Context, req AgentRequest, onEvent func(Event) error) (string, error)
    // streams a turn, runs the tools it asked for, feeds the results back, repeats; returns the text of the FINAL turn;
    // after MaxTurns it asks for a final answer; the whole run stays on the provider it started with; safe for
    // concurrent calls (each Bedrock stream gets its own event-stream codec)

// package paper
type OrderRequest struct { Symbol, Side string; Notional, Qty, StopLoss, TakeProfit *float64; Source, Note string }
type Trade struct{ /* contract Trade */ }
type Position struct{ /* contract Position */ }
type Portfolio struct{ /* contract Portfolio */ }
type ValidationError struct{ Msg string }
var ErrInsufficient = errors.New("insufficient buying power")
type HaltedError struct{ Symbol string } // an order that opens, adds to or flips a position in a halted pair
func NewEngine(db *pgxpool.Pool, m *market.Client) *Engine
func (e *Engine) Order(ctx context.Context, userID string, req OrderRequest) (Trade, Portfolio, error) // serialized per user (SELECT … FOR UPDATE on accounts)
func (e *Engine) Orders(ctx context.Context, userID string, reqs []OrderRequest) ([]Trade, Portfolio, error) // atomic basket; rolls back every leg on error
func (e *Engine) Portfolio(ctx context.Context, userID string) (Portfolio, error)
func (e *Engine) Trades(ctx context.Context, userID string, limit int) ([]Trade, error)
func (e *Engine) Close(ctx context.Context, userID, symbol, source string) (Trade, Portfolio, error)
func (e *Engine) Reset(ctx context.Context, userID string) (Portfolio, error) // needs no market data
func (e *Engine) RunWatcher(ctx context.Context)    // every 15 s: close positions whose stop/target is touched
func (e *Engine) Register(mux *http.ServeMux, require func(http.Handler) http.Handler)
// pure fill rules, shared with simulations (accounting.go)
const Slippage = 0.0002 // 2 bps against every fill
const FeeRate = 0.001   // 0.1% of notional per fill
func FillPrice(mark float64, buy bool) float64     // mark ± Slippage
type Fill struct { Qty, AvgPrice, Realized, CashDelta, Fee float64 } // resulting position; Realized is net of Fee
func Book(qty, avg, tradeQty, price float64) Fill  // average-cost accounting for a signed trade at price
// OrderRequest.RefID *string (json:"-", server-only: aipf invest legs → trades.ref_id);
// Trade.Ref *TradeRef {Kind chat|portfolio|autopilot, ID, Label}, filled by Trades (history.go refs), null elsewhere
// source "alert" (alerts fills, RefID = the alert → Ref {Kind alert, Label = note}); Money(x) formats prices for messages
func CheckProtection(qty, mark float64, stop, target *float64) error // long stop < mark < target, short the reverse;
    // ValidationError naming the side and mark; PATCH /api/portfolio/positions/{symbol} and chat set_protection share it
type RefPnl struct { Amount float64; Trades int; FirstAt time.Time; RealizedPnl, UnrealizedPnl float64 }
func (e *Engine) ByRef(ctx context.Context, userID string) (map[string]RefPnl, error) // per ref_id: the FIFO lots
    // its trades opened (attribute keyed by ref instead of source), at live marks; aipf's `invested`
// portfolio health (contract "Portfolio health"), 0 credits
type Review struct{ /* contract PortfolioReview, json camelCase; Holdings []Holding, Concentration *Concentration, VaR *VaR, … */ }
func (e *Engine) Review(ctx context.Context, userID string, now time.Time) (Review, error) // positions at live marks + the last
    // 90 daily returns of the held pairs and BTC (closed rows only, actual gaps stay NaN), requiring at least 20 common
    // observations for whole-book risk. Every nonzero long/short holding is material; missing marks/history suppress
    // aggregate claims while known exposures, stops, prediction stakes and supported pair comparisons remain available.
    // Holdings, revision and 30d equity replay use one captured ledger snapshot, released before HTTP; core mark
    // failure returns market.ErrUpstream, daily/BTC/replay history failure degrades coverage or drawdown to unavailable.
    // The free chat tool reuses these facts, adds no cards/mutations, and permits at most one warranted confirmable action.
func (e *Engine) Overlap(ctx context.Context, userID string, basket map[string]float64, now time.Time) (Overlap, error)
    // response-local caller holdings/weights and basket comparisons; AIPF detail never persists it in static JSON/caches

// package research — full contract in the doc comment of internal/research/research.go
var ErrUnavailable error // search disabled / over the daily budget / Brave failed → answer "search unavailable"
var ErrBadURL error      // ReadPage: not a public http(s) URL on port 80/443
type Result struct { Title, URL, Source, Description string; PublishedAt *time.Time }   // json camelCase
type Page struct { URL, Title, Text, Via string }                                        // Via "cache" | "crawl4ai" | "direct"
type NewsItem struct{ /* contract NewsItem */ }
type NewsFilter struct { Symbol, Topic string; Since time.Duration; Limit int }
type MarketBrief struct{ /* contract MarketBrief */ }                                   // Sentiment []Sentiment{Symbol, Score, Label, Reason}
func New(cfg Config, db *pgxpool.Pool, ai *llm.Client) *Service                          // ai nil → no brief
func (s *Service) Enabled() bool                                                         // Brave key present
func (s *Service) SetSearch(braveKey, crawlBase, crawlToken string)                      // aiconfig: at boot and on every save
func (s *Service) SearchNews(ctx context.Context, query, freshness string, count int) ([]Result, error) // freshness pd|pw|pm|""
    // or "YYYY-MM-DDtoYYYY-MM-DD" (valid dates, from ≤ to); count 1..20
func (s *Service) SearchWeb(ctx context.Context, query string, count int) ([]Result, error)
func (s *Service) ReadPage(ctx context.Context, url string, maxChars int) (Page, error)   // cleaned text ≤ maxChars (≤ 20,000); pass a deadline
func (s *Service) Headlines(ctx context.Context, symbol string, since time.Duration, limit int) ([]NewsItem, error) // stored; no Brave call
func (s *Service) News(ctx context.Context, f NewsFilter) ([]NewsItem, error)
func (s *Service) LatestBrief(ctx context.Context) (*MarketBrief, error)                 // nil before the first brief
func (s *Service) Ingest(ctx context.Context) error                                      // one run; never two at once
func (s *Service) Run(ctx context.Context)                                               // the 2 h loop (main)
func (s *Service) Register(mux *http.ServeMux, require func(http.Handler) http.Handler)
func Untrusted(text string) string                                                       // <untrusted_web_content> wrapper for prompts
func Known(url string) bool                                                              // recognized outlet: ranked first
func Relevant(title, description, url, source, symbol string) bool                       // a stored wire headline may reach an AI surface
    // (filter.go: not junk, title about markets, names symbol); signals calls it on read for the headlines its boards store
```

Accounts rows are created lazily (`insert … on conflict do nothing`) on first portfolio/order access.

### Admin console (contract §3 "Admin console")
```go
// package admin — Register mounts the whole "/api/admin/" prefix once, behind adminOrigin (ADMIN_ORIGINS) and
//   d.Auth.RequireAdmin, on a sub-mux.
//   Mutations: one pgx transaction each = lock the target row (select … for no key update, which lets paper's FK
//   checks through) + change + admin_audit row(s). Copy/disable/delete lock order is autopilot advisory (when needed) → user FOR NO KEY UPDATE → account → copy books.
//   Demote / disable / delete first lock the active admin rows in id order (keepAdmin): 409 when no other active
//   admin would remain, 403 (app.ErrForbidden) when the caller is no longer an active admin. Self → 409 before that.
//   Overview and user detail are one pgx.Batch round trip each (named args: UTC day windows + latest marks).
// package auth
var BcryptCost = 12                                               // tests lower it to bcrypt.MinCost
func HashPassword(password string) (string, error)                // bcrypt at BcryptCost
func TokenHash(r *http.Request) ([]byte, bool)                    // SHA-256 of the tsid cookie (the caller's session)
func (s *Service) Require(next http.Handler) http.Handler         // one statement: live session of an enabled user,
    // touches users.last_active_at at most once per 5 min
func (s *Service) RequireAdmin(next http.Handler) http.Handler    // Require + role 'admin' (403 "Admins only."),
    // Cache-Control: no-store, non-GET limited to 60 per admin per 10 min (429 rate_limited)
func (s *Service) EnsureUser(ctx context.Context, email, name, password, role string, initiallyEnabled ...bool) (User, error)
    // boot bootstrap (initiallyEnabled seeds the new user's profile): create when missing, else refresh only a changed password hash (and delete its sessions);
    // never touches role, name, credit_limit or disabled_at (a mismatch with the config is logged)
// type User has Role ("user" | "admin"); login rejects disabled accounts like a wrong password and sets last_login_at
// package usage — Spend/Get read coalesce(users.credit_limit, default) inside their one statement
func (m *Meter) DefaultLimit() int                                // AI_DAILY_CREDITS
// package paper
func ResetTx(ctx context.Context, tx pgx.Tx, userID string) error // Reset's body in the caller's transaction
// package research (status.go)
func (s *Service) BraveUsage() (used, limit int)                  // this UTC day, in memory
func (s *Service) Crawler(ctx context.Context) CrawlerStatus      // {base, configured, reachable *bool}: GET {CRAWL_BASE}/health, 3 s, cached 60 s
// package app — Deps has Version (main.commit via -ldflags "-X main.commit=<sha12>", which the Dockerfile sets from its
//   COMMIT build arg; else vcs.revision; else "dev"), StartedAt and Managed (lower-cased DEMO_EMAIL / ADMIN_EMAIL);
//   var ErrForbidden → 403 forbidden "Admins only."
// package aiconfig — admin.Register calls d.Settings.Register(sub): the settings endpoints sit behind the same gate.
//   Audit actions settings.update, settings.chatgpt.signin, settings.chatgpt.signout (detail: changed field names only).
//   GET /auth/callback is outside /api/admin: the single-use sign-in state stands in for a session.
```

## Testing

DB tests read `DATABASE_URL_TEST` (a Postgres URL, e.g. `postgres://trade:trade@127.0.0.1:5432/trade_test?sslmode=disable`)
and skip when it is unset. Compose and CI run Postgres 18. Pure logic (indicators, outlook, backtest, accounting,
validation, JSON extraction) gets plain `go test` tests. Tests never touch the network. Shared fakes:
`internal/llm/llmtest` (a Bedrock Converse/ConverseStream server the real SDK client talks to via
`aws.Config.BaseEndpoint`; `Request.AfterTools` marks a call after a tool round in either mode,
`Reply.Deltas`/`AfterFirst` script and pause a stream; the HTTP wires test against `httptest` servers in their own
`*_test.go` files), `internal/market/markettest` (Binance ticker + klines and Hyperliquid; `SetPrice` moves a ticker
price) and `internal/app/apptest` (a full `app.Deps` on a throwaway schema from `storetest.Schema`, a signed-in user,
request helpers) for handler tests. CI (`.github/workflows/ci.yml`, job "API unit tests"): gofmt, `go vet`,
`go test -race` with coverage on Postgres 18, failing under 75% total.

Integration suite (`cmd/api/integration_test.go` harness, `integration_flows_test.go` tests): each `TestIntegration*`
sets the environment, calls `config.Load` and `run` like `main`, on a 127.0.0.1:0 listener and a fresh schema of
`INTEGRATION_DATABASE_URL` (skips when unset) that the app migrates. Upstreams are local: `llmtest` through
`AI_PROVIDER=bedrock` and `AWS_ENDPOINT_URL_BEDROCK_RUNTIME` (static test credentials, no AWS config files, one
attempt), `markettest` through
`BINANCE_BASE`, and a fake Brave (`BRAVE_BASE`) and crawl4ai (`CRAWL_BASE`) whose article URLs are public IP
literals, so the SSRF guard needs no DNS and the direct fetch is never dialled. Users are inserted with bcrypt at
MinCost and log in over HTTP (the cost-12 demo user only in `TestIntegrationAuth`). Server logs go to a per-test
buffer printed on failure. CI's "API integration tests" job (same workflow) runs `go test -race -json ./cmd/api/` on
Postgres 18, and `api/ci/check_go_results.py` (stdlib Python, tests in `api/ci/test_check_go_results.py`) fails on any
failed or skipped test and on a missing name from its `REQUIRED_TESTS`.

## Agent desk (contract "Agent chat" → "Agent desk")
```go
// package forecast (export.go) — the chat's draft_forecast tool; the caller spends (and refunds) the draft credit
func DraftIdea(ctx context.Context, d *app.Deps, idea string) (Draft, error)
// package chat
//   desk.go    report reads price_rule and result.market (opaque JSON, passed through); forecastRef.lean() "No 54%",
//              citeAs() "… (leans No)", stance(symbol, side) from a price rule on the trade's symbol
//   web.go     sourceRef.Kind wire | news | web | page (the tool that numbered it first)
//   actions.go draft_forecast (1 credit, one per message), build_portfolio, backtest_strategy (no spend) → cards;
//              actionStatus fills forecast_draft.forecast / build_action.result on GET /api/chats/{id}
//              (ponytail: matched by question / prompt text and time; upgrade = an actionId stored on the entity)
```

Stored briefs and signal boards retain complete immutable model-input provenance internally. Reads suppress an
entire unverifiable result, including summary/sentiment/rationale and signal history, until normal scheduled
regeneration; they make no repair calls. The signals board falls back to the newest stored board that still
validates, and its job replaces an unverifiable one within minutes (boot, a 5-minute watchdog, or a read's nudge). Cached page reads reject junk publishers while retaining legitimate
explicit nonfinancial research, and ingestion skips rejected legacy wire rows before spending crawl slots.

## Live arena and copy delivery

`internal/arena/live.go` owns the singleton three-persona desk, pre-inference durable claims (`live_attempts`),
closed 4h cadence, post-inference market refresh, bench and bounded board/page reads. `copy.go` owns plan activation
cursors, recovery from committed steps, proportional ownership reconciliation and stop. `copy_deliveries` uniquely
records every plan/step/leg together with its fill or skip; a whole step and every reconciled book commit atomically.
At most 150 pending plan/step deliveries are visited per tick. Admin disable calls `StopCopiesForDisabled` in its
disable/audit transaction; its user row lock serializes with recovery even while off.

`paper.Engine.LockTx(ctx, tx, userID, quotes)` returns a retained account-lock `Ledger` with `Portfolio` and `Order`.
The caller locks the user first and owns commit/rollback; it commits automation books/cursors with engine fills.
Manual orders, reset and the watcher retain the same account lock. Autopilot deployment checks core/copy exclusion
in its user/account transaction; admin acquires `autopilot.LockTx` before users to avoid the reverse lock order.

`market.Client.FreshAssets` refreshes the shared quote cache after model inference. The public board omits provider
IDs, exposes lastError/stale/history bounds and uses the latest 360 bars for risk/trading metrics. Total return and
annualized return stay since launch. Admin's claimed-attempt ×4 request ceiling is a bound, not observed spend.

Live board and plan reads use read-only repeatable-read transactions. `paper.Engine.ByRefTx` reads FIFO attribution
from that same snapshot at the board's supplied quote marks, without account locks or lazy-account creation. A shared
step and its follower delivery remain separate durable commits; reads may observe delivery pending, but cannot mix
pre-commit books/metadata with post-commit fills or bars.

## Yahoo feed

`internal/yahoo` reads `query1.finance.yahoo.com` without a key. Consumers take `d.YahooSource()` (`app.Deps.Yahoo`,
`yahoo.Disabled` when unset) and code against `yahoo.Source`: `Chart(ctx, symbol, range, interval)`, `Quotes(ctx,
symbols)` / `Quote`, `FX(ctx, ccy)` and `FXHistory(ctx, ccy, range)`. Quotes batch 20 symbols per v7 spark call
(keyless, verified 2026-10-06) and fall back to per-symbol 5d charts when spark is unreadable. `range=max` (daily
or weekly) is fetched as `period1=0&period2=now`, because Yahoo's own `max` serves monthly bars. FX uses the pair's
newest daily close, since the meta price is rounded to 4 decimals (IDRUSD=X reads 0.0001). Every default client
shares one gate: 2 req/s (burst 4), 4 in flight, 10 s per attempt, two retries of a 429 or 5xx, and a 3-minute
breaker after 5 consecutive 429s. Caching: quotes and FX 60 s, daily charts 1 h, weekly/monthly/max 12 h, not-found
15 min, bounded to about 1M cached bars; `yahoo.NoCache(ctx)` keeps a Chart out of it (batch jobs). Identical
concurrent calls share one fetch. Failures are
`*yahoo.UnavailableError` (`errors.Is yahoo.ErrUnavailable`, `NotFound` for unserved symbols). WriteError maps them
to 502 upstream and 404 respectively. Bodies are never logged. Pure helpers: `AllTimeHigh`, `TrailingReturns` /
`ReturnPct`, `VolatilityPct` (√252), `WeeklyCloses`, `SMA`, `RSI` (Wilder), `DrawdownPct`, `ToUSD`. `YAHOO_BASE`
points the client at `yahootest` (apptest wires a `Fake`; the integration boot serves one, read through a
`Config{Unpaced: true}` client: its own gate, no pacing).
Chat's `get_stock_quote` (`internal/chat/quote_tool.go`) is a consumer: one `Quote` plus one `Chart(max, 1d)` per call,
FX for a non-USD listing; `describeUniverse` (tools.go) computes the prompt's universe line from `market.Universe`.
`lens.go` validates the request's `lens` and adds the message's lens line (the user's choice, or for auto the hint of
`profile.StyleHorizon`, the saved style's horizon).

Quote-feed stocks (`market/universe_quotefeed.go`, venue `yahoo`): 138 rows (67, then the 71 Singapore, Taiwan and Korea
focus rows) appended to `Universe` in `init` after every perp (`QuoteFeeds`; `AssetInfo.QuoteFeed()`; `Perp()` stays true
for them, as for every stock). Listed instruments (`market/universe_listed.go`): 604 more quote feeds, loaded strictly
from the embedded `listed.json` that `cmd/genlisted build` writes (offline, from `screener.Stocks()`, `market.Universe`,
`screener.ADRFor` and the measurements; append-only), kept out of `Universe` so no prompt, enum or batch job sees them;
Lookup, Instruments, the poller, Assets (after the named rows), the screener's trade map, get_stock_quote and the
community Sharpe correction do. `market/quotefeed.go`
prices them through `Client.Yahoo` (main and apptest set it to `d.Yahoo`; nil leaves them out): spark quotes × FX,
by tier (`feedState.due`: named and hot rows 1 min open / 15 min closed; other listed rows 5 / 15, held after a settled
close to the next open on a verified calendar), at most 20 spark calls per refresh in priority order (`plan`), one
detached refresh at a time (`feedState.running`; a read waits ≤5 s only for named or hot rows, each 20-symbol batch
lands with its FX), a failed row waits 30 s (`failedAt`), the open breaker pauses the feed (`yahoo.IsPaused`), spark off
(`yahoo.Batcher`) reads named and hot rows only, FX 5 min while a venue is open and 30 min otherwise; `RunFeeds` (task
`quote_feeds`, interest from main's `feedInterest` query) keeps it warm; `FillAssets` re-reads before a paper fill.
`Asset.QuoteFeed` (local price, FX, asOf, marketState), stale after 4 days or 20 min unread while open (`Halted`,
which paper turns into a refusal of every order); candles from `Chart` in USD (`1d` stamped 00:00 UTC of the local
date, `4h` resampled from 60m), cached 15 min while the session is closed. `yahoo.Meta` also reads
`chartPreviousClose` and the session's day high, low and volume.
The screener's watch tier (`market/listingquote.go`): `Client.Watch(rows)` keeps the constituents no quote-feed instrument
reads (a perp's or ADR's local line, a research-only row: 62 today) as non-instrument rows in `feedState.watch`, read
last in each `plan` (same batches and cap), each read setting its next one (`watchNext`: 15 min open, the settled read an
hour after the close, then the next open); `Client.ListingQuotes(tickers)` answers held quotes by Yahoo ticker from the
trade tier (`feedPairs`) or the watch tier with session, interval (`tradeDue`), stale time, the measured delay
(`YahooDelay`) and FX, reading nothing.
`market/session.go` adds SGX, SET and TWSE calendars (with an optional `since`/`until` day horizon) and the unknown
IDX, BURSA and HOSE venues. The Signals board (`signals.analyze`) and AI portfolio builds (`aipf.dailies`) leave the
quote feeds out without reading their Yahoo history.

## Universe history and saved benchmarks

HIP3 candle reads and cached hourly range fanout share a client admission capacity of three. Range admission happens
before goroutine creation; canceled waiters retain caller context, every owned request is joined and releases capacity,
and HTTP runs outside cache-state mutexes. The reviewed quote/FreshAssets and fetch-start candle-finality contracts
remain intact. Candles validate the provider's distinct millisecond open/close times, exact prefixed identity, interval,
finite OHLC/base-unit volume and duplicate/order/end behavior. Retained older history is honestly empty; no synthetic
bars or Binance substitution. Each perp's session follows its `Listing.SessionVenue` calendar in `market/session.go`
(NYSE 2026–2028; KRX, TSE and HKEX 2026–2027, lunch breaks closed, HKEX eve half-days) with embedded tzdata and
unknown states outside a venue's horizon. Session closure does not halt paper orders.

`aipf.historyBatch` owns three joined workers per operation; the shared market-client gate also bounds overlapping
HIP3 requests across operations. `chooseBenchmark` uses normalized stock/index weight strictly above 0.5 for SP500,
otherwise BTC. `backtest(hs, closes, benchmark) (Backtest,error)` intersects actual closed dates and uses the latest
consecutive daily window. It persists identity/start/end/count in JSONB, with volatility null below 31 common closes.
Legacy loads default BTC without rewriting the stored JSON. `actual` consumes the saved benchmark and the known
candle grid; `sinceSeries` preserves fees, top-up time-weighting and first/last thinning. A first investment outside
retained benchmark history has no since-investment comparison; the portfolio line may show its explicit retained
window. `btcReturnPct`/`btcSeries` remain BTC-only. No SQL or dependency change is required.
