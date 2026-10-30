-- The Polymarket market a prediction-board draft came from ({eventSlug, question}); a binary run compares
-- against it first. Null for every other forecast.
alter table forecasts add column linked_market jsonb;
