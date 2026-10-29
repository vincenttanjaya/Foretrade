package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// Settings choose the provider behind a Client. The zero value is "AI off".
type Settings struct {
	Provider        string      // a Providers id ("openai", "anthropic", "ollama", …); "" is off
	Model           string      // provider model id; "" → the preset's default, if it has one
	BaseURL         string      // "" → the preset's default base URL (bedrock: the endpoint override)
	APIKey          string      // never log; optional for local servers (ollama, lmstudio, custom)
	Effort          string      // "" → the preset's default | "on" | "off": send the reasoning effort or not
	AWSRegion       string      // bedrock
	KnowledgeCutoff time.Time   // zero → DefaultKnowledgeCutoff
	ChatGPT         TokenSource // chatgpt: the signed-in plan's access tokens
}

// TokenSource hands out ChatGPT access tokens; the settings layer owns storage and refresh.
type TokenSource interface {
	Token(ctx context.Context) (string, error)   // a valid access token, refreshed when near expiry
	Refresh(ctx context.Context) (string, error) // force a refresh (after a 401), then the new token
}

// ProviderInfo is one preset the admin console offers.
type ProviderInfo struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Wire           string `json:"wire"` // chat | messages | responses | converse
	DefaultBaseURL string `json:"defaultBaseUrl"`
	DefaultModel   string `json:"defaultModel"`
	KeyEnv         string `json:"keyEnv,omitempty"` // vendor env fallback for AI_API_KEY
	NeedsKey       bool   `json:"needsKey"`
	EffortDefault  bool   `json:"effortDefault"`
	KeyURL         string `json:"keyUrl,omitempty"` // where to get a key
	Note           string `json:"note,omitempty"`
}

// ModelInfo is one entry of a provider's model list.
type ModelInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// DefaultKnowledgeCutoff applies when Settings.KnowledgeCutoff is zero.
var DefaultKnowledgeCutoff = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

// ErrNotConfigured is every call's error while no provider is set.
var ErrNotConfigured = fmt.Errorf("%w: no AI provider configured", ErrUpstream)

const (
	wireChat      = "chat"
	wireMessages  = "messages"
	wireResponses = "responses"
	wireConverse  = "converse"
)

// Providers is the preset catalogue. A default model is set only where its id
// is a stable alias; elsewhere the admin picks one from the provider's list.
var Providers = []ProviderInfo{
	{ID: "openai", Label: "OpenAI (API key)", Wire: wireResponses, DefaultBaseURL: "https://api.openai.com/v1",
		KeyEnv: "OPENAI_API_KEY", NeedsKey: true, EffortDefault: true, KeyURL: "https://platform.openai.com/api-keys"},
	{ID: "chatgpt", Label: "ChatGPT plan (sign in with ChatGPT)", Wire: wireResponses, DefaultBaseURL: "https://api.openai.com/v1",
		EffortDefault: true, Note: "Uses the signed-in person's ChatGPT plan. A plan belongs to one person, so Foretrade " +
			"switches to owner-only mode: only admins can use AI features. Use an API key to share AI with other users."},
	{ID: "anthropic", Label: "Anthropic Claude (API key)", Wire: wireMessages, DefaultBaseURL: "https://api.anthropic.com",
		DefaultModel: "claude-sonnet-5-5", KeyEnv: "ANTHROPIC_API_KEY", NeedsKey: true, EffortDefault: true,
		KeyURL: "https://console.anthropic.com/settings/keys"},
	{ID: "bedrock", Label: "Amazon Bedrock (AWS credentials)", Wire: wireConverse, DefaultModel: "global.anthropic.claude-sonnet-5-5",
		EffortDefault: true, Note: "Uses the AWS credentials of the server (environment, profile or instance role) " +
			"and the region below. With the default effort setting, only Claude Sonnet 5.5 gets an effort level."},
	{ID: "openrouter", Label: "OpenRouter (many models, one key)", Wire: wireChat, DefaultBaseURL: "https://openrouter.ai/api/v1",
		KeyEnv: "OPENROUTER_API_KEY", NeedsKey: true, EffortDefault: true, KeyURL: "https://openrouter.ai/settings/keys"},
	{ID: "gemini", Label: "Google Gemini (API key)", Wire: wireChat, DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		KeyEnv: "GEMINI_API_KEY", NeedsKey: true, EffortDefault: true, KeyURL: "https://aistudio.google.com/apikey"},
	{ID: "groq", Label: "Groq", Wire: wireChat, DefaultBaseURL: "https://api.groq.com/openai/v1",
		KeyEnv: "GROQ_API_KEY", NeedsKey: true, KeyURL: "https://console.groq.com/keys"},
	{ID: "deepseek", Label: "DeepSeek", Wire: wireChat, DefaultBaseURL: "https://api.deepseek.com", DefaultModel: "deepseek-flash",
		KeyEnv: "DEEPSEEK_API_KEY", NeedsKey: true, KeyURL: "https://platform.deepseek.com/api_keys"},
	{ID: "xai", Label: "xAI Grok", Wire: wireChat, DefaultBaseURL: "https://api.x.ai/v1",
		KeyEnv: "XAI_API_KEY", NeedsKey: true, KeyURL: "https://console.x.ai"},
	{ID: "mistral", Label: "Mistral AI", Wire: wireChat, DefaultBaseURL: "https://api.mistral.ai/v1", DefaultModel: "mistral-large-latest",
		KeyEnv: "MISTRAL_API_KEY", NeedsKey: true, KeyURL: "https://console.mistral.ai/api-keys"},
	{ID: "ollama", Label: "Ollama (local models)", Wire: wireChat, DefaultBaseURL: "http://host.docker.internal:11434/v1",
		Note: "Runs models on this computer. From Docker the default URL reaches the host through host.docker.internal; " +
			"without Docker use http://localhost:11434/v1. On Linux, start Ollama with OLLAMA_HOST=0.0.0.0 so the container " +
			"can reach it. Pick a model that supports tool calling."},
	{ID: "lmstudio", Label: "LM Studio (local models)", Wire: wireChat, DefaultBaseURL: "http://host.docker.internal:1234/v1",
		Note: "Runs models on this computer: start the server in LM Studio's Developer tab. From Docker the default URL " +
			"reaches the host through host.docker.internal; without Docker use http://localhost:1234/v1. On Linux, turn on " +
			"\"Serve on Local Network\" so the container can reach it. Pick a model that supports tool calling."},
	{ID: "custom", Label: "Other OpenAI-compatible server", Wire: wireChat,
		Note: "Any server that speaks the OpenAI Chat Completions API (vLLM, llama.cpp, LiteLLM, …). Enter its base URL, usually ending in /v1."},
}

// ProviderByID finds a preset.
func ProviderByID(id string) (ProviderInfo, bool) {
	i := slices.IndexFunc(Providers, func(p ProviderInfo) bool { return p.ID == id })
	if i < 0 {
		return ProviderInfo{}, false
	}
	return Providers[i], true
}

// NewFromSettings builds a client for s; invalid settings are an error.
func NewFromSettings(s Settings) (*Client, error) {
	c := &Client{}
	return c, c.Configure(s)
}

// Configure validates s and swaps the provider atomically; on error the old one
// stays. In-flight calls finish on the provider they started with.
func (c *Client) Configure(s Settings) error {
	if s.Provider == "" {
		c.cur.Store(nil)
		return nil
	}
	a, err := build(s)
	if err != nil {
		return err
	}
	if a.model == "" {
		p, _ := ProviderByID(a.id)
		return fmt.Errorf("choose a model for %s", p.Label)
	}
	c.cur.Store(a)
	return nil
}

// build validates s and makes its provider; the model may still be empty.
func build(s Settings) (*active, error) {
	p, ok := ProviderByID(s.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown AI provider %q", s.Provider)
	}
	s.Model, s.BaseURL, s.APIKey = strings.TrimSpace(s.Model), strings.TrimSpace(s.BaseURL), strings.TrimSpace(s.APIKey) // pasted values
	a := &active{id: p.ID, s: s, model: s.Model, effort: p.EffortDefault}
	if a.model == "" {
		a.model = p.DefaultModel
	}
	switch s.Effort {
	case "on":
		a.effort = true
	case "off":
		a.effort = false
	case "":
		if p.Wire == wireConverse {
			a.effort = sonnetEffort(a.model) // today's gate: other Bedrock models reject the field
		}
	default:
		return nil, fmt.Errorf("effort must be on or off, not %q", s.Effort)
	}
	if p.Wire == wireConverse {
		var opts []func(*awsconfig.LoadOptions) error
		if s.AWSRegion != "" {
			opts = append(opts, awsconfig.WithRegion(s.AWSRegion))
		}
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
		if err != nil {
			return nil, fmt.Errorf("AWS configuration: %w", err)
		}
		if s.BaseURL != "" {
			cfg.BaseEndpoint = aws.String(s.BaseURL)
		}
		a.b = newConverse(cfg, a.model)
		return a, nil
	}

	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = p.DefaultBaseURL
	}
	if base == "" {
		return nil, errors.New("enter the server's base URL")
	}
	if u, err := url.Parse(base); err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("base URL %q is not an http(s) URL", base)
	}
	if p.NeedsKey && s.APIKey == "" {
		return nil, fmt.Errorf("%s needs an API key", p.Label)
	}
	ep := endpoint{base: base, headers: http.Header{}, secret: s.APIKey}
	switch {
	case p.ID == "chatgpt":
		if s.ChatGPT == nil {
			return nil, errors.New("sign in with ChatGPT first")
		}
		ep.tokens = s.ChatGPT
	case p.Wire == wireMessages:
		ep.headers.Set("x-api-key", s.APIKey)
		ep.headers.Set("anthropic-version", "2023-06-01")
		ep.base = strings.TrimSuffix(base, "/v1") // the paths carry /v1
	case s.APIKey != "":
		ep.headers.Set("Authorization", "Bearer "+s.APIKey)
	}
	switch p.Wire {
	case wireChat:
		a.b = &chatWire{endpoint: ep, name: p.ID, model: a.model}
	case wireMessages:
		a.b = &messagesWire{endpoint: ep, name: p.ID, model: a.model}
	case wireResponses:
		a.b = &responsesWire{endpoint: ep, name: p.ID, model: a.model, chatgpt: p.ID == "chatgpt"}
	}
	return a, nil
}

// Settings are the current settings, secrets included: callers mask them.
func (c *Client) Settings() Settings {
	if a := c.cur.Load(); a != nil {
		return a.s
	}
	return Settings{}
}

// Configured reports whether a provider is set.
func (c *Client) Configured() bool { return c.cur.Load() != nil }

// Model is the effective model id, "" when off.
func (c *Client) Model() string {
	if a := c.cur.Load(); a != nil {
		return a.model
	}
	return ""
}

// KnowledgeCutoff is the effective training-data cutoff.
func (c *Client) KnowledgeCutoff() time.Time {
	if t := c.Settings().KnowledgeCutoff; !t.IsZero() {
		return t
	}
	return DefaultKnowledgeCutoff
}

// testTokens bounds Test's reply; a max_tokens stop still passes.
const testTokens = 64

// Test makes one tiny completion ("Reply OK") with the configured effort and
// no retries; a max_tokens stop passes. Its error text never holds the API key.
func (c *Client) Test(ctx context.Context) error {
	a := c.cur.Load()
	if a == nil {
		return ErrNotConfigured
	}
	_, err := a.b.turn(ctx, &call{turns: userTurn(Message{Text: "Reply OK"}), effort: a.effortFor(Low),
		maxTokens: testTokens, noRetries: true}, nil, nil)
	return err
}

// ListModels asks the provider s describes for its models (s need not be saved).
func ListModels(ctx context.Context, s Settings) ([]ModelInfo, error) {
	if s.Provider == "" {
		return nil, ErrNotConfigured
	}
	a, err := build(s)
	if err != nil {
		return nil, err
	}
	return a.b.models(ctx)
}
