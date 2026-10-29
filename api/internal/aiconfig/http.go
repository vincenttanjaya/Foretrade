package aiconfig

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

// Register mounts the settings endpoints on the admin console's gated mux
// (admin.Register: adminOrigin and RequireAdmin in front).
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/settings", s.get)
	mux.HandleFunc("PUT /api/admin/settings", s.put)
	mux.HandleFunc("POST /api/admin/settings/test", s.testDraft)
	mux.HandleFunc("POST /api/admin/settings/models", s.listModels)
	mux.HandleFunc("POST /api/admin/settings/chatgpt/start", s.start)
	mux.HandleFunc("POST /api/admin/settings/chatgpt/signout", s.signOut)
}

// invalid is a 400 whose text is shown to the admin.
type invalid string

func (e invalid) Error() string { return string(e) }

var (
	errEnv       = errors.New("AI settings are set by the environment (AI_PROVIDER): change them there and restart.")
	errForbidden = errors.New("the acting admin no longer exists")
)

// fail writes err: an invalid → 400 with its text, errEnv → 409, else a logged 500.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var bad invalid
	switch {
	case errors.As(err, &bad):
		httpx.Error(w, http.StatusBadRequest, "bad_request", string(bad))
	case errors.Is(err, errEnv):
		httpx.Error(w, http.StatusConflict, "conflict", errEnv.Error())
	case errors.Is(err, errForbidden):
		httpx.Error(w, http.StatusForbidden, "forbidden", "Admins only.")
	default:
		httpx.ServerError(w, r, err)
	}
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	row, err := s.read(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s.view(row))
}

// draft is a partial AI config; an absent field keeps the current value.
type draft struct {
	Provider        *string `json:"provider"`
	Model           *string `json:"model"`
	BaseURL         *string `json:"baseUrl"`
	Effort          *string `json:"effort"`
	AWSRegion       *string `json:"awsRegion"`
	KnowledgeCutoff *string `json:"knowledgeCutoff"`
	APIKey          string  `json:"apiKey"`
	ClearKey        bool    `json:"clearKey"`
}

type searchDraft struct {
	BraveAPIKey      string  `json:"braveApiKey"`
	ClearBraveAPIKey bool    `json:"clearBraveApiKey"`
	CrawlBase        *string `json:"crawlBase"`
	CrawlToken       string  `json:"crawlToken"`
	ClearCrawlToken  bool    `json:"clearCrawlToken"`
}

type body struct {
	AI     *draft       `json:"ai"`
	Search *searchDraft `json:"search"`
}

// secret resolves a write-only field: a new value, a clear, or the current
// one. It reports whether the stored column changes.
func secret(cur string, stored bool, in string, clear bool) (string, bool) {
	switch {
	case in != "":
		return in, in != cur
	case clear:
		return "", stored
	}
	return cur, false
}

// text trims an optional field and checks its length.
func text(dst *string, in *string, name string, limit int) error {
	if in == nil {
		return nil
	}
	if v := strings.TrimSpace(*in); len(v) <= limit {
		*dst = v
		return nil
	}
	return invalid(fmt.Sprintf("%s must be at most %d characters.", name, limit))
}

// httpURL accepts a field's new value when it is "" or an absolute http(s)
// URL, without its trailing slash.
func httpURL(v *string, in *string, name string) error {
	if in == nil {
		return nil
	}
	*v = strings.TrimRight(*v, "/")
	if u, err := url.Parse(*v); *v != "" && (err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "") {
		return invalid(name + " must be an http(s) URL.")
	}
	return nil
}

// merge applies d over cur and names the changed fields. The current key
// survives only while the provider and the base URL stay: a stored key never
// follows a request to another host.
func merge(cur llm.Settings, keyStored bool, d *draft) (llm.Settings, []string, error) {
	next, cutoff := cur, ""
	if !cur.KnowledgeCutoff.IsZero() {
		cutoff = cur.KnowledgeCutoff.Format(time.DateOnly)
	}
	newCutoff := cutoff
	for _, err := range []error{text(&next.Provider, d.Provider, "provider", 40), text(&next.Model, d.Model, "model", 200),
		text(&next.BaseURL, d.BaseURL, "baseUrl", 2048), text(&next.Effort, d.Effort, "effort", 10),
		text(&next.AWSRegion, d.AWSRegion, "awsRegion", 40), text(&newCutoff, d.KnowledgeCutoff, "knowledgeCutoff", 10),
		httpURL(&next.BaseURL, d.BaseURL, "baseUrl")} {
		if err != nil {
			return cur, nil, err
		}
	}
	next.Provider = strings.ToLower(next.Provider)
	if next.Provider == "chatgpt" {
		next.BaseURL = "" // a fixed endpoint (withChatGPT): store no base URL that a later switch would inherit
	}
	if next.Effort != "" && next.Effort != "on" && next.Effort != "off" {
		return cur, nil, invalid(`effort must be "", "on" or "off".`)
	}
	if newCutoff != cutoff {
		t, err := time.Parse(time.DateOnly, newCutoff)
		if newCutoff != "" && err != nil {
			return cur, nil, invalid("knowledgeCutoff must be a date YYYY-MM-DD.")
		}
		next.KnowledgeCutoff = t
	}
	if len(d.APIKey) > 4096 {
		return cur, nil, invalid("apiKey is too long.")
	}
	moved := next.Provider != cur.Provider || next.BaseURL != cur.BaseURL
	var keyChanged bool
	next.APIKey, keyChanged = secret(cur.APIKey, keyStored, strings.TrimSpace(d.APIKey), d.ClearKey || moved)
	var changed []string
	for _, f := range []struct {
		name string
		diff bool
	}{{"provider", next.Provider != cur.Provider}, {"model", next.Model != cur.Model}, {"baseUrl", next.BaseURL != cur.BaseURL},
		{"effort", next.Effort != cur.Effort}, {"awsRegion", next.AWSRegion != cur.AWSRegion}, {"knowledgeCutoff", newCutoff != cutoff},
		{"apiKey", keyChanged}} {
		if f.diff {
			changed = append(changed, f.name)
		}
	}
	return next, changed, nil
}

// mergeSearch applies d over cur like merge: a new crawl base drops the
// stored crawl token unless a new one comes with it.
func mergeSearch(cur search, stored map[string]bool, d *searchDraft) (search, []string, error) {
	next := cur
	if err := text(&next.crawlBase, d.CrawlBase, "crawlBase", 2048); err != nil {
		return cur, nil, err
	}
	if err := httpURL(&next.crawlBase, d.CrawlBase, "crawlBase"); err != nil {
		return cur, nil, err
	}
	var changed []string
	var braveChanged, tokenChanged bool
	next.brave, braveChanged = secret(cur.brave, stored["braveApiKey"], strings.TrimSpace(d.BraveAPIKey), d.ClearBraveAPIKey)
	next.crawlToken, tokenChanged = secret(cur.crawlToken, stored["crawlToken"], strings.TrimSpace(d.CrawlToken),
		d.ClearCrawlToken || next.crawlBase != cur.crawlBase)
	if braveChanged {
		changed = append(changed, "braveApiKey")
	}
	if next.crawlBase != cur.crawlBase {
		changed = append(changed, "crawlBase")
	}
	if tokenChanged {
		changed = append(changed, "crawlToken")
	}
	return next, changed, nil
}

// put saves a partial change. Any AI change is tested first (one Test on
// the merged draft): a failure saves nothing and returns the provider's
// message. Only the changed columns are written, in one transaction with
// the settings.update audit row naming them.
func (s *Service) put(w http.ResponseWriter, r *http.Request) {
	var b body
	if err := httpx.Decode(w, r, &b); err != nil {
		fail(w, r, invalid(err.Error()))
		return
	}
	ctx, admin := r.Context(), httpx.UserID(r.Context())
	cur, err := s.read(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	next, aiChanged, searchChanged := cur, []string(nil), []string(nil)
	if b.AI != nil && s.envMode() {
		err = errEnv
	}
	if b.AI != nil && err == nil {
		next.ai, aiChanged, err = merge(cur.ai, cur.stored["apiKey"], b.AI)
	}
	if b.Search != nil && err == nil {
		next.search, searchChanged, err = mergeSearch(cur.search, cur.stored, b.Search)
	}
	changed := append(aiChanged, searchChanged...)
	var sets []string
	var args []any
	if err == nil {
		sets, args, err = s.columns(admin, next, changed) // seals new secrets: no SECRET_KEY fails here, before a test
	}
	if err == nil && len(aiChanged) > 0 && next.ai.Provider != "" {
		if err = s.check(ctx, s.withChatGPT(next.ai)); err != nil {
			err = invalid(err.Error())
		}
	}
	if err == nil && len(changed) > 0 {
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "update ai_settings set "+strings.Join(sets, ", ")+" where id = 1", args...); err != nil {
				return err
			}
			return audit(ctx, tx, admin, "settings.update", changed)
		})
		if err == nil {
			err = s.reload(ctx)
		}
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	s.get(w, r)
}

// columns are the update's assignments for the changed fields of next,
// secrets sealed (a cleared one is null).
func (s *Service) columns(admin string, next row, changed []string) (sets []string, args []any, err error) {
	sets, args = []string{"updated_at = now()", "updated_by = $1"}, []any{admin}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	sealed := func(col, hintCol, v string) {
		var b []byte
		if v != "" && err == nil {
			if b, err = s.box.seal([]byte(v)); err != nil {
				err = invalid(err.Error())
			}
		}
		add(col, b)
		add(hintCol, hint(v))
	}
	a, k := next.ai, next.search
	for _, name := range changed {
		switch name {
		case "provider":
			add("provider", a.Provider)
		case "model":
			add("model", a.Model)
		case "baseUrl":
			add("base_url", a.BaseURL)
		case "effort":
			add("effort", a.Effort)
		case "awsRegion":
			add("aws_region", a.AWSRegion)
		case "knowledgeCutoff":
			var t *time.Time
			if !a.KnowledgeCutoff.IsZero() {
				t = &a.KnowledgeCutoff
			}
			add("knowledge_cutoff", t)
		case "apiKey":
			sealed("secret", "secret_hint", a.APIKey)
		case "braveApiKey":
			sealed("brave_api_key", "brave_hint", k.brave)
		case "crawlBase":
			add("crawl_base", k.crawlBase)
		case "crawlToken":
			sealed("crawl_token", "crawl_hint", k.crawlToken)
		}
	}
	return sets, args, err
}

// reload reads the row and puts it in force.
func (s *Service) reload(ctx context.Context) error {
	r, err := s.read(ctx)
	if err == nil {
		s.apply(r)
	}
	return err
}

// audit writes one admin_audit row in tx naming the changed fields, never
// their values (the same row admin's record writes, without a target user).
func audit(ctx context.Context, tx pgx.Tx, admin, action string, changed []string) error {
	err := tx.QueryRow(ctx, `insert into admin_audit (admin_id, admin_email, action, detail)
		select id, email, $2, $3 from users where id = $1 returning id`, admin, action, map[string]any{"changed": changed}).Scan(new(string))
	if errors.Is(err, pgx.ErrNoRows) {
		return errForbidden
	}
	return err
}

// check runs the provider test with a minute's budget.
func (s *Service) check(ctx context.Context, st llm.Settings) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return s.test(ctx, st)
}

// draftSettings merges a {ai: draft} body over the settings in force.
func (s *Service) draftSettings(w http.ResponseWriter, r *http.Request) (llm.Settings, error) {
	var b body
	if err := httpx.Decode(w, r, &b); err != nil {
		return llm.Settings{}, invalid(err.Error())
	}
	cur, err := s.read(r.Context())
	if err != nil {
		return llm.Settings{}, err
	}
	base, stored := cur.ai, cur.stored["apiKey"]
	if s.envMode() {
		base, stored = s.env, false
	}
	if b.AI != nil {
		if base, _, err = merge(base, stored, b.AI); err != nil {
			return llm.Settings{}, err
		}
	}
	return s.withChatGPT(base), nil
}

// testDraft is POST /api/admin/settings/test: always 200, ok or the provider's error.
func (s *Service) testDraft(w http.ResponseWriter, r *http.Request) {
	st, err := s.draftSettings(w, r)
	var bad invalid
	switch {
	case errors.As(err, &bad):
	case err != nil:
		fail(w, r, err)
		return
	case st.Provider == "":
		err = invalid("Choose a provider first.")
	default:
		start := time.Now()
		if err = s.check(r.Context(), st); err == nil {
			httpx.JSON(w, http.StatusOK, map[string]any{"ok": true, "provider": st.Provider, "model": st.Model,
				"latencyMs": time.Since(start).Milliseconds()})
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
}

// listModels is POST /api/admin/settings/models: the draft provider's models.
func (s *Service) listModels(w http.ResponseWriter, r *http.Request) {
	st, err := s.draftSettings(w, r)
	if err != nil {
		fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	models, err := s.models(ctx, st)
	if err != nil {
		fail(w, r, invalid(err.Error()))
		return
	}
	if models == nil {
		models = []llm.ModelInfo{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"models": models})
}
