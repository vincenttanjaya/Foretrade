-- Free notify-only alerts can target a canonical leaf without a paid forecast.
alter table alerts
  add column market_slug text,
  add column market_id text,
  add column linked_market jsonb,
  add column market_odds jsonb,
  drop constraint alerts_trigger_shape,
  add constraint alerts_direct_target check (
    (market_slug is null and market_id is null and linked_market is null and market_odds is null)
    or (market_slug is not null and market_id is not null and market_slug ~ '^[a-z0-9][a-z0-9-]{0,199}$' and market_id ~ '^[0-9]{1,20}$'
        and linked_market is not null and market_odds is not null
        and jsonb_typeof(linked_market) = 'object' and jsonb_typeof(market_odds) = 'object'
        and coalesce(linked_market->>'marketId' = market_id, false) and coalesce(linked_market->>'marketSlug' = market_slug, false)
        and coalesce(jsonb_typeof(linked_market->'conditionId') = 'string'
          and linked_market->>'conditionId' ~ '^0x[0-9a-fA-F]{64}$', false)
        and coalesce(jsonb_typeof(linked_market->'eventSlug') = 'string'
          and linked_market->>'eventSlug' ~ '^[a-z0-9][a-z0-9-]{0,199}$', false)
        and coalesce(jsonb_typeof(linked_market->'question') = 'string'
          and char_length(linked_market->>'question') between 10 and 280, false)
        and coalesce(jsonb_typeof(linked_market->'description') = 'string'
          and char_length(linked_market->>'description') between 1 and 8000
          and char_length(btrim(linked_market->>'description')) > 0, false)
        and coalesce(jsonb_typeof(linked_market->'resolutionSource') in ('string', 'null')
          and char_length(coalesce(linked_market->>'resolutionSource', '')) <= 2048, false)
        and coalesce(jsonb_typeof(linked_market->'endDate') = 'string'
          and linked_market->>'endDate' ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$'
          and (linked_market->>'endDate')::timestamptz > '0001-01-01T00:00:00Z'::timestamptz, false)
        and coalesce(jsonb_typeof(linked_market->'asOf') = 'string'
          and linked_market->>'asOf' ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$'
          and (linked_market->>'asOf')::timestamptz > '0001-01-01T00:00:00Z'::timestamptz, false))),
  add constraint alerts_trigger_shape check (
    (trigger = 'price' and forecast_id is null and market_slug is null and op is not null and price is not null
      and every is null and at_minute is null and weekday is null and next_run_at is null)
    or (trigger = 'schedule' and forecast_id is null and market_slug is null and op is null and price is null
      and every is not null and at_minute is not null and next_run_at is not null
      and (every = 'week') = (weekday is not null) and stop_loss is null and take_profit is null)
    or (trigger = 'odds' and ((forecast_id is not null and market_slug is null) or (forecast_id is null and market_slug is not null))
      and op is not null and price > 0 and price < 1 and action = 'notify' and symbol = ''
      and notional is null and stop_loss is null and take_profit is null
      and every is null and at_minute is null and weekday is null and next_run_at is null));
create index alerts_direct_odds_active on alerts (market_id) where trigger = 'odds' and status = 'active' and market_id is not null;
