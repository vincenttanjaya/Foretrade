-- Keep 0023's applied full-stem ID intact. Claims are durable before inference;
-- delivery IDs commit with account fills and all copy-book changes.
create table live_attempts (agent_id uuid not null references live_agents on delete cascade,
  bar_t bigint not null, claimed_at timestamptz not null, completed_at timestamptz,
  primary key (agent_id, bar_t));
create index live_attempts_claimed on live_attempts (claimed_at);
alter table copy_plans add column start_bar_t bigint;
update copy_plans p set start_bar_t = a.last_bar_t, last_step = a.steps - 1
  from live_agents a where a.id = p.agent_id and p.last_step is null;
create table copy_deliveries (plan_id uuid not null references copy_plans on delete cascade,
  step int not null, leg int not null, trade_id uuid references trades on delete set null,
  status text not null check (status in ('filled', 'skipped')),
  primary key (plan_id, step, leg));
create index live_steps_bar on live_steps (bar_t desc, agent_id);
