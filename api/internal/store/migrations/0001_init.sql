create table users (id uuid primary key default gen_random_uuid(), email text not null unique,
  name text not null, password_hash text not null, created_at timestamptz not null default now());
create table sessions (token_hash bytea primary key, user_id uuid not null references users on delete cascade,
  expires_at timestamptz not null, created_at timestamptz not null default now());
create table accounts (user_id uuid primary key references users on delete cascade,
  cash double precision not null default 100000, starting_balance double precision not null default 100000,
  realized_pnl double precision not null default 0, updated_at timestamptz not null default now());
create table positions (user_id uuid not null references users on delete cascade, symbol text not null,
  qty double precision not null, avg_price double precision not null, stop_loss double precision,
  take_profit double precision, updated_at timestamptz not null default now(), primary key (user_id, symbol));
create table trades (id uuid primary key default gen_random_uuid(), user_id uuid not null references users on delete cascade,
  symbol text not null, side text not null check (side in ('buy','sell')), qty double precision not null,
  price double precision not null, notional double precision not null, fee double precision not null,
  realized_pnl double precision not null default 0, source text not null, note text not null default '',
  created_at timestamptz not null default now());
create index trades_user_created on trades (user_id, created_at desc);
create table ai_usage (user_id uuid not null references users on delete cascade, day date not null,
  credits int not null default 0, primary key (user_id, day));
create table chats (id uuid primary key default gen_random_uuid(), user_id uuid not null references users on delete cascade,
  title text not null, created_at timestamptz not null default now(), updated_at timestamptz not null default now());
create table chat_messages (id uuid primary key default gen_random_uuid(), chat_id uuid not null references chats on delete cascade,
  role text not null check (role in ('user','assistant')), content text not null, cards jsonb not null default '[]',
  created_at timestamptz not null default now());
create index chat_messages_chat on chat_messages (chat_id, created_at);
create table forecasts (id uuid primary key default gen_random_uuid(), user_id uuid not null references users on delete cascade,
  shape text not null, question text not null, outcomes jsonb not null, resolution_criteria text not null default '',
  resolution_date date not null, category text not null default 'other',
  status text not null check (status in ('running','completed','failed','archived')),
  error text, result jsonb, created_at timestamptz not null default now(), completed_at timestamptz);
create index forecasts_user_created on forecasts (user_id, created_at desc);
create table strategies (id uuid primary key default gen_random_uuid(), user_id uuid not null references users on delete cascade,
  name text not null, description text not null, prompt text not null, spec jsonb not null, metrics jsonb not null,
  equity jsonb not null, benchmark jsonb not null, trades jsonb not null, explanation text not null default '',
  created_at timestamptz not null default now());
create table ai_portfolios (id uuid primary key default gen_random_uuid(), user_id uuid not null references users on delete cascade,
  name text not null, thesis text not null, prompt text not null, holdings jsonb not null, backtest jsonb not null,
  created_at timestamptz not null default now());
