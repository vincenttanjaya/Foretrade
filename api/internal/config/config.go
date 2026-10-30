// Package config loads the API configuration from the environment.
package config

import (
	"cmp"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/rtquote"
)

type Config struct {
	Port           string
	DatabaseURL    string
	AllowedOrigins []string
	AdminOrigins   []string // the only browser origins /api/admin/* serves
	CookieSecure   bool
	AIDailyCredits int // 0 blocks AI for users without their own limit
	// TrustedProxies are the reverse proxies whose X-Forwarded-For the sign-in
	// limiter reads (TRUSTED_PROXIES, CIDRs or addresses); loopback always is one.
	TrustedProxies []netip.Prefix

	// BinanceBase is the market-data API; "" → https://data-api.binance.vision.
	BinanceBase string

	// AI is the environment's AI config: AI_PROVIDER, AI_MODEL (BEDROCK_MODEL
	// for bedrock), AI_BASE_URL, AI_API_KEY or the provider's own key variable,
	// AI_EFFORT, AWS_REGION and MODEL_KNOWLEDGE_CUTOFF (zero → the llm default).
	// A Provider puts the whole AI config in env mode: the admin console shows
	// it read-only. Without one the stored settings rule.
	AI llm.Settings // APIKey: never log
	// SecretKey encrypts the stored AI and search secrets (SECRET_KEY, 64 hex
	// characters); unset or invalid, they cannot be saved.
	SecretKey string // never log

	// Demo user and bootstrap admin, each ensured at boot when its email and
	// password are both set (created when missing, else only the password is refreshed).
	DemoEmail     string
	DemoPassword  string // never log
	DemoName      string
	AdminEmail    string
	AdminPassword string // never log

	// Market intelligence. Without a Brave key search is off; without a crawl
	// token pages are read by the direct fetch only.
	BraveAPIKey     string // never log
	BraveBase       string // "" → https://api.search.brave.com/res/v1
	BraveDailyLimit int
	CrawlBase       string
	CrawlToken      string // never log

	// Forecast desk research. ForecastDeepResearch is false only
	// for FORECAST_RESEARCH_MODE=quick (the one-shot stage); ForecastSearches is
	// deep research's per-run Brave call cap, 5..40.
	ForecastDeepResearch bool
	ForecastSearches     int
	// ForecastMethods is false only for FORECAST_METHODS=off: the
	// desk's numeric questions then keep the event-frame roles and prompts.
	ForecastMethods bool

	// PolymarketBase is the Gamma API the forecast desk reads market odds from.
	PolymarketBase string // "" in the env → https://gamma-api.polymarket.com

	// HyperliquidBase is the perp-data API behind the trade page's derivatives row; "" → https://api.hyperliquid.xyz.
	HyperliquidBase string

	// YahooBase is the keyless Yahoo Finance chart/quote API; "" → https://query1.finance.yahoo.com.
	YahooBase string

	// ScreenerMaxTickers caps the Asia screener's weekday job (SCREENER_MAX_TICKERS): 0 (unset)
	// screens every constituent, N > 0 the first N, and -1 ("0" or "off") turns the job and
	// the screener's own quote tier (market.Client.Watch) off.
	ScreenerMaxTickers int

	// LiveArena runs the shared live paper desk (contract "Live arena") in this
	// process. Opt-in (LIVE_ARENA=on): its model calls spend the operator's key.
	LiveArena bool

	// QuotesRealtime are the venues whose real-time quote sources are on
	// (QUOTES_REALTIME, lower-case tokens krx hkex hose sgx twse; unset or ""
	// → none: opt-in, since each source's terms are the operator's call).
	// QuotesRealtimeUnknown are tokens it named that are no venue (logged at
	// boot, ignored).
	QuotesRealtime        []string
	QuotesRealtimeUnknown []string
	// QuotesRealtimeTWSEYahoo puts Yahoo Taiwan behind TWSE's MIS
	// (QUOTES_REALTIME_TWSE_YAHOO; on unless off, 0, false or no).
	QuotesRealtimeTWSEYahoo bool
	// QuotesRealtimeBase sends every real-time source to one host
	// (QUOTES_REALTIME_BASE): tests only, never set in production.
	QuotesRealtimeBase string

	// NotifyEmailFrom is the SES sender of notification emails
	// (NOTIFY_EMAIL_FROM); "" when it is off, 0, false or no: email is off.
	NotifyEmailFrom string
	// AppURL is the web app's origin for links in notifications (APP_URL);
	// unset → the first ALLOWED_ORIGINS entry that is not an admin origin.
	AppURL string

	// Serving (contract §3). WebDir and AdminDir are the built SPAs (WEB_DIR,
	// ADMIN_DIR; "" serves the API alone). AdminPort (ADMIN_PORT) opens a second
	// listener for the admin console; "" serves everything on Port.
	WebDir    string
	AdminDir  string
	AdminPort string
}

// Load reads the environment. An invalid MODEL_KNOWLEDGE_CUTOFF, AI_EFFORT,
// COOKIE_SECURE or TRUSTED_PROXIES, or an invalid bootstrap account, fails it.
func Load() (Config, error) {
	c := FromEnv()
	if v := os.Getenv("MODEL_KNOWLEDGE_CUTOFF"); v != "" && c.AI.KnowledgeCutoff.IsZero() {
		return c, fmt.Errorf("MODEL_KNOWLEDGE_CUTOFF must be a date YYYY-MM-DD, got %q", v)
	}
	if e := c.AI.Effort; e != "" && e != "on" && e != "off" {
		return c, fmt.Errorf("AI_EFFORT must be on or off, got %q", e)
	}
	if v := strings.TrimSpace(os.Getenv("COOKIE_SECURE")); v != "" {
		if _, err := strconv.ParseBool(v); err != nil {
			return c, fmt.Errorf("COOKIE_SECURE must be true or false, got %q", v)
		}
	}
	if _, bad := prefixes(os.Getenv("TRUSTED_PROXIES")); bad != "" {
		return c, fmt.Errorf("TRUSTED_PROXIES must be a comma list of CIDRs or addresses, got %q", bad)
	}
	return c, c.validate()
}

// validate checks the bootstrap accounts: passwords bcrypt can hash (at most
// 72 bytes), an admin password of at least 12 and an admin email other than
// the demo account's. Messages never hold the values.
func (c Config) validate() error {
	if c.DemoEmail != "" && len(c.DemoPassword) > 72 {
		return fmt.Errorf("DEMO_PASSWORD must be at most 72 bytes")
	}
	if c.AdminEmail == "" || c.AdminPassword == "" {
		return nil
	}
	if n := len(c.AdminPassword); n < 12 || n > 72 {
		return fmt.Errorf("ADMIN_PASSWORD must be 12 to 72 bytes")
	}
	if strings.EqualFold(strings.TrimSpace(c.AdminEmail), strings.TrimSpace(c.DemoEmail)) {
		return fmt.Errorf("ADMIN_EMAIL must differ from DEMO_EMAIL")
	}
	return nil
}

func FromEnv() Config {
	c := Config{
		Port:           cmp.Or(os.Getenv("PORT"), "8080"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		AllowedOrigins: splitList(cmp.Or(os.Getenv("ALLOWED_ORIGINS"), "http://localhost:5173,http://localhost:5174")),
		AdminOrigins:   splitList(cmp.Or(os.Getenv("ADMIN_ORIGINS"), "http://localhost:5174")),
		AIDailyCredits: envCount("AI_DAILY_CREDITS", 500),
		DemoEmail:      os.Getenv("DEMO_EMAIL"),
		DemoPassword:   os.Getenv("DEMO_PASSWORD"),
		DemoName:       os.Getenv("DEMO_NAME"),
		BinanceBase:    os.Getenv("BINANCE_BASE"),
		AdminEmail:     os.Getenv("ADMIN_EMAIL"),
		AdminPassword:  os.Getenv("ADMIN_PASSWORD"),

		BraveAPIKey:     os.Getenv("BRAVE_API_KEY"),
		BraveBase:       os.Getenv("BRAVE_BASE"),
		BraveDailyLimit: envInt("BRAVE_DAILY_LIMIT", 5000),
		CrawlBase:       os.Getenv("CRAWL_BASE"),
		CrawlToken:      os.Getenv("CRAWL_TOKEN"),

		AI:        aiFromEnv(),
		SecretKey: strings.TrimSpace(os.Getenv("SECRET_KEY")),

		ForecastDeepResearch: !strings.EqualFold(strings.TrimSpace(os.Getenv("FORECAST_RESEARCH_MODE")), "quick"),
		ForecastSearches:     min(max(envInt("FORECAST_RESEARCH_SEARCHES", 18), 5), 40),
		ForecastMethods:      !strings.EqualFold(strings.TrimSpace(os.Getenv("FORECAST_METHODS")), "off"),

		PolymarketBase: cmp.Or(os.Getenv("POLYMARKET_BASE"), "https://gamma-api.polymarket.com"),

		HyperliquidBase: os.Getenv("HYPERLIQUID_BASE"),

		YahooBase: os.Getenv("YAHOO_BASE"),

		ScreenerMaxTickers: screenerCap(os.Getenv("SCREENER_MAX_TICKERS")),

		LiveArena: strings.EqualFold(strings.TrimSpace(os.Getenv("LIVE_ARENA")), "on"),

		QuotesRealtimeTWSEYahoo: !offWord(os.Getenv("QUOTES_REALTIME_TWSE_YAHOO")),
		QuotesRealtimeBase:      strings.TrimSpace(os.Getenv("QUOTES_REALTIME_BASE")),
	}
	c.CookieSecure, _ = strconv.ParseBool(strings.TrimSpace(os.Getenv("COOKIE_SECURE")))
	c.TrustedProxies, _ = prefixes(os.Getenv("TRUSTED_PROXIES"))
	c.QuotesRealtime, c.QuotesRealtimeUnknown = rtquote.ParseSwitches(os.Getenv("QUOTES_REALTIME"))
	if from := strings.TrimSpace(os.Getenv("NOTIFY_EMAIL_FROM")); !offWord(from) {
		c.NotifyEmailFrom = from
	}
	c.AppURL = strings.TrimSuffix(strings.TrimSpace(os.Getenv("APP_URL")), "/")
	for _, o := range c.AllowedOrigins {
		if c.AppURL == "" && !slices.Contains(c.AdminOrigins, o) {
			c.AppURL = o
		}
	}
	c.WebDir, c.AdminDir, c.AdminPort = os.Getenv("WEB_DIR"), os.Getenv("ADMIN_DIR"), os.Getenv("ADMIN_PORT")
	return c
}

// offWord reports whether s turns a default-on switch off.
func offWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func date(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

// vendorKeys are the providers' own key variables, AI_API_KEY's fallbacks.
// ponytail: mirrors llm.Providers' KeyEnv; one table if they ever drift.
var vendorKeys = map[string][]string{
	"openai": {"OPENAI_API_KEY"}, "anthropic": {"ANTHROPIC_API_KEY"}, "openrouter": {"OPENROUTER_API_KEY"},
	"gemini": {"GEMINI_API_KEY", "GOOGLE_API_KEY"}, "groq": {"GROQ_API_KEY"}, "deepseek": {"DEEPSEEK_API_KEY"},
	"xai": {"XAI_API_KEY"}, "mistral": {"MISTRAL_API_KEY"},
}

// aiFromEnv reads the AI_* variables; an unparsable cutoff is left zero.
func aiFromEnv() llm.Settings {
	s := llm.Settings{
		Provider:        strings.ToLower(strings.TrimSpace(os.Getenv("AI_PROVIDER"))),
		Model:           strings.TrimSpace(os.Getenv("AI_MODEL")),
		BaseURL:         strings.TrimSpace(os.Getenv("AI_BASE_URL")),
		APIKey:          strings.TrimSpace(os.Getenv("AI_API_KEY")),
		Effort:          strings.ToLower(strings.TrimSpace(os.Getenv("AI_EFFORT"))),
		AWSRegion:       strings.TrimSpace(os.Getenv("AWS_REGION")),
		KnowledgeCutoff: date(os.Getenv("MODEL_KNOWLEDGE_CUTOFF")),
	}
	if s.Provider == "bedrock" && s.Model == "" {
		s.Model = strings.TrimSpace(os.Getenv("BEDROCK_MODEL")) // deprecated alias
	}
	for _, k := range vendorKeys[s.Provider] {
		s.APIKey = cmp.Or(s.APIKey, strings.TrimSpace(os.Getenv(k)))
	}
	return s
}

// screenerCap reads SCREENER_MAX_TICKERS: unset or invalid → 0 (every
// constituent), a positive N → N, "0" or "off" → -1 (no runs).
func screenerCap(s string) int {
	s = strings.TrimSpace(s)
	if s == "0" || strings.EqualFold(s, "off") {
		return -1
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return 0
}

// prefixes parses a comma list of CIDRs or single addresses; bad is the first
// entry that is neither (skipped). IPv4-mapped entries are stored as IPv4,
// since auth's clientIP unmaps the addresses it checks.
func prefixes(s string) (out []netip.Prefix, bad string) {
	for _, v := range splitList(s) {
		if a, err := netip.ParseAddr(v); err == nil {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		} else if p, err := netip.ParsePrefix(v); err == nil {
			if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
		} else if bad == "" {
			bad = v
		}
	}
	return out, bad
}

// envCount reads a non-negative integer, falling back to def.
func envCount(key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && n >= 0 {
		return n
	}
	return def
}

// envInt reads a positive integer, falling back to def.
func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}
