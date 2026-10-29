package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidLarkWebhook(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://open.larksuite.com/open-apis/bot/v2/hook/0a1b2c3d-4e5f":    true,
		" https://open.feishu.cn/open-apis/bot/v2/hook/abc ":                true,
		"http://open.larksuite.com/open-apis/bot/v2/hook/abc":               false, // not TLS
		"https://evil.example/open-apis/bot/v2/hook/abc":                    false,
		"https://open.larksuite.com.evil.example/open-apis/bot/v2/hook/abc": false,
		"https://user@open.larksuite.com/open-apis/bot/v2/hook/abc":         false,
		"https://open.larksuite.com/open-apis/bot/v2/hook/":                 false,
		"https://open.larksuite.com/open-apis/bot/v2/hook/abc/../x":         false,
		"https://open.larksuite.com/open-apis/bot/v2/hook/abc?x=1":          false,
		"https://open.larksuite.com/other/path":                             false,
		"":                                                                  false,
	} {
		if ValidLarkWebhook(raw) != want {
			t.Errorf("%q: want %v", raw, want)
		}
	}
	if got := MaskLarkWebhook("https://open.larksuite.com/open-apis/bot/v2/hook/0a1b2c3d-4e5f"); got != "https://open.larksuite.com/open-apis/bot/v2/hook/…4e5f" {
		t.Errorf("mask %q", got)
	}
}

// The signature matches Lark's documented algorithm (key = ts + "\n" + secret,
// empty message), and the post carries the title, body and link.
func TestLarkSend(t *testing.T) {
	var got map[string]any
	reply := `{"code":0,"msg":"success"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "open.larksuite.com" || r.URL.Path != "/open-apis/bot/v2/hook/abc" {
			t.Errorf("request to %s%s", r.Host, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		got = nil
		json.Unmarshal(raw, &got)
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	// The real Lark URL, delivered to the test server.
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(r)
	})}
	l := &Lark{Client: client, Now: func() time.Time { return time.Unix(1599360473, 0) }}
	hook := "https://open.larksuite.com/open-apis/bot/v2/hook/abc"
	m := Message{Title: "SOL fell below $175.00", Body: "Now $174.80", Link: "https://app.example.com/automations"}
	if err := l.Send(context.Background(), hook, "demo", m); err != nil {
		t.Fatal(err)
	}
	if got["timestamp"] != "1599360473" || got["sign"] != "l1N0gAcBjdwBvGm1xMjOF0XSyaLRpR7tuO5dHfhAYc8=" || got["msg_type"] != "post" {
		t.Fatalf("payload %v", got)
	}
	raw, _ := json.Marshal(got["content"])
	for _, s := range []string{m.Title, m.Body, m.Link} {
		if !strings.Contains(string(raw), s) {
			t.Errorf("content lacks %q: %s", s, raw)
		}
	}
	// Unsigned: no timestamp or sign.
	if err := l.Send(context.Background(), hook, "", m); err != nil || got["sign"] != nil {
		t.Fatalf("unsigned: %v %v", err, got)
	}
	// Lark refuses with a code: an error naming it.
	reply = `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`
	if err := l.Send(context.Background(), hook, "demo", m); err == nil || !strings.Contains(err.Error(), "19021") {
		t.Fatalf("refused: %v", err)
	}
	if err := l.Send(context.Background(), "https://evil.example/x", "", m); err == nil {
		t.Fatal("foreign host sent")
	}
}

func TestEmailBodies(t *testing.T) {
	text, page := emailBodies(Message{Title: "BTC <rose>", Body: "Now $1 & more", Link: "https://app.example.com/x?a=1&b=2"})
	if !strings.Contains(text, "Now $1 & more") || !strings.Contains(text, "https://app.example.com/x?a=1&b=2") {
		t.Errorf("text %q", text)
	}
	if !strings.Contains(page, "BTC &lt;rose&gt;") || !strings.Contains(page, "Now $1 &amp; more") || !strings.Contains(page, `href="https://app.example.com/x?a=1&amp;b=2"`) {
		t.Errorf("html %q", page)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
