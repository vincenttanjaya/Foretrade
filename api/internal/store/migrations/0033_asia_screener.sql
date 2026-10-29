-- Asia screener (contract "Asia screener"): a weekly job reads each embedded index constituent's lifetime
-- weekly Yahoo chart. screener_runs is its run log (one running at a time); screener_rows keeps one row per
-- Yahoo ticker. A failed fetch updates only the status columns, so values from an earlier run survive.
create table screener_runs (
  id uuid primary key default gen_random_uuid(),
  started_at timestamptz not null default now(),
  heartbeat_at timestamptz not null default now(),
  finished_at timestamptz,
  status text not null default 'running' check (status in ('running','completed','failed')),
  tickers integer not null check (tickers >= 0),
  done integer not null default 0,
  ok integer not null default 0,
  insufficient integer not null default 0,
  unavailable integer not null default 0,
  failed integer not null default 0,
  error text);
create index screener_runs_started on screener_runs (started_at desc);
create unique index screener_runs_one_running on screener_runs (status) where status = 'running';

create table screener_rows (
  ticker text primary key,
  status text not null check (status in ('ok','insufficient_history','unavailable','failed')),
  reason text,
  attempted_at timestamptz not null,
  run_id uuid references screener_runs on delete set null,
  computed_at timestamptz,
  yahoo_name text,
  currency text,
  price double precision,
  price_at timestamptz,
  fx_usd double precision,
  fx_pair text,
  fx_at timestamptz,
  price_usd double precision,
  weeks integer,
  first_week timestamptz,
  sma200w double precision,
  pct_vs_sma double precision,
  prev_pct_vs_sma double precision,
  rsi14w double precision,
  ath double precision,
  ath_at timestamptz,
  drawdown_pct double precision);
