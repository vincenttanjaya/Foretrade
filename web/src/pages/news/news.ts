// Market wire helpers (contract "Market intelligence"): defensive parsing of /api/news and /api/news/brief,
// filter queries and link helpers shared with the agent's citations. Plain TS so news.test.ts can run it.
import type { BriefSource, MarketBrief, NewsItem, NewsRegion, Sentiment } from '../../lib/types'
import { SYMBOLS } from '../strategies/strategy'

/** Every universe symbol in contract §2 order. */
const ORDER: readonly string[] = SYMBOLS.map(([s]) => s)
/** The 12 core assets (contract §2): the wire's filter chips and the brief's sentiment grid. */
export const CORE = ORDER.slice(0, 12)

const isSymbol = (s: unknown): s is string => typeof s === 'string' && ORDER.includes(s)
const isString = (s: unknown): s is string => typeof s === 'string'
const arr = <T>(a: T[] | null | undefined): T[] => (Array.isArray(a) ? a : [])

/** The wire's regions in tab order (GET /api/news?region=). */
export const REGIONS: readonly NewsRegion[] = ['crypto', 'us', 'east_asia', 'southeast_asia']
export const REGION_LABEL: Record<NewsRegion, string> = { crypto: 'Crypto', us: 'US', east_asia: 'East Asia', southeast_asia: 'Southeast Asia' }

const isRegion = (r: unknown): r is NewsRegion => typeof r === 'string' && (REGIONS as readonly string[]).includes(r)
/** "?region=east_asia" → 'east_asia'; null (All) when absent or anything the API would reject. */
export const regionParam = (v: string | null): NewsRegion | null => (isRegion(v) ? v : null)
/** Known regions only, once each, in tab order. */
const regionsOf = (raw: unknown): NewsRegion[] => REGIONS.filter((r) => arr(raw as unknown[]).includes(r))

/** Only http(s) links are rendered: everything here comes from the open web. */
export function isHttpUrl(u: unknown): u is string {
  if (typeof u !== 'string') return false
  try {
    const { protocol } = new URL(u)
    return protocol === 'http:' || protocol === 'https:'
  } catch {
    return false
  }
}

/** "https://www.reuters.com/x" → "reuters.com"; '' when unparseable. */
export function domainOf(u: string): string {
  try {
    return new URL(u).hostname.replace(/^www\./, '')
  } catch {
    return ''
  }
}

/** One-letter stand-in for a favicon: the first letter of the source's domain. */
export function monogram(source: string, url: string): string {
  return ((domainOf(url) || source).match(/[a-z0-9]/i)?.[0] ?? '•').toUpperCase()
}

const ENTITIES: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', '#39': "'", '#x27': "'" }

/** Search snippets arrive with <strong> highlights and entities; React would print them literally. */
export function plain(s: unknown): string {
  if (typeof s !== 'string') return ''
  return s
    .replace(/<[^>]*>/g, '')
    .replace(/&(amp|lt|gt|quot|apos|nbsp|#39|#x27);/g, (_, e: string) => ENTITIES[e])
    .replace(/\s+/g, ' ')
    .trim()
}

/** GET /api/news body → renderable items (missing title or non-http url dropped, unknown tickers ignored). */
export function asNews(raw: unknown): NewsItem[] {
  return arr(raw as NewsItem[]).flatMap((i) => {
    const title = plain(i?.title)
    if (!title || !isHttpUrl(i.url)) return []
    return [
      {
        id: isString(i.id) && i.id ? i.id : i.url,
        title,
        url: i.url,
        source: plain(i.source) || domainOf(i.url),
        description: plain(i.description),
        publishedAt: isString(i.publishedAt) ? i.publishedAt : null,
        symbols: arr(i.symbols).filter(isSymbol),
        topic: i.topic === 'macro' ? 'macro' : 'asset',
        regions: regionsOf(i.regions),
        hasContent: i.hasContent === true,
      },
    ]
  })
}

const clamp = (n: unknown) => (typeof n === 'number' && Number.isFinite(n) ? Math.max(-1, Math.min(1, n)) : 0)

/** The model's label when valid, else one derived from the score. */
function labelOf(s: Sentiment): Sentiment['label'] {
  if (s.label === 'bullish' || s.label === 'bearish' || s.label === 'neutral') return s.label
  const score = clamp(s.score)
  return score > 0.15 ? 'bullish' : score < -0.15 ? 'bearish' : 'neutral'
}

/** GET /api/news/brief body → a brief safe to render, or null (none yet, or not a brief). */
export function asBrief(raw: unknown): MarketBrief | null {
  const b = raw as MarketBrief | null
  const headline = plain(b?.headline)
  if (!b || !headline) return null
  return {
    ...b,
    headline,
    summary: plain(b.summary),
    bullets: arr(b.bullets).flatMap((x) =>
      plain(x?.text) ? [{ text: plain(x.text), symbols: arr(x.symbols).filter(isSymbol), sources: arr(x.sources).filter(isString) }] : [],
    ),
    sentiment: arr(b.sentiment)
      .filter((s) => isSymbol(s?.symbol))
      .map((s) => ({ symbol: s.symbol, score: clamp(s.score), label: labelOf(s), reason: plain(s.reason) }))
      .sort((a, z) => ORDER.indexOf(a.symbol) - ORDER.indexOf(z.symbol)),
    // Optional per-region breakdown: one summary per known region, in tab order.
    regions: REGIONS.flatMap((region) => {
      const summary = plain(arr(b.regions).find((x) => x?.region === region)?.summary)
      return summary ? [{ region, summary }] : []
    }),
    // The cited items stored with the brief: same rules as the wire (a title and an http link, plain text).
    sources: arr(b.sources).flatMap((x): BriefSource[] => {
      const title = plain(x?.title)
      if (!title || !isString(x.id) || !x.id || !isHttpUrl(x.url)) return []
      return [{ id: x.id, title, url: x.url, source: plain(x.source) || domainOf(x.url), publishedAt: isString(x.publishedAt) ? x.publishedAt : null }]
    }),
    articleCount: typeof b.articleCount === 'number' && Number.isFinite(b.articleCount) ? b.articleCount : 0,
    createdAt: isString(b.createdAt) ? b.createdAt : '',
  }
}

/** A brief is stale past 6 h: three missed 2-hour refreshes. */
export const BRIEF_STALE_MS = 6 * 3_600_000
export const briefStale = (b: Pick<MarketBrief, 'createdAt'>, now = Date.now()) => now - Date.parse(b.createdAt) > BRIEF_STALE_MS

/** What a bullet's citation links to. */
export type Cited = Pick<NewsItem, 'id' | 'title' | 'url' | 'source'>

/** A bullet's source ids → their items: unknown ids dropped, duplicates once, at most `max`. */
export function resolveSources(ids: string[], byId: Map<string, Cited>, max = 3): Cited[] {
  return [...new Set(ids)].flatMap((id) => byId.get(id) ?? []).slice(0, max)
}

/** Id → citable item: the brief's stored sources win over the wire's items with the same id. */
export const citations = (brief: MarketBrief | null, ...wires: NewsItem[][]): Map<string, Cited> =>
  new Map<string, Cited>([...wires.flat(), ...(brief?.sources ?? [])].map((i) => [i.id, i]))

/** Filter chip (+ region tab) → GET /api/news query. A filter is 'all', 'macro' or a universe symbol; null region = All. */
export function newsQuery(filter: string, region: NewsRegion | null = null): { symbol?: string; topic?: 'macro'; region?: NewsRegion } {
  const q = filter === 'all' ? {} : filter === 'macro' ? { topic: 'macro' as const } : { symbol: filter }
  return region ? { ...q, region } : q
}

/** What the wire is showing, for its heading and empty state: "East Asia macro", "SOL", "macro"; '' = everything. */
export function wireScope(filter: string, region: NewsRegion | null): string {
  return [region ? REGION_LABEL[region] : '', filter === 'all' ? '' : filter].filter(Boolean).join(' ')
}

/** Diverging score bar from the centre, in % of the track: −1 fills the left half, +1 the right half. */
export function scoreBar(score: number): { left: number; width: number } {
  const s = clamp(score)
  return { left: 50 + Math.min(s, 0) * 50, width: Math.abs(s) * 50 }
}
