package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMarketIntelligenceConfig(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "env-brave")
	t.Setenv("CRAWL_TOKEN", "env-crawl")
	t.Setenv("CRAWL_BASE", "")
	t.Setenv("BRAVE_DAILY_LIMIT", "0")
	t.Setenv("BRAVE_BASE", "")
	c := FromEnv()
	if c.BraveAPIKey != "env-brave" || c.CrawlToken != "env-crawl" || c.CrawlBase != "" || c.BraveDailyLimit != 5000 ||
		c.BraveBase != "" {
		t.Fatalf("defaults: %+v", c)
	}
	t.Setenv("BRAVE_DAILY_LIMIT", "200")
	t.Setenv("BRAVE_BASE", "http://127.0.0.1:9/res/v1")
	if c = FromEnv(); c.BraveDailyLimit != 200 || c.BraveBase != "http://127.0.0.1:9/res/v1" {
		t.Fatalf("overrides: limit %d base %q", c.BraveDailyLimit, c.BraveBase)
	}
}

// MODEL_KNOWLEDGE_CUTOFF is optional (zero → the llm default); a value that is
// not a date fails Load (boot) instead of silently disabling the arena's warning.
func TestKnowledgeCutoff(t *testing.T) {
	t.Setenv("MODEL_KNOWLEDGE_CUTOFF", "")
	if c, err := Load(); err != nil || !c.AI.KnowledgeCutoff.IsZero() {
		t.Fatalf("default: %v %v", c.AI.KnowledgeCutoff, err)
	}
	t.Setenv("MODEL_KNOWLEDGE_CUTOFF", "2026-08-15")
	if c := FromEnv(); c.AI.KnowledgeCutoff != time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("override: %v", c.AI.KnowledgeCutoff)
	}
	for _, bad := range []string{"2026-13-01", "June 2026", "2026-6-30"} {
		t.Setenv("MODEL_KNOWLEDGE_CUTOFF", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MODEL_KNOWLEDGE_CUTOFF") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// AI_*: the provider's own key backs AI_API_KEY, BEDROCK_MODEL is AI_MODEL's
// deprecated alias for bedrock, and an effort other than on/off fails Load.
func TestAIEnv(t *testing.T) {
	for _, k := range []string{"AI_PROVIDER", "AI_MODEL", "AI_BASE_URL", "AI_API_KEY", "AI_EFFORT", "AWS_REGION", "MODEL_KNOWLEDGE_CUTOFF",
		"BEDROCK_MODEL", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "SECRET_KEY"} {
		t.Setenv(k, "")
	}
	if c, err := Load(); err != nil || c.AI.Provider != "" || c.AI.APIKey != "" || c.SecretKey != "" {
		t.Fatalf("unset: %+v %v", c.AI, err)
	}
	t.Setenv("AI_PROVIDER", " Gemini ")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	if c := FromEnv(); c.AI.Provider != "gemini" || c.AI.APIKey != "google-key" {
		t.Fatalf("vendor fallback: %q %t", c.AI.Provider, c.AI.APIKey == "google-key")
	}
	t.Setenv("GEMINI_API_KEY", "gemini-key")
	if c := FromEnv(); c.AI.APIKey != "gemini-key" {
		t.Fatal("GEMINI_API_KEY comes before GOOGLE_API_KEY")
	}
	t.Setenv("AI_API_KEY", "ai-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	if c := FromEnv(); c.AI.APIKey != "ai-key" {
		t.Fatal("AI_API_KEY wins over the vendor variable")
	}
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_PROVIDER", "groq")
	if c := FromEnv(); c.AI.APIKey != "" {
		t.Fatal("another vendor's key must not apply")
	}

	t.Setenv("AI_PROVIDER", "bedrock")
	t.Setenv("BEDROCK_MODEL", "old-model")
	t.Setenv("AWS_REGION", "eu-west-1")
	if c := FromEnv(); c.AI.Model != "old-model" || c.AI.AWSRegion != "eu-west-1" {
		t.Fatalf("bedrock alias: %+v", c.AI)
	}
	t.Setenv("AI_MODEL", "new-model")
	if c := FromEnv(); c.AI.Model != "new-model" {
		t.Fatalf("AI_MODEL wins: %q", c.AI.Model)
	}
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("AI_MODEL", "")
	if c := FromEnv(); c.AI.Model != "" {
		t.Fatal("BEDROCK_MODEL is bedrock's alias only")
	}

	for v, want := range map[string]string{" ON ": "on", "off": "off", "": ""} {
		t.Setenv("AI_EFFORT", v)
		if c, err := Load(); err != nil || c.AI.Effort != want {
			t.Errorf("AI_EFFORT=%q: %q %v", v, c.AI.Effort, err)
		}
	}
	t.Setenv("AI_EFFORT", "high")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AI_EFFORT") {
		t.Fatalf("bad effort: %v", err)
	}
	t.Setenv("AI_EFFORT", "")
	t.Setenv("SECRET_KEY", " "+strings.Repeat("ab", 32)+" ")
	if c := FromEnv(); c.SecretKey != strings.Repeat("ab", 32) {
		t.Fatal("SECRET_KEY is trimmed")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "DATABASE_URL", "ALLOWED_ORIGINS", "ADMIN_ORIGINS", "COOKIE_SECURE",
		"AI_DAILY_CREDITS", "DEMO_EMAIL", "DEMO_PASSWORD", "DEMO_NAME", "ADMIN_EMAIL", "ADMIN_PASSWORD", "BINANCE_BASE", "NOTIFY_EMAIL_FROM"} {
		t.Setenv(k, "")
	}
	c := FromEnv()
	if c.Port != "8080" || c.NotifyEmailFrom != "" ||
		len(c.AllowedOrigins) != 2 || c.AllowedOrigins[0] != "http://localhost:5173" || c.AllowedOrigins[1] != "http://localhost:5174" ||
		len(c.AdminOrigins) != 1 || c.AdminOrigins[0] != "http://localhost:5174" || c.CookieSecure || c.AIDailyCredits != 500 || c.AdminEmail != "" || c.AdminPassword != "" ||
		c.BinanceBase != "" {
		t.Fatalf("defaults: %+v", c)
	}
	t.Setenv("ALLOWED_ORIGINS", " https://app.example.com, ,http://localhost:5173 ")
	t.Setenv("ADMIN_ORIGINS", "https://admin.example.com")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("AI_DAILY_CREDITS", "abc")
	t.Setenv("BINANCE_BASE", "http://127.0.0.1:9")
	c = FromEnv()
	if len(c.AllowedOrigins) != 2 || c.AllowedOrigins[0] != "https://app.example.com" ||
		len(c.AdminOrigins) != 1 || c.AdminOrigins[0] != "https://admin.example.com" || !c.CookieSecure || c.AIDailyCredits != 500 ||
		c.BinanceBase != "http://127.0.0.1:9" {
		t.Fatalf("overrides: %+v", c)
	}
}

// The bootstrap admin fails boot with a short password or the demo's email;
// the error never carries the password.
func TestAdminConfig(t *testing.T) {
	t.Setenv("MODEL_KNOWLEDGE_CUTOFF", "")
	t.Setenv("DEMO_EMAIL", "demo@example.com")
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
	t.Setenv("ADMIN_PASSWORD", "short-pw")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ADMIN_PASSWORD") || strings.Contains(err.Error(), "short-pw") {
		t.Fatalf("short password: %v", err)
	}
	t.Setenv("ADMIN_PASSWORD", "long-enough-pw")
	t.Setenv("ADMIN_EMAIL", " Demo@example.com ")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ADMIN_EMAIL") {
		t.Fatalf("admin email equals the demo's: %v", err)
	}
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
	if c, err := Load(); err != nil || c.AdminEmail != "admin@example.com" || c.AdminPassword != "long-enough-pw" {
		t.Fatalf("valid: %v", err)
	}
	t.Setenv("ADMIN_PASSWORD", "")
	if _, err := Load(); err != nil {
		t.Fatalf("no admin configured: %v", err)
	}
}

func TestLiveArenaKillSwitch(t *testing.T) {
	for _, v := range []string{"", "on", " OFF "} {
		t.Setenv("LIVE_ARENA", v)
		if c := FromEnv(); c.LiveArena == (v == " OFF ") {
			t.Fatalf("env %q: live=%v", v, c.LiveArena)
		}
	}
}

// SCREENER_MAX_TICKERS: unset or junk screens everything, N caps, 0/off stops the job.
func TestScreenerCap(t *testing.T) {
	for v, want := range map[string]int{"": 0, "abc": 0, "-3": 0, "25": 25, " 3 ": 3, "0": -1, "off": -1, " OFF ": -1} {
		t.Setenv("SCREENER_MAX_TICKERS", v)
		if got := FromEnv().ScreenerMaxTickers; got != want {
			t.Fatalf("SCREENER_MAX_TICKERS=%q: %d, want %d", v, got, want)
		}
	}
}

// The desk's methods (round 9b) are on unless FORECAST_METHODS is off (any case).
func TestForecastMethodsConfig(t *testing.T) {
	for v, on := range map[string]bool{"": true, "on": true, "off": false, " OFF ": false, "anything": true} {
		t.Setenv("FORECAST_METHODS", v)
		if c := FromEnv(); c.ForecastMethods != on {
			t.Errorf("FORECAST_METHODS=%q: %v", v, c.ForecastMethods)
		}
	}
}

// Deep research is the default; quick (any case) restores the one-shot stage,
// and the per-run Brave cap defaults to 18, clamped to 5..40.
func TestForecastResearchConfig(t *testing.T) {
	t.Setenv("FORECAST_RESEARCH_MODE", "")
	t.Setenv("FORECAST_RESEARCH_SEARCHES", "")
	if c := FromEnv(); !c.ForecastDeepResearch || c.ForecastSearches != 18 {
		t.Fatalf("defaults: deep %v searches %d", c.ForecastDeepResearch, c.ForecastSearches)
	}
	for mode, deep := range map[string]bool{"quick": false, " Quick ": false, "deep": true, "anything": true} {
		t.Setenv("FORECAST_RESEARCH_MODE", mode)
		if c := FromEnv(); c.ForecastDeepResearch != deep {
			t.Errorf("mode %q: deep %v", mode, c.ForecastDeepResearch)
		}
	}
	for v, want := range map[string]int{"12": 12, "2": 5, "99": 40, "0": 18, "x": 18, "-3": 18} {
		t.Setenv("FORECAST_RESEARCH_SEARCHES", v)
		if c := FromEnv(); c.ForecastSearches != want {
			t.Errorf("searches %q = %d, want %d", v, c.ForecastSearches, want)
		}
	}
}

// QUOTES_REALTIME: unset or empty is none (opt-in), a list those it names
// (unknown tokens kept apart); QUOTES_REALTIME_TWSE_YAHOO is on unless off.
func TestQuotesRealtimeSwitches(t *testing.T) {
	t.Setenv("QUOTES_REALTIME", "")
	os.Unsetenv("QUOTES_REALTIME")
	t.Setenv("QUOTES_REALTIME_TWSE_YAHOO", "")
	t.Setenv("QUOTES_REALTIME_BASE", "")
	c := FromEnv()
	if len(c.QuotesRealtime) != 0 || !c.QuotesRealtimeTWSEYahoo || c.QuotesRealtimeBase != "" ||
		len(c.QuotesRealtimeUnknown) != 0 {
		t.Fatalf("defaults: %v %v %q", c.QuotesRealtime, c.QuotesRealtimeTWSEYahoo, c.QuotesRealtimeBase)
	}
	t.Setenv("QUOTES_REALTIME", "")
	if c := FromEnv(); len(c.QuotesRealtime) != 0 {
		t.Fatalf("empty: %v", c.QuotesRealtime)
	}
	t.Setenv("QUOTES_REALTIME", " SGX,krx, tse ")
	t.Setenv("QUOTES_REALTIME_BASE", " http://127.0.0.1:9 ")
	for _, off := range []string{"off", "OFF", "0", "false", "no"} {
		t.Setenv("QUOTES_REALTIME_TWSE_YAHOO", off)
		c := FromEnv()
		if strings.Join(c.QuotesRealtime, ",") != "krx,sgx" || strings.Join(c.QuotesRealtimeUnknown, ",") != "tse" ||
			c.QuotesRealtimeTWSEYahoo || c.QuotesRealtimeBase != "http://127.0.0.1:9" {
			t.Fatalf("%q: %v %v %v %q", off, c.QuotesRealtime, c.QuotesRealtimeUnknown, c.QuotesRealtimeTWSEYahoo, c.QuotesRealtimeBase)
		}
	}
	t.Setenv("QUOTES_REALTIME_TWSE_YAHOO", "on")
	if !FromEnv().QuotesRealtimeTWSEYahoo {
		t.Fatal("on")
	}
}
