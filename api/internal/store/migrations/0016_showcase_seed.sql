-- Showcase seeding (API contract "Showcase content"): a forecast an admin
-- copied into a showcase account remembers its source row, so a re-seed skips
-- it, the copy's update history can be rebuilt, and the bell stays quiet.
alter table forecasts add column seeded_from uuid; -- the source row an admin showcase copy came from; null for the user's own runs
