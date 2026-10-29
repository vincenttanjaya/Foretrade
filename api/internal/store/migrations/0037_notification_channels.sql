-- Bell items also sent by email (Amazon SES) and to the user's own Lark custom bot (contract "Notification channels").
-- *_since: a channel never sends items from before it was last turned on, so enabling one does not replay 14 days.
alter table users add column notify_email boolean not null default false,
  add column notify_email_since timestamptz not null default now(),
  add column notify_lark boolean not null default false,
  add column notify_lark_since timestamptz not null default now(),
  add column lark_webhook text, -- a Lark or Feishu custom-bot webhook URL (notify.ValidLarkWebhook)
  add column lark_secret text; -- the bot's signing secret, when it has signature verification on
alter table users add constraint users_notify_lark_webhook check (not notify_lark or lark_webhook is not null);

-- One row per item sent per channel: claimed before sending, so an item goes out at most once per channel.
create table notification_deliveries (
  user_id uuid not null references users on delete cascade,
  item_id text not null, -- the bell item's id, "<kind>:<row id>"
  channel text not null check (channel in ('email', 'lark')),
  sent_at timestamptz not null default now(),
  error text, -- the send's failure, null when delivered
  primary key (user_id, item_id, channel));
create index notification_deliveries_sent on notification_deliveries (sent_at);
