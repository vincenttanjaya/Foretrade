-- Paper autopilots: a strategy spec snapshot that a server goroutine trades
-- on the paper engine, one running per user (API contract "Autopilot").
create table autopilots (id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users on delete cascade,
  strategy_id text not null,               -- a saved strategy uuid or a built-in Labs id
  name text not null, spec jsonb not null, -- snapshot at deploy
  allocation_usd double precision not null, max_drawdown_pct double precision not null,
  status text not null check (status in ('running','stopped')), stop_reason text,
  position_qty double precision not null default 0, avg_price double precision not null default 0,
  realized_pnl double precision not null default 0, peak_equity double precision not null,
  last_bar_t bigint, log jsonb not null default '[]',
  started_at timestamptz not null default now(), stopped_at timestamptz);
create unique index autopilots_one_running on autopilots (user_id) where status = 'running';
create index autopilots_user on autopilots (user_id, started_at desc);
