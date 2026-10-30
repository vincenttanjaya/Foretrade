// Command api is the Foretrade backend: a self-hosted paper-trading desk
// with an AI agent, copilot, forecast desk, strategy studio, AI portfolios
// and AI backtest arena, on the AI provider the admin console configures.
package main

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/admin"
	"github.com/vincenttanjaya/Foretrade/api/internal/aiconfig"
	"github.com/vincenttanjaya/Foretrade/api/internal/aipf"
	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/arena"
	"github.com/vincenttanjaya/Foretrade/api/internal/auth"
	"github.com/vincenttanjaya/Foretrade/api/internal/autopilot"
	"github.com/vincenttanjaya/Foretrade/api/internal/chat"
	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/copilot"
	"github.com/vincenttanjaya/Foretrade/api/internal/forecast"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
	"github.com/vincenttanjaya/Foretrade/api/internal/notify"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/screener"
	"github.com/vincenttanjaya/Foretrade/api/internal/signals"
	"github.com/vincenttanjaya/Foretrade/api/internal/store"
	"github.com/vincenttanjaya/Foretrade/api/internal/strategy"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// commit is the build's git commit, set by the Dockerfile's COMMIT build arg: -ldflags "-X main.commit=<sha>".
var commit string

func main() {
	time.Local = time.UTC // pgx scans timestamptz in time.Local; the API serializes every time in UTC ("…Z").
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := start(ctx); err != nil {
		slog.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

// start loads the configuration and serves on PORT (and ADMIN_PORT when set)
// until ctx ends.
func start(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		return err
	}
	var o runOptions
	if cfg.AdminPort != "" {
		if o.admin, err = net.Listen("tcp", ":"+cfg.AdminPort); err != nil {
			ln.Close()
			return err
		}
	}
	return runWithOptions(ctx, cfg, ln, o)
}

// run wires the app from cfg and serves it on ln until ctx ends. The
// integration tests boot it on 127.0.0.1:0 with fake upstreams.
func run(ctx context.Context, cfg config.Config, ln net.Listener) error {
	return runWithOptions(ctx, cfg, ln, runOptions{})
}

// runOptions observes actual production work; zero values preserve run's wiring.
// The worker hook wraps, rather than replaces, each actual Run method.
type runOptions struct {
	worker          func(string, func())
	event           func(string)
	shutdownTimeout time.Duration
	deps            func(*app.Deps)
	admin           net.Listener // ADMIN_PORT's listener (serve.go); nil serves everything on ln
}

func (o runOptions) emit(event string) {
	if o.event != nil {
		o.event(event)
	}
}

func runWithOptions(ctx context.Context, cfg config.Config, ln net.Listener, o runOptions) error {
	defer ln.Close() // Serve closes it too; this covers a failed boot
	if o.admin != nil {
		defer o.admin.Close()
	}
	started := time.Now().UTC()
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { o.emit("pool_close_begin"); pool.Close(); o.emit("pool_close_done") }()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}

	mkt := market.NewClient(cfg.BinanceBase)
	mkt.HyperliquidBase = cfg.HyperliquidBase
	meter := usage.NewMeter(pool, cfg.AIDailyCredits)
	authSvc := auth.NewService(pool, meter, cfg.CookieSecure)
	authSvc.TrustedProxies = cfg.TrustedProxies
	managed, err := ensureAccounts(ctx, authSvc, cfg)
	if err != nil {
		return err
	}
	engine := paper.NewEngine(pool, mkt)
	ai, err := llm.NewFromSettings(llm.Settings{}) // off until the settings load
	if err != nil {
		return err
	}
	meter.OwnerOnly = func() bool { return ai.Settings().Provider == "chatgpt" } // the plan is the owner's alone
	intel := research.New(research.Config{BraveBase: cfg.BraveBase, DailyLimit: cfg.BraveDailyLimit}, pool, ai)
	settings := aiconfig.New(pool, cfg, ai, intel)
	// puts the stored provider and search keys in force: a bad secret or provider is logged; only a database error stops boot
	if err := settings.Load(ctx); err != nil {
		return err
	}
	deps := &app.Deps{DB: pool, Market: mkt, LLM: ai, Usage: meter, Paper: engine, Auth: authSvc, Research: intel, Settings: settings,
		Version: apiVersion(), StartedAt: started, Managed: managed, AdminOrigins: cfg.AdminOrigins}
	deps.PolymarketBase = cfg.PolymarketBase
	deps.ForecastDeepResearch, deps.ForecastSearches = cfg.ForecastDeepResearch, cfg.ForecastSearches
	deps.ForecastMethods = cfg.ForecastMethods
	deps.LiveArena = cfg.LiveArena
	if cfg.NotifyEmailFrom != "" {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return err
		}
		deps.Mailer = notify.NewSES(awsCfg, cfg.NotifyEmailFrom)
	}
	deps.AppURL = cfg.AppURL
	deps.Yahoo = yahoo.New(yahoo.Config{Base: cfg.YahooBase})
	mkt.Yahoo = deps.Yahoo // prices and charts the quote-feed stocks
	mkt.Interest = feedInterest(pool)
	useRealtime(mkt, cfg)
	if cfg.ScreenerMaxTickers >= 0 {
		// the screener's constituents no quote feed reads: its own slow tier
		slog.Info("screener: watch tier", "listings", mkt.Watch(screener.QuoteWatch()))
	}
	tasks := app.NewTasks(ctx, o.event)
	ctx = tasks.Context()
	deps.Tasks = tasks

	if o.deps != nil {
		o.deps(deps)
	}
	launch := func(name string, work func()) {
		tasks.Go(name, func(context.Context) {
			if o.worker != nil {
				o.worker(name, work)
			} else {
				work()
			}
		})
	}
	launch("paper", func() { engine.RunWatcher(ctx) })
	launch("alerts", func() { alerts.Run(ctx, deps) })
	launch("research", func() { intel.Run(ctx) })
	researchScheduler := alerts.NewResearchScheduler(deps, chat.NewResearchRunner(deps), alerts.NewResearchStore(deps))
	launch("scheduled_research", func() { researchScheduler.Run(ctx) })
	launch("signals", func() { signals.Run(ctx, deps) })
	launch("forecast_recovery", forecast.Recover(ctx, deps))
	launch("arena_recovery", arena.Recover(ctx, deps))
	launch("live_arena", func() { arena.RunLive(ctx, deps) })
	launch("autopilot", func() { autopilot.Run(ctx, deps) })
	launch("screener", func() { screener.Run(ctx, deps, screener.Options{MaxTickers: cfg.ScreenerMaxTickers}) })
	launch("quote_feeds", func() { mkt.RunFeeds(ctx) })
	launch("realtime_quotes", func() { mkt.RunRealtime(ctx) })

	srv := &http.Server{
		Handler:           tasks.Handler(sites(handler(cfg.AllowedOrigins, deps), cfg, o.admin)),
		BaseContext:       func(l net.Listener) context.Context { return listenerContext(ctx, l, o.admin) },
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// WriteTimeout stays 0: chat streams over SSE for as long as the agent runs.
	}
	shutdown := make(chan error, 1)
	budget := o.shutdownTimeout
	if budget == 0 {
		budget = 20 * time.Second
	}
	go func() {
		<-ctx.Done()
		tasks.Stop()
		sctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		err := srv.Shutdown(sctx)
		if err != nil {
			slog.Warn("shutdown timed out; closing open streams", "err", err)
			o.emit("http_close_begin")
			err = errors.Join(err, srv.Close())
		}
		o.emit("http_shutdown_done")
		if drainErr := tasks.Wait(sctx); drainErr != nil {
			o.emit("drain_timeout")
			slog.Error("process drain timed out; waiting for actual completion", "err", drainErr)
			err = errors.Join(err, drainErr)
			tasks.Wait(context.Background())
		}
		o.emit("drain_complete")
		shutdown <- err
	}()
	warnCalendarHorizons(time.Now())
	slog.Info("listening", "addr", ln.Addr().String(), "ai", ai.Settings().Provider, "model", ai.Model(),
		"search", intel.Enabled(), "liveArena", cfg.LiveArena)
	adminErr := serveAdmin(srv, o.admin, tasks.Stop)
	serveErr := srv.Serve(ln)
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	tasks.Stop() // also cancels workers and joins shutdown after unexpected Serve failure
	return errors.Join(serveErr, <-adminErr, <-shutdown)
}

// useRealtime switches on the real-time quote sources QUOTES_REALTIME and
// QUOTES_REALTIME_TWSE_YAHOO name (off by default: read each source's terms
// first) and logs which.
func useRealtime(mkt *market.Client, cfg config.Config) {
	adapters := rtquote.Enabled(cfg.QuotesRealtime, cfg.QuotesRealtimeTWSEYahoo)
	mkt.UseRealtime(adapters, cfg.QuotesRealtimeBase)
	names := make([]string, len(adapters))
	for i, a := range adapters {
		names[i] = a.Info().Name
	}
	if len(cfg.QuotesRealtimeUnknown) > 0 {
		slog.Warn("realtime quotes: QUOTES_REALTIME names no such venue; ignored", "tokens", strings.Join(cfg.QuotesRealtimeUnknown, ","))
	}
	slog.Info("realtime quotes", "venues", strings.Join(cfg.QuotesRealtime, ","), "sources", strings.Join(names, ","),
		"testBase", cfg.QuotesRealtimeBase != "")
}

// feedInterest is the quote-feed poller's interest set: every symbol a user
// holds, has an active alert on, or runs an autopilot on (strategy.Spec's
// JSON key is "symbol"). Those rows are read every minute while open.
func feedInterest(pool *pgxpool.Pool) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		rows, err := pool.Query(ctx, `select symbol from positions where qty <> 0
			union select symbol from alerts where status = 'active' and symbol <> ''
			union select spec->>'symbol' from autopilots where status = 'running' and spec->>'symbol' is not null`)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[string])
	}
}

// warnCalendarHorizons logs, once at boot, every session venue whose verified
// calendar ends within market.CalendarWarnDays: its next year's dates are due.
func warnCalendarHorizons(now time.Time) {
	h := market.CalendarHorizons(now)
	if len(h) == 0 {
		return
	}
	venues := make([]string, len(h))
	for i, v := range h {
		venues[i] = v.Venue + " " + v.Last.Format(time.DateOnly)
	}
	slog.Warn("session calendars end soon: add each venue's next official calendar (internal/market/session.go)",
		"withinDays", market.CalendarWarnDays, "lastVerified", strings.Join(venues, ", "))
}

// ensureAccounts creates the demo user and the bootstrap admin when missing
// (else only refreshes a changed password) and returns their emails: the
// accounts whose passwords the config owns.
func ensureAccounts(ctx context.Context, a *auth.Service, cfg config.Config) ([]string, error) {
	var managed []string
	for _, acct := range []struct {
		email, password, name, role string
		initiallyEnabled            bool
	}{
		{cfg.DemoEmail, cfg.DemoPassword, cmp.Or(cfg.DemoName, "Demo Investor"), "user", false},
		{cfg.AdminEmail, cfg.AdminPassword, "Admin", "admin", true},
	} {
		if acct.email == "" || acct.password == "" {
			continue
		}
		u, err := a.EnsureUser(ctx, acct.email, acct.name, acct.password, acct.role, acct.initiallyEnabled)
		if err != nil {
			return nil, err
		}
		slog.Info("bootstrap account ready", "email", u.Email, "role", u.Role)
		managed = append(managed, u.Email)
	}
	return managed, nil
}

// version is the commit baked in at build time, else the VCS revision Go
// stamped into the binary, cut to 12 characters, when it is a commit id
// (7 to 40 lower-case hex digits); else "dev" ("nogit" or a
// stray ldflag never reaches /healthz).
func version() string {
	v := commit
	if info, ok := debug.ReadBuildInfo(); ok && v == "" {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				v = s.Value
			}
		}
	}
	if !commitID.MatchString(v) {
		return "dev"
	}
	return v[:min(len(v), 12)]
}

var commitID = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// apiVersion is version, computed once at boot: /healthz's X-Api-Commit
// header and the admin overview.
var apiVersion = sync.OnceValue(version)

// handler mounts every route and wraps them in the middleware chain.
func handler(allowedOrigins []string, d *app.Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Api-Commit", apiVersion()) // the commit id only
		if d.Market != nil {
			// per real-time venue: the source serving, its status, its newest trade's age; nothing else
			w.Header().Set("X-Quote-Sources", d.Market.QuoteSources())
		}
		w.Write([]byte("ok"))
	})
	if d.Settings != nil {
		mux.HandleFunc("GET /auth/callback", d.Settings.Callback) // ChatGPT sign-in's redirect: its state stands in for a session
	}
	d.Auth.Register(mux)
	profile.Register(mux, d.DB, d.Auth.Require)
	d.Market.Register(mux, d.Auth.Require)
	d.Paper.Register(mux, d.Auth.Require)
	d.Research.Register(mux, d.Auth.Require)
	signals.Register(mux, d)
	d.Paper.RegisterHistory(mux, d.Auth.Require)
	chat.Register(mux, d)
	copilot.Register(mux, d)
	forecast.Register(mux, d)
	strategy.Register(mux, d)
	aipf.Register(mux, d)
	arena.Register(mux, d)
	autopilot.Register(mux, d)
	alerts.Register(mux, d)
	screener.Register(mux, d)
	admin.Register(mux, d)
	return httpx.Log(httpx.Recover(httpx.CORS(allowedOrigins, httpx.CSRF(allowedOrigins, mux))))
}
