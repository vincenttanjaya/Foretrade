create table signal_boards (id uuid primary key default gen_random_uuid(), board jsonb not null, created_at timestamptz not null default now());
create index signal_boards_created on signal_boards (created_at desc);
