import { describe, expect, it } from 'vitest'
import { asBrief, asNews, briefStale, citations, CORE, domainOf, isHttpUrl, monogram, newsQuery, plain, regionParam, REGIONS, resolveSources, scoreBar, wireScope } from './news'

describe('links', () => {
  // Only http(s) is ever rendered.
  it.each(['https://reuters.com/a', 'http://x.io'])('accepts %s', (ok) => expect(isHttpUrl(ok)).toBe(true))
  it.each(['javascript:alert(1)', 'data:text/html,hi', 'ftp://x', '/relative', '', null, 42])('rejects %s', (bad) => expect(isHttpUrl(bad)).toBe(false))

  it('derives domains and monograms', () => {
    expect(domainOf('https://www.coindesk.com/markets/x')).toBe('coindesk.com')
    expect(domainOf('not a url')).toBe('')
    expect(monogram('Reuters', 'https://www.reuters.com/x')).toBe('R')
    expect(monogram('The Block', 'nope'), 'falls back to source').toBe('T')
    expect(monogram('', ''), 'placeholder').toBe('•')
  })
})

describe('plain', () => {
  it('strips tags, decodes entities and collapses whitespace in search snippets', () => {
    expect(plain('<strong>Bitcoin</strong> &amp; ETH  rally&#39;s\n end')).toBe("Bitcoin & ETH rally's end")
    expect(plain(undefined)).toBe('')
  })
})

describe('asNews', () => {
  it('drops junk, fills defaults and ignores unknown tickers', () => {
    const news = asNews([
      {
        id: 'n1',
        title: 'Fed <b>holds</b>',
        url: 'https://www.reuters.com/fed',
        source: '',
        description: 'Rates &amp; more',
        publishedAt: '2026-10-02T08:00:00Z',
        symbols: ['BTC', 'FOO', 7],
        topic: 'macro',
        regions: ['east_asia', 'mars', 'us', 'us', 3],
        hasContent: true,
      },
      { id: 'n2', title: 'No link', url: 'javascript:alert(1)' },
      { id: 'n3', title: '', url: 'https://x.io' },
      null,
      { title: 'Bare', url: 'https://bare.io/p' },
    ])
    expect(news).toEqual([
      {
        id: 'n1',
        title: 'Fed holds',
        url: 'https://www.reuters.com/fed',
        source: 'reuters.com',
        description: 'Rates & more',
        publishedAt: '2026-10-02T08:00:00Z',
        symbols: ['BTC'],
        topic: 'macro',
        regions: ['us', 'east_asia'], // known ones, once each, in tab order
        hasContent: true,
      },
      { id: 'https://bare.io/p', title: 'Bare', url: 'https://bare.io/p', source: 'bare.io', description: '', publishedAt: null, symbols: [], topic: 'asset', regions: [], hasContent: false },
    ])
    expect(asNews(null)).toEqual([])
    expect(asNews({ news: [] })).toEqual([])
  })

  it('keeps tags for every universe asset, while the chips and grid stay on the 12 core ones', () => {
    const [item] = asNews([{ id: 'a', title: 'Aave and Bittensor rally', url: 'https://x.io/a', symbols: ['AAVE', 'TAO', 'PEPE'] }])
    expect(item.symbols).toEqual(['AAVE', 'TAO'])
    expect(CORE).toHaveLength(12)
    expect([CORE[0], CORE[11]]).toEqual(['BTC', 'PAXG'])
  })
})

describe('asBrief', () => {
  it('is null when absent or headline-less', () => {
    expect(asBrief(null)).toBeNull()
    expect(asBrief({ headline: '' })).toBeNull()
  })

  it('cleans bullets and sentiment, derives missing labels and orders by the universe', () => {
    const brief = asBrief({
      id: 'b1',
      headline: 'Risk-on into the Fed',
      summary: 'Crypto firm.',
      bullets: [{ text: 'BTC ETF inflows', symbols: ['BTC', 'XYZ'], sources: ['n1', 3] }, { text: '' }, null],
      sentiment: [
        { symbol: 'PAXG', score: -0.4, label: 'bearish', reason: 'Yields up' },
        { symbol: 'ETH', score: 5, label: 'weird', reason: '' },
        { symbol: 'BTC', score: Number.NaN, label: 'neutral', reason: 'Flat' },
        { symbol: 'SOL', score: -0.9, label: '', reason: '' },
        { symbol: 'XRP', score: 0.05, label: '', reason: '' },
        { symbol: 'NOPE', score: 1, label: 'bullish', reason: '' },
      ],
      articleCount: 42,
      window: { from: '', to: '' },
      createdAt: '2026-10-02T06:00:00Z',
    })!
    expect(brief.bullets).toEqual([{ text: 'BTC ETF inflows', symbols: ['BTC'], sources: ['n1'] }])
    expect(brief.sentiment.map((s) => [s.symbol, s.score, s.label])).toEqual([
      ['BTC', 0, 'neutral'],
      ['ETH', 1, 'bullish'], // score clamped, label derived from it
      ['SOL', -0.9, 'bearish'],
      ['XRP', 0.05, 'neutral'],
      ['PAXG', -0.4, 'bearish'],
    ])
    expect(brief.articleCount).toBe(42)
  })

  it('tolerates non-array bullets and odd counts', () => {
    const b = asBrief({ headline: 'x', bullets: 'nope', sentiment: {}, regions: 'nope', articleCount: 'many', createdAt: 5 })!
    expect([b.bullets, b.sentiment, b.regions, b.articleCount, b.createdAt]).toEqual([[], [], [], 0, ''])
    expect(asBrief({ headline: 'x' })!.regions, 'an API without the breakdown').toEqual([])
  })

  it('keeps the stored sources a bullet can cite, as plain text with http links only', () => {
    const b = asBrief({
      headline: 'x',
      sources: [
        { id: 'n9', title: 'BOK <b>holds</b>', url: 'https://www.yna.co.kr/x', source: '', publishedAt: '2026-10-07T01:00:00Z' },
        { id: 'n8', title: 'No link', url: 'javascript:alert(1)', source: 'X' },
        { id: '', title: 'No id', url: 'https://a.io' },
        { id: 'n7', title: '', url: 'https://a.io' },
        null,
      ],
    })!
    expect(b.sources).toEqual([{ id: 'n9', title: 'BOK holds', url: 'https://www.yna.co.kr/x', source: 'yna.co.kr', publishedAt: '2026-10-07T01:00:00Z' }])
    expect(asBrief({ headline: 'x' })!.sources, 'an API without stored sources').toEqual([])
  })

  it('is stale past 6 hours', () => {
    const now = Date.parse('2026-10-08T12:00:00Z')
    expect(briefStale({ createdAt: '2026-10-08T05:59:00Z' }, now)).toBe(true)
    expect(briefStale({ createdAt: '2026-10-08T06:00:00Z' }, now)).toBe(false)
  })

  it('keeps one summary per known region, in tab order', () => {
    const b = asBrief({
      headline: 'x',
      regions: [
        { region: 'east_asia', summary: 'TSMC <b>guides</b> higher.' },
        { region: 'us', summary: 'Fed on hold.' },
        { region: 'us', summary: 'A second US take.' },
        { region: 'southeast_asia', summary: '' },
        { region: 'europe', summary: 'Not a wire region.' },
        null,
      ],
    })!
    expect(b.regions).toEqual([
      { region: 'us', summary: 'Fed on hold.' },
      { region: 'east_asia', summary: 'TSMC guides higher.' },
    ])
  })
})

describe('resolveSources', () => {
  const news = asNews([
    { id: 'n1', title: 'A', url: 'https://a.io' },
    { title: 'Bare', url: 'https://bare.io/p' },
  ])
  const byId = new Map(news.map((n) => [n.id, n]))

  it('dedupes, drops unknown ids and caps the count', () => {
    expect(resolveSources(['n1', 'missing', 'n1', 'https://bare.io/p'], byId).map((n) => n.id)).toEqual(['n1', 'https://bare.io/p'])
    expect(resolveSources(['n1', 'https://bare.io/p'], byId, 1)).toHaveLength(1)
  })

  it("prefers the brief's stored copy of a source over the wire's", () => {
    const brief = asBrief({ headline: 'x', sources: [{ id: 'n1', title: 'Stored A', url: 'https://a.io/stored', source: 'A' }, { id: 'n2', title: 'Old', url: 'https://b.io' }] })
    const map = citations(brief, news)
    expect(resolveSources(['n1', 'n2', 'https://bare.io/p'], map).map((n) => n.title)).toEqual(['Stored A', 'Old', 'Bare'])
    expect(citations(null, news).get('n1')?.title).toBe('A')
  })
})

describe('filters and score bars', () => {
  it('maps filter chips to the API query', () => {
    expect(newsQuery('all')).toEqual({})
    expect(newsQuery('macro')).toEqual({ topic: 'macro' })
    expect(newsQuery('SOL')).toEqual({ symbol: 'SOL' })
  })

  it('adds the region tab to the query, and All sends none', () => {
    expect(newsQuery('all', null)).toStrictEqual({})
    expect(newsQuery('all', 'east_asia')).toEqual({ region: 'east_asia' })
    expect(newsQuery('macro', 'us')).toEqual({ topic: 'macro', region: 'us' })
    expect(newsQuery('SOL', 'southeast_asia')).toEqual({ symbol: 'SOL', region: 'southeast_asia' })
  })

  it('reads ?region= as a known region or All', () => {
    expect(REGIONS).toEqual(['crypto', 'us', 'east_asia', 'southeast_asia'])
    for (const r of REGIONS) expect(regionParam(r)).toBe(r)
    expect(regionParam(null)).toBeNull()
    for (const bad of ['', 'all', 'EAST_ASIA', 'east-asia', 'europe']) expect(regionParam(bad), bad).toBeNull()
  })

  it('names what the wire shows', () => {
    expect(wireScope('all', null)).toBe('')
    expect(wireScope('macro', null)).toBe('macro')
    expect(wireScope('SOL', null)).toBe('SOL')
    expect(wireScope('all', 'southeast_asia')).toBe('Southeast Asia')
    expect(wireScope('macro', 'us')).toBe('US macro')
    expect(wireScope('BTC', 'east_asia')).toBe('East Asia BTC')
  })

  it('draws a diverging bar from the centre', () => {
    expect(scoreBar(1)).toEqual({ left: 50, width: 50 })
    expect(scoreBar(-0.5)).toEqual({ left: 25, width: 25 })
    expect(scoreBar(-3), 'clamped').toEqual({ left: 0, width: 50 })
    expect(scoreBar(Number.NaN), 'NaN → empty').toEqual({ left: 50, width: 0 })
  })
})
