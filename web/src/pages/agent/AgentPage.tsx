import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router'
import { useToast } from '../../components/toast'
import { Button } from '../../components/ui/Button'
import { EmptyState } from '../../components/ui/EmptyState'
import { ApiError, api, streamChat } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { cx } from '../../lib/cx'
import { usePoll } from '../../lib/usePoll'
import type { ChatAttachmentInput, ChatLens, ChatMessage, ChatMode, ChatStreamRequest, ChatSummary } from '../../lib/types'
import { inputMetadata } from './attachments'
import { ChatHistory } from './ChatHistory'
import { Composer } from './Composer'
import { ConversationSkeleton, ErrorRow, TurnView } from './Conversation'
import { IconArrowDown, IconHistory, IconPlus } from '../../components/ui/icons'
import { applyEvent, fromMessage, pendingAssistant, userTurn, type Turn } from './turns'
import { Welcome } from './Welcome'

type Load = { state: 'idle' } | { state: 'loading' } | { state: 'error'; error: ApiError }

const LATEST_PILL_PX = 120 // the "Latest" pill shows once the reader is further than this from the bottom
const PENDING_POLL_MS = 2000 // a reopened chat whose answer is still running re-reads it this often…
const PENDING_MAX_POLLS = 150 // …for at most 5 minutes (the server gives a run 4)
const UNFINISHED = { code: 'unfinished', message: "This answer didn't finish. Ask again." }
const unfinished = (t: Turn): Turn => ({ ...t, status: 'error', error: UNFINISHED })

const asApiError = (err: unknown, fallback: string) => (err instanceof ApiError ? err : new ApiError(0, 'internal', fallback))

type ChatContext = NonNullable<ChatStreamRequest['context']>

/** A chat opened from a forecast report starts with its forecast card: reopening it keeps the desk brief, so a
 * later trade still gets its stance badge and "Informed by" line. */
function reopenedContext(messages: ChatMessage[]): ChatContext | undefined {
  const card = messages.find((m) => m.role === 'assistant')?.cards?.find((c) => c.type === 'forecast')
  return card?.type === 'forecast' && typeof card.data?.id === 'string' ? { forecastId: card.data.id } : undefined
}

/** Router state other pages send to start a chat: navigate('/', { state: { prompt, context: { symbol?, forecastId? } } }). */
type Entry = { prompt?: unknown; context?: { symbol?: unknown; forecastId?: unknown } } | null

function entryContext(c: NonNullable<Entry>['context']): ChatContext | undefined {
  const out: ChatContext = {}
  if (typeof c?.symbol === 'string') out.symbol = c.symbol
  if (typeof c?.forecastId === 'string') out.forecastId = c.forecastId
  return out.symbol || out.forecastId ? out : undefined
}

/** Routes `/` (new chat) and `/chat/:chatId`. One instance serves both, so a stream survives its own redirect. */
export function AgentPage() {
  const { chatId } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const { setCredits, refresh: refreshCredits } = useAuth()
  const { toast } = useToast()

  const [turns, setTurns] = useState<Turn[]>([])
  const [load, setLoad] = useState<Load>({ state: 'idle' })
  const [loadAttempt, setLoadAttempt] = useState(0)
  const [chats, setChats] = useState<ChatSummary[] | null>(null)
  const [chatsError, setChatsError] = useState<string | null>(null)
  const [chatsAttempt, setChatsAttempt] = useState(0)
  const [away, setAway] = useState(false) // user scrolled up from the latest message
  const [composerEpoch, setComposerEpoch] = useState(0)
  const [mode, setMode] = useState<ChatMode>(() => {
    try {
      return localStorage.getItem('agent-answer-mode') === 'fast' ? 'fast' : 'quality'
    } catch {
      return 'quality'
    }
  })
  const [lens, setLens] = useState<ChatLens>(() => {
    try {
      const saved = localStorage.getItem('agent-answer-lens')
      return saved === 'investor' || saved === 'trader' ? saved : 'auto'
    } catch {
      return 'auto'
    }
  })

  const shown = useRef<string | undefined>(undefined) // chat whose turns are on screen (undefined = new chat)
  const stream = useRef<AbortController | null>(null)
  const scroller = useRef<HTMLDivElement>(null)
  const stick = useRef(true) // keep following the bottom while the answer streams
  const drawer = useRef<HTMLDivElement>(null)
  const context = useRef<ChatContext | undefined>(undefined) // from the entry state; sent with every message of this chat
  const entered = useRef<unknown>(null) // the entry state already sent
  const polls = useRef(0) // re-reads of a pending chat so far
  const retryImages = useRef<{ userKey: string; attachments: ChatAttachmentInput[] } | null>(null)

  const waiting = turns.some((t) => t.status === 'pending')
  const streaming = waiting || turns.some((t) => t.status === 'streaming') // the composer is locked either way

  function chooseMode(next: ChatMode) {
    if (streaming) return
    setMode(next)
    try {
      localStorage.setItem('agent-answer-mode', next)
    } catch {
      // The selected mode still lasts for this page when storage is blocked.
    }
  }

  function chooseLens(next: ChatLens) {
    if (streaming) return
    setLens(next)
    try {
      localStorage.setItem('agent-answer-lens', next)
    } catch {
      // As with the mode, the lens still lasts for this page.
    }
  }

  // Recent chats for the left column.
  useEffect(() => {
    const ctrl = new AbortController()
    setChatsError(null)
    api
      .chats(ctrl.signal)
      .then((res) => setChats(res.chats ?? []))
      .catch((err) => !ctrl.signal.aborted && setChatsError(asApiError(err, "Couldn't load your chats.").message))
    return () => ctrl.abort()
  }, [chatsAttempt])

  // Opening a chat (or "/") swaps the conversation. Skipped when the URL only caught up with our own stream.
  useEffect(() => {
    if (chatId === shown.current) return
    stream.current?.abort()
    stream.current = null
    retryImages.current = null
    setComposerEpoch(n => n + 1)
    shown.current = undefined
    context.current = undefined
    setTurns([])
    if (!chatId) {
      setLoad({ state: 'idle' })
      return
    }
    setLoad({ state: 'loading' })
    const ctrl = new AbortController()
    api
      .chat(chatId, ctrl.signal)
      .then(({ chat }) => {
        shown.current = chatId
        stick.current = true
        polls.current = 0
        context.current = reopenedContext(chat.messages ?? [])
        const stored = (chat.messages ?? []).map(fromMessage)
        setTurns(chat.pending ? [...stored, { ...pendingAssistant(), status: 'pending' }] : stored)
        setLoad({ state: 'idle' })
      })
      .catch((err) => !ctrl.signal.aborted && setLoad({ state: 'error', error: asApiError(err, "Couldn't load this chat.") }))
    return () => ctrl.abort()
  }, [chatId, loadAttempt])

  useEffect(() => () => { stream.current?.abort(); retryImages.current = null }, []) // leaving the page ends the stream

  // The answer to a reopened chat is still being generated (its stream was left): re-read the chat until it is stored.
  usePoll(
    () => {
      const id = shown.current
      if (!id) return
      if (++polls.current > PENDING_MAX_POLLS) {
        setTurns((ts) => ts.map((t) => (t.status === 'pending' ? unfinished(t) : t)))
        return
      }
      api
        .chat(id)
        .then(({ chat }) => {
          if (shown.current !== id || chat.pending) return
          const stored = (chat.messages ?? []).map(fromMessage)
          // A run that produced nothing stores no answer: offer Retry under the question instead.
          const answer = stored.at(-1)?.role === 'assistant' ? [] : [unfinished(pendingAssistant())]
          setTurns((ts) => (ts.some((t) => t.status === 'pending') ? [...stored, ...answer] : ts))
        })
        .catch(() => {}) // the next tick tries again
    },
    PENDING_POLL_MS,
    waiting,
  )

  // Another page started this chat (forecast report, signals): send its prompt once, then drop the state so a reload
  // or Back doesn't send it again. Waits for the history list, else a list that loads after the new chat exists
  // leaves it out of the sidebar; and a tick, since StrictMode's mount → unmount → mount runs the abort above first.
  const listed = chats !== null || chatsError !== null
  useEffect(() => {
    const entry = location.state as Entry
    const prompt = typeof entry?.prompt === 'string' ? entry.prompt.trim() : ''
    if (chatId || !listed || !prompt || prompt.length > 4000 || entered.current === entry) return
    const timer = setTimeout(() => {
      entered.current = entry
      navigate('/', { replace: true, state: null })
      context.current = entryContext(entry?.context)
      send(prompt)
    })
    return () => clearTimeout(timer)
  }, [chatId, location.state, listed])

  useLayoutEffect(() => {
    const el = scroller.current
    if (el && stick.current && turns.length > 0) el.scrollTop = el.scrollHeight
  }, [turns])

  // A live answer is revealed between this page's renders (reveal.ts): follow the conversation's growth too. Resize
  // observers run after layout and before paint, so the bottom stays put frame by frame.
  const follow = useCallback((list: HTMLOListElement | null) => {
    if (!list || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => {
      const el = scroller.current
      if (el && stick.current) el.scrollTop = el.scrollHeight
    })
    observer.observe(list)
    return () => observer.disconnect()
  }, [])

  function onScroll() {
    const el = scroller.current!
    const below = el.scrollHeight - el.scrollTop - el.clientHeight
    stick.current = below < 96
    setAway(below > LATEST_PILL_PX) // near the bottom the pill would only cover the last card
  }

  function send(text: string, attachments?: ChatAttachmentInput[]) {
    if (streaming || stream.current) return
    const ctrl = new AbortController()
    stream.current = ctrl
    const reply = pendingAssistant()
    // Images show as thumbnails while this page holds their bytes; history keeps names only.
    const user = userTurn(text, attachments?.map(a => ({ ...inputMetadata(a), ...(a.kind === 'image' ? { preview: `data:${a.mediaType};base64,${a.data}` } : {}) })))
    retryImages.current = attachments?.length ? { userKey: user.key, attachments } : null
    stick.current = true
    setTurns((ts) => [...ts, user, reply])

    void streamChat(
      { chatId: shown.current, message: text, mode, lens, context: context.current, ...(attachments?.length ? { attachments } : {}) },
      (ev) => {
        if (ctrl.signal.aborted) return
        if (ev.event === 'chat') {
          const { chatId: id, title } = ev.data
          setChats((cs) => cs && [{ id, title, updatedAt: new Date().toISOString() }, ...cs.filter((c) => c.id !== id)])
          if (shown.current !== id) {
            shown.current = id // set first, so the chatId effect leaves this conversation alone
            navigate(`/chat/${encodeURIComponent(id)}`, { replace: true })
          }
          return
        }
        // Terminal events unlock the composer before reader cancellation finishes. Only release this owner;
        // its eventual finally must leave a newer attempt's controller intact.
        if ((ev.event === 'done' || ev.event === 'error') && stream.current === ctrl) stream.current = null
        if (ev.event === 'done') {
          if (ev.data?.credits) setCredits(ev.data.credits)
          if (retryImages.current?.userKey === user.key) retryImages.current = null
        }
        if (ev.event === 'error') void refreshCredits().catch(() => {}) // the server may have spent credits
        setTurns((ts) => ts.map((t) => (t.key === reply.key ? applyEvent(t, ev) : t)))
      },
      ctrl.signal,
    ).finally(() => {
      if (stream.current === ctrl) stream.current = null
    })
  }

  function stop() {
    stream.current?.abort()
    stream.current = null
    retryImages.current = null
    setTurns((ts) => ts.map((t) => (t.status === 'streaming' || t.status === 'pending' ? { ...t, status: 'stopped' } : t)))
    void refreshCredits().catch(() => {}) // no `done` event, so re-read the balance
  }

  function retry(key: string) {
    const i = turns.findIndex((t) => t.key === key)
    const prompt = turns[i - 1]
    if (i < 1 || prompt.role !== 'user') return
    const attachments = retryImages.current?.userKey === prompt.key ? retryImages.current.attachments : undefined
    if (prompt.attachments?.length && !attachments) return
    stream.current?.abort()
    stream.current = null
    // ponytail: the failed attempt's user message is already stored server-side, so a retry repeats it
    // in the chat's history; fine for a demo, dedupe server-side if it shows.
    setTurns((ts) => ts.filter((t) => t.key !== key && t.key !== prompt.key))
    send(prompt.content, attachments)
  }

  function newChat() {
    stream.current?.abort()
    stream.current = null
    shown.current = undefined
    context.current = undefined
    retryImages.current = null
    setComposerEpoch(n => n + 1)
    setTurns([])
    setLoad({ state: 'idle' })
    drawer.current?.hidePopover()
    navigate('/', { replace: !chatId })
  }

  async function deleteChat(id: string) {
    try {
      await api.deleteChat(id)
      setChats((cs) => cs && cs.filter((c) => c.id !== id))
      if (id === chatId || id === shown.current) newChat()
      toast('Chat deleted', 'success')
    } catch (err) {
      toast(asApiError(err, "Couldn't delete the chat.").message, 'error')
    }
  }

  function jumpToLatest() {
    stick.current = true
    scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: 'smooth' })
  }

  const history = (inDrawer: boolean) => (
    <ChatHistory
      chats={chats}
      error={chatsError}
      activeId={chatId}
      onNew={newChat}
      onRetry={() => setChatsAttempt((n) => n + 1)}
      onDelete={deleteChat}
      onNavigate={inDrawer ? () => drawer.current?.hidePopover() : undefined}
      onClose={inDrawer ? () => drawer.current?.hidePopover() : undefined}
    />
  )

  const title = chatId ? (chats?.find((c) => c.id === chatId)?.title ?? 'Chat') : 'New chat'
  const conversation = turns.length > 0 || load.state === 'loading'

  let body
  if (load.state === 'error') {
    body =
      load.error.status === 404 ? (
        <div className="mx-auto max-w-xl px-4 py-16">
          <EmptyState
            title="This chat isn't here."
            description="It may have been deleted, or the link belongs to another account."
            action={<Button onClick={newChat}>Start a new chat</Button>}
          />
        </div>
      ) : (
        <div className="mx-auto max-w-3xl px-4 py-10">
          <ErrorRow code={load.error.code} message={load.error.message} onRetry={() => setLoadAttempt((n) => n + 1)} />
        </div>
      )
  } else if (!conversation) {
    body = <Welcome key={composerEpoch} onSend={send} onStop={stop} streaming={streaming} mode={mode} onModeChange={chooseMode} lens={lens} onLensChange={chooseLens} />
  } else {
    body = (
      <div className="mx-auto w-full max-w-3xl px-4 pt-6 pb-10 md:pt-10">
        <h1 className="sr-only">{title}</h1>
        {load.state === 'loading' ? (
          <ConversationSkeleton />
        ) : (
          <ol ref={follow} aria-label="Conversation" aria-live="polite" aria-busy={streaming} className="space-y-8">
            {turns.map((t, i) => (
              <TurnView
                key={t.key}
                turn={t}
                onRetry={i === turns.length - 1 ? () => retry(t.key) : undefined}
                onFollowup={i === turns.length - 1 ? send : undefined}
                reattach={!!turns[i - 1]?.attachments?.length && retryImages.current?.userKey !== turns[i - 1]?.key}
              />
            ))}
          </ol>
        )}
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0">
      <aside className="hidden w-[272px] shrink-0 border-r border-border bg-surface/50 lg:block">{history(false)}</aside>

      {/* Below lg the history is a native popover drawer: Escape and light-dismiss come free. */}
      <div
        id="agent-chat-history"
        ref={drawer}
        popover="auto"
        className="fixed inset-y-0 right-auto left-0 m-0 h-dvh max-h-none w-[min(320px,86vw)] animate-fade-up border-0 border-r border-border bg-surface p-0 text-text shadow-[16px_0_48px_-24px_rgb(15_27_45/0.45)] backdrop:bg-[#0F1B2D]/35 lg:hidden"
      >
        {history(true)}
      </div>

      <section className="relative flex min-w-0 flex-1 flex-col">
        <div className="flex h-12 shrink-0 items-center gap-2 border-b border-border px-2 lg:hidden">
          <button
            type="button"
            popoverTarget="agent-chat-history"
            className="inline-flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-[13px] font-medium text-muted transition-colors hover:bg-surface-2 hover:text-text"
          >
            <IconHistory size={17} />
            Chats
          </button>
          <span className="min-w-0 flex-1 truncate text-center text-[13.5px] font-medium text-text">
            {title}
          </span>
          <button
            type="button"
            onClick={newChat}
            aria-label="New chat"
            title="New chat"
            className="grid size-8 place-items-center rounded-lg text-muted transition-colors hover:bg-surface-2 hover:text-text"
          >
            <IconPlus size={18} />
          </button>
        </div>

        {/* `relative` keeps absolutely-positioned sr-only text inside this scroller, else it overflows <main> (double scroll on mobile). */}
        <div ref={scroller} onScroll={onScroll} className="relative min-h-0 flex-1 overflow-y-auto">
          {body}
        </div>

        {conversation && load.state !== 'error' && (
          <div className="relative shrink-0 px-3 pt-1 pb-3 sm:px-4 md:pb-5">
            <button
              type="button"
              onClick={jumpToLatest}
              tabIndex={away ? 0 : -1}
              aria-hidden={!away}
              className={cx(
                'absolute -top-11 left-1/2 inline-flex h-8 -translate-x-1/2 items-center gap-1.5 rounded-full border border-border bg-surface px-3 text-[12.5px] font-medium text-text shadow-[0_8px_24px_-12px_rgb(15_27_45/0.35)] transition-opacity duration-150 hover:bg-surface-2',
                away ? 'opacity-100' : 'pointer-events-none opacity-0',
              )}
            >
              <IconArrowDown size={14} />
              Latest
            </button>
            <Composer
              key={composerEpoch}
              className="mx-auto max-w-3xl"
              onSend={send}
              onStop={stop}
              streaming={streaming}
              mode={mode}
              onModeChange={chooseMode}
              lens={lens}
              onLensChange={chooseLens}
              disabled={load.state === 'loading'}
              autoFocus
            />
          </div>
        )}
      </section>
    </div>
  )
}
