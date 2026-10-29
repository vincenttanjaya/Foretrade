import { memo, useMemo, useState } from 'react'
import remarkGfm from 'remark-gfm'
import { Button } from '../../components/ui/Button'
import { Disclaimer } from '../../components/ui/Disclaimer'
import { IconAlert, IconCheck, IconChevron } from '../../components/ui/icons'
import { LogoMark } from '../../components/ui/Logo'
import { Markdown } from '../../components/ui/Markdown'
import { Skeleton, Spinner } from '../../components/ui/Spinner'
import { cx } from '../../lib/cx'
import type { Card } from '../../lib/types'
import { attachmentLabel, formatBytes } from './attachments'
import { AgentCard } from './cards'
import { citationSources, remarkCitations } from './citations'
import { FileGlyph } from './FileGlyph'
import { useRevealedTurn } from './reveal'
import { errorCopy, type ToolStep, type Turn } from './turns'

/**
 * One message: user bubble on the right, assistant answer (tools · markdown · cards) on the left.
 * `onFollowup` (the latest turn only) turns its followups card into one-tap chips once the answer is done.
 * A live answer is shown as revealed (reveal.ts): its text paced, its chips and cards after the text they followed.
 */
export function TurnView({ turn: received, onRetry, onFollowup, reattach }: { turn: Turn; onRetry?: () => void; onFollowup?: (prompt: string) => void; reattach?: boolean }) {
  const turn = useRevealedTurn(received)
  if (turn.role === 'user') {
    return (
      <li className="flex animate-fade-up justify-end">
        <div className="max-w-[85%] rounded-2xl rounded-br-md bg-ink px-4 py-2.5 text-[14.5px] leading-relaxed [overflow-wrap:anywhere] whitespace-pre-wrap text-ink-fg">
          {turn.content}
          {!!turn.attachments?.length && <ul aria-label="Attachments" className="mt-2 flex flex-wrap justify-end gap-1.5 whitespace-normal">
            {turn.attachments.map((file, i) => (
              <li key={i} className="flex max-w-full min-w-0 items-center gap-2 rounded-lg border border-current/25 p-1 pr-2.5 text-[12px] leading-tight">
                {file.preview ? <img src={file.preview} alt="" className="size-10 shrink-0 rounded-md object-cover" /> : <FileGlyph attachment={file} className="opacity-85" />}
                <span className="flex min-w-0 flex-col">
                  <span className="[overflow-wrap:anywhere]">{file.name}</span>
                  <span className="text-[11px] opacity-70">{attachmentLabel(file)} · {formatBytes(file.size)}{file.kind === 'document' && file.pages ? ` · ${file.pages} ${file.pages === 1 ? 'page' : 'pages'}` : ''}</span>
                </span>
              </li>
            ))}
          </ul>}
        </div>
      </li>
    )
  }

  const streaming = turn.status === 'streaming'
  const toolsRunning = turn.tools.some((t) => t.ok === undefined)
  // Cards that arrive before any text wait for it, so the answer doesn't push them down mid-stream.
  const cards = streaming && !turn.content ? [] : turn.cards.filter((c) => c.type !== 'followups')
  const followups = onFollowup && !streaming ? turn.cards.filter((c) => c.type === 'followups').at(-1) : undefined
  const report = turn.cards.find((c) => c.type === 'forecast') // a handoff's answer: its outlook links to this report
  const forecastId = report?.type === 'forecast' ? report.data.id : undefined
  const sources = citationSources(turn.cards) // an outlook's scenario inputs cite these as [n]
  return (
    <li className="flex gap-3">
      <LogoMark size={26} className="mt-0.5 hidden sm:block" />
      <div className="min-w-0 flex-1 space-y-3.5">
        {turn.tools.length > 0 && <ToolStrip tools={turn.tools} streaming={streaming} done={doneLine(turn)} />}
        {turn.status === 'pending' && (
          <div role="status" className="flex h-6 items-center gap-2.5 text-[13px] text-muted">
            <Spinner className="size-3.5 text-accent" />
            Still working on this answer…
          </div>
        )}
        {turn.content ? (
          <Answer text={turn.content} cards={turn.cards} />
        ) : (
          streaming && !toolsRunning && <Thinking label={turn.tools.length ? 'Writing the answer' : 'Thinking'} />
        )}
        {cards.map((card, i) => (
          <div key={i} className="animate-fade-up">
            <AgentCard card={card} answer={turn.content} forecastId={forecastId} sources={sources} durable={turn.status === 'done' && !!turn.persistedMessageId} />
          </div>
        ))}
        {turn.status === 'stopped' && (
          <p className="text-[12px] text-muted">Stopped here. The full answer is saved to this chat when it finishes.</p>
        )}
        {turn.status === 'error' && turn.error && (
          <ErrorRow code={reattach ? 'reattach' : turn.error.code} message={turn.error.message} onRetry={reattach ? undefined : onRetry} />
        )}
        {(turn.content || cards.length > 0) && <Disclaimer className="pt-0.5" />}
        {followups?.type === 'followups' && (
          <ul aria-label="Suggested follow-ups" className="flex animate-fade-up flex-wrap gap-2">
            {followups.data.prompts.map((p) => (
              <li key={p} className="max-w-full">
                <button
                  type="button"
                  onClick={() => onFollowup?.(p)}
                  className="max-w-full rounded-full border border-border bg-surface px-3 py-1.5 text-left text-[13px] leading-snug text-text transition-colors hover:border-accent/50 hover:bg-surface-2"
                >
                  {p}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </li>
  )
}

/**
 * Assistant markdown; with a `sources` card, its [n] marks become superscript links to those sources. Memoized: the
 * page re-renders on every stream event, the markdown only when the revealed text or the cards change (~30 fps).
 */
const Answer = memo(function Answer({ text, cards }: { text: string; cards: Card[] }) {
  // With no sources, remarkCitations leaves every text node as is.
  const plugins = useMemo(() => [remarkGfm, remarkCitations(citationSources(cards))], [cards])
  return <Markdown remarkPlugins={plugins}>{text}</Markdown>
})

/** "Done in 22 s · 5 tools · 13 sources" for a live turn that finished; null otherwise (stored turns have no timing). */
function doneLine(turn: Turn): string | null {
  if (turn.status !== 'done' || turn.elapsedMs == null) return null
  const secs = Math.round(turn.elapsedMs / 1000)
  const time = secs >= 60 ? `${Math.floor(secs / 60)} min${secs % 60 ? ` ${secs % 60} s` : ''}` : `${secs} s`
  const sources = turn.cards.find((c) => c.type === 'sources')
  const n = sources?.type === 'sources' ? sources.data.items.length : 0
  const count = (k: number, noun: string) => (k ? `${k} ${noun}${k === 1 ? '' : 's'}` : '')
  return [`Done in ${time}`, count(turn.tools.length, 'tool'), count(n, 'source')].filter(Boolean).join(' · ')
}

/** Live tool activity: chips go spinner → ✓; "Show work" lists each step's summary; `done` sums the finished run up. */
function ToolStrip({ tools, streaming, done }: { tools: ToolStep[]; streaming: boolean; done: string | null }) {
  const [open, setOpen] = useState(false)
  const hasSummaries = tools.some((t) => t.summary)
  return (
    <div>
      <ul aria-label="Agent activity" className="flex flex-wrap items-center gap-1.5">
        {tools.map((t) => (
          <li
            key={t.id}
            title={t.summary || undefined}
            className="inline-flex h-7 max-w-full animate-fade-up items-center gap-1.5 rounded-full border border-border bg-surface px-2.5 text-[12px] text-text"
          >
            {t.ok === undefined ? (
              streaming ? (
                <Spinner className="size-3.5 text-accent" label="Running" />
              ) : (
                <span aria-hidden="true" className="size-1.5 rounded-full bg-muted/60" />
              )
            ) : t.ok ? (
              <IconCheck size={13} strokeWidth={2.4} className="shrink-0 text-up" />
            ) : (
              <IconAlert size={13} strokeWidth={2} className="shrink-0 text-down" />
            )}
            <span className="truncate">{t.label}</span>
          </li>
        ))}
        {hasSummaries && (
          <li>
            <button
              type="button"
              onClick={() => setOpen(!open)}
              aria-expanded={open}
              className="inline-flex h-7 items-center gap-0.5 rounded-full px-2 text-[12px] font-medium text-muted transition-colors hover:bg-surface-2 hover:text-text"
            >
              <IconChevron size={13} strokeWidth={2} className={cx('transition-transform duration-150', open && 'rotate-90')} />
              {open ? 'Hide work' : 'Show work'}
            </button>
          </li>
        )}
        {done && <li className="text-[12px] text-muted tabular-nums">{done}</li>}
      </ul>
      {open && (
        <ol className="mt-2.5 animate-fade-up space-y-1.5 border-l-2 border-accent/40 py-0.5 pl-3.5">
          {tools.map((t, i) => (
            <li key={t.id} className="text-[13px] leading-snug">
              <span className="mr-2 font-mono text-[10.5px] text-muted">{String(i + 1).padStart(2, '0')}</span>
              <span className="text-text">{t.label}</span>
              {t.summary && <span className="text-muted"> — {t.summary}</span>}
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}

function Thinking({ label }: { label: string }) {
  return (
    <div role="status" className="flex h-6 items-center gap-2.5 text-[13px] text-muted">
      <span aria-hidden="true" className="flex gap-1">
        {[0, 1, 2].map((i) => (
          <span key={i} className="size-1.5 animate-pulse rounded-full bg-accent" style={{ animationDelay: `${i * 180}ms` }} />
        ))}
      </span>
      {label}
    </div>
  )
}

/** Inline error with an optional Retry; 429 ai_quota gets the friendly credits copy and no Retry. */
export function ErrorRow({ code, message, onRetry }: { code: string; message: string; onRetry?: () => void }) {
  const copy = errorCopy(code ?? 'internal', message ?? '')
  return (
    <div role="alert" className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border border-down/25 bg-down/6 px-3.5 py-3">
      <IconAlert size={18} className="shrink-0 text-down" />
      <div className="min-w-0 flex-1">
        <p className="text-[13.5px] font-medium text-text">{copy.title}</p>
        {copy.detail && <p className="mt-0.5 text-[13px] leading-snug text-muted">{copy.detail}</p>}
      </div>
      {copy.retry && onRetry && (
        <Button size="sm" variant="secondary" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  )
}

/** Placeholder while a stored chat loads; same rhythm as a real exchange so nothing jumps. */
export function ConversationSkeleton() {
  return (
    <div aria-hidden="true" className="space-y-8">
      <div className="flex justify-end">
        <Skeleton className="h-11 w-3/5 max-w-sm rounded-2xl" />
      </div>
      <div className="flex gap-3">
        <Skeleton className="hidden size-[26px] rounded-full sm:block" />
        <div className="flex-1 space-y-2.5">
          <Skeleton className="h-4 w-11/12" />
          <Skeleton className="h-4 w-4/5" />
          <Skeleton className="h-4 w-2/3" />
          <Skeleton className="mt-5 h-44 w-full rounded-[14px]" />
        </div>
      </div>
    </div>
  )
}
