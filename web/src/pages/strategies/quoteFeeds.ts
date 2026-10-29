// Quote-feed stocks (venue "yahoo", contract §2): HSDT, US-listed Asian ADRs and stocks, Asian local listings, then the
// Singapore, Taiwan and Korea focus rows, priced from Yahoo in USD and following their listing's regular hours (not 24/7
// like the perps). Spread into SYMBOLS.
// No import from strategy.ts: it spreads these rows into SYMBOLS at load, so a cycle would read them uninitialised.
import { basisText, behindMinutes, delaySeconds, feedQuote, freshness, freshnessTag, priceLabel, realtimeOpen, sourceName, utcTime } from '../../lib/quoteLabel'
import type { Asset, Instrument, QuoteClass, QuoteFeed } from '../../lib/types'
import type { ChartBars } from '../trade/util'

type Venue = NonNullable<Asset['sessionVenue']>
type Ccy = NonNullable<Asset['localCurrency']>
/** [symbol, name, region, session venue, listing currency, Yahoo ticker], in the API's order. */
type Row = readonly [string, string, 'us' | 'asia', Venue, Ccy, string]

const ROWS: readonly Row[] = [
  ['HSDT', 'Solana Company', 'us', 'NYSE', 'USD', 'HSDT'],
  ['SE', 'Sea Limited', 'asia', 'NYSE', 'USD', 'SE'],
  ['GRAB', 'Grab Holdings', 'asia', 'NYSE', 'USD', 'GRAB'],
  ['PHI', 'PLDT', 'asia', 'NYSE', 'USD', 'PHI'],
  ['TLK', 'Telkom Indonesia (ADR)', 'asia', 'NYSE', 'USD', 'TLK'],
  ['PDD', 'PDD Holdings', 'asia', 'NYSE', 'USD', 'PDD'],
  ['JD', 'JD.com', 'asia', 'NYSE', 'USD', 'JD'],
  ['BIDU', 'Baidu', 'asia', 'NYSE', 'USD', 'BIDU'],
  ['NIO', 'NIO', 'asia', 'NYSE', 'USD', 'NIO'],
  ['LI', 'Li Auto', 'asia', 'NYSE', 'USD', 'LI'],
  ['XPEV', 'XPeng', 'asia', 'NYSE', 'USD', 'XPEV'],
  ['NTES', 'NetEase', 'asia', 'NYSE', 'USD', 'NTES'],
  ['TCOM', 'Trip.com Group', 'asia', 'NYSE', 'USD', 'TCOM'],
  ['BILI', 'Bilibili', 'asia', 'NYSE', 'USD', 'BILI'],
  ['FUTU', 'Futu Holdings', 'asia', 'NYSE', 'USD', 'FUTU'],
  ['TME', 'Tencent Music', 'asia', 'NYSE', 'USD', 'TME'],
  ['BEKE', 'KE Holdings', 'asia', 'NYSE', 'USD', 'BEKE'],
  ['ZTO', 'ZTO Express', 'asia', 'NYSE', 'USD', 'ZTO'],
  ['YUMC', 'Yum China', 'asia', 'NYSE', 'USD', 'YUMC'],
  ['VIPS', 'Vipshop', 'asia', 'NYSE', 'USD', 'VIPS'],
  ['SONY', 'Sony Group', 'asia', 'NYSE', 'USD', 'SONY'],
  ['TM', 'Toyota Motor', 'asia', 'NYSE', 'USD', 'TM'],
  ['MUFG', 'Mitsubishi UFJ Financial', 'asia', 'NYSE', 'USD', 'MUFG'],
  ['HMC', 'Honda Motor', 'asia', 'NYSE', 'USD', 'HMC'],
  ['SMFG', 'Sumitomo Mitsui Financial', 'asia', 'NYSE', 'USD', 'SMFG'],
  ['CPNG', 'Coupang', 'asia', 'NYSE', 'USD', 'CPNG'],
  ['KB', 'KB Financial Group', 'asia', 'NYSE', 'USD', 'KB'],
  ['SHG', 'Shinhan Financial Group', 'asia', 'NYSE', 'USD', 'SHG'],
  ['PKX', 'POSCO Holdings', 'asia', 'NYSE', 'USD', 'PKX'],
  ['UMC', 'United Microelectronics (UMC)', 'asia', 'NYSE', 'USD', 'UMC'],
  ['ASX', 'ASE Technology', 'asia', 'NYSE', 'USD', 'ASX'],
  ['INFY', 'Infosys', 'asia', 'NYSE', 'USD', 'INFY'],
  ['HDB', 'HDFC Bank', 'asia', 'NYSE', 'USD', 'HDB'],
  ['IBN', 'ICICI Bank', 'asia', 'NYSE', 'USD', 'IBN'],
  ['DBS', 'DBS Group', 'asia', 'SGX', 'SGD', 'D05.SI'],
  ['OCBC', 'OCBC', 'asia', 'SGX', 'SGD', 'O39.SI'],
  ['UOB', 'UOB (United Overseas Bank)', 'asia', 'SGX', 'SGD', 'U11.SI'],
  ['SINGTEL', 'Singtel', 'asia', 'SGX', 'SGD', 'Z74.SI'],
  ['BBCA', 'Bank Central Asia', 'asia', 'IDX', 'IDR', 'BBCA.JK'],
  ['BBRI', 'Bank Rakyat Indonesia', 'asia', 'IDX', 'IDR', 'BBRI.JK'],
  ['BMRI', 'Bank Mandiri', 'asia', 'IDX', 'IDR', 'BMRI.JK'],
  ['TLKM', 'Telkom Indonesia', 'asia', 'IDX', 'IDR', 'TLKM.JK'],
  ['PTT', 'PTT', 'asia', 'SET', 'THB', 'PTT.BK'],
  ['ADVANC', 'Advanced Info Service', 'asia', 'SET', 'THB', 'ADVANC.BK'],
  ['CPALL', 'CP All', 'asia', 'SET', 'THB', 'CPALL.BK'],
  ['AOT', 'Airports of Thailand', 'asia', 'SET', 'THB', 'AOT.BK'],
  ['MAYBANK', 'Maybank', 'asia', 'BURSA', 'MYR', '1155.KL'],
  ['PBBANK', 'Public Bank', 'asia', 'BURSA', 'MYR', '1295.KL'],
  ['TENAGA', 'Tenaga Nasional', 'asia', 'BURSA', 'MYR', '5347.KL'],
  ['VCB', 'Vietcombank', 'asia', 'HOSE', 'VND', 'VCB.VN'],
  ['VHM', 'Vinhomes', 'asia', 'HOSE', 'VND', 'VHM.VN'],
  ['FPT', 'FPT Corporation', 'asia', 'HOSE', 'VND', 'FPT.VN'],
  ['TENCENT', 'Tencent', 'asia', 'HKEX', 'HKD', '0700.HK'],
  ['XIAOMI', 'Xiaomi', 'asia', 'HKEX', 'HKD', '1810.HK'],
  ['MEITUAN', 'Meituan', 'asia', 'HKEX', 'HKD', '3690.HK'],
  ['BYD', 'BYD', 'asia', 'HKEX', 'HKD', '1211.HK'],
  ['AIA', 'AIA Group', 'asia', 'HKEX', 'HKD', '1299.HK'],
  ['TOKYOELEC', 'Tokyo Electron', 'asia', 'TSE', 'JPY', '8035.T'],
  ['KEYENCE', 'Keyence', 'asia', 'TSE', 'JPY', '6861.T'],
  ['ADVANTEST', 'Advantest', 'asia', 'TSE', 'JPY', '6857.T'],
  ['NINTENDO', 'Nintendo', 'asia', 'TSE', 'JPY', '7974.T'],
  ['FASTRETAIL', 'Fast Retailing', 'asia', 'TSE', 'JPY', '9983.T'],
  ['LGES', 'LG Energy Solution', 'asia', 'KRX', 'KRW', '373220.KS'],
  ['NAVER', 'Naver', 'asia', 'KRX', 'KRW', '035420.KS'],
  ['MEDIATEK', 'MediaTek', 'asia', 'TWSE', 'TWD', '2454.TW'],
  ['HONHAI', 'Hon Hai (Foxconn)', 'asia', 'TWSE', 'TWD', '2317.TW'],
  ['DELTA', 'Delta Electronics', 'asia', 'TWSE', 'TWD', '2308.TW'],
  // Singapore: SGX lines (SGD, or USD where SGX quotes the line in USD) and Singapore issuers listed in the US.
  ['KEPPEL', 'Keppel Ltd', 'asia', 'SGX', 'SGD', 'BN4.SI'],
  ['SEMBCORP', 'Sembcorp Industries', 'asia', 'SGX', 'SGD', 'U96.SI'],
  ['STENG', 'Singapore Technologies Engineering', 'asia', 'SGX', 'SGD', 'S63.SI'],
  ['WILMAR', 'Wilmar International', 'asia', 'SGX', 'SGD', 'F34.SI'],
  ['SIA', 'Singapore Airlines', 'asia', 'SGX', 'SGD', 'C6L.SI'],
  ['SGXL', 'Singapore Exchange (SGX Group)', 'asia', 'SGX', 'SGD', 'S68.SI'],
  ['GENTING', 'Genting Singapore', 'asia', 'SGX', 'SGD', 'G13.SI'],
  ['CAPLAND', 'CapitaLand Investment', 'asia', 'SGX', 'SGD', '9CI.SI'],
  ['YZJ', 'Yangzijiang Shipbuilding', 'asia', 'SGX', 'SGD', 'BS6.SI'],
  ['VENTURE', 'Venture Corporation', 'asia', 'SGX', 'SGD', 'V03.SI'],
  ['CITYDEV', 'City Developments', 'asia', 'SGX', 'SGD', 'C09.SI'],
  ['THAIBEV', 'Thai Beverage', 'asia', 'SGX', 'SGD', 'Y92.SI'],
  ['JARDINE', 'Jardine Matheson Holdings', 'asia', 'SGX', 'USD', 'J36.SI'],
  ['UOL', 'UOL Group', 'asia', 'SGX', 'SGD', 'U14.SI'],
  ['KARO', 'Karooooo Ltd', 'asia', 'NYSE', 'USD', 'KARO'],
  ['HKLAND', 'Hongkong Land Holdings', 'asia', 'SGX', 'USD', 'H78.SI'],
  ['JARDINESEA', 'Jardine Matheson Southeast Asia (formerly Jardine Cycle & Carriage)', 'asia', 'SGX', 'SGD', 'C07.SI'],
  ['SEATRIUM', 'Seatrium', 'asia', 'SGX', 'SGD', '5E2.SI'],
  ['KLIC', 'Kulicke and Soffa Industries', 'asia', 'NYSE', 'USD', 'KLIC'],
  ['SATS', 'SATS Ltd', 'asia', 'SGX', 'SGD', 'S58.SI'],
  // Taiwan: TWSE lines in TWD and Taiwanese ADRs.
  ['QUANTA', 'Quanta Computer', 'asia', 'TWSE', 'TWD', '2382.TW'],
  ['FUBON', 'Fubon Financial Holding', 'asia', 'TWSE', 'TWD', '2881.TW'],
  ['CATHAY', 'Cathay Financial Holding', 'asia', 'TWSE', 'TWD', '2882.TW'],
  ['CTBC', 'CTBC Financial Holding', 'asia', 'TWSE', 'TWD', '2891.TW'],
  ['LARGAN', 'Largan Precision', 'asia', 'TWSE', 'TWD', '3008.TW'],
  ['ASUS', 'ASUSTeK Computer', 'asia', 'TWSE', 'TWD', '2357.TW'],
  ['ACCTON', 'Accton Technology', 'asia', 'TWSE', 'TWD', '2345.TW'],
  ['WISTRON', 'Wistron Corporation', 'asia', 'TWSE', 'TWD', '3231.TW'],
  ['WIWYNN', 'Wiwynn Corporation', 'asia', 'TWSE', 'TWD', '6669.TW'],
  ['EVERGREEN', 'Evergreen Marine', 'asia', 'TWSE', 'TWD', '2603.TW'],
  ['FORMOSA', 'Formosa Plastics', 'asia', 'TWSE', 'TWD', '1301.TW'],
  ['CHINASTEEL', 'China Steel', 'asia', 'TWSE', 'TWD', '2002.TW'],
  ['NOVATEK', 'Novatek Microelectronics', 'asia', 'TWSE', 'TWD', '3034.TW'],
  ['REALTEK', 'Realtek Semiconductor', 'asia', 'TWSE', 'TWD', '2379.TW'],
  ['UNIMICRON', 'Unimicron Technology', 'asia', 'TWSE', 'TWD', '3037.TW'],
  ['ADVANTECH', 'Advantech', 'asia', 'TWSE', 'TWD', '2395.TW'],
  ['CHAILEASE', 'Chailease Holding', 'asia', 'TWSE', 'TWD', '5871.TW'],
  ['ESUN', 'E.Sun Financial Holding', 'asia', 'TWSE', 'TWD', '2884.TW'],
  ['MEGA', 'Mega Financial Holding', 'asia', 'TWSE', 'TWD', '2886.TW'],
  ['CHT', 'Chunghwa Telecom', 'asia', 'NYSE', 'USD', 'CHT'],
  ['HIMX', 'Himax Technologies', 'asia', 'NYSE', 'USD', 'HIMX'],
  ['SIMO', 'Silicon Motion Technology', 'asia', 'NYSE', 'USD', 'SIMO'],
  ['NANYAPLAS', 'Nan Ya Plastics', 'asia', 'TWSE', 'TWD', '1303.TW'],
  ['ELITEMAT', 'Elite Material', 'asia', 'TWSE', 'TWD', '2383.TW'],
  ['NANYATECH', 'Nanya Technology', 'asia', 'TWSE', 'TWD', '2408.TW'],
  // Korea: KRX lines in KRW and Korean ADRs.
  ['SAMBIO', 'Samsung Biologics', 'asia', 'KRX', 'KRW', '207940.KS'],
  ['KIA', 'Kia Corporation', 'asia', 'KRX', 'KRW', '000270.KS'],
  ['CELLTRION', 'Celltrion', 'asia', 'KRX', 'KRW', '068270.KS'],
  ['SAMSDI', 'Samsung SDI', 'asia', 'KRX', 'KRW', '006400.KS'],
  ['LGCHEM', 'LG Chem', 'asia', 'KRX', 'KRW', '051910.KS'],
  ['MOBIS', 'Hyundai Mobis', 'asia', 'KRX', 'KRW', '012330.KS'],
  ['SAMCT', 'Samsung C&T', 'asia', 'KRX', 'KRW', '028260.KS'],
  ['HANWHAAERO', 'Hanwha Aerospace', 'asia', 'KRX', 'KRW', '012450.KS'],
  ['HDHHI', 'HD Hyundai Heavy Industries', 'asia', 'KRX', 'KRW', '329180.KS'],
  ['HDKSOE', 'HD Korea Shipbuilding & Offshore Engineering', 'asia', 'KRX', 'KRW', '009540.KS'],
  ['DOOSANENER', 'Doosan Enerbility', 'asia', 'KRX', 'KRW', '034020.KS'],
  ['HANA', 'Hana Financial Group', 'asia', 'KRX', 'KRW', '086790.KS'],
  ['KAKAO', 'Kakao Corp', 'asia', 'KRX', 'KRW', '035720.KS'],
  ['SAMLIFE', 'Samsung Life Insurance', 'asia', 'KRX', 'KRW', '032830.KS'],
  ['LGE', 'LG Electronics', 'asia', 'KRX', 'KRW', '066570.KS'],
  ['SKINNO', 'SK Innovation', 'asia', 'KRX', 'KRW', '096770.KS'],
  ['SKINC', 'SK Inc', 'asia', 'KRX', 'KRW', '034730.KS'],
  ['KRAFTON', 'Krafton', 'asia', 'KRX', 'KRW', '259960.KS'],
  ['HYBE', 'HYBE', 'asia', 'KRX', 'KRW', '352820.KS'],
  ['KOREAZINC', 'Korea Zinc', 'asia', 'KRX', 'KRW', '010130.KS'],
  ['HANWHAOCEAN', 'Hanwha Ocean', 'asia', 'KRX', 'KRW', '042660.KS'],
  ['SEMCO', 'Samsung Electro-Mechanics', 'asia', 'KRX', 'KRW', '009150.KS'],
  ['KEP', 'Korea Electric Power', 'asia', 'NYSE', 'USD', 'KEP'],
  ['SKM', 'SK Telecom', 'asia', 'NYSE', 'USD', 'SKM'],
  ['KT', 'KT Corporation', 'asia', 'NYSE', 'USD', 'KT'],
  ['LPL', 'LG Display', 'asia', 'NYSE', 'USD', 'LPL'],
]

/** [symbol, name] rows for the instrument table (SYMBOLS). */
export const QUOTE_FEED_SYMBOLS: readonly (readonly [string, string])[] = ROWS.map(([s, name]) => [s, name] as const)
/** The configured listing behind each quote-feed symbol, for pickers and fixtures that have no live asset. */
export const QUOTE_FEED_LISTINGS: Readonly<Record<string, { region: 'us' | 'asia'; sessionVenue: Venue; localCurrency: Ccy; yahoo: string }>> =
  Object.fromEntries(ROWS.map(([s, , region, sessionVenue, localCurrency, yahoo]) => [s, { region, sessionVenue, localCurrency, yahoo }]))

export const isQuoteFeed = (symbol: string) => symbol in QUOTE_FEED_LISTINGS
/**
 * A live asset priced from the Yahoo quote feed: the API's venue, else the configured table, else the instrument
 * catalogue's row (a listed Asian stock before its first quote, which must never fall through to a Binance pair).
 */
export const quoteFeedAsset = (symbol: string, a?: Pick<Asset, 'venue'>, row?: Pick<Instrument, 'venue'>) =>
  a?.venue === 'yahoo' || (!a && (isQuoteFeed(symbol) || row?.venue === 'yahoo'))

type Listing = Pick<Asset, 'session' | 'sessionVenue' | 'localCurrency' | 'quoteFeed'>
/** The live asset, else (while the assets load, or with no quote) the configured listing's or catalogue row's venue and currency. */
export const feedListing = (symbol: string, a?: Asset, row?: Pick<Instrument, 'venue' | 'sessionVenue' | 'localCurrency'>): Listing | undefined => {
  if (a) return a
  const l = QUOTE_FEED_LISTINGS[symbol]
  if (l) return { session: null, sessionVenue: l.sessionVenue, localCurrency: l.localCurrency, quoteFeed: null }
  return row?.venue === 'yahoo' ? { session: null, sessionVenue: row.sessionVenue, localCurrency: row.localCurrency, quoteFeed: null } : undefined
}

/** ISO time → "Oct 6, 09:14 UTC", or "Oct 6, 09:14:05 UTC" with seconds (strategy.ts utcTime with the date). */
function utcAt(iso: string, seconds = false) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return `${d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })}, ${d.toISOString().slice(11, seconds ? 19 : 16)} UTC`
}

export { feedQuote, realtimeOpen }

/** Exchange name for prose: "SGX", "Bursa Malaysia", "US market". */
export const venueName = (venue: string | null | undefined) =>
  venue === 'NYSE' ? 'US market' : venue === 'BURSA' ? 'Bursa Malaysia' : (venue ?? 'exchange')

/** ISO time → "01:12 UTC"; '' when it does not parse. */
export function utcClock(iso: string | null | undefined) {
  const d = new Date(iso ?? '')
  return Number.isNaN(d.getTime()) ? '' : `${d.toISOString().slice(11, 16)} UTC`
}

/**
 * How a quote feed's price reads beside it while its venue is open, from the quote's own source and delay, never "live"
 * for a delayed price: "real-time · 04:20:15 UTC", "about 2 min behind · 04:18 UTC", "delayed · last trade 01:12 UTC".
 */
export const openQuoteLabel = (q: QuoteFeed) => priceLabel(feedQuote(q))

/** The ticket's word for the price: the open quote's label, else "last close" (closed) or "last quote" (unknown). */
export const feedPriceState = (q: QuoteFeed) => (q.marketState === 'open' ? openQuoteLabel(q) : q.marketState === 'closed' ? 'last close' : 'last quote')

/** A price cell's caption: "Real-time price", "Price, 2 min behind", "Delayed price", "Last close" or "Last quote". */
export function feedPriceCaption(q: QuoteFeed) {
  if (q.marketState !== 'open') return q.marketState === 'closed' ? 'Last close' : 'Last quote'
  const fq = feedQuote(q)
  const kind = freshness(fq)
  return kind === 'realtime' ? 'Real-time price' : kind === 'behind' ? `Price, ${behindMinutes(fq)} min behind` : 'Delayed price'
}

/** Short source names for the header's feed line: "Yahoo", "Naver", "TWSE MIS". */
const SOURCE_SHORT: Record<string, string> = { yahoo: 'Yahoo', naver: 'Naver', 'yahoo-tw': 'Yahoo Taiwan' }
export const sourceShort = (q: QuoteFeed | null | undefined) => {
  const s = q ? feedQuote(q).source : 'yahoo'
  return SOURCE_SHORT[s ?? 'yahoo'] ?? sourceName(s)
}

/** What paper fills use while the venue is open, by the quote's freshness ("Naver Finance's real-time price"). */
function openFills(q: QuoteFeed) {
  const fq = feedQuote(q)
  const kind = freshness(fq)
  const who = fq.source === 'yahoo' ? "Yahoo's" : `${sourceName(fq.source)}'s` // Yahoo's: today's wording
  const when = (word: string, seconds = false) => (fq.lastTradeAt ? ` (${word} ${utcAt(fq.lastTradeAt, seconds)})` : '')
  if (kind === 'realtime') return `${who} real-time price${when('last trade', true)}`
  if (kind === 'behind') return `${who} price, about ${behindMinutes(fq)} min behind the exchange${when(fq.timeBasis === 'snapshot' ? 'snapshot of' : 'last trade')}`
  return `${who} latest trade, which is delayed${when('last trade')}`
}

/**
 * The quote-feed hours note for the header and ticket: prices follow the listing's regular hours; while open, fills use
 * the quote's source (real-time, a minute or two behind, or Yahoo's delayed trade); outside them (as now, when closed)
 * fills use the last close, with the quote's as-of time.
 */
export function quoteFeedNote(a: Pick<Asset, 'sessionVenue' | 'quoteFeed'> | undefined) {
  const venue = venueName(a?.sessionVenue)
  const q = a?.quoteFeed
  const asOf = q?.asOf ? ` (as of ${utcAt(q.asOf)})` : ''
  if (q?.marketState === 'open') return `Quote feed: prices follow ${venue} hours. It is open now, so fills use ${openFills(q)}; outside its hours they use the last close.`
  if (q?.marketState === 'closed') return `Quote feed: prices follow ${venue} hours. Outside them, as now, paper fills use the last close${asOf}.`
  return `Quote feed: prices follow ${venue} hours; outside them paper fills use the last close${asOf}.`
}

/**
 * Yahoo's measured delay per venue (minutes behind the exchange, 2026-10-13/14), for the chart's bars, which stay
 * Yahoo's whichever source prices the quote. Venues not here (the US) have no measured figure.
 */
export const YAHOO_DELAY_MINUTES: Readonly<Partial<Record<Venue, number>>> = {
  KRX: 20, TWSE: 20, TSE: 15, HKEX: 15, BURSA: 15, HOSE: 15, SGX: 10, IDX: 10, SET: 30,
}

/**
 * The header's line under the price: the open quote's label ("real-time · 04:20:15 UTC"), else "last close · Oct 14,
 * 06:30 UTC" (closed) or "last quote · …" (unknown).
 */
export function feedPriceLine(q: QuoteFeed) {
  if (q.marketState === 'open') return openQuoteLabel(q)
  const at = q.asOf ? utcAt(q.asOf) : '—'
  return `${q.marketState === 'closed' ? 'last close' : 'last quote'}${at === '—' ? '' : ` · ${at}`}`
}

const CLASS_TEXT: Record<QuoteClass, string> = { realtime: 'a real-time source', 'near-realtime': 'a snapshot about once a minute', delayed: 'a delayed feed' }

/** The price line's tooltip: whose quote, what kind of source, how far behind it was when read, and what its time is. */
export function feedQuoteTitle(q: QuoteFeed) {
  const fq = feedQuote(q)
  const d = delaySeconds(fq)
  const read = fq.readAt ? utcTime(fq.readAt, true) : ''
  const behind = d == null ? '' : `${d < 90 ? `${Math.round(d)} s` : `about ${Math.round(d / 60)} min`} behind the exchange when read${read ? ` at ${read}` : ''}`
  const kind = q.class ? CLASS_TEXT[q.class] : 'a delayed feed'
  return [`${sourceName(fq.source)}, ${kind}`, behind, basisText(fq.timeBasis)].filter(Boolean).join('; ')
}

/**
 * A quote fresher than Yahoo's bars (real-time, and SGX's snapshot) moves the chart's last bar at its own trade time, in
 * session and out of it: after a close or in a lunch break its last trade (the settled close) is still ahead of Yahoo's
 * delayed bars for 10 to 30 minutes, and once they show it, folding it changes nothing.
 */
export const movesBars = (q: QuoteFeed | null | undefined) => !!q && (q.class === 'realtime' || q.class === 'near-realtime')

/**
 * The chart's bars label for a quote feed on a venue with a measured Yahoo delay: the bars are Yahoo's, so say how
 * delayed, and what moves the last one when a fresher quote does. Undefined elsewhere (the US): today's badge.
 */
export function chartBars(a: Pick<Asset, 'sessionVenue' | 'quoteFeed'> | undefined, interval: string, moving: boolean): ChartBars | undefined {
  const n = a?.sessionVenue ? YAHOO_DELAY_MINUTES[a.sessionVenue] : undefined
  if (n == null) return undefined
  const fq = a?.quoteFeed ? feedQuote(a.quoteFeed) : null
  const bars = `Yahoo bars · ${n} min delayed`
  const head = `The bars are Yahoo's ${interval} bars in USD, about ${n} min behind the exchange (measured), reloaded every 30 s.`
  if (!moving || !fq) return { bars, title: head }
  if (a?.quoteFeed?.marketState !== 'open') {
    const at = fq.lastTradeAt ? ` (${utcAt(fq.lastTradeAt, true)})` : ''
    return { bars, last: 'last bar at the last trade', title: `${head} The last bar ends at ${sourceName(fq.source)}'s last trade${at}, which Yahoo's delayed bars may not show yet.` }
  }
  const kind = freshness(fq)
  return {
    bars,
    last: `last bar ${freshnessTag(kind, behindMinutes(fq))}`,
    title: `${head} The last bar moves with ${sourceName(fq.source)}'s price (${priceLabel(fq)})${realtimeOpen(a?.quoteFeed) ? ', read every 5 s' : ''}: its high and low are only the prices read here.`,
  }
}

/**
 * A venue group's tag in the market list, from its rows' quotes: what most of its open prices are ("real-time", "about
 * 1 min behind", "delayed"), "closed" when none is open; an API before the real-time sources: "delayed", as before.
 * Each row's own label is in its tooltip.
 */
export function groupFreshness(assets: Pick<Asset, 'quoteFeed'>[]): { tag: string; title: string } | null {
  const quotes = assets.flatMap((a) => (a.quoteFeed ? [a.quoteFeed] : []))
  if (!quotes.length) return null
  if (!quotes.some((q) => q.class)) return { tag: 'delayed', title: 'Yahoo quotes reach us delayed; each one shows its last trade time' }
  const open = quotes.filter((q) => q.marketState === 'open')
  const names = [...new Set((open.length ? open : quotes).map((q) => sourceName(feedQuote(q).source)))].join(', ')
  if (!open.length) return { tag: 'closed', title: `Closed now: these prices are the last close (${names}). Each row's tooltip has its own label.` }
  const count = { realtime: 0, behind: 0, delayed: 0 }
  let minutes = Infinity
  for (const q of open) {
    const fq = feedQuote(q)
    const kind = freshness(fq)
    count[kind]++
    if (kind === 'behind') minutes = Math.min(minutes, behindMinutes(fq))
  }
  const kind = (['realtime', 'behind', 'delayed'] as const).reduce((best, k) => (count[k] > count[best] ? k : best), 'realtime')
  const tag = freshnessTag(kind, Number.isFinite(minutes) ? minutes : 1)
  return { tag, title: `${count[kind]} of ${open.length} open prices here are ${tag} (from ${names}). Each row's tooltip has its own label.` }
}

/**
 * The header's session line for a quote feed: "SGX session closed" from the venue's verified calendar. IDX, Bursa and
 * HOSE have none (`session` null), so their line gives Yahoo's own state, as the hours note and the fills follow it.
 */
export function quoteFeedSession(a: Pick<Asset, 'session' | 'sessionVenue' | 'quoteFeed'> | undefined) {
  const venue = a?.sessionVenue ?? 'Exchange'
  if (a?.session) return `${venue} session ${a.session}`
  const state = a?.quoteFeed?.marketState
  return state === 'open' || state === 'closed' ? `${venue} session ${state} · per Yahoo` : `${venue} session unknown`
}

/** "SGD→USD at Yahoo FX: 1 SGD = $0.7822 (as of Oct 6, 09:14 UTC)"; empty for USD listings. */
export function quoteFeedFx(a: Pick<Asset, 'localCurrency' | 'quoteFeed'> | undefined) {
  const q = a?.quoteFeed
  const ccy = q?.currency ?? a?.localCurrency
  if (!ccy || ccy === 'USD') return ''
  const rate = q && q.fx > 0 ? `: 1 ${ccy} = $${q.fx.toPrecision(4)}${q.fxAsOf ? ` (as of ${utcAt(q.fxAsOf)})` : ''}` : ''
  return `${ccy}→USD at Yahoo FX${rate}`
}
