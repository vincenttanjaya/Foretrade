// Package notify delivers one bell item outside the app: an email through
// Amazon SES, or a post to the user's own Lark custom bot. It knows nothing of
// alerts or users; internal/alerts decides what to send and to whom.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Message is one bell item as sent: Link is absolute (the web app's origin + the item's path).
type Message struct {
	Title, Body, Link string
}

// Mailer sends one email.
type Mailer interface {
	Send(ctx context.Context, to string, m Message) error
}

// SES sends from From (e.g. "Foretrade <alerts@example.com>") with the
// instance's credentials. The From domain must be verified in SES, and the
// account out of the SES sandbox to reach unverified recipients.
type SES struct {
	Client *sesv2.Client
	From   string
}

// NewSES is an SES mailer in cfg's region.
func NewSES(cfg aws.Config, from string) *SES {
	return &SES{Client: sesv2.NewFromConfig(cfg), From: from}
}

func (s *SES) Send(ctx context.Context, to string, m Message) error {
	text, page := emailBodies(m)
	_, err := s.Client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.From),
		Destination:      &types.Destination{ToAddresses: []string{to}},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(m.Title), Charset: aws.String("UTF-8")},
			Body: &types.Body{
				Text: &types.Content{Data: aws.String(text), Charset: aws.String("UTF-8")},
				Html: &types.Content{Data: aws.String(page), Charset: aws.String("UTF-8")},
			},
		}},
	})
	return err
}

// emailBodies are the plain-text and HTML bodies of m.
func emailBodies(m Message) (string, string) {
	text := m.Body + "\n\n" + m.Link + "\n\n— Foretrade (paper trading). Turn these emails off in Account → Notifications.\n"
	page := `<div style="font-family:system-ui,-apple-system,Segoe UI,sans-serif;font-size:15px;line-height:1.5;color:#111">` +
		`<p style="font-size:17px;font-weight:600;margin:0 0 8px">` + html.EscapeString(m.Title) + `</p>` +
		`<p style="margin:0 0 16px">` + html.EscapeString(m.Body) + `</p>` +
		`<p style="margin:0 0 24px"><a href="` + html.EscapeString(m.Link) + `">Open in Foretrade</a></p>` +
		`<p style="font-size:12px;color:#666;margin:0">Paper trading only. Turn these emails off in Account → Notifications.</p></div>`
	return text, page
}

// Lark custom-bot webhooks: only Lark's and Feishu's own hosts, so a saved
// URL cannot point the server at anything else.
var larkHosts = map[string]bool{"open.larksuite.com": true, "open.feishu.cn": true}

const larkPath = "/open-apis/bot/v2/hook/"

// ValidLarkWebhook reports whether raw is a Lark or Feishu custom-bot webhook URL.
func ValidLarkWebhook(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && larkHosts[u.Host] && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		strings.HasPrefix(u.Path, larkPath) && len(u.Path) > len(larkPath) && !strings.Contains(u.Path[len(larkPath):], "/")
}

// MaskLarkWebhook shows a saved webhook without its token: the host and the token's last 4 characters.
func MaskLarkWebhook(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(u.Path) <= len(larkPath) {
		return ""
	}
	token := u.Path[len(larkPath):]
	return "https://" + u.Host + larkPath + "…" + token[max(0, len(token)-4):]
}

// Lark posts to custom-bot webhooks. Client nil uses a 10 s client; Now nil uses time.Now.
type Lark struct {
	Client *http.Client
	Now    func() time.Time
}

// Send posts m as a rich-text message to webhook, signed when secret is set
// (the bot's "Set signature verification"). Lark answers 200 with a nonzero
// code for a refused message (bad signature, keyword filter, rate limit).
func (l *Lark) Send(ctx context.Context, webhook, secret string, m Message) error {
	if !ValidLarkWebhook(webhook) {
		return errors.New("not a Lark custom-bot webhook URL")
	}
	payload := map[string]any{"msg_type": "post", "content": map[string]any{"post": map[string]any{"en_us": map[string]any{
		"title": m.Title,
		"content": [][]map[string]string{
			{{"tag": "text", "text": m.Body}},
			{{"tag": "a", "text": "Open in Foretrade", "href": m.Link}},
		},
	}}}}
	if secret != "" {
		now := time.Now
		if l.Now != nil {
			now = l.Now
		}
		ts := strconv.FormatInt(now().Unix(), 10)
		payload["timestamp"], payload["sign"] = ts, larkSign(ts, secret)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(webhook), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("lark: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lark: HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("lark: unreadable reply")
	}
	if out.Code != 0 {
		return fmt.Errorf("lark: %s (code %d)", out.Msg, out.Code)
	}
	return nil
}

// larkSign is Lark's custom-bot signature: base64(HMAC-SHA256(key = ts + "\n" + secret, message = "")).
func larkSign(ts, secret string) string {
	mac := hmac.New(sha256.New, []byte(ts+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
