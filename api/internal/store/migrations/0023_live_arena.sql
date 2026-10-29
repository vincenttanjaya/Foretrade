-- The shared live paper desk (API contract "Live arena and copy plans"): one
-- desk row, its three agents, the bars they decided on, their steps, and the
-- copy plans that mirror an agent's fills into a user's paper account.
create table live_desk (id smallint primary key check (id = 1), started_at timestamptz not null default now(),
  universe text[] not null, start_marks jsonb not null,  -- {symbol: price} at the start: the baselines' entry prices
  last_step_at timestamptz, last_error text);
create table live_agents (id uuid primary key default gen_random_uuid(), persona_id text not null unique, name text not null,
  cash double precision not null default 100000, positions jsonb not null default '{}',  -- {symbol: {qty, avg}}
  notes jsonb not null default '[]',                     -- the last three rationales
  fails int not null default 0, benched_until timestamptz,
  last_bar_t bigint,                                     -- open time (unix s) of the last closed 4h bar decided on
  steps int not null default 0, started_at timestamptz not null default now());
create table live_bars (bar_t bigint primary key, decided_at timestamptz not null default now(),
  marks jsonb not null);                                 -- {symbol: price} the step filled and marked at
create table live_steps (agent_id uuid not null references live_agents on delete cascade,
  step int not null, bar_t bigint not null references live_bars, decided_at timestamptz not null default now(),
  equity double precision not null, cash double precision not null, positions jsonb not null, targets jsonb,
  fills jsonb not null default '[]', tools jsonb not null default '[]', reasoning text not null default '', error text,
  primary key (agent_id, step));
create table copy_plans (id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users on delete cascade, agent_id uuid not null references live_agents on delete cascade,
  ratio double precision not null check (ratio > 0 and ratio <= 1),
  max_usd double precision not null check (max_usd between 100 and 50000),
  status text not null default 'active' check (status in ('active','stopped')), stop_reason text,
  positions jsonb not null default '{}',                 -- {symbol: qty} the plan's own book
  last_step int,                                         -- the agent step last mirrored
  log jsonb not null default '[]', created_at timestamptz not null default now(), stopped_at timestamptz);
create unique index copy_plans_one_active on copy_plans (user_id, agent_id) where status = 'active';
create index copy_plans_user on copy_plans (user_id, created_at desc);
create index copy_plans_active on copy_plans (agent_id) where status = 'active';
create index copy_plans_created on copy_plans (created_at);
