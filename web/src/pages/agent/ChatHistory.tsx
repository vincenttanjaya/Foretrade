import { useState } from 'react'
import { Link } from 'react-router'
import { Button } from '../../components/ui/Button'
import { Skeleton } from '../../components/ui/Spinner'
import { cx } from '../../lib/cx'
import { timeAgo } from '../../lib/format'
import type { ChatSummary } from '../../lib/types'
import { IconPlus, IconTrash, IconX } from '../../components/ui/icons'

/** Left column: New chat + recent chats (newest first), delete with an inline confirm. */
export function ChatHistory({
  chats,
  error,
  activeId,
  onNew,
  onRetry,
  onDelete,
  onNavigate,
  onClose,
}: {
  chats: ChatSummary[] | null // null until the first load
  error: string | null
  activeId?: string
  onNew: () => void
  onRetry: () => void
  onDelete: (id: string) => Promise<void>
  onNavigate?: () => void
  onClose?: () => void // drawer mode
}) {
  return (
    <div className="flex h-full flex-col">
      <div className="flex gap-2 p-3">
        <Button variant="secondary" className="flex-1 justify-start" onClick={onNew}>
          <IconPlus size={16} />
          New chat
        </Button>
        {onClose && (
          <Button variant="ghost" onClick={onClose} aria-label="Close chat history" className="w-10 shrink-0 px-0!">
            <IconX size={18} />
          </Button>
        )}
      </div>
      <div className="label px-4 pt-1 pb-2">Recent chats</div>
      <nav aria-label="Chat history" className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {chats === null ? (
          error ? (
            <div className="px-2 py-3 text-[13px] text-muted">
              <p>{error}</p>
              <button type="button" onClick={onRetry} className="mt-1.5 font-medium text-accent-ink hover:underline">
                Try again
              </button>
            </div>
          ) : (
            <ul aria-hidden="true">
              {Array.from({ length: 6 }, (_, i) => (
                <li key={i} className="flex h-[52px] flex-col justify-center gap-1.5 px-3">
                  <Skeleton className="h-3.5" />
                  <Skeleton className="h-2.5 w-14" />
                </li>
              ))}
            </ul>
          )
        ) : chats.length === 0 ? (
          <p className="px-2 py-3 text-[13px] leading-relaxed text-muted">No chats yet. Ask a question to start one.</p>
        ) : (
          <ul className="space-y-0.5">
            {chats.map((c) => (
              <HistoryRow key={c.id} chat={c} active={c.id === activeId} onDelete={onDelete} onNavigate={onNavigate} />
            ))}
          </ul>
        )}
      </nav>
    </div>
  )
}

function HistoryRow({
  chat,
  active,
  onDelete,
  onNavigate,
}: {
  chat: ChatSummary
  active: boolean
  onDelete: (id: string) => Promise<void>
  onNavigate?: () => void
}) {
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const title = chat.title || 'Untitled chat'

  if (confirming) {
    return (
      <li
        className="flex h-[52px] items-center gap-1.5 rounded-lg bg-down/6 pr-1.5 pl-3"
        onKeyDown={(e) => e.key === 'Escape' && setConfirming(false)}
      >
        <span className="min-w-0 flex-1 truncate text-[13px] text-text">Delete this chat?</span>
        <button
          type="button"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            await onDelete(chat.id) // the row disappears on success; on failure the page toasts
            setBusy(false)
            setConfirming(false)
          }}
          className="h-7 rounded-md bg-down px-2.5 text-[12px] font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          {busy ? 'Deleting…' : 'Delete'}
        </button>
        <button
          type="button"
          autoFocus
          disabled={busy}
          onClick={() => setConfirming(false)}
          className="h-7 rounded-md px-2 text-[12px] text-muted transition-colors hover:bg-surface-2 hover:text-text"
        >
          Cancel
        </button>
      </li>
    )
  }

  return (
    <li className="group relative">
      <Link
        to={`/chat/${encodeURIComponent(chat.id)}`}
        onClick={onNavigate}
        aria-current={active ? 'page' : undefined}
        className={cx(
          'flex h-[52px] flex-col justify-center rounded-lg pr-10 pl-3 transition-colors',
          active ? 'bg-surface-2' : 'hover:bg-surface-2/70',
        )}
      >
        <span className={cx('truncate text-[13.5px] leading-snug', active ? 'font-medium text-text' : 'text-text/85')}>
          {title}
        </span>
        <span className="mt-0.5 font-mono text-[10.5px] text-muted">{timeAgo(chat.updatedAt)}</span>
      </Link>
      {active && <span aria-hidden="true" className="absolute top-1/2 left-0 h-5 w-[3px] -translate-y-1/2 rounded-r-full bg-accent" />}
      <button
        type="button"
        onClick={() => setConfirming(true)}
        aria-label={`Delete chat: ${title}`}
        title="Delete chat"
        className="absolute top-1/2 right-1.5 grid size-7 -translate-y-1/2 place-items-center rounded-md text-muted opacity-0 transition-[opacity,color,background-color] group-focus-within:opacity-100 group-hover:opacity-100 hover:bg-down/10 hover:text-down focus-visible:opacity-100 pointer-coarse:opacity-100"
      >
        <IconTrash size={15} />
      </button>
    </li>
  )
}
