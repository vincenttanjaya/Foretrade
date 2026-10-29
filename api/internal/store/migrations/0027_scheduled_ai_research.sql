-- Scheduled research uses the existing automation and usage ownership model.
alter table alerts
  drop constraint alerts_action_check,
  add constraint alerts_action_check check(action in ('notify','buy','sell','research')),
  drop constraint alerts_status_check,
  add constraint alerts_status_check check(status in ('active','fired','failed','cancelled','paused')),
  drop constraint alerts_check,
  add constraint alerts_notional_shape check((action in ('notify','research')) = (notional is null)),
  add column prompt text,
  add column thesis jsonb,
  add column notify_when text,
  add column quota_streak integer not null default 0 check(quota_streak between 0 and 3),
  add column report_chat_id uuid references chats on delete set null,
  add constraint alerts_research_shape check (
    (action='research' and trigger='schedule' and prompt is not null and notify_when is not null and char_length(prompt) between 10 and 500
      and notify_when in ('always','changed','invalidated') and stop_loss is null and take_profit is null
      and (thesis is null or (jsonb_typeof(thesis)='object'
        and thesis ? 'statement' and thesis ? 'invalidation' and jsonb_typeof(thesis->'statement')='string' and char_length(thesis->>'statement')<=300
        and jsonb_typeof(thesis->'invalidation')='string' and char_length(thesis->>'invalidation')<=300)))
    or (action<>'research' and prompt is null and thesis is null and notify_when is null and status<>'paused'));

alter table alert_runs
  drop constraint alert_runs_status_check,
  add constraint alert_runs_status_check check(status in ('ok','failed','running','interrupted','cancelled')),
  add column slot_at timestamptz,
  add column charged_day date,
  add column cost integer not null default 0 check(cost in (0,2)),
  add column attempted boolean not null default false,
  add column refunded boolean not null default false,
  add column slot integer,
  add column fence bigint,
  add column report_chat_id uuid references chats on delete set null,
  add column report text,
  add column sources jsonb,
  add column verdict text check(verdict in ('holds','weakened','invalidated','unknown')),
  add column excerpt text,
  add column notification_eligible boolean not null default false,
  add constraint alert_runs_paid_shape check(cost=0 or (charged_day is not null and slot_at is not null and slot is not null and slot in (1,2) and fence is not null));
create unique index alert_runs_research_slot on alert_runs(alert_id,slot_at) where slot_at is not null;
create unique index alert_runs_one_running on alert_runs(alert_id) where status='running';

-- Exactly two shared slots; RunID may precede claim and has no cascading FK.
-- Expiry recovery must settle an old paid run before the slot can be reused.
create table research_worker_slots (
  slot integer primary key check(slot in (1,2)),
  fence bigint not null default 0,
  run_id uuid unique,
  admitted_at timestamptz,
  deadline timestamptz,
  lease_until timestamptz,
  check((run_id is null and admitted_at is null and deadline is null and lease_until is null)
    or (run_id is not null and admitted_at is not null and deadline is not null and lease_until is not null and deadline=admitted_at+interval '120 seconds'
      and lease_until=admitted_at+interval '130 seconds'))
);
insert into research_worker_slots(slot) values(1),(2);
