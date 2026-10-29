-- Signals board pick ledger (contract "Signals board", rule steady-v1): every pick a shown board made, how it left,
-- and its fixed-horizon score against a random pick from the same list. Kept indefinitely (about 3 rows a day).
create table signal_picks (
  id uuid primary key default gen_random_uuid(),
  rule text not null default 'steady-v1',
  symbol text not null,
  side text not null check (side in ('bullish', 'bearish')),
  picked_at timestamptz not null,                 -- createdAt of the board that picked it
  price_at_pick double precision not null check (price_at_pick > 0),
  target double precision not null,
  invalidation double precision not null,
  valid_until timestamptz not null,
  score_at_pick integer not null,
  universe jsonb not null,                        -- [{"s":"BTC","p":62000.1}, ...]: the pick board's eligible universe
  exit_reason text check (exit_reason in ('target_hit', 'invalidated', 'expired')),
  exit_at timestamptz,                            -- close of the 1h bar that touched the level, or valid_until
  exit_price double precision,                    -- the level, or the last 1h close at or before valid_until
  return_pct double precision,                    -- exit_price vs price_at_pick, in the pick's direction, %
  removed_at timestamptz,                         -- createdAt of the first board without it
  scored_at timestamptz,                          -- createdAt of the board that scored it (first valid board >= picked_at + 72 h)
  horizon_h double precision,                     -- scored_at - picked_at, hours (72 to about 76)
  result_pct double precision,                    -- move to the scoring board's price, in the pick's direction, %
  random_pct double precision,                    -- the pick-time universe's mean move to the scoring board's prices, in the pick's direction, %
  random_up_share double precision,               -- share of that universe that moved in the pick's direction (0..1)
  universe_n integer,
  unique (symbol, side, picked_at));
create index signal_picks_unscored on signal_picks (picked_at) where scored_at is null;
create index signal_picks_removed on signal_picks (removed_at desc) where removed_at is not null;
create table signal_ledger_sync (id boolean primary key default true check (id), last_board_at timestamptz not null);
