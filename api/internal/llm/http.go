package llm

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// httpClient serves the HTTP wires. It has no overall timeout: a stream runs
// as long as the model writes, and the caller's context ends it.
var httpClient = &http.Client{Transport: func() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 5 * time.Minute // a local model may load and read a long prompt first
	return t
}()}

const (
	maxRetries    = 2
	maxRetryAfter = 30 * time.Second // a longer Retry-After fails the call instead
)

type obj = map[string]any

// endpoint is one provider's base URL and credentials.
type endpoint struct {
	base    string      // no trailing slash
	headers http.Header // static: API key, API version
	tokens  TokenSource // chatgpt: the bearer token, refreshed once after a 401
	secret  string      // removed from provider messages
}

// send makes one request and returns its 2xx response, retrying before any
// response body is read: up to maxRetries more attempts on network errors,
// 408, 429 and 5xx (never a ChatGPT plan error below 500), after Retry-After
// or 0.5 s doubling. noRetries makes one attempt. A non-2xx response is a
// *ProviderError.
func (e *endpoint) send(ctx context.Context, method, path string, body []byte, noRetries bool) (*http.Response, error) {
	refresh, refreshed := false, false
	for attempt := 0; ; {
		resp, err := e.do(ctx, method, path, body, refresh)
		refresh = false
		var wait time.Duration
		switch {
		case errors.As(err, new(tokenError)) || err != nil && ctx.Err() != nil:
			return nil, err
		case err != nil:
			wait, _ = retryAfter(nil, attempt)
		case resp.StatusCode < 300:
			return resp, nil
		default:
			pe := readError(resp, e.secret)
			if pe.Status == http.StatusUnauthorized && e.tokens != nil && !refreshed {
				refresh, refreshed = true, true // revoked or rotated elsewhere: one forced refresh
				continue
			}
			var ok bool
			if wait, ok = retryAfter(resp.Header, attempt); !ok || !retryable(pe) {
				return nil, pe
			}
			err = pe
		}
		if noRetries || attempt == maxRetries {
			return nil, err
		}
		attempt++
		if err := pause(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// tokenError is a TokenSource failure: no request was sent.
type tokenError struct{ error }

func (e tokenError) Unwrap() error { return e.error }

func (e *endpoint) do(ctx context.Context, method, path string, body []byte, refresh bool) (*http.Response, error) {
	var rd io.Reader = http.NoBody
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header = e.headers.Clone()
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.tokens != nil {
		get := e.tokens.Token
		if refresh {
			get = e.tokens.Refresh
		}
		tok, err := get(ctx)
		if err != nil {
			return nil, tokenError{err}
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return httpClient.Do(req)
}

func retryable(pe *ProviderError) bool {
	if pe.Status < 500 && (strings.HasPrefix(pe.Code, "subscription_sharing_") || strings.HasPrefix(pe.Code, "chatpass_v2_")) {
		return false // a plan, policy or eligibility answer: the same request gets the same one
	}
	return pe.Status == http.StatusRequestTimeout || pe.Status == http.StatusTooManyRequests || pe.Status >= 500
}

// retryAfter is the wait before retry attempt+1: the Retry-After header (seconds
// or a date) when sent, else 0.5 s doubling; false when it is too long to wait.
func retryAfter(h http.Header, attempt int) (time.Duration, bool) {
	d := 500 * time.Millisecond << attempt
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			d = time.Duration(s) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			d = time.Until(t)
		}
	}
	return max(d, 0), d <= maxRetryAfter
}

func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// readError reads a non-2xx response (at most 64 KiB) as a *ProviderError.
func readError(resp *http.Response, secret string) *ProviderError {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	pe := errorFrom(b)
	pe.Status = resp.StatusCode
	pe.Msg = cmp.Or(pe.Msg, http.StatusText(resp.StatusCode))
	if len(secret) >= 8 { // real keys are long; a short one would garble the message
		pe.Msg = strings.ReplaceAll(pe.Msg, secret, "[redacted]")
	}
	return pe
}

// planMessages explain the ChatGPT plan errors an admin can act on.
var planMessages = map[string]string{
	"subscription_sharing_usage_limit_exceeded": "ChatGPT plan limit reached; see https://chatgpt.com/settings/usage",
	"subscription_sharing_invalid_user":         "the ChatGPT sign-in is no longer valid; sign in again",
}

// errorFrom reads a provider error body or stream event: {"error":{"message",
// "type"|"code","param"}}, {"error":"text"}, {"type":"error","code","message"},
// {"detail":…} (ChatGPT admission), a one-element array of one (Gemini), or text.
func errorFrom(b []byte) *ProviderError {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return &ProviderError{Msg: clip(string(b))}
	}
	if a, ok := v.([]any); ok && len(a) > 0 {
		v = a[0]
	}
	m, _ := v.(map[string]any)
	switch e := m["error"].(type) {
	case map[string]any:
		m = e
	case string:
		return &ProviderError{Msg: e}
	}
	str := func(k string) string { s, _ := m[k].(string); return s }
	pe := &ProviderError{Code: cmp.Or(str("code"), str("type"), str("status")), param: str("param")}
	pe.Msg = cmp.Or(planMessages[pe.Code], str("message"), str("detail"), clip(string(b)))
	return pe
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:500] + "…"
	}
	return s
}

// readSSE calls fn for each event of a server-sent event stream: data-only
// (Chat Completions) or event+data (Anthropic, Responses). Comments, pings
// and the [DONE] marker are skipped.
func readSSE(r io.Reader, fn func(event string, data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 32<<20) // a completed response or encrypted reasoning can pass 64 KiB
	var ev string
	var data []byte
	flush := func() error {
		defer func() { ev, data = "", nil }()
		if data == nil || string(data) == "[DONE]" {
			return nil
		}
		return fn(ev, data)
	}
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			if err := flush(); err != nil {
				return err
			}
		case line[0] == ':': // comment or keep-alive
		case bytes.HasPrefix(line, []byte("event:")):
			ev = string(bytes.TrimSpace(line[6:]))
		case bytes.HasPrefix(line, []byte("data:")):
			d := bytes.TrimPrefix(line[5:], []byte(" "))
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, d...) // append copies: the Scanner reuses its buffer
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

// stream POSTs body to path and feeds each SSE event to fn, after onRaw.
// Failures are ErrUpstream under op (a provider rejection as *ProviderError),
// except a callbackError, which comes back as the caller's own error.
func (e *endpoint) stream(ctx context.Context, op, path string, body []byte, noRetries bool, onRaw func(), fn func(event string, data []byte) error) error {
	resp, err := e.send(ctx, http.MethodPost, path, body, noRetries)
	if err != nil {
		return upstream(op, err)
	}
	defer resp.Body.Close()
	err = readSSE(resp.Body, func(event string, data []byte) error {
		if onRaw != nil {
			onRaw() // admission to the stream precedes parsing or suppression
		}
		return fn(event, data)
	})
	var cb callbackError
	if errors.As(err, &cb) {
		return cb.err // the caller's own error, e.g. client went away
	}
	if err != nil {
		return upstream(op, err)
	}
	return nil
}

// getJSON GETs path into out, with one attempt.
func (e *endpoint) getJSON(ctx context.Context, op, path string, out any) error {
	resp, err := e.send(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return upstream(op, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return upstream(op, errors.New("unreadable model list"))
	}
	return nil
}

// jsonBody encodes v without HTML escaping, so replayed provider content
// goes back as it came.
func jsonBody(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), err
}

// emit sends a text delta to onEvent (nil for a one-shot completion).
func emit(onEvent func(Event) error, text string) error {
	if onEvent == nil || text == "" {
		return nil
	}
	if err := onEvent(Event{Kind: "delta", Text: text}); err != nil {
		return callbackError{err}
	}
	return nil
}

// objectArgs is the call's input when raw is a JSON object ("" is {}), else
// {} marked invalid: the model gets an error result and can try again.
func objectArgs(id, name, raw string) toolCall {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil || m == nil {
		return toolCall{ID: id, Name: name, Input: json.RawMessage("{}"), invalid: true}
	}
	return toolCall{ID: id, Name: name, Input: json.RawMessage(raw)}
}

func dataURL(mime string, b []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}
