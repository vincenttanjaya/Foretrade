-- An agent trade card's id on the trade its Confirm placed: one fill per card.
alter table trades add column proposal_id uuid;
create unique index trades_proposal on trades (proposal_id) where proposal_id is not null;
