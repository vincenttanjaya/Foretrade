-- Forecast materials (FDOC): what the desk read from each file the user attached: name, type, size, summary,
-- findings and a bounded quoted excerpt. Never the file's bytes.
alter table forecasts add column materials jsonb not null default '[]';
