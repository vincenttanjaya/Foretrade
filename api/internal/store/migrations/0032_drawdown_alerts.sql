-- Wave 3: all-time-high drawdown triggers (API contract "All-time-high drawdown triggers"). One-shot: fire once when the price is at
-- or below ath × (1 − drawdown_bps / 10000), then notify or sell sell_pct of the current paper long. The stored high
-- ratchets up when a check sees a higher price (ath_live: set by a checked price, not read from the daily history);
-- ath_source/ath_ticker/ath_currency/ath_from say where it came from and what it covers. outcome records how it fired.
alter table alerts
  add column drawdown_bps integer check (drawdown_bps between 1 and 9999),
  add column sell_pct smallint check (sell_pct between 1 and 100),
  add column ath double precision check (ath > 0),
  add column ath_at timestamptz,
  add column ath_from timestamptz,
  add column ath_source text check (ath_source in ('binance','yahoo','perp')),
  add column ath_ticker text,
  add column ath_currency text,
  add column ath_live boolean,
  add column outcome text check (outcome in ('notified','sold','nothing_to_sell')),
  drop constraint alerts_trigger_check,
  add constraint alerts_trigger_check check (trigger in ('price','schedule','odds','drawdown')),
  drop constraint alerts_notional_shape,
  add constraint alerts_notional_shape check (case when trigger = 'drawdown' then notional is null
    else (action in ('notify','research')) = (notional is null) end),
  drop constraint alerts_trigger_shape,
  add constraint alerts_trigger_shape check (
    (trigger = 'price' and forecast_id is null and market_slug is null and op is not null and price is not null
      and every is null and at_minute is null and weekday is null and next_run_at is null)
    or (trigger = 'schedule' and forecast_id is null and market_slug is null and op is null and price is null
      and every is not null and at_minute is not null and next_run_at is not null
      and (every = 'week') = (weekday is not null) and stop_loss is null and take_profit is null)
    or (trigger = 'odds' and ((forecast_id is not null and market_slug is null) or (forecast_id is null and market_slug is not null))
      and op is not null and price > 0 and price < 1 and action = 'notify' and symbol = ''
      and notional is null and stop_loss is null and take_profit is null
      and every is null and at_minute is null and weekday is null and next_run_at is null)
    or (trigger = 'drawdown' and forecast_id is null and market_slug is null and op is null and price is null
      and every is null and at_minute is null and weekday is null and next_run_at is null
      and symbol <> '' and action in ('notify','sell') and notional is null and stop_loss is null and take_profit is null)),
  add constraint alerts_drawdown_shape check (
    (trigger = 'drawdown' and drawdown_bps is not null and ath is not null and ath_at is not null and ath_from is not null
      and ath_source is not null and ath_ticker is not null and ath_currency is not null and ath_live is not null
      and (action = 'sell') = (sell_pct is not null) and (status = 'fired') = (outcome is not null))
    or (trigger <> 'drawdown' and drawdown_bps is null and sell_pct is null and ath is null and ath_at is null and ath_from is null
      and ath_source is null and ath_ticker is null and ath_currency is null and ath_live is null and outcome is null));
create index alerts_drawdown_active on alerts (symbol) where trigger = 'drawdown' and status = 'active';
