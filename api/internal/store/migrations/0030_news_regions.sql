-- News regions: the markets a stored headline touches (crypto, us, east_asia, southeast_asia; '{}' global or
-- unclassified), sorted, for GET /api/news?region= and the market brief. Backfill from what earlier runs searched:
-- every asset row came from a core crypto search and every macro row from a US macro search, and a tagged
-- symbol gives its own region (a crypto asset, a US stock or index, an East Asian stock or ETF).
alter table news_items add column regions text[] not null default '{}';
update news_items set regions = array_remove(array[
  case when topic = 'asset' or exists (select 1 from unnest(symbols) s where s <> all (
    '{NVDA,TSLA,AAPL,MSFT,AMZN,GOOGL,META,SP500,XYZ100,TSM,BABA,SMSN,SKHX,HYUNDAI,SOFTBANK,MINIMAX,ZHIPU,EWJ,EWY}'::text[])) then 'crypto' end,
  case when symbols && '{TSM,BABA,SMSN,SKHX,HYUNDAI,SOFTBANK,MINIMAX,ZHIPU,EWJ,EWY}'::text[] then 'east_asia' end,
  case when topic = 'macro' or symbols && '{NVDA,TSLA,AAPL,MSFT,AMZN,GOOGL,META,SP500,XYZ100}'::text[] then 'us' end], null);
create index news_items_regions on news_items using gin (regions);
