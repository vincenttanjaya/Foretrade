-- Admin console (API contract §3 "Admin console"): roles, disabled accounts,
-- per-user credit limits, login/activity timestamps and the audit log.
alter table users
  add column role text not null default 'user' check (role in ('user','admin')),
  add column disabled_at timestamptz,                                       -- null = active
  add column credit_limit int check (credit_limit between 0 and 100000),   -- null = AI_DAILY_CREDITS
  add column last_login_at timestamptz,
  add column last_active_at timestamptz;                                    -- last authenticated request, written at most every 5 min
create table admin_audit (id uuid primary key default gen_random_uuid(),
  admin_id uuid not null, admin_email text not null,  -- no foreign keys: entries outlive the accounts they name,
  action text not null,                               -- so both emails are snapshots taken at write time
  target_user_id uuid, target_email text,
  detail jsonb not null default '{}', created_at timestamptz not null default now());
create index admin_audit_created on admin_audit (created_at desc);
create index admin_audit_target on admin_audit (target_user_id, created_at desc) where target_user_id is not null;
-- per-user lookups (revoke, session counts, user detail lists, cascade deletes)
create index sessions_user on sessions (user_id);
create index chats_user_updated on chats (user_id, updated_at desc);
create index strategies_user_created on strategies (user_id, created_at desc);
create index ai_portfolios_user_created on ai_portfolios (user_id, created_at desc);
-- overview windows and daily series
create index ai_usage_day on ai_usage (day);
create index chat_messages_user_created on chat_messages (created_at) where role = 'user';
create index forecasts_created on forecasts (created_at);
create index strategies_created on strategies (created_at);
create index ai_portfolios_created on ai_portfolios (created_at);
create index arena_runs_created on arena_runs (created_at);
create index trades_created on trades (created_at);
