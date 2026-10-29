-- A price alert's level in the listing's own currency (contract "Automations": currency). Null is USD, as every
-- alert before it. Only a quote-feed stock's local currency (its quoteFeed.currency) is accepted, and only to notify.
alter table alerts add column currency text check (currency ~ '^[A-Z]{3}$' and currency <> 'USD');
