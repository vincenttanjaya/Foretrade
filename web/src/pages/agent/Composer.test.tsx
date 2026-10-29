import { act, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderPage, stubSession } from '../../test/utils'
import { Composer } from './Composer'

type Handler<T> = ((e: T) => void) | null

/** A stand-in for the browser's SpeechRecognition: tests push results and errors by hand. */
class FakeRecognition {
  static last: FakeRecognition
  continuous = true
  interimResults = false
  lang = ''
  onresult: Handler<{ results: unknown[] }> = null
  onerror: Handler<{ error: string }> = null
  onend: (() => void) | null = null
  started = false
  aborted = false
  constructor() {
    FakeRecognition.last = this
  }
  start() {
    this.started = true
  }
  stop() {
    this.onend?.()
  }
  abort() {
    this.aborted = true
    this.onend?.()
  }
  /** Each part is [transcript, isFinal], in order. */
  say(...parts: [string, boolean][]) {
    act(() => this.onresult?.({ results: parts.map(([t, isFinal]) => Object.assign([{ transcript: t }], { isFinal })) }))
  }
}

beforeEach(() => stubSession())
afterEach(() => vi.unstubAllGlobals())

const open = () => {
  const onSend = vi.fn()
  const r = renderPage(<Composer onSend={onSend} onStop={() => {}} streaming={false} mode="quality" onModeChange={() => {}} lens="auto" onLensChange={() => {}} />)
  return { onSend, box: screen.getByRole('textbox', { name: 'Message the agent' }) as HTMLTextAreaElement, ...r }
}

describe('Composer dictation', () => {
  it('shows no mic where the browser has no speech recognition', () => {
    vi.stubGlobal('SpeechRecognition', undefined)
    vi.stubGlobal('webkitSpeechRecognition', undefined)
    open()
    expect(screen.queryByRole('button', { name: 'Dictate' })).not.toBeInTheDocument()
  })

  it('appends what was said after the typed text, previews interim words and never sends', async () => {
    vi.stubGlobal('webkitSpeechRecognition', FakeRecognition)
    const { user, onSend, box } = open()
    await user.type(box, 'Please')
    await user.click(screen.getByRole('button', { name: 'Dictate' }))
    const rec = FakeRecognition.last
    expect([rec.started, rec.continuous, rec.interimResults, rec.lang]).toEqual([true, false, true, navigator.language || 'en-US'])
    expect(screen.getByRole('button', { name: 'Stop dictation' })).toHaveAttribute('aria-pressed', 'true')

    rec.say(['buy $500', false])
    expect(box.value).toBe('Please buy $500')
    rec.say(['buy $500 of SOL', true])
    act(() => rec.onend?.()) // the browser ends a single-utterance session itself
    expect(box.value).toBe('Please buy $500 of SOL')
    expect(screen.getByRole('button', { name: 'Dictate' })).toHaveAttribute('aria-pressed', 'false')
    expect(onSend).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Send message' }))
    expect(onSend).toHaveBeenCalledWith('Please buy $500 of SOL')
  })

  it('stops listening on send, and a late result cannot refill the box', async () => {
    vi.stubGlobal('webkitSpeechRecognition', FakeRecognition)
    const { user, onSend, box } = open()
    await user.click(screen.getByRole('button', { name: 'Dictate' }))
    const rec = FakeRecognition.last
    rec.say(['what moved crypto today', false])
    await user.click(screen.getByRole('button', { name: 'Send message' }))
    expect(onSend).toHaveBeenCalledWith('what moved crypto today')
    expect(rec.aborted).toBe(true)
    rec.say(['what moved crypto today', true])
    expect(box.value).toBe('')
    expect(screen.getByRole('button', { name: 'Dictate' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('stops on Escape and toasts only a blocked microphone', async () => {
    vi.stubGlobal('webkitSpeechRecognition', FakeRecognition)
    const { user, box } = open()
    await user.click(screen.getByRole('button', { name: 'Dictate' }))
    FakeRecognition.last.say(['hello', true])
    await user.type(box, '{Escape}')
    expect(box.value).toBe('hello')
    expect(screen.getByRole('button', { name: 'Dictate' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Dictate' }))
    act(() => FakeRecognition.last.onerror?.({ error: 'no-speech' }))
    expect(screen.queryByText('Microphone access is blocked for this site.')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Dictate' }))
    act(() => FakeRecognition.last.onerror?.({ error: 'not-allowed' }))
    expect(screen.getByText('Microphone access is blocked for this site.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Dictate' })).toBeInTheDocument()
  })
})

describe('Composer answer lens', () => {
  it('sits next to the answer mode, reports the chosen lens and locks while an answer streams', async () => {
    const onLensChange = vi.fn()
    const r = renderPage(<Composer onSend={() => {}} onStop={() => {}} streaming={false} mode="quality" onModeChange={() => {}} lens="investor" onLensChange={onLensChange} />)
    const [mode, lens] = screen.getAllByRole('combobox')
    expect(mode).toHaveAccessibleName('Answer mode')
    expect(lens).toHaveAccessibleName('Answer lens')
    expect(lens).toHaveValue('investor')
    expect(lens).toHaveAttribute('title', 'Long-term view: thesis, valuation and scenario odds over years')
    await r.user.selectOptions(lens, 'trader')
    expect(onLensChange).toHaveBeenCalledExactlyOnceWith('trader')
    r.unmount()
    renderPage(<Composer onSend={() => {}} onStop={() => {}} streaming mode="quality" onModeChange={() => {}} lens="auto" onLensChange={onLensChange} />)
    expect(screen.getByRole('combobox', { name: 'Answer lens' })).toBeDisabled()
    expect(screen.getByRole('combobox', { name: 'Answer lens' })).toHaveAccessibleDescription('Reads each question as an investor, a trader or both')
  })
})
