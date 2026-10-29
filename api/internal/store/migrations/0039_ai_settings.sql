-- AI provider and search settings (API contract "Settings"): one row, edited in
-- the admin console. Secrets are nonce‖AES-256-GCM under SECRET_KEY (AAD
-- "ai_settings"); the API returns only their hints ("…" + last 4, keys ≥ 16 chars).
create table ai_settings (
  id               int primary key default 1 check (id = 1),
  provider         text not null default '',   -- an llm.Providers id; '' = AI off
  base_url         text not null default '',   -- '' = the preset's
  model            text not null default '',   -- '' = the preset's
  effort           text not null default '' check (effort in ('', 'on', 'off')),
  aws_region       text not null default '',   -- bedrock
  knowledge_cutoff date,                       -- null = the default
  secret           bytea,                      -- the API key
  secret_hint      text not null default '',
  chatgpt          bytea,                      -- the ChatGPT sign-in credential (JSON)
  host_id          text not null default 'urn:uuid:' || gen_random_uuid(), -- ext_agent_host_id, set once
  brave_api_key    bytea,
  brave_hint       text not null default '',
  crawl_base       text not null default '',
  crawl_token      bytea,
  crawl_hint       text not null default '',
  updated_at       timestamptz not null default now(),
  updated_by       uuid                        -- no foreign key, like admin_audit
);
insert into ai_settings default values;
