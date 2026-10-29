// Paced reveal of a live answer. Text reaches the page in bursts (network chunks, and the API sends a turn it held
// after a tool round as one delta), so the reader sees it through a pacer: a requestAnimationFrame loop that shows
// characters at a rate set by the backlog, renders at most ~30 times a second, and never lets received text wait
// longer than PACE.maxLagMs. Only text that arrives after a turn mounts is paced: stored and reopened messages show
// at once, and so does everything under prefers-reduced-motion or in a hidden tab.
import { useEffect, useMemo, useRef, useState } from 'react'
import type { Turn } from './turns'

export const PACE = {
  minRate: 40, // characters/s once nearly caught up: the gentle end of the reveal
  tauMs: 300, // the backlog shrinks with this time constant, so a large burst comes out fast
  maxLagMs: 1000, // received text is on screen at most this long after it arrives
  flushMs: 250, // after done, an error or Stop, the rest is on screen within this long
  frameMs: 32, // at most one render per this long (~30 fps): each render re-parses the answer's markdown
}

export type Pacer = {
  /** The received text is now `length` long (it only grows while the stream is live). */
  receive(length: number, now: number): void
  /** The stream ended: what is left comes out within PACE.flushMs. */
  finish(now: number): void
  /** Shows everything received at once. */
  skip(): void
  /** Advances to `now` (ms) and returns how much to show, in UTF-16 units (not yet snapped to a character). */
  tick(now: number): number
  /** Everything received is shown. */
  readonly idle: boolean
}

/** The reveal's clock-driven core, without React or the DOM; `start` is shown from the outset. */
export function createPacer(start = 0, now = 0): Pacer {
  let shown = start
  let target = start
  let last = now
  let ended = false
  let due: { end: number; at: number }[] = [] // `end` must be shown by `at`, in arrival order
  return {
    receive(length, t) {
      if (length < target) {
        target = length
        shown = Math.min(shown, length)
        due = due.filter((d) => d.end <= length)
      }
      if (length <= target) return
      if (shown >= target) last = t // a reveal that was caught up starts from now, not from its last frame
      target = length
      due.push({ end: length, at: t + (ended ? PACE.flushMs : PACE.maxLagMs) })
    },
    finish(t) {
      if (ended) return
      ended = true
      if (shown < target) due.push({ end: target, at: t + PACE.flushMs })
    },
    skip() {
      shown = target
      due = []
    },
    tick(t) {
      const dt = Math.max(0, t - last)
      last = t
      if (shown >= target) return shown
      const backlog = target - shown
      // Proportional to the backlog (fast when far behind), never slower than minRate (no crawl at the end) …
      let step = Math.max((PACE.minRate * dt) / 1000, backlog * (1 - Math.exp(-dt / PACE.tauMs)))
      // … and fast enough that each piece of text is out by its deadline: the gap closes linearly from the last frame.
      for (const d of due) {
        const gap = d.end - shown
        if (gap > 0) step = Math.max(step, d.at <= t ? gap : (gap * dt) / (d.at - t + dt))
      }
      shown = Math.min(target, shown + step)
      due = due.filter((d) => d.end > shown)
      return shown
    },
    get idle() {
      return shown >= target
    },
  }
}

const graphemes = typeof Intl !== 'undefined' && typeof Intl.Segmenter === 'function' ? new Intl.Segmenter(undefined, { granularity: 'grapheme' }) : undefined

/**
 * The first character boundary at or after `i`, so a cut never splits a surrogate pair (emoji, rare CJK), an emoji
 * sequence or a letter from its combining marks. CJK text has no spaces to wait for, so the unit is the character.
 */
export function charBoundary(text: string, i: number): number {
  if (i <= 0) return 0
  if (i >= text.length) return text.length
  if (graphemes) {
    const from = Math.max(0, i - 24)
    for (const { index, segment } of graphemes.segment(text.slice(from, i + 24))) {
      const end = from + index + segment.length
      if (end >= i) return Math.min(end, text.length)
    }
  }
  const c = text.charCodeAt(i)
  return c >= 0xdc00 && c <= 0xdfff ? i + 1 : i
}

/** Motion is off (prefers-reduced-motion) or nobody can see it (hidden tab): show text as it arrives. */
function instant(): boolean {
  if (typeof document !== 'undefined' && document.hidden) return true
  try {
    return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
  } catch {
    return false
  }
}

const now = () => performance.now()

/**
 * How much of `text` to show. Text present when the component mounts shows at once; when it mounts `live`, later
 * text is paced, and once `live` turns false the rest comes out within PACE.flushMs. The result only grows (on a
 * character boundary) and ends at exactly text.length.
 */
export function useReveal(text: string, live: boolean): number {
  const [paced] = useState(live) // a turn that is already over when it mounts is never paced
  const [shown, setShown] = useState(text.length)
  const box = useRef<{ pacer: Pacer; text: string; drawn: number; drewAt: number; frame: number } | null>(null)

  useEffect(() => {
    if (!paced) return
    const t = now()
    const s = (box.current ??= { pacer: createPacer(text.length, t), text, drawn: text.length, drewAt: -Infinity, frame: 0 })
    s.text = text
    s.pacer.receive(text.length, t)
    if (!live) s.pacer.finish(t)
    const draw = (n: number) => {
      if (n === s.drawn) return
      s.drawn = n
      setShown(n)
    }
    const showAll = () => {
      s.pacer.skip()
      draw(s.text.length)
    }
    const frame = () => {
      s.frame = 0
      const t = now()
      if (instant()) return showAll()
      if (t - s.drewAt >= PACE.frameMs) {
        s.drewAt = t
        draw(Math.max(s.drawn, charBoundary(s.text, Math.floor(s.pacer.tick(t)))))
      }
      if (s.drawn < s.text.length) s.frame = requestAnimationFrame(frame)
    }
    if (instant()) showAll()
    else if (s.drawn < text.length) s.frame = requestAnimationFrame(frame)
    return () => {
      cancelAnimationFrame(s.frame)
      s.frame = 0
    }
  }, [paced, text, live])

  // A tab hidden mid-reveal gets no frames: show the rest now, so nothing animates when the reader comes back.
  useEffect(() => {
    if (!paced) return
    const onVisibility = () => {
      const s = box.current
      if (!document.hidden || !s) return
      s.pacer.skip()
      s.drawn = s.text.length
      setShown(s.drawn)
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => document.removeEventListener('visibilitychange', onVisibility)
  }, [paced])

  return paced ? Math.min(shown, text.length) : text.length
}

/**
 * The turn as the reader sees it: its revealed text (settled while live), the tool chips and cards that arrived by
 * then (each records the text length it arrived at), and still `streaming` until all its text is out, so done, an
 * error or Stop land after the text they followed. The same object once it is over and everything is shown.
 */
export function useRevealedTurn(turn: Turn): Turn {
  const n = useReveal(turn.content, turn.role === 'assistant' && turn.status === 'streaming')
  const tools = countUpTo(turn.tools.map((s) => s.at), n)
  const cards = countUpTo(turn.cardsAt ?? [], n, turn.cards.length)
  // Stable arrays, so the memoized markdown re-renders only when what it shows changes.
  const toolList = useMemo(() => (tools < turn.tools.length ? turn.tools.slice(0, tools) : turn.tools), [turn.tools, tools])
  const cardList = useMemo(() => (cards < turn.cards.length ? turn.cards.slice(0, cards) : turn.cards), [turn.cards, cards])
  const behind = n < turn.content.length
  if (!behind && turn.status !== 'streaming') return turn
  return { ...turn, content: settle(turn.content.slice(0, n)), tools: toolList, cards: cardList, status: behind ? 'streaming' : turn.status }
}

/** How many leading items arrived at or before text position n (`at` missing: from the start). */
function countUpTo(at: (number | undefined)[], n: number, length = at.length): number {
  let k = 0
  while (k < length && (at[k] ?? 0) <= n) k++
  return k
}

const PIPE_LINE = /^ {0,3}\|/
const DELIMITER_ROW = /^ {0,3}\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/

/**
 * Revealed text cut mid-markdown, made to render as it will once complete: a table waits until its delimiter row is
 * in (so its header never flashes as raw pipes), and an open `**` on the last line is closed (bold grows instead of
 * showing asterisks), or dropped when nothing follows it yet. Display only: the final text is never settled.
 */
export function settle(text: string): string {
  const lines = text.split('\n')
  const end = lines.at(-1) === '' ? lines.length - 2 : lines.length - 1
  let first = end
  while (first >= 0 && PIPE_LINE.test(lines[first])) first--
  first++
  if (first <= end && !(first + 1 < lines.length - 1 && DELIMITER_ROW.test(lines[first + 1]))) {
    return lines.slice(0, first).join('\n') + (first > 0 ? '\n' : '')
  }

  const start = text.lastIndexOf('\n') + 1
  let line = text.slice(start)
  if (line.includes('`')) return text // inline code: asterisks there are literal
  if (/(^|[^*])\*$/.test(line)) line = line.slice(0, -1) // a lone trailing * may be the first half of **
  if ((line.split('**').length - 1) % 2) {
    const open = line.lastIndexOf('**')
    if (!line.slice(open + 2).trim()) line = line.slice(0, open)
    else {
      const space = line.length - line.trimEnd().length
      line = `${line.slice(0, line.length - space)}**${line.slice(line.length - space)}`
    }
  }
  return text.slice(0, start) + line
}
