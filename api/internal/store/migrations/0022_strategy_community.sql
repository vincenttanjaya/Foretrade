-- Round 6: community strategies (publish / star / copy).
alter table strategies
  add column published_at timestamptz,
  add column copied_from uuid references strategies on delete set null;
create table strategy_stars (
  user_id uuid not null references users on delete cascade,
  strategy_id uuid not null references strategies on delete cascade,
  created_at timestamptz not null default now(),
  primary key (user_id, strategy_id));
create index strategies_published on strategies (published_at desc) where published_at is not null;
create index strategies_copied_from on strategies (copied_from) where copied_from is not null;
