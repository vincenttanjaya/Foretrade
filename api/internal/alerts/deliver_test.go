package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/notify"
)

type fakeMailer struct {
	mu   sync.Mutex
	sent []string // "to|title|body|link"
	err  error
}

func (f *fakeMailer) Send(_ context.Context, to string, m notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, to+"|"+m.Title+"|"+m.Body+"|"+m.Link)
	return f.err
}

func (f *fakeMailer) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

// fakeLark is a Lark bot behind the real webhook host: it records each post's
// title and answers reply.
type fakeLark struct {
	mu     sync.Mutex
	titles []string
	signed []bool
	reply  string
}

func newFakeLark(t *testing.T) (*fakeLark, *notify.Lark) {
	f := &fakeLark{reply: `{"code":0,"msg":"success"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Sign    string `json:"sign"`
			Content struct {
				Post map[string]struct{ Title string }
			}
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.titles, f.signed = append(f.titles, p.Content.Post["en_us"].Title), append(f.signed, p.Sign != "")
		reply := f.reply
		f.mu.Unlock()
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: rewrite(strings.TrimPrefix(srv.URL, "http://"))}
	return f, &notify.Lark{Client: client}
}

func (f *fakeLark) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.titles
	f.titles = nil
	return out
}

type rewrite string

func (host rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = "http", string(host)
	return http.DefaultTransport.RoundTrip(r)
}

const hook = "https://open.larksuite.com/open-apis/bot/v2/hook/0a1b2c3d-4e5f"

// Channels: settings round-trip, new bell items go out once per channel with
// absolute links, nothing from before a channel was turned on, a failed send
// keeps its error, and a disabled account gets nothing.
func TestDeliver(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	mail := &fakeMailer{}
	lark, client := newFakeLark(t)
	e.Mailer, e.Lark, e.AppURL = mail, client, "https://trade.test"
	type channels struct{ Channels Channels }
	get := func() Channels {
		return apptest.JSON[channels](t, e.Do("GET", "/api/notifications/channels", nil)).Channels
	}

	if c := get(); c.Email.Enabled || !c.Email.Available || c.Email.Address == "" || c.Lark.Enabled || c.Lark.Webhook != nil || c.Lark.Signed {
		t.Fatalf("defaults %+v", c)
	}
	for body, want := range map[string]string{
		`{"lark":true}`: "webhook URL",
		`{"lark":true,"larkWebhook":"https://evil.example/open-apis/bot/v2/hook/x"}`: "Lark custom bot",
		`{"larkSecret":"` + strings.Repeat("s", 129) + `"}`:                          "128",
	} {
		if w := e.Do("PUT", "/api/notifications/channels", json.RawMessage(body)); w.Code != 400 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}

	// Fired before the channels are on: never sent.
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	fired := func(symbol string) {
		exec(`insert into alerts (user_id, symbol, op, price, status, fired_price, fired_at) values ($1, $2, 'below', 175, 'fired', 174.8, clock_timestamp())`, e.User, symbol)
	}
	fired("ETH")
	w := e.Do("PUT", "/api/notifications/channels", map[string]any{"email": true, "lark": true, "larkWebhook": hook, "larkSecret": "s3cret"})
	c := apptest.JSON[channels](t, w).Channels
	if w.Code != 200 || !c.Email.Enabled || !c.Lark.Enabled || c.Lark.Webhook == nil || *c.Lark.Webhook != "https://open.larksuite.com/open-apis/bot/v2/hook/…4e5f" || !c.Lark.Signed {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if err := Deliver(ctx, e.Deps); err != nil || len(mail.take()) != 0 || len(lark.take()) != 0 {
		t.Fatalf("history replayed: %v", err)
	}

	fired("SOL")
	if err := Deliver(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	got := mail.take()
	if len(got) != 1 || !strings.HasSuffix(got[0], "|SOL fell below $175.00|Now $174.80|https://trade.test/automations") || !strings.Contains(got[0], "@") {
		t.Fatalf("email %v", got)
	}
	if titles := lark.take(); len(titles) != 1 || titles[0] != "SOL fell below $175.00" || !lark.signed[len(lark.signed)-1] {
		t.Fatalf("lark %v", titles)
	}
	if err := Deliver(ctx, e.Deps); err != nil || len(mail.take()) != 0 || len(lark.take()) != 0 {
		t.Fatalf("sent twice: %v", err)
	}

	// Lark refuses: the error is kept, email still goes, neither retries.
	lark.mu.Lock()
	lark.reply = `{"code":19021,"msg":"sign match fail"}`
	lark.mu.Unlock()
	fired("BTC")
	if err := Deliver(ctx, e.Deps); err != nil || len(mail.take()) != 1 || len(lark.take()) != 1 {
		t.Fatalf("refused: %v", err)
	}
	var errText string
	e.DB.QueryRow(ctx, `select coalesce(error, '') from notification_deliveries where user_id = $1 and channel = 'lark' order by sent_at desc limit 1`, e.User).Scan(&errText)
	if !strings.Contains(errText, "19021") {
		t.Fatalf("stored error %q", errText)
	}
	if err := Deliver(ctx, e.Deps); err != nil || len(lark.take()) != 0 {
		t.Fatalf("retried: %v", err)
	}

	// A busy account: 10 per pass per channel, the rest next pass.
	for range 12 {
		fired("ADA")
	}
	Deliver(ctx, e.Deps)
	if n := len(mail.take()); n != deliverBatch {
		t.Fatalf("first pass %d", n)
	}
	Deliver(ctx, e.Deps)
	if n := len(mail.take()); n != 2 {
		t.Fatalf("second pass %d", n)
	}
	lark.take()

	// Email off, and a disabled account: nothing.
	if w := e.Do("PUT", "/api/notifications/channels", map[string]any{"email": false}); w.Code != 200 || apptest.JSON[channels](t, w).Channels.Email.Enabled {
		t.Fatalf("email off: %s", w.Body)
	}
	fired("DOT")
	exec("update users set disabled_at = now() where id = $1", e.User)
	if err := Deliver(ctx, e.Deps); err != nil || len(mail.take()) != 0 || len(lark.take()) != 0 {
		t.Fatalf("disabled: %v", err)
	}
	exec("update users set disabled_at = null where id = $1", e.User)
	if err := Deliver(ctx, e.Deps); err != nil || len(mail.take()) != 0 || len(lark.take()) != 1 {
		t.Fatalf("re-enabled: lark only")
	}

	// Clearing the webhook needs Lark off; then it is gone.
	if w := e.Do("PUT", "/api/notifications/channels", map[string]any{"larkWebhook": ""}); w.Code != 400 {
		t.Fatalf("cleared while on: %d", w.Code)
	}
	if c := apptest.JSON[channels](t, e.Do("PUT", "/api/notifications/channels", map[string]any{"lark": false, "larkWebhook": "", "larkSecret": ""})).Channels; c.Lark.Enabled || c.Lark.Webhook != nil || c.Lark.Signed {
		t.Fatalf("cleared %+v", c)
	}
}

// A server without SES refuses to turn email on; the test send reports each
// channel and is throttled.
func TestChannelsTest(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	if w := e.Do("PUT", "/api/notifications/channels", map[string]any{"email": true}); w.Code != 400 || !strings.Contains(w.Body.String(), "not set up") {
		t.Fatalf("no mailer: %d %s", w.Code, w.Body)
	}
	lark, client := newFakeLark(t)
	e.Lark, e.AppURL = client, "https://trade.test"
	if w := e.Do("PUT", "/api/notifications/channels", map[string]any{"lark": true, "larkWebhook": hook}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	w := e.Do("POST", "/api/notifications/channels/test", map[string]any{})
	var out map[string]string
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["email"] != "off" || out["lark"] != "sent" || len(lark.take()) != 1 || lark.signed[0] {
		t.Fatalf("test: %d %v", w.Code, out)
	}
	if w := e.Do("POST", "/api/notifications/channels/test", map[string]any{}); w.Code != 429 {
		t.Fatalf("throttle: %d", w.Code)
	}
}
