// [n] citations through the real react-markdown pipeline.
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { describe, expect, it } from 'vitest'
import type { Card } from '../../lib/types'
import { citationSources, linkCitations, remarkCitations } from './citations'

const card: Card = {
  type: 'sources',
  data: {
    items: [
      { n: 2, title: 'ETF flows &amp; more', url: 'https://www.coindesk.com/etf', source: '', publishedAt: null },
      { n: 1, title: 'Fed holds rates', url: 'https://reuters.com/fed', source: 'reuters.com', publishedAt: '2026-10-02T08:00:00Z' },
      { n: 3, title: 'Evil', url: 'javascript:alert(1)', source: 'x', publishedAt: null },
      { n: 1, title: 'Duplicate number', url: 'https://dup.io', source: 'dup.io', publishedAt: null },
      { n: 0, title: 'Bad number', url: 'https://zero.io', source: 'zero.io', publishedAt: null },
    ],
  },
}
const sources = citationSources([{ type: 'price_outlook', data: {} as never }, card])
const render = (md: string, byN = sources) => renderToStaticMarkup(createElement(ReactMarkdown, { remarkPlugins: [remarkGfm, remarkCitations(byN)] }, md))
const link1 = '<sup class="ml-px leading-[0]"><a href="https://reuters.com/fed"'

describe('citationSources', () => {
  it('keeps valid sources only, in number order (first n wins, javascript: dropped)', () => {
    expect([...sources.keys()]).toEqual([1, 2])
  })

  it('cleans titles and falls back to the domain for the source', () => {
    expect([sources.get(2)?.source, sources.get(2)?.title]).toEqual(['coindesk.com', 'ETF flows & more'])
    expect(sources.get(1)?.publishedAt).toBe('2026-10-02T08:00:00Z')
  })

  it('is empty without a sources card, or with a malformed one', () => {
    expect(citationSources([]).size).toBe(0)
    expect(citationSources([{ type: 'sources', data: { items: 'x' } } as never]).size).toBe(0)
  })
})

describe('remarkCitations', () => {
  it('turns [n] in prose into superscript links', () => {
    const prose = render('The Fed held [1]; ETF inflows rose [2][3] and [1, 2]. Footnote-ish [^1] and [10].')
    expect(prose, '[1] becomes a superscript link').toContain(link1)
    expect(prose, 'link title names the source').toContain('title="1. Fed holds rates (reuters.com)"')
    expect(prose, 'link text is the number').toContain('>1</a></sup>')
    expect(prose, '[2] links to source 2').toContain('href="https://www.coindesk.com/etf"')
    expect(prose, '[3] has no source, so it stays text').toContain('[3]')
    expect(prose, 'footnote syntax untouched').toContain('[^1]')
    expect(prose, 'unknown number untouched').toContain('[10]')
    expect(prose.match(/<sup/g) ?? [], '[1], [2], and [1, 2] → four superscripts').toHaveLength(4)
    expect(prose, 'no javascript: url').not.toContain('javascript:')
  })

  it('leaves code spans and fenced code alone', () => {
    const code = render('Inline `arr[1]` here.\n\n```\nx = y[1]\n```\n')
    expect(code).not.toContain('<sup')
    expect(code).toContain('arr[1]')
  })

  it('links citations inside table cells and emphasis, never inside an existing link', () => {
    const table = render('| asset | note |\n|---|---|\n| BTC | inflows [2] |\n')
    expect(table).toContain('<table>')
    expect(table).toContain('<td>inflows <sup')
    const linked = render('[see [1]](https://example.com) and **bold [1]**')
    expect(linked.match(/<a /g) ?? [], 'no anchor nested inside a link; citation in bold still linked').toHaveLength(2)
    expect(linked).toContain('<strong>bold <sup')
  })

  it('changes nothing without sources', () => {
    expect(render('Text [1] stays.', new Map())).toContain('Text [1] stays.')
  })

  it('skips childless nodes', () => {
    const leaf = { type: 'thematicBreak' }
    linkCitations(leaf, sources)
    expect(leaf).toEqual({ type: 'thematicBreak' })
  })
})
