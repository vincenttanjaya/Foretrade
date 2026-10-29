-- Explicit preferences/memories are private owner state; initialize once in
-- the application, which knows whether the configured account is the demo.
create table user_profiles (
  user_id uuid primary key references users on delete cascade,
  preferences jsonb not null,
  memories jsonb not null default '[]' check (jsonb_typeof(memories) = 'array' and jsonb_array_length(memories) <= 20),
  revision bigint not null default 0 check (revision >= 0),
  updated_at timestamptz not null default now(),
  check (jsonb_typeof(preferences) = 'object')
);

-- No text, chat/message FK or active-memory FK: deleted memories/chats cannot
-- remove the terminal receipt or allow an old card to resurrect private text.
create table profile_memory_receipts (
  user_id uuid not null references user_profiles on delete cascade,
  proposal_id uuid not null,
  memory_id uuid not null,
  confirmed_at timestamptz not null default now(),
  deleted_at timestamptz,
  primary key (user_id, proposal_id),
  unique (user_id, memory_id)
);
