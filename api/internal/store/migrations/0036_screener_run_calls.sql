-- Asia screener run log: the HTTP requests each run sent to Yahoo, retries included (contract "Asia screener", run.yahooCalls).
-- Null on runs recorded before it was counted.
alter table screener_runs add column yahoo_calls integer check (yahoo_calls >= 0);
