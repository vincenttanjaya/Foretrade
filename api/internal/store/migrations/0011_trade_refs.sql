-- The AI portfolio an invest leg belongs to (account history links each fill back to it); null otherwise.
alter table trades add column ref_id uuid;
