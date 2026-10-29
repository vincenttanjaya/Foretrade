-- Signals board refresh state (contract "Signals board"): one row, shared by every API process. failures, the
-- last error and the retry time survive a restart, so the retry ceiling holds across deploys; model_day and
-- model_calls cap the board's rationale model calls per UTC day.
create table signals_refresh (
  id boolean primary key default true check (id),
  failures integer not null default 0 check (failures >= 0),
  last_error text,
  last_error_at timestamptz,
  retry_at timestamptz,
  model_day date,
  model_calls integer not null default 0 check (model_calls >= 0),
  updated_at timestamptz not null default now());
