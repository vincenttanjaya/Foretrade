-- Paper positions on a forecast's matched Polymarket market: bought from paper cash, settled by the forecast's resolution.
create table market_positions (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users on delete cascade,
  forecast_id uuid not null references forecasts on delete cascade,
  outcome text not null, -- 'Yes' for binary, else MarketOdds.outcome
  side text not null check (side in ('yes','no')),
  question text not null,
  url text not null, -- the market as matched at entry
  price double precision not null check (price > 0 and price < 1), -- per $1 contract, incl. 1¢ slippage
  contracts double precision not null check (contracts > 0),
  cost double precision not null check (cost > 0),
  status text not null default 'open' check (status in ('open','won','lost')),
  payout double precision, -- null while open
  created_at timestamptz not null default now(),
  settled_at timestamptz);
create index market_positions_user on market_positions (user_id, created_at desc);
create index market_positions_open on market_positions (forecast_id) where status = 'open';
