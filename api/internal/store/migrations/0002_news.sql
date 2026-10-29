create table news_items (id uuid primary key default gen_random_uuid(), url text not null unique, title text not null,
  source text not null, description text not null default '', published_at timestamptz, symbols text[] not null default '{}',
  topic text not null default 'asset', content text, crawled_at timestamptz, fetched_at timestamptz not null default now());
create index news_items_published on news_items (published_at desc nulls last);
create index news_items_symbols on news_items using gin (symbols);
create table market_briefs (id uuid primary key default gen_random_uuid(), brief jsonb not null, created_at timestamptz not null default now());
