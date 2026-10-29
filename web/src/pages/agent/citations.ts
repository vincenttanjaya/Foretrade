// Inline citations: the agent numbers every search/read result per message and cites them as [n]; the stream
// ends with a `sources` card listing them. This turns [n] in the answer into superscript links to those sources.
import type { Card, SourceRef } from '../../lib/types'
import { domainOf, isHttpUrl, plain } from '../news/news'

/** A `sources` card's items → valid sources by number (non-http urls and bad numbers dropped). */
export function citationSources(cards: Card[]): Map<number, SourceRef> {
  const byN = new Map<number, SourceRef>()
  for (const card of cards) {
    if (card.type !== 'sources' || !Array.isArray(card.data.items)) continue
    for (const s of card.data.items) {
      if (!s || !Number.isInteger(s.n) || s.n < 1 || byN.has(s.n) || !isHttpUrl(s.url)) continue
      const source = plain(s.source) || domainOf(s.url)
      const publishedAt = typeof s.publishedAt === 'string' ? s.publishedAt : null
      byN.set(s.n, { n: s.n, url: s.url, source, title: plain(s.title) || source, publishedAt, kind: s.kind })
    }
  }
  return new Map([...byN].sort(([a], [z]) => a - z))
}

// Just enough of mdast (remark's syntax tree) for this transform.
export type MdNode = { type: string; value?: string; url?: string; title?: string | null; children?: MdNode[]; data?: Record<string, unknown> }

const CITE = /\[(\d{1,2}(?:\s*,\s*\d{1,2})*)\]/g // [3] or [1, 4]

/** Every number the answer cites as [n] or [1, 4]. */
export function citedNumbers(text: string): Set<number> {
  return new Set([...text.matchAll(CITE)].flatMap((m) => m[1].split(',').map(Number)))
}

/**
 * Replaces [n] in text nodes with <sup><a href=url>n</a></sup>. Code, inline code and raw HTML are not text
 * nodes, so they are never touched; text already inside a link is skipped (no nested anchors). A bracket
 * citing any number without a source stays plain text.
 */
export function linkCitations(node: MdNode, byN: Map<number, SourceRef>): void {
  if (!node.children || node.type === 'link' || node.type === 'linkReference') return
  node.children = node.children.flatMap((child) => {
    if (child.type !== 'text') {
      linkCitations(child, byN)
      return [child]
    }
    return splitText(child.value ?? '', byN)
  })
}

function splitText(text: string, byN: Map<number, SourceRef>): MdNode[] {
  const out: MdNode[] = []
  let last = 0
  for (const m of text.matchAll(CITE)) {
    const refs = m[1].split(',').map((n) => byN.get(Number(n)))
    if (refs.some((r) => !r)) continue
    if (m.index > last) out.push({ type: 'text', value: text.slice(last, m.index) })
    for (const r of refs as SourceRef[]) out.push(citation(r))
    last = m.index + m[0].length
  }
  if (last === 0) return [{ type: 'text', value: text }]
  if (last < text.length) out.push({ type: 'text', value: text.slice(last) })
  return out
}

// An unknown node with data.hName renders as that element (mdast-util-to-hast), so this becomes <sup><a/></sup>.
const citation = (s: SourceRef): MdNode => ({
  type: 'citation',
  data: { hName: 'sup', hProperties: { className: ['ml-px', 'leading-[0]'] } },
  children: [
    {
      type: 'link',
      url: s.url,
      title: `${s.n}. ${s.title} (${s.source})`,
      data: {
        hProperties: {
          className: [
            'inline-block min-w-[1.6em] rounded-[4px] bg-accent-soft px-1 text-center font-mono text-[10.5px] font-medium leading-[1.5] text-accent-ink no-underline hover:bg-accent hover:text-[#17120a]',
          ],
        },
      },
      children: [{ type: 'text', value: String(s.n) }],
    },
  ],
})

/** remark plugin wrapper for react-markdown's `remarkPlugins`. */
export const remarkCitations = (byN: Map<number, SourceRef>) => () => (tree: MdNode) => linkCitations(tree, byN)
