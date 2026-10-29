-- Booking order: a basket's legs share one created_at (now() is per transaction).
alter table trades add column seq bigint generated always as identity;
