package alerts

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/notify"
)

// deliverBatch caps the items one pass sends a user per channel; the rest go
// on the next pass (15 s later), oldest first.
const deliverBatch = 10

// recipient is a user with a notification channel on.
type recipient struct {
	id, email, larkWebhook, larkSecret string
	emailOn, larkOn                    bool
	emailSince, larkSince              time.Time
}

// Deliver sends every enabled account's new bell items (feed) by email and to
// its Lark bot: items newer than the channel's *_since and not yet in
// notification_deliveries, oldest first. Each item is claimed per channel
// before it is sent, so it goes out at most once there; a failed send keeps
// its error and is not retried.
// ponytail: reads every recipient's feed each pass; fine for a demo's users, a queue if it grows.
func Deliver(ctx context.Context, d *app.Deps) error {
	rows, _ := d.DB.Query(ctx, `select id::text, email, notify_email, notify_email_since, notify_lark, notify_lark_since,
			coalesce(lark_webhook, ''), coalesce(lark_secret, '')
		from users where disabled_at is null and (notify_email or notify_lark)`)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (recipient, error) {
		var u recipient
		return u, r.Scan(&u.id, &u.email, &u.emailOn, &u.emailSince, &u.larkOn, &u.larkSince, &u.larkWebhook, &u.larkSecret)
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, u := range list {
		errs = append(errs, deliverTo(ctx, d, u))
	}
	return errors.Join(errs...)
}

func deliverTo(ctx context.Context, d *app.Deps, u recipient) error {
	items, _, err := feed(ctx, d.DB, u.id)
	if err != nil {
		return err
	}
	rows, _ := d.DB.Query(ctx, `select channel || ' ' || item_id from notification_deliveries
		where user_id = $1 and sent_at > now() - interval '15 days'`, u.id)
	done, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	slices.Reverse(items) // oldest first
	for _, ch := range []string{"email", "lark"} {
		on, since := u.emailOn && d.Mailer != nil, u.emailSince
		if ch == "lark" {
			on, since = u.larkOn && u.larkWebhook != "", u.larkSince
		}
		sent := 0
		for _, it := range items {
			if !on || sent == deliverBatch {
				break
			}
			if !it.At.After(since) || slices.Contains(done, ch+" "+it.ID) {
				continue
			}
			tag, err := d.DB.Exec(ctx, `insert into notification_deliveries (user_id, item_id, channel) values ($1, $2, $3)
				on conflict do nothing`, u.id, it.ID, ch)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				continue // another pass or instance has it
			}
			sent++
			if err := send(ctx, d, u, ch, message(d, it.Title, it.Body, it.Link)); err != nil {
				slog.Warn("notification delivery", "channel", ch, "user", u.id, "item", it.ID, "err", err)
				d.DB.Exec(ctx, `update notification_deliveries set error = $4 where user_id = $1 and item_id = $2 and channel = $3`,
					u.id, it.ID, ch, clip(err.Error(), 500))
			}
		}
	}
	return nil
}

// message is a bell item as sent: its link made absolute on the web app.
func message(d *app.Deps, title, body, link string) notify.Message {
	return notify.Message{Title: title, Body: body, Link: strings.TrimSuffix(d.AppURL, "/") + link}
}

func send(ctx context.Context, d *app.Deps, u recipient, ch string, m notify.Message) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if ch == "email" {
		return d.Mailer.Send(ctx, u.email, m)
	}
	l := d.Lark
	if l == nil {
		l = &notify.Lark{}
	}
	return l.Send(ctx, u.larkWebhook, u.larkSecret, m)
}

// Channels is the caller's notification channels (contract NotificationChannels).
type Channels struct {
	Email struct {
		Enabled   bool   `json:"enabled"`
		Address   string `json:"address"`
		Available bool   `json:"available"` // this server can send email
	} `json:"email"`
	Lark struct {
		Enabled bool    `json:"enabled"`
		Webhook *string `json:"webhook"` // masked: host and the token's last 4 characters
		Signed  bool    `json:"signed"`  // a signing secret is saved
	} `json:"lark"`
}

func (h *handler) channels(ctx context.Context, uid string) (Channels, error) {
	var c Channels
	var hook, secret string
	err := h.d.DB.QueryRow(ctx, `select email, notify_email, notify_lark, coalesce(lark_webhook, ''), coalesce(lark_secret, '')
		from users where id = $1`, uid).Scan(&c.Email.Address, &c.Email.Enabled, &c.Lark.Enabled, &hook, &secret)
	c.Email.Available = h.d.Mailer != nil
	if hook != "" {
		masked := notify.MaskLarkWebhook(hook)
		c.Lark.Webhook = &masked
	}
	c.Lark.Signed = secret != ""
	return c, err
}

func (h *handler) getChannels(w http.ResponseWriter, r *http.Request) {
	c, err := h.channels(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"channels": c})
}

// putChannels turns the channels on or off and saves or clears the Lark
// webhook and secret (omitted: kept; "": cleared). Turning a channel on
// starts it from now.
func (h *handler) putChannels(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       *bool   `json:"email"`
		Lark        *bool   `json:"lark"`
		LarkWebhook *string `json:"larkWebhook"`
		LarkSecret  *string `json:"larkSecret"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		app.WriteError(w, bad("%s", err.Error()))
		return
	}
	ctx, uid := r.Context(), httpx.UserID(r.Context())
	cur, err := h.channels(ctx, uid)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	var hook, secret string
	if err := h.d.DB.QueryRow(ctx, "select coalesce(lark_webhook, ''), coalesce(lark_secret, '') from users where id = $1", uid).Scan(&hook, &secret); err != nil {
		app.WriteError(w, err)
		return
	}
	email, lark := cur.Email.Enabled, cur.Lark.Enabled
	if in.Email != nil {
		email = *in.Email
	}
	if in.Lark != nil {
		lark = *in.Lark
	}
	if in.LarkWebhook != nil {
		hook = strings.TrimSpace(*in.LarkWebhook)
		if hook != "" && !notify.ValidLarkWebhook(hook) {
			app.WriteError(w, bad("Paste the webhook URL of a Lark custom bot (https://open.larksuite.com/open-apis/bot/v2/hook/…)."))
			return
		}
	}
	if in.LarkSecret != nil {
		secret = strings.TrimSpace(*in.LarkSecret)
		if len(secret) > 128 {
			app.WriteError(w, bad("The signing secret is at most 128 characters."))
			return
		}
	}
	switch {
	case email && !cur.Email.Enabled && h.d.Mailer == nil:
		app.WriteError(w, bad("Email notifications are not set up on this server."))
		return
	case lark && hook == "":
		app.WriteError(w, bad("Add your Lark bot's webhook URL to turn Lark on."))
		return
	}
	if _, err := h.d.DB.Exec(ctx, `update users set
			notify_email_since = case when $2 and not notify_email then now() else notify_email_since end,
			notify_lark_since = case when $3 and not (notify_lark and lark_webhook is not distinct from nullif($4, '')) then now() else notify_lark_since end,
			notify_email = $2, notify_lark = $3, lark_webhook = nullif($4, ''), lark_secret = nullif($5, '')
		where id = $1`, uid, email, lark, hook, secret); err != nil {
		app.WriteError(w, err)
		return
	}
	h.getChannels(w, r)
}

var (
	testMu   sync.Mutex
	lastTest = map[string]time.Time{} // user id → its last test send
)

// testChannels sends one test message to each channel the caller has on and
// reports each outcome: "sent", "off" or the send's error. At most one test a user per 10 s.
func (h *handler) testChannels(w http.ResponseWriter, r *http.Request) {
	ctx, uid := r.Context(), httpx.UserID(r.Context())
	testMu.Lock()
	if clock().Sub(lastTest[uid]) < 10*time.Second {
		testMu.Unlock()
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Wait a few seconds before sending another test.")
		return
	}
	lastTest[uid] = clock()
	testMu.Unlock()
	var u recipient
	if err := h.d.DB.QueryRow(ctx, `select id::text, email, notify_email, notify_lark, coalesce(lark_webhook, ''), coalesce(lark_secret, '')
		from users where id = $1`, uid).Scan(&u.id, &u.email, &u.emailOn, &u.larkOn, &u.larkWebhook, &u.larkSecret); err != nil {
		app.WriteError(w, err)
		return
	}
	m := message(h.d, "Test notification", "Foretrade alerts will arrive here.", "/account")
	out := map[string]string{"email": "off", "lark": "off"}
	if u.emailOn && h.d.Mailer != nil {
		out["email"] = outcome(send(ctx, h.d, u, "email", m))
	}
	if u.larkOn && u.larkWebhook != "" {
		out["lark"] = outcome(send(ctx, h.d, u, "lark", m))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func outcome(err error) string {
	if err != nil {
		return clip(err.Error(), 300)
	}
	return "sent"
}
