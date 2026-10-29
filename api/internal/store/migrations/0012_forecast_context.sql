-- Forecast desk user context (API contract "Forecast desk"): the user's own
-- notes for the panel, shown to it as an unverified claim (S0).
alter table forecasts add column context text not null default '';
