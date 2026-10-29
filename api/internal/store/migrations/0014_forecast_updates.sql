-- Forecast updates (API contract "Forecast desk"): an update is a new run of
-- the same question that points at the run it updates; the lineage is the
-- forecast's probability history.
alter table forecasts add column parent_id uuid references forecasts on delete set null;
create index forecasts_parent on forecasts (parent_id) where parent_id is not null;
-- One running update per run, so two concurrent updates cannot both start.
create unique index forecasts_one_running_update on forecasts (parent_id) where status = 'running';
