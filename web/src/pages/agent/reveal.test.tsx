import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { charBoundary, createPacer, PACE, settle, useReveal, useRevealedTurn } from './reveal'
import { applyEvent, pendingAssistant, type Turn } from './turns'
import type { ChatStreamEvent } from '../../lib/types'

const FRAME = 1000 / 60
const TICK = 16 // the fake requestAnimationFrame's frame

/** Ticks `pacer` at 60 fps from `from` to `to` ms, collecting [time, shown] after each frame. */
function run(pacer: ReturnType<typeof createPacer>, from: number, to: number, onFrame?: (t: number) => void) {
  const out: [number, number][] = []
  for (let t = from + FRAME; t <= to + 1e-9; t += FRAME) {
    onFrame?.(t)
    out.push([t, pacer.tick(t)])
  }
  return out
}

describe('createPacer', () => {
  it('reveals monotonically, never past what arrived, and never stalls while behind', () => {
    const p = createPacer()
    const bursts = [[0, 300], [400, 20], [450, 25], [1200, 600], [1250, 5]] // [ms, characters], between frames
    let target = 0
    let prev = 0
    for (let t = FRAME; t <= 3000; t += FRAME) {
      const behind = prev < target // as of the last frame
      for (const [at, n] of bursts) if (at <= t && at > t - FRAME) p.receive((target += n), at)
      const shown = p.tick(t)
      expect(shown).toBeGreaterThanOrEqual(prev)
      expect(shown).toBeLessThanOrEqual(target)
      if (behind) expect(shown - prev).toBeGreaterThanOrEqual(Math.min((PACE.minRate * FRAME) / 1000, target - prev) - 1e-9) // no stall
      prev = shown
    }
    expect(prev).toBe(target)
  })

  it('is fast when far behind and gentle when nearly caught up', () => {
    const far = createPacer()
    far.receive(2000, 0)
    expect(far.tick(FRAME)).toBeGreaterThan(100) // a big burst starts coming out at once
    const near = createPacer()
    near.receive(10, 0)
    expect(near.tick(FRAME)).toBeLessThan(2) // a few characters trickle out
    const frames = run(near, FRAME, 1000)
    const done = frames.find(([, s]) => s >= 10)![0]
    expect(done).toBeGreaterThan(150) // ~40 characters a second, not a flash
    expect(done).toBeLessThanOrEqual(PACE.maxLagMs + FRAME)
  })

  it('shows every piece within maxLagMs of its arrival, however large the burst', () => {
    for (const size of [1, 50, 600, 20_000]) {
      const p = createPacer()
      p.receive(size, 0)
      const frames = run(p, 0, 2000)
      const out = frames.find(([, s]) => s >= size)![0]
      expect(out, `${size} characters`).toBeLessThanOrEqual(PACE.maxLagMs + FRAME)
    }
  })

  it('keeps a scripted model stream within the lag bound, in smaller steps than its bursts', () => {
    // A scripted stream shaped like a prod answer: the API's held first sentence after a tool round (one 180-character
    // delta), irregular 8–60 character chunks, a 1.5 s tool pause, then another held sentence and chunks.
    let seed = 7
    const rand = () => ((seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31)
    const arrivals: [number, number][] = [[0, 180]] // [time, characters]
    let t = 0
    for (let i = 0; i < 40; i++) arrivals.push([(t += 30 + rand() * 120), 8 + Math.floor(rand() * 52)])
    arrivals.push([(t += 1500), 220])
    for (let i = 0; i < 40; i++) arrivals.push([(t += 30 + rand() * 120), 8 + Math.floor(rand() * 52)])

    const p = createPacer()
    const marks: [number, number][] = [] // [arrival time, received length]
    let received = 0
    let next = 0
    let maxStep = 0
    let prev = 0
    for (const [time, shown] of run(p, -FRAME, t + 2000, (time) => {
      while (next < arrivals.length && arrivals[next][0] <= time) {
        received += arrivals[next][1]
        marks.push([arrivals[next][0], received])
        p.receive(received, time)
        next++
      }
    })) {
      for (const [at, len] of marks) if (time - at > PACE.maxLagMs + FRAME) expect(shown).toBeGreaterThanOrEqual(len)
      maxStep = Math.max(maxStep, shown - prev)
      prev = shown
    }
    expect(prev).toBe(received)
    expect(maxStep).toBeLessThan(180 / 2) // the largest burst comes out over several frames
  })

  it('flushes what is left within flushMs once the stream ends', () => {
    const p = createPacer()
    p.receive(5000, 0)
    p.tick(FRAME)
    p.finish(FRAME)
    const frames = run(p, FRAME, 1000)
    expect(frames.find(([, s]) => s >= 5000)![0]).toBeLessThanOrEqual(FRAME + PACE.flushMs + FRAME)
    const late = createPacer()
    late.finish(0)
    late.receive(40, 0) // text after the end comes out on the flush deadline too
    expect(run(late, 0, 400).find(([, s]) => s >= 40)![0]).toBeLessThanOrEqual(PACE.flushMs + FRAME)
  })

  it('starts from text already there and resumes from now after idling', () => {
    const p = createPacer(12, 0)
    expect([p.idle, p.tick(5000)]).toEqual([true, 12])
    p.receive(112, 10_000) // ten seconds idle must not count as one huge frame
    expect(p.tick(10_000 + FRAME)).toBeLessThan(20)
    p.skip()
    expect([p.idle, p.tick(10_100)]).toEqual([true, 112])
  })
})

describe('charBoundary', () => {
  it('never splits a character, an emoji sequence or a combining mark', () => {
    const text = 'BTC 👍🏽 up · 比特币 𠀋 é 👨‍👩‍👧 end'
    for (let i = 0; i <= text.length; i++) {
      const cut = charBoundary(text, i)
      expect(cut).toBeGreaterThanOrEqual(i)
      const segments = [...new Intl.Segmenter(undefined, { granularity: 'grapheme' }).segment(text)].map((s) => s.index)
      expect(cut === text.length || segments.includes(cut), `cut ${cut} for ${i}`).toBe(true)
    }
    expect(charBoundary('比特币', 1)).toBe(1) // CJK: one character at a time
    expect(charBoundary('a😀b', 2)).toBe(3)
    expect([charBoundary('abc', -1), charBoundary('abc', 9)]).toEqual([0, 3])
  })
})

describe('settle', () => {
  it('waits for a table until its delimiter row is in', () => {
    expect(settle('Intro\n\n| Asset | 24h |')).toBe('Intro\n\n')
    expect(settle('Intro\n\n| Asset | 24h |\n')).toBe('Intro\n\n')
    expect(settle('Intro\n\n| Asset | 24h |\n|---|--')).toBe('Intro\n\n')
    expect(settle('| Asset | 24h |\n|---|---|')).toBe('')
    expect(settle('Intro\n\n| Asset | 24h |\n|---|---|\n')).toBe('Intro\n\n| Asset | 24h |\n|---|---|\n')
    expect(settle('| A | B |\n| :--- | ---: |\n| BTC | +2')).toBe('| A | B |\n| :--- | ---: |\n| BTC | +2')
    expect(settle('| A |\n|---|\n| x |\n\nAfter')).toBe('| A |\n|---|\n| x |\n\nAfter')
  })

  it('closes an open ** on the last line, or drops it while nothing follows', () => {
    expect(settle('BTC is in an **upt')).toBe('BTC is in an **upt**')
    expect(settle('BTC is in an **up ')).toBe('BTC is in an **up** ')
    expect(settle('BTC is in an **')).toBe('BTC is in an ')
    expect(settle('BTC is in an *')).toBe('BTC is in an ')
    expect(settle('**View:** bull')).toBe('**View:** bull')
    expect(settle('**View*')).toBe('**View**')
    expect(settle('- **BTC** up\n- **ETH')).toBe('- **BTC** up\n- **ETH**')
    expect(settle('Run `a **b')).toBe('Run `a **b') // inline code keeps its asterisks
    expect(settle('2 * 3 = 6')).toBe('2 * 3 = 6')
  })
})

describe('useReveal', () => {
  function Probe({ text, live }: { text: string; live: boolean }) {
    return <output>{text.slice(0, useReveal(text, live))}</output>
  }
  const shown = () => screen.getByRole('status').textContent ?? ''
  const frames = (ms: number) => act(() => vi.advanceTimersByTime(ms))
  const long = 'BTC holds its 4h uptrend above the 20 EMA while ETF inflows continue for a third day. '.repeat(6)

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'performance', 'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows a stored or finished message at once', () => {
    const { rerender } = render(<Probe text={long} live={false} />)
    expect(shown()).toBe(long)
    rerender(<Probe text={long + 'More.'} live={false} />)
    expect(shown()).toBe(long + 'More.')
  })

  it('shows what is there when a live turn mounts, then paces what follows to the exact text', () => {
    const { rerender } = render(<Probe text="Already here. " live />)
    expect(shown()).toBe('Already here. ')
    rerender(<Probe text={'Already here. ' + long} live />)
    expect(shown()).toBe('Already here. ') // nothing jumps in with the delta
    const seen: string[] = []
    for (let i = 0; i < 80; i++) {
      frames(TICK)
      seen.push(shown())
    }
    expect(seen.at(-1)).toBe('Already here. ' + long)
    seen.reduce((a, b) => {
      expect(b.startsWith(a)).toBe(true) // monotonic: each render extends the last
      return b
    })
    expect(new Set(seen).size).toBeGreaterThan(10) // revealed in many small steps
    expect(new Set(seen).size).toBeLessThanOrEqual(Math.ceil((80 * TICK) / PACE.frameMs) + 1) // ≤ ~30 renders a second
  })

  it('catches up within about a second of the text arriving', () => {
    const { rerender } = render(<Probe text="" live />)
    rerender(<Probe text={long.repeat(20)} live />)
    frames(PACE.maxLagMs + 2 * TICK)
    expect(shown()).toBe(long.repeat(20))
  })

  it('flushes the rest within ~300 ms on done, an error or Stop, ending at exactly the received text', () => {
    const { rerender } = render(<Probe text="" live />)
    rerender(<Probe text={long} live />)
    frames(2 * TICK)
    expect(shown().length).toBeLessThan(long.length)
    rerender(<Probe text={long + 'Last.'} live={false} />)
    frames(300)
    expect(shown()).toBe(long + 'Last.')
  })

  it('shows text at once under prefers-reduced-motion', () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q === '(prefers-reduced-motion: reduce)', media: q }))
    const { rerender } = render(<Probe text="" live />)
    rerender(<Probe text={long} live />)
    expect(shown()).toBe(long)
  })

  it('shows text at once in a hidden tab, and the rest when a tab is hidden mid-reveal', () => {
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    const { rerender } = render(<Probe text="" live />)
    rerender(<Probe text={long} live />)
    expect(shown()).toBe(long)
    hidden.mockReturnValue(false)
    rerender(<Probe text={long + long} live />)
    frames(2 * TICK)
    expect(shown().length).toBeLessThan(2 * long.length)
    hidden.mockReturnValue(true)
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    expect(shown()).toBe(long + long)
  })
})

describe('useRevealedTurn', () => {
  function View({ turn }: { turn: Turn }) {
    const t = useRevealedTurn(turn)
    return (
      <output>
        {[t.status, t.tools.map((s) => s.id).join(','), t.cards.map((c) => c.type).join(','), t.content].join('|')}
      </output>
    )
  }
  const shown = () => (screen.getByRole('status').textContent ?? '').split('|')

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame', 'performance', 'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps tool chips and cards after the text they followed, and done after all of it', () => {
    const first = 'BTC holds its uptrend above the 20 EMA, with ETF inflows for a third day [1]. '
    const events: ChatStreamEvent[] = [
      { event: 'delta', data: { text: first } },
      { event: 'tool_call', data: { id: 'a', name: 'get_asset_analysis', label: 'Analyzing BTC', input: {} } },
      { event: 'tool_result', data: { id: 'a', name: 'get_asset_analysis', ok: true, summary: 'ok' } },
      { event: 'card', data: { type: 'followups', data: { prompts: ['Next?'] } } },
      { event: 'delta', data: { text: '**View:** bullish.' } },
      { event: 'card', data: { type: 'sources', data: { items: [] } } },
      { event: 'done', data: { messageId: 'm1', credits: { used: 2, limit: 500, resetsAt: '' } } },
    ]
    const live = pendingAssistant()
    const { rerender } = render(<View turn={live} />)
    const final = events.reduce(applyEvent, live) // one chunk: every event in a single render
    rerender(<View turn={final} />)
    expect(shown()).toEqual(['streaming', '', '', ''])
    const seen: string[][] = []
    for (let i = 0; i < 40; i++) {
      act(() => vi.advanceTimersByTime(TICK))
      seen.push(shown())
    }
    for (const [status, tools, cards, text] of seen) {
      if (tools) expect(text.length).toBeGreaterThanOrEqual(first.length)
      if (cards.includes('followups')) expect(tools).toBe('a')
      if (cards.includes('sources') || status === 'done') expect(text).toBe(first + '**View:** bullish.')
      if (text.length < first.length) expect([tools, cards]).toEqual(['', ''])
    }
    expect(seen.at(-1)).toEqual(['done', 'a', 'followups,sources', first + '**View:** bullish.'])
    expect(seen.some(([, tools, , text]) => tools === 'a' && text.length < (first + '**View:** bullish.').length)).toBe(true)
  })

  it('returns a finished or stored turn as is', () => {
    const stored: Turn = { ...pendingAssistant(), status: 'done', content: 'Here is the **outlook**.' }
    const { rerender } = render(<View turn={stored} />)
    expect(shown()).toEqual(['done', '', '', 'Here is the **outlook**.'])
    rerender(<View turn={{ ...stored, status: 'error', content: 'Partial **ans' }} />)
    expect(shown()).toEqual(['error', '', '', 'Partial **ans']) // exact text, never settled once over
  })
})
