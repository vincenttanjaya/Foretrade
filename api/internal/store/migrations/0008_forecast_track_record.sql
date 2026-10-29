-- Forecast desk track record (API contract "Forecast desk"): resolution, Brier
-- scoring and the shared no-look-ahead historical replay rows.
alter table forecasts
  alter column user_id drop not null,                 -- null = a shared historical-replay row
  add column price_rule jsonb,                        -- PriceRule; binary universe price questions only
  add column outcome text,                            -- the outcome that happened; null = unresolved
  add column resolved_at timestamptz,
  add column resolved_by text check (resolved_by in ('auto','manual')),
  add column replay_key text unique,
  add column as_of date,
  add column anchor_p double precision,
  add constraint forecasts_owner_or_replay check ((user_id is null) = (replay_key is not null));
create index forecasts_rules_due on forecasts (resolution_date) where price_rule is not null and outcome is null;
