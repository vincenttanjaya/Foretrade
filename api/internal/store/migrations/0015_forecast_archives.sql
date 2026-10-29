-- Preserve lifecycle state while a forecast is archived. Existing archives
-- have lost that state: a saved report identifies completed runs; the rest
-- restore as failed because their background worker cannot be resumed.
alter table forecasts add column archived_from_status text
  check (archived_from_status in ('running', 'completed', 'failed'));
update forecasts set archived_from_status = case when result is not null then 'completed' else 'failed' end
  where status = 'archived';
