-- Bumped in the transaction of every account mutation. Portfolio snapshots carry
-- it so a client can ignore one older than a snapshot it already applied.
alter table accounts add column revision bigint not null default 0;
