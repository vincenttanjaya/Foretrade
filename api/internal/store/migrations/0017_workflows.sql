-- Workflows: schedule triggers on automations, and a log of their runs.
alter table alerts
  add column trigger text not null default 'price' check (trigger in ('price','schedule')),
  add column every text check (every in ('day','week')),
  add column at_minute int check (at_minute between 0 and 1439), -- minutes after 00:00 UTC
  add column weekday smallint check (weekday between 0 and 6), -- 0 = Sunday, weekly only
  add column next_run_at timestamptz,
  alter column op drop not null,
  alter column price drop not null,
  add constraint alerts_trigger_shape check (
    (trigger = 'price' and op is not null and price is not null and every is null and at_minute is null and weekday is null and next_run_at is null)
    or (trigger = 'schedule' and op is null and price is null and every is not null and at_minute is not null and next_run_at is not null
        and (every = 'week') = (weekday is not null) and stop_loss is null and take_profit is null));
create index alerts_schedules_due on alerts (next_run_at) where trigger = 'schedule' and status = 'active';
-- Runs older than 30 days are pruned hourly by the alerts check (API contract §4).
create table alert_runs (
  id uuid primary key default gen_random_uuid(),
  alert_id uuid not null references alerts on delete cascade,
  user_id uuid not null references users on delete cascade,
  ran_at timestamptz not null default now(),
  status text not null check (status in ('ok','failed')),
  body text not null default '',
  trade_id uuid references trades on delete set null,
  error text);
create index alert_runs_user on alert_runs (user_id, ran_at desc);
create index alert_runs_alert on alert_runs (alert_id, ran_at desc); -- an automation's last run
