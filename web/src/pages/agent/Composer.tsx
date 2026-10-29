import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { Link } from 'react-router'
import { useToast } from '../../components/toast'
import { Select, Textarea } from '../../components/ui/Input'
import { cx } from '../../lib/cx'
import { IconArrowUp, IconPaperclip, IconStop } from '../../components/ui/icons'
import { AttachmentChips } from '../../components/attachments/AttachmentChips'
import { useAttachmentQueue } from '../../components/attachments/useAttachmentQueue'
import type { ChatAttachmentInput, ChatLens, ChatMode } from '../../lib/types'
import { ACCEPT, MAX_ATTACHMENTS, messageCost, pastesText } from './attachments'
import { runeLength } from './images'

// The browser's speech-to-text (Chrome, Edge, Safari); lib.dom has no types for it.
type Recognition = {
  continuous: boolean
  interimResults: boolean
  lang: string
  onresult: ((e: { results: ArrayLike<ArrayLike<{ transcript: string }> & { isFinal: boolean }> }) => void) | null
  onerror: ((e: { error: string }) => void) | null
  onend: (() => void) | null
  start(): void
  stop(): void
  abort(): void
}
type SpeechWindow = { SpeechRecognition?: new () => Recognition; webkitSpeechRecognition?: new () => Recognition }
const joinWords = (...parts: string[]) => parts.map((p) => p.trim()).filter(Boolean).join(' ')

const MAX = 4000 // contract: message 1..4000 runes

// Only grab focus where it won't pop a phone keyboard over the page.
const finePointer = typeof matchMedia === 'function' && matchMedia('(pointer: fine)').matches

const kbd = 'rounded border border-border bg-surface-2 px-1 py-px font-mono text-[10px] text-text'
const MODE_DESCRIPTION = {
  fast: 'Clear, concise answers with fewer steps',
  quality: 'Thorough explanations and structured insights',
}
const LENS_DESCRIPTION = {
  auto: 'Reads each question as an investor, a trader or both',
  investor: 'Long-term view: thesis, valuation and scenario odds over years',
  trader: 'Setups: entry, stop, target and reward:risk over hours to weeks',
}
const shortcut = 'inline-flex min-h-8 items-center rounded-full border border-border px-2.5 text-[12px] font-medium whitespace-nowrap text-muted transition-colors hover:bg-surface-2 hover:text-text'
const HELP = `Up to ${MAX_ATTACHMENTS} files: photos (resized to fit 1.5 MB) and PDF, Word, Excel, CSV, text, Markdown or HTML up to 4.5 MB each. 3 credits per message, +1 for each document after the first.`

/** Message box: Enter sends, Shift+Enter adds a line, Escape stops dictation or a running answer. */
export function Composer({
  onSend,
  onStop,
  streaming,
  mode,
  onModeChange,
  lens,
  onLensChange,
  disabled,
  autoFocus,
  className,
}: {
  onSend: (text: string, attachments?: ChatAttachmentInput[]) => void
  onStop: () => void
  streaming: boolean
  mode: ChatMode
  onModeChange: (mode: ChatMode) => void
  lens: ChatLens
  onLensChange: (lens: ChatLens) => void
  disabled?: boolean
  autoFocus?: boolean
  className?: string
}) {
  const id = useId()
  const box = useRef<HTMLTextAreaElement>(null)
  const [text, setText] = useState('')
  const [listening, setListening] = useState(false)
  const [dragging, setDragging] = useState(false)
  const queue = useAttachmentQueue({ max: MAX_ATTACHMENTS, disabled: streaming || disabled, per: 'per message' })
  const { files, errors: fileErrors } = queue
  const rec = useRef<Recognition | null>(null)
  const { toast } = useToast()
  const Speech = (window as unknown as SpeechWindow).SpeechRecognition ?? (window as unknown as SpeechWindow).webkitSpeechRecognition
  const length = runeLength(text)
  const tooLong = length > MAX
  const processing = queue.processing
  const canSend = (text.trim() !== '' || files.length > 0) && !processing && !tooLong && !streaming && !disabled
  const cost = messageCost(files)

  const attach = queue.attach
  const removeFile = (key: number) => {
    queue.remove(key)
    box.current?.focus()
  }

  // On mount and again when it re-enables (a stored chat finished loading).
  useEffect(() => {
    if (autoFocus && finePointer && !disabled) box.current?.focus()
  }, [autoFocus, disabled])

  // Dictation fills the box (interim words as a preview) and never sends: the user still taps Send.
  const dictate = () => {
    if (rec.current) return rec.current.stop() // its onend lands the final words
    if (!Speech) return
    const r = new Speech()
    const base = text
    let final = ''
    r.continuous = false
    r.interimResults = true
    r.lang = navigator.language || 'en-US'
    r.onresult = (e) => {
      let interim = ''
      final = ''
      for (const res of Array.from(e.results)) {
        if (res.isFinal) final = joinWords(final, res[0].transcript)
        else interim = joinWords(interim, res[0].transcript)
      }
      setText(joinWords(base, final, interim))
    }
    r.onerror = (e) => {
      if (e.error === 'not-allowed') toast('Microphone access is blocked for this site.', 'error')
      r.stop()
    }
    r.onend = () => {
      rec.current = null
      setListening(false)
      setText(joinWords(base, final))
    }
    rec.current = r
    setListening(true)
    r.start()
  }
  // Send and unmount drop the session without letting a late result refill the box.
  const cancelDictation = () => {
    const r = rec.current
    if (!r) return
    rec.current = r.onresult = r.onerror = r.onend = null
    r.abort()
    setListening(false)
  }
  useEffect(() => {
    if (!listening) return
    const hide = () => document.hidden && rec.current?.stop()
    document.addEventListener('visibilitychange', hide)
    return () => document.removeEventListener('visibilitychange', hide)
  }, [listening])
  useEffect(() => () => rec.current?.abort(), [])

  const submit = () => {
    if (!canSend) return
    cancelDictation()
    const attachments = queue.inputs()
    const fallback = attachments.every(a => a.kind === 'image') ? 'Read this chart' : 'Read the attached files'
    if (attachments.length) onSend(text.trim() || fallback, attachments)
    else onSend(text.trim())
    queue.clear()
    setText('')
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault() // also covers ⌘/Ctrl+Enter
      submit()
    } else if (e.key === 'Escape' && listening) {
      e.preventDefault()
      rec.current?.stop()
    } else if (e.key === 'Escape' && streaming) {
      e.preventDefault()
      onStop()
    }
  }

  return (
    <form
      onDragOver={e => {
        if (!e.dataTransfer.types.includes('Files')) return
        e.preventDefault()
        if (!streaming && !disabled) setDragging(true)
      }}
      onDragLeave={e => { if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false) }}
      onDrop={e => {
        setDragging(false)
        if (!e.dataTransfer.files.length) return
        e.preventDefault()
        attach(Array.from(e.dataTransfer.files))
      }}
      onPaste={e => {
        const files = Array.from(e.clipboardData.files)
        if (!files.length) for (const item of Array.from(e.clipboardData.items)) {
          if (item.kind === 'file') { const file = item.getAsFile(); if (file) files.push(file) }
        }
        if (!files.length || pastesText(e.clipboardData, files)) return
        e.preventDefault()
        attach(files)
      }}
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
      className={cx(
        'rounded-2xl border border-border bg-surface shadow-[0_1px_2px_rgb(15_27_45/0.04),0_10px_30px_-18px_rgb(15_27_45/0.25)]',
        'transition-[border-color,box-shadow] duration-150 focus-within:border-accent focus-within:ring-[3px] focus-within:ring-accent/20',
        tooLong && 'border-down!',
        dragging && 'border-accent ring-[3px] ring-accent/20',
        className,
      )}
    >
      <label htmlFor={id} className="sr-only">
        Message the agent
      </label>
      <Textarea
        id={id}
        ref={box}
        rows={2}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKeyDown}
        disabled={disabled}
        aria-invalid={tooLong || undefined}
        placeholder="Ask about any of BTC, ETH, SOL… or say 'buy $500 of ETH'"
        className="max-h-56 overflow-y-auto border-0! bg-transparent! px-4! pt-3.5! pb-1! text-[15px]! focus:ring-0!"
      />
      <AttachmentChips
        files={files}
        errors={fileErrors}
        disabled={streaming || disabled}
        onRemove={removeFile}
        help={HELP}
        helpId={`${id}-file-help`}
        errorId={`${id}-file-error`}
        listClassName="px-4 pt-2"
        errorClassName="px-4 pt-2"
        helpClassName={cx('px-4 pt-1.5 text-[11.5px]', !files.length && !fileErrors.length && 'sr-only')}
      />
      <div className="flex flex-wrap items-center gap-2 px-4 py-2">
        <label className={cx(shortcut, 'cursor-pointer gap-1 focus-within:ring-2 focus-within:ring-accent', (streaming || disabled) && 'pointer-events-none opacity-45')}>
          <IconPaperclip size={14} /> Attach
          <input type="file" aria-label="Attach files or photos" aria-describedby={`${id}-file-help${fileErrors.length ? ` ${id}-file-error` : ''}`} accept={ACCEPT} multiple disabled={streaming || disabled} className="sr-only" onChange={e => { attach(Array.from(e.target.files ?? [])); e.target.value = '' }} />
        </label>
        <Select
          aria-label="Answer mode"
          aria-describedby={`${id}-mode`}
          value={mode}
          onChange={(e) => onModeChange(e.target.value as ChatMode)}
          disabled={streaming || disabled}
          title={MODE_DESCRIPTION[mode]}
          className="h-8! w-auto! rounded-full pl-2.5 text-[12px] font-medium"
        >
          <option value="fast">Fast</option>
          <option value="quality">Quality</option>
        </Select>
        <span id={`${id}-mode`} className="sr-only">{MODE_DESCRIPTION[mode]}</span>
        <Select
          aria-label="Answer lens"
          aria-describedby={`${id}-lens`}
          value={lens}
          onChange={(e) => onLensChange(e.target.value as ChatLens)}
          disabled={streaming || disabled}
          title={LENS_DESCRIPTION[lens]}
          className="h-8! w-auto! rounded-full pl-2.5 text-[12px] font-medium"
        >
          <option value="auto">Lens: Auto</option>
          <option value="investor">Lens: Investor</option>
          <option value="trader">Lens: Trader</option>
        </Select>
        <span id={`${id}-lens`} className="sr-only">{LENS_DESCRIPTION[lens]}</span>
        <Link to="/strategies" className={shortcut}>Strategy Studio</Link>
        <Link to="/automations" className={shortcut}>Workflow</Link>
      </div>
      <div className="flex items-center gap-3 pr-2.5 pb-2.5 pl-4">
        <span className="hidden items-center gap-1 text-[11.5px] text-muted sm:inline-flex">
          <kbd className={kbd}>Enter</kbd> to send <span aria-hidden="true">·</span> <kbd className={kbd}>Shift</kbd>+
          <kbd className={kbd}>Enter</kbd> new line
        </span>
        <span
          className={cx('ml-auto font-mono text-[11px] tabular-nums', tooLong ? 'text-down' : 'text-muted')}
          title={files.length ? `This message spends ${cost} of your daily AI credits` : 'Each message spends 2 of your daily AI credits'}
        >
          {length > MAX - 400 ? `${length.toLocaleString()} / ${MAX.toLocaleString()}${files.length ? ` · ${cost} cr` : ''}` : `${cost} cr / msg`}
        </span>
        {Speech && (
          <button
            type="button"
            onClick={dictate}
            disabled={disabled}
            aria-label={listening ? 'Stop dictation' : 'Dictate'}
            aria-pressed={listening}
            title={listening ? 'Stop dictation' : 'Dictate'}
            className={cx(
              'grid size-9 place-items-center rounded-full transition-colors disabled:pointer-events-none disabled:opacity-30',
              listening ? 'animate-pulse bg-down/12 text-down' : 'text-muted hover:bg-surface-2 hover:text-text',
            )}
          >
            <svg width={18} height={18} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.7} strokeLinecap="round" aria-hidden="true">
              <rect x="9" y="3" width="6" height="11" rx="3" />
              <path d="M5.5 11a6.5 6.5 0 0 0 13 0M12 17.5V21" />
            </svg>
          </button>
        )}
        {streaming ? (
          <button
            type="button"
            onClick={onStop}
            className="inline-flex h-9 items-center gap-1.5 rounded-full border border-border bg-surface px-3.5 text-[13px] font-medium text-text transition-colors hover:bg-surface-2"
          >
            <IconStop size={16} />
            Stop
          </button>
        ) : (
          <button
            type="submit"
            disabled={!canSend}
            aria-label="Send message"
            title="Send (Enter)"
            className="grid size-9 place-items-center rounded-full bg-ink text-ink-fg shadow-sm transition-[opacity,transform] duration-150 hover:scale-105 active:scale-95 disabled:pointer-events-none disabled:opacity-30"
          >
            <IconArrowUp size={18} strokeWidth={2} />
          </button>
        )}
      </div>
    </form>
  )
}
