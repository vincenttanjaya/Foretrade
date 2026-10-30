-- Prediction-market odds triggers (notify only) on a forecast's matched Polymarket market.
-- symbol stays '' for them; price is the Yes-probability level (0..1) and fired_price the Yes price that fired it.
alter table alerts
  add column forecast_id uuid references forecasts on delete cascade,
  drop constraint alerts_trigger_check,
  add constraint alerts_trigger_check check (trigger in ('price','schedule','odds')),
  drop constraint alerts_trigger_shape,
  add constraint alerts_trigger_shape check (
    (trigger = 'price' and forecast_id is null and op is not null and price is not null and every is null and at_minute is null and weekday is null and next_run_at is null)
    or (trigger = 'schedule' and forecast_id is null and op is null and price is null and every is not null and at_minute is not null and next_run_at is not null
        and (every = 'week') = (weekday is not null) and stop_loss is null and take_profit is null)
    or (trigger = 'odds' and forecast_id is not null and op is not null and price > 0 and price < 1 and action = 'notify'
        and every is null and at_minute is null and weekday is null and next_run_at is null));
create index alerts_odds_active on alerts (forecast_id) where trigger = 'odds' and status = 'active';
