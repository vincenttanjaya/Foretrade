create table arena_runs (id uuid primary key default gen_random_uuid(), user_id uuid not null references users on delete cascade,
  name text not null, status text not null check (status in ('running','completed','failed','cancelled')),
  cadence text not null check (cadence in ('1d','4h')),
  start_at timestamptz not null,                 -- first decision time (start date 00:00 UTC)
  steps int not null check (steps between 1 and 90), steps_done int not null default 0,
  universe text[] not null, model text not null, knowledge_cutoff date not null,
  agents jsonb not null,                         -- [{name, personaId, instruction, metrics|null}]
  benchmarks jsonb not null,                     -- {"btc": number[steps+1], "basket": number[steps+1]} USD per mark
  credits int not null,                          -- charged minus refunds
  error text, created_at timestamptz not null default now(), completed_at timestamptz);
create index arena_runs_user_created on arena_runs (user_id, created_at desc);
create unique index arena_runs_one_running on arena_runs (user_id) where status = 'running';
create table arena_steps (run_id uuid not null references arena_runs on delete cascade,
  agent smallint not null,                       -- index into arena_runs.agents
  step int not null,                             -- 0-based; decision time = start_at + step × cadence
  equity double precision not null, cash double precision not null,
  positions jsonb not null,                      -- full snapshot at the step's close
  targets jsonb,                                 -- null = held
  fills jsonb not null default '[]', tools jsonb not null default '[]', reasoning text not null default '', error text,
  primary key (run_id, agent, step));
