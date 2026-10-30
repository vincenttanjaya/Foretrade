-- Live run trace (API contract "Live run progress, full panel"): what a
-- running forecast's pipeline has done so far, for the report page to show.
alter table forecasts add column progress jsonb; -- live run trace; null for older runs and replays
