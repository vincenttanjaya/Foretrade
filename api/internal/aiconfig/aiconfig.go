// Package aiconfig owns the AI provider and search settings the admin console
// edits (API contract "Settings"): the one ai_settings row with its secrets
// sealed under SECRET_KEY, the environment's precedence over it, the admin
// endpoints, and "Sign in with ChatGPT" with the token source behind it. Every
// change reconfigures the llm client and the research service's keys at once.
//
// AI_PROVIDER set puts the whole AI config in env mode (read-only in the
// console, Test still works); the ChatGPT credential always lives in the row.
// Search keys: the row's value where set, else the environment's.
package aiconfig

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

type Service struct {
	db        *pgxpool.Pool
	box       *box         // nil: SECRET_KEY unset or invalid
	env       llm.Settings // the AI_* environment; a Provider → env mode
	envSearch search       // BRAVE_API_KEY, CRAWL_BASE, CRAWL_TOKEN
	llm       *llm.Client
	research  *research.Service

	// The provider calls; tests replace them.
	test   func(context.Context, llm.Settings) error
	models func(context.Context, llm.Settings) ([]llm.ModelInfo, error)

	authBase string // ChatGPT's OAuth issuer; tests point it at a fake
	http     *http.Client
	keys     jwks

	mu      sync.Mutex
	failure string             // the last Configure error, shown as ai.error
	cred    *credential        // the stored ChatGPT credential as last read
	pending map[string]pending // ChatGPT sign-ins in flight, by state
}

type search struct{ brave, crawlBase, crawlToken string }

// New builds the service; Load puts the stored settings in force.
func New(db *pgxpool.Pool, cfg config.Config, ai *llm.Client, rs *research.Service) *Service {
	return &Service{db: db, box: newBox(cfg.SecretKey), env: cfg.AI, envSearch: search{cfg.BraveAPIKey, cfg.CrawlBase, cfg.CrawlToken},
		llm: ai, research: rs, test: testProvider, models: llm.ListModels,
		authBase: "https://auth.openai.com", http: &http.Client{Timeout: 30 * time.Second}, pending: map[string]pending{}}
}

// testProvider makes the one tiny completion a save or a Test runs.
func testProvider(ctx context.Context, s llm.Settings) error {
	c, err := llm.NewFromSettings(s)
	if err != nil {
		return err
	}
	return c.Test(ctx)
}

// Load puts the stored settings in force at boot. A secret that can't be
// decrypted or a provider that won't configure is logged, never fatal.
func (s *Service) Load(ctx context.Context) error {
	r, err := s.read(ctx)
	if err != nil {
		return err
	}
	if s.box == nil {
		slog.Warn("SECRET_KEY is not 64 hex characters: AI and search keys can't be saved in the admin console")
	}
	for name := range r.broken {
		slog.Warn("settings: a stored secret can't be decrypted (SECRET_KEY changed?); it is ignored until entered again", "secret", name)
	}
	s.apply(r)
	st, k := s.settings(r), s.searchKeys(r)
	slog.Info("settings", "ai", st.Provider, "model", st.Model, "envMode", s.envMode(), "search", k.brave != "",
		"crawler", k.crawlBase != "" && k.crawlToken != "")
	return nil
}

func (s *Service) envMode() bool { return s.env.Provider != "" }

// row is ai_settings with its secrets opened. A secret that can't be opened
// is absent and named in broken; stored names the non-null secret columns.
type row struct {
	ai                            llm.Settings // APIKey: the opened secret
	keyHint, braveHint, crawlHint string
	hostID                        string
	cred                          *credential
	search                        search
	stored, broken                map[string]bool // by field name: apiKey, chatgpt, braveApiKey, crawlToken
}

func (s *Service) read(ctx context.Context) (row, error) {
	r := row{stored: map[string]bool{}, broken: map[string]bool{}}
	var cutoff *time.Time
	var key, chat, brave, crawl []byte
	scan := func() error {
		return s.db.QueryRow(ctx, `select provider, base_url, model, effort, aws_region, knowledge_cutoff, secret, secret_hint, chatgpt,
			host_id, brave_api_key, brave_hint, crawl_base, crawl_token, crawl_hint from ai_settings where id = 1`).Scan(
			&r.ai.Provider, &r.ai.BaseURL, &r.ai.Model, &r.ai.Effort, &r.ai.AWSRegion, &cutoff, &key, &r.keyHint, &chat,
			&r.hostID, &brave, &r.braveHint, &r.search.crawlBase, &crawl, &r.crawlHint)
	}
	err := scan()
	if errors.Is(err, pgx.ErrNoRows) { // deleted to reset the settings: start again from the defaults
		if _, err = s.db.Exec(ctx, "insert into ai_settings default values on conflict do nothing"); err == nil {
			err = scan()
		}
	}
	if err != nil {
		return r, err
	}
	if cutoff != nil {
		r.ai.KnowledgeCutoff = *cutoff
	}
	r.ai.APIKey = string(s.reveal(&r, "apiKey", key))
	r.search.brave = string(s.reveal(&r, "braveApiKey", brave))
	r.search.crawlToken = string(s.reveal(&r, "crawlToken", crawl))
	if plain := s.reveal(&r, "chatgpt", chat); plain != nil {
		r.cred = new(credential)
		if json.Unmarshal(plain, r.cred) != nil {
			r.cred, r.broken["chatgpt"] = nil, true
		}
	}
	return r, nil
}

// reveal opens a sealed column, marking it stored, and broken when it can't
// be opened (SECRET_KEY changed or unset).
func (s *Service) reveal(r *row, name string, sealed []byte) []byte {
	if sealed == nil {
		return nil
	}
	r.stored[name] = true
	plain, err := s.box.open(sealed)
	if err != nil {
		r.broken[name] = true
		return nil
	}
	return plain
}

// settings are the AI settings in force: the environment's in env mode, else the row's.
func (s *Service) settings(r row) llm.Settings {
	if s.envMode() {
		return s.withChatGPT(s.env)
	}
	return s.withChatGPT(r.ai)
}

// withChatGPT hands chatgpt settings the stored sign-in's tokens. They are
// for the plan's fixed endpoint only, so no base URL goes with them.
func (s *Service) withChatGPT(st llm.Settings) llm.Settings {
	if st.Provider == "chatgpt" {
		st.BaseURL, st.ChatGPT = "", s
	}
	return st
}

// searchKeys are the row's search settings where set, else the environment's.
// The env crawl token only goes with the env crawl base, never to a base set here.
func (s *Service) searchKeys(r row) search {
	k := search{cmp.Or(r.search.brave, s.envSearch.brave), cmp.Or(r.search.crawlBase, s.envSearch.crawlBase), r.search.crawlToken}
	if r.search.crawlBase == "" {
		k.crawlToken = cmp.Or(k.crawlToken, s.envSearch.crawlToken)
	}
	return k
}

// apply puts r in force: the llm client (on error the old provider stays and
// the error shows in the console) and the research service's keys.
func (s *Service) apply(r row) {
	err := s.llm.Configure(s.settings(r))
	s.mu.Lock()
	s.failure, s.cred = "", r.cred
	if err != nil {
		s.failure = err.Error()
	}
	s.mu.Unlock()
	if err != nil {
		// The row is the truth: never keep serving the previous provider (its key may be gone).
		_ = s.llm.Configure(llm.Settings{})
		slog.Warn("settings: AI provider not configured", "err", err)
	}
	k := s.searchKeys(r)
	s.research.SetSearch(k.brave, k.crawlBase, k.crawlToken)
}

type secretView struct {
	Set  bool   `json:"set"`
	Hint string `json:"hint"`
}

type sourcedSecret struct {
	secretView
	Source string `json:"source"` // "db" | "env" | ""
}

// View is GET /api/admin/settings. It never holds a secret, only its hint.
type View struct {
	AI struct {
		Provider        string     `json:"provider"`
		Model           string     `json:"model"`
		BaseURL         string     `json:"baseUrl"`
		Effort          string     `json:"effort"`
		AWSRegion       string     `json:"awsRegion"`
		KnowledgeCutoff string     `json:"knowledgeCutoff"` // "" = llm.DefaultKnowledgeCutoff
		APIKey          secretView `json:"apiKey"`
		Source          string     `json:"source"` // "db" | "env" | "none"
		ReadOnly        bool       `json:"readOnly"`
		ChatGPT         struct {
			SignedIn bool   `json:"signedIn"`
			Email    string `json:"email"`
			Error    string `json:"error"`
		} `json:"chatgpt"`
		Status string `json:"status"` // "off" | "ready" | "error"
		Error  string `json:"error"`
	} `json:"ai"`
	Search struct {
		BraveAPIKey     sourcedSecret `json:"braveApiKey"`
		CrawlBase       string        `json:"crawlBase"`
		CrawlBaseSource string        `json:"crawlBaseSource"`
		CrawlToken      sourcedSecret `json:"crawlToken"`
		Error           string        `json:"error"`
	} `json:"search"`
	Providers    []llm.ProviderInfo `json:"providers"`
	SecretKeySet bool               `json:"secretKeySet"`
}

const reenter = " can't be decrypted (did SECRET_KEY change?): enter it again."

func (s *Service) view(r row) View {
	var v View
	st, a := s.settings(r), &v.AI
	a.Provider, a.Model, a.BaseURL, a.Effort, a.AWSRegion = st.Provider, st.Model, st.BaseURL, st.Effort, st.AWSRegion
	if !st.KnowledgeCutoff.IsZero() {
		a.KnowledgeCutoff = st.KnowledgeCutoff.Format(time.DateOnly)
	}
	a.Source, a.APIKey = "none", secretView{r.ai.APIKey != "", r.keyHint}
	switch {
	case s.envMode():
		a.Source, a.ReadOnly, a.APIKey = "env", true, secretView{st.APIKey != "", hint(st.APIKey)}
	case r.ai.Provider != "":
		a.Source = "db"
	}
	a.ChatGPT.SignedIn = r.cred.signedIn()
	if r.cred != nil {
		a.ChatGPT.Email = r.cred.Email
	}
	switch {
	case r.broken["chatgpt"]:
		a.ChatGPT.Error = "The stored ChatGPT sign-in can't be decrypted (did SECRET_KEY change?): sign in again."
	case r.cred != nil && !a.ChatGPT.SignedIn:
		a.ChatGPT.Error = "The ChatGPT sign-in has ended: sign in again."
	}
	s.mu.Lock()
	failure := s.failure
	s.mu.Unlock()
	a.Status = "ready"
	switch {
	case st.Provider == "":
		a.Status = "off"
	case !s.envMode() && r.broken["apiKey"]: // the cause of the failure it brings
		a.Status, a.Error = "error", "The stored API key"+reenter
	case failure != "":
		a.Status, a.Error = "error", failure
	case st.Provider == "chatgpt" && !a.ChatGPT.SignedIn:
		a.Status, a.Error = "error", cmp.Or(a.ChatGPT.Error, "Sign in with ChatGPT.")
	}

	q, k, e := &v.Search, r.search, s.envSearch
	switch {
	case k.brave != "":
		q.BraveAPIKey = sourcedSecret{secretView{true, r.braveHint}, "db"}
	case e.brave != "":
		q.BraveAPIKey = sourcedSecret{secretView{true, hint(e.brave)}, "env"}
	}
	switch {
	case k.crawlBase != "":
		q.CrawlBase, q.CrawlBaseSource = k.crawlBase, "db"
	case e.crawlBase != "":
		q.CrawlBase, q.CrawlBaseSource = e.crawlBase, "env"
	}
	switch {
	case k.crawlToken != "":
		q.CrawlToken = sourcedSecret{secretView{true, r.crawlHint}, "db"}
	case k.crawlBase == "" && e.crawlToken != "":
		q.CrawlToken = sourcedSecret{secretView{true, hint(e.crawlToken)}, "env"}
	}
	if r.broken["braveApiKey"] || r.broken["crawlToken"] {
		q.Error = "A stored search key" + reenter
	}
	v.Providers, v.SecretKeySet = llm.Providers, s.box != nil
	return v
}
