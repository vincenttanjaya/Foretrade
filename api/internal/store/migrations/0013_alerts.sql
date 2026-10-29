-- Automations (API contract "Automations"): price triggers that notify or place
-- a paper order, checked every 15 s; and when the user last opened the bell.
create table alerts (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users on delete cascade,
  symbol text not null,
  op text not null check (op in ('above','below')),
  price double precision not null check (price > 0),
  action text not null default 'notify' check (action in ('notify','buy','sell')),
  notional double precision,
  stop_loss double precision,
  take_profit double precision,
  note text not null default '',
  source text not null default 'manual' check (source in ('manual','ticket','agent')),
  proposal_id uuid unique,                 -- the agent card it came from: one alert per card
  status text not null default 'active' check (status in ('active','fired','failed','cancelled')),
  trade_id uuid references trades on delete set null,
  error text,
  fired_price double precision,
  created_at timestamptz not null default now(),
  fired_at timestamptz,
  check ((action = 'notify') = (notional is null)),
  check (notional is null or notional >= 10));
create index alerts_active on alerts (symbol) where status = 'active';
create index alerts_user_created on alerts (user_id, created_at desc);
alter table users add column notifications_seen_at timestamptz not null default now();
