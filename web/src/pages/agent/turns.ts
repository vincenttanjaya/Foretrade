// Conversation state for the agent page: stored messages and the live stream fold into the same Turn shape.
import type { Card, ChatAttachmentMetadata, ChatMessage, ChatStreamEvent } from '../../lib/types'
import { safeAttachmentMetadata } from './attachments'
import { domainOf } from '../news/news'

// ok undefined = still running; at = the answer's length when the call arrived (live turns), so a paced reveal keeps
// the chip after the text before it
export type ToolStep = { id: string; label: string; ok?: boolean; summary?: string; at?: number }

// preview: an image's data URL, only on a live turn whose bytes are still in memory
export type TurnAttachment = ChatAttachmentMetadata & { preview?: string }

export type Turn = {
  key: string
  role: 'user' | 'assistant'
  content: string // markdown for the assistant, plain text for the user
  cards: Card[]
  attachments?: TurnAttachment[]
  tools: ToolStep[] // live turns only; stored messages don't keep tool activity
  // pending: a reopened chat whose answer the server is still generating (the page polls for it)
  status: 'done' | 'streaming' | 'pending' | 'stopped' | 'error'
  error?: { code: string; message: string }
  startedAt?: number // live turns: when the message was sent (ms)
  elapsedMs?: number // live turns: from send to `done`
  persistedMessageId?: string // Only owned stored assistant messages or a real persisted SSE done.
  cardsAt?: number[] // live turns: the answer's length when each card arrived (parallel to `cards`), like ToolStep.at
}

let seq = 0
const nextKey = () => `t${++seq}`

export const userTurn = (content: string, attachments: TurnAttachment[] = []): Turn => ({
  key: nextKey(),
  role: 'user',
  content,
  attachments,
  cards: [],
  tools: [],
  status: 'done',
})

export const pendingAssistant = (): Turn => ({
  key: nextKey(),
  role: 'assistant',
  content: '',
  cards: [],
  tools: [],
  status: 'streaming',
  startedAt: Date.now(),
})

export const fromMessage = (m: ChatMessage): Turn => ({
  key: m.id || nextKey(),
  role: m.role === 'user' ? 'user' : 'assistant',
  content: m.content ?? '',
  attachments: safeAttachmentMetadata(m.attachments),
  cards: (m.cards ?? []).filter(isCard),
  tools: [],
  status: 'done',
  ...(m.role === 'assistant' && m.id ? { persistedMessageId: m.id } : {}),
})

const CARD_TYPES = new Set(['memory', 'trade_proposal', 'price_outlook', 'sources', 'forecast', 'followups', 'forecast_draft', 'build_action', 'protection', 'price_alert'])

/**
 * Only the card types the contract defines, with a data object (sources: items, followups: prompts, forecast_draft: a
 * draft, build_action: a known kind and a prompt); anything else is ignored.
 */
export function isCard(c: unknown): c is Card {
  const card = c as Card | null
  if (!card || !CARD_TYPES.has(card.type) || typeof card.data !== 'object' || !card.data) return false
  switch (card.type) {
    case 'memory':
      return typeof card.data.proposalId === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(card.data.proposalId) && typeof card.data.text === 'string' && !!card.data.text.trim() && [...card.data.text].length <= 200 && ['open', 'confirmed', 'deleted'].includes(card.data.status) && (card.data.memoryId === undefined || typeof card.data.memoryId === 'string')
    case 'followups':
      return Array.isArray(card.data.prompts)
    case 'sources':
      return Array.isArray(card.data.items)
    case 'forecast_draft':
      return typeof card.data.draft?.question === 'string'
    case 'build_action':
      return (card.data.kind === 'portfolio' || card.data.kind === 'strategy') && typeof card.data.prompt === 'string'
  }
  return true
}

export const clip = (s: string, max: number) => (s.length > max ? `${s.slice(0, max - 1).trimEnd()}…` : s)

/** Keep the server label; known read tools retain human copy if a label is missing. */
export function toolLabel(name: string, label: string | undefined, input: unknown): string {
  if (label && label !== name) return label
  const i = (input && typeof input === 'object' ? input : {}) as Record<string, unknown>
  const q = typeof i.query === 'string' ? i.query.trim() : ''
  const about = q ? `: “${clip(q, 48)}”` : ''
  switch (name) {
    case 'remember':
      return 'Suggesting a memory'
    case 'get_trades':
      return 'Reading your paper trades'
    case 'get_pnl_attribution':
      return 'Checking your P&L attribution'
    case 'get_automations':
      return 'Reviewing your automations'
    case 'get_autopilots':
      return 'Checking your autopilots'
    case 'get_ai_portfolios':
      return 'Reviewing your AI portfolios'
    case 'search_news':
      return `Searching the news${about}`
    case 'web_search':
      return `Searching the web${about}`
    case 'read_page': {
      const host = typeof i.url === 'string' ? domainOf(i.url) : ''
      return host ? `Reading ${host}` : 'Reading a page'
    }
    case 'get_market_wire':
      return typeof i.symbol === 'string' && i.symbol ? `Checking the market wire for ${i.symbol.toUpperCase()}` : 'Checking the market wire'
    case 'get_stock_quote': {
      const symbol = typeof i.symbol === 'string' ? clip(i.symbol.trim().toUpperCase(), 24) : ''
      return symbol ? `Looking up ${symbol} quote` : 'Looking up a stock quote'
    }
  }
  return name
}

/** Folds one stream event into the assistant turn being streamed. `chat` is routing, handled by the page. */
export function applyEvent(t: Turn, ev: ChatStreamEvent): Turn {
  switch (ev.event) {
    case 'tool_call':
      if (ev.data.name === 'suggest_followups') return t // its result is the chips under the answer
      return { ...t, tools: [...t.tools, { id: ev.data.id, label: toolLabel(ev.data.name, ev.data.label, ev.data.input), at: t.content.length }] }
    case 'tool_result':
      return {
        ...t,
        tools: t.tools.map((s) => (s.id === ev.data.id ? { ...s, ok: ev.data.ok !== false, summary: ev.data.summary } : s)),
      }
    case 'delta':
      return { ...t, content: t.content + (ev.data.text ?? '') }
    case 'card':
      return isCard(ev.data) ? { ...t, cards: [...t.cards, ev.data], cardsAt: [...(t.cardsAt ?? t.cards.map(() => 0)), t.content.length] } : t
    case 'done':
      return { ...t, status: 'done', elapsedMs: t.startedAt == null ? undefined : Date.now() - t.startedAt, ...(t.role === 'assistant' && typeof ev.data.messageId === 'string' && ev.data.messageId.trim() ? { persistedMessageId: ev.data.messageId } : {}) }
    case 'error':
      return { ...t, status: 'error', error: ev.data }
    default:
      return t
  }
}

/** User-facing copy for a stream / API error code. `retry` = worth offering a Retry button. */
export function errorCopy(code: string, message: string): { title: string; detail: string; retry: boolean } {
  switch (code) {
    case 'reattach':
      return { title: 'Attach the files again', detail: 'Saved chats keep file names, not the files themselves. Attach them again to ask again.', retry: false }
    case 'ai_quota':
      return {
        title: 'Daily AI credits used up',
        detail: "Credits reset at 00:00 UTC. Charts, backtests and paper trading don't use credits.",
        retry: false,
      }
    case 'ai_off':
      return { title: "AI isn't set up yet", detail: message, retry: false }
    case 'unreadable_file':
      return { title: "This file can't be read here", detail: message, retry: false }
    case 'rate_limited':
      return { title: 'Too many requests', detail: 'Give it a few seconds, then retry.', retry: true }
    case 'upstream':
      return { title: "The desk couldn't reach its data", detail: message || 'Market data or the model is unavailable right now.', retry: true }
    case 'network':
      return { title: 'Connection lost', detail: message, retry: true }
    case 'unfinished': // a reopened chat whose answer never arrived
      return { title: "This answer didn't finish. Ask again.", detail: '', retry: true }
    default:
      return { title: 'Something went wrong', detail: message || 'Please try again.', retry: true }
  }
}

/** "$2,000" / "2000.50" → 2000.5; anything that isn't a plain positive amount → NaN. */
export function parseAmount(s: string): number {
  const clean = s.replace(/[$,\s]/g, '')
  return /^\d+(\.\d*)?$|^\.\d+$/.test(clean) ? Number(clean) : NaN
}
