import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ArrowDownIcon, SendIcon } from 'lucide-react'
import { useChoose, useConfirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { useExpectingReply } from '@/hooks/use-expecting-reply'
import { useFollowBottom } from '@/hooks/use-follow-bottom'
import { markRead, readMarks } from '@/lib/actions'
import { newClientID } from '@/lib/session'
import { api, type Chat, type History, type HistoryMessage, type HistoryTool, type Live, type PromptLink } from '@/lib/api'
import { addOlderPage, follow, matchOutbox, maxShown, toolMessages, trimShown, visible, type OutboxMessage, type Shown } from '@/lib/history'
import { forgetMessages, shownMessages, usePendingMessages, useSendMessage } from '@/lib/send'
import { useLive } from '@/lib/queries'
import { forgetSettling, pruneSettling, useSettling } from '@/lib/settling'
import { reportError, ui } from '@/lib/ui-state'
import { cn } from '@/lib/utils'
import { AgentProblem, AgentQuestion, ChatMessage, LivePreview, SentMessage } from './chat-message'

// useShown keeps what the conversation displays: each newer latest page is
// folded in, so the list only grows at the end while the branch is the same.
function useShown(latest: History) {
  const [state, setState] = useState(() => ({ latest, shown: follow(undefined, latest) }))
  let current = state
  if (state.latest !== latest) {
    current = { latest, shown: follow(state.shown, latest) }
    setState(current)
  }
  const setShown = useCallback((update: (shown: Shown) => Shown) => setState(previous => ({ ...previous, shown: update(previous.shown) })), [])
  return [current.shown, setShown] as const
}

// useLiveTools places the tool calls the agent is running. One history
// already shows is drawn there with its live state (overlays, by message);
// the others are listed live (unsavedTools). Older drivers report only the
// latest call.
function useLiveTools(messages: HistoryMessage[], live: Live | null | undefined) {
  const owners = useMemo(() => toolMessages(messages), [messages])
  const liveTools = useMemo(() => (live?.tools?.length ? live.tools : live?.tool ? [live.tool] : []), [live?.tools, live?.tool])
  const overlays = useMemo(() => {
    const byMessage = new Map<string, HistoryTool[]>()
    for (const tool of liveTools) {
      const owner = tool.id ? owners.get(tool.id) : undefined
      if (owner) byMessage.set(owner, [...(byMessage.get(owner) ?? []), tool])
    }
    return byMessage
  }, [liveTools, owners])
  return { overlays, unsavedTools: liveTools.filter(tool => !tool.id || !owners.has(tool.id)) }
}

// useOutbox is what this tab sent and history does not show yet (pending).
// Sent messages history already shows are never drawn twice: they are left
// out at once, and forgotten after the render. A confirmed message keeps the
// key its pending bubble had (keyOf), so React updates that bubble in place
// rather than drawing a new one: no blink.
function useOutbox(id: string, messages: HistoryMessage[], links: PromptLink[] | undefined) {
  const outgoing = usePendingMessages(id)
  const outbox = useMemo(() => matchOutbox(outgoing, messages, links), [outgoing, messages, links])
  const pending = outbox.remaining
  const keys = useRef(new Map<string, string>())
  for (const [historyID, clientID] of outbox.confirmed) keys.current.set(historyID, clientID)
  useEffect(() => {
    shownMessages.set(id, messages)
    const confirmed = outgoing.filter(message => !pending.includes(message)).map(message => message.client_id)
    if (confirmed.length) forgetMessages(id, new Set(confirmed))
  }, [id, messages, outgoing, pending])
  return { pending, keyOf: (message: { id: string }) => keys.current.get(message.id) ?? message.id }
}

export function Conversation({ chat, onRetry }: { chat: Chat; onRetry: (message: OutboxMessage) => void }) {
  const id = chat.item.id
  const { data: live } = useLive(id)
  const [shown, setShown] = useShown(chat.history)
  const [loadingOlder, setLoadingOlder] = useState(false)
  const { viewport, content, atBottom, scrollToEnd, keepPlace } = useFollowBottom()
  const { overlays, unsavedTools } = useLiveTools(shown.messages, live)
  const messages = useMemo(() => shown.messages.filter(visible), [shown.messages])
  // Entries history shows, by id and folded native id, to match saved live text.
  const entries = useMemo(() => new Set(shown.messages.flatMap(message => [message.id, ...(message.native_ids ?? [])])), [shown.messages])
  const held = useSettling(id)
  useEffect(() => pruneSettling(id, ids => ids.some(entry => entries.has(entry))), [id, entries, held])
  const savedInHistory = !!live?.saved_as?.some(entry => entries.has(entry))

  const { pending, keyOf } = useOutbox(id, shown.messages, live?.prompts)

  // A long session keeps a bounded tail while the reader follows the end;
  // reading further up is never cut short.
  useEffect(() => {
    if (atBottom && shown.messages.length > maxShown) setShown(trimShown)
  }, [atBottom, shown.messages.length, setShown])
  useEffect(
    () => () => {
      forgetSettling(id)
      shownMessages.delete(id)
    },
    [id],
  )

  // Sending a message returns the reader to the end to follow the reply.
  const sent = useRef(pending.length)
  useEffect(() => {
    if (pending.length > sent.current) scrollToEnd()
    sent.current = pending.length
  }, [pending.length, scrollToEnd])

  const loadOlder = async () => {
    if (loadingOlder || !shown.cursor) return
    setLoadingOlder(true)
    try {
      const page = await api<History>(`/api/items/${id}/history?${new URLSearchParams({ leaf: chat.history.leaf_id, before: shown.cursor })}`)
      keepPlace()
      setShown(current => addOlderPage(current, page))
    } catch (error) {
      reportError((error as Error).message)
    } finally {
      setLoadingOlder(false)
    }
  }

  // The agent shows as working from sending to the end of its turn, and
  // while it starts.
  const busy = chat.status === 'busy'
  const problem = chat.status === 'problem'
  // A new reply in history ends the expectation even if the turn was too
  // quick to be seen working.
  const answers = useMemo(() => messages.filter(message => message.role === 'assistant').length, [messages])
  const expecting = useExpectingReply(pending, live, busy, problem, answers)
  const starting = ui.use(state => state.pendingLaunches.has(id))
  // While a message is being sent; after delivery the expectation of a reply
  // takes over, and ends on any problem.
  const awaiting = pending.some(message => message.status === 'sending')
  // A question pauses the turn: the agent waits on the operator, not the
  // other way round.
  const asking = !!chat.asking
  const working = (busy || awaiting || expecting || starting) && !asking
  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div ref={viewport} className="quiet-scroll min-h-0 flex-1 overflow-y-auto overscroll-contain" role="log" aria-label="Conversation history">
        <div ref={content} className="mx-auto flex w-full max-w-3xl flex-col gap-5 px-4 py-6">
          {!!shown.cursor && (
            <Button variant="ghost" size="sm" className="self-center" onClick={loadOlder} disabled={loadingOlder}>
              {loadingOlder ? 'Loading…' : 'Load older messages'}
            </Button>
          )}
          {chat.history.truncated && <p className="self-center text-xs text-muted-foreground">Oversized content in this page was shortened.</p>}
          {!messages.length && !pending.length && (taskNotSent(chat, working) ? <TaskNotSent chat={chat} /> : <p className="self-center text-sm text-muted-foreground">No messages yet.</p>)}
          {/* One keyed list, so a sent message moves from pending to history
              without being redrawn. */}
          {[
            ...messages.map(message =>
              message.role === 'user' ? (
                <SentMessage key={keyOf(message)} text={message.text} images={message.images} />
              ) : (
                <ChatMessage key={message.id} message={message} liveTools={overlays.get(message.id)} />
              ),
            ),
            ...held
              .filter(message => !message.ids.some(entry => entries.has(entry)))
              .map(message => <ChatMessage key={`held-${message.ids[0]}`} message={{ id: message.ids[0], role: 'assistant', text: message.text }} />),
            ...pending.map(message => <SentMessage key={message.client_id} text={message.text} images={message.images} status={message.status} onRetry={() => onRetry(message)} />),
          ]}
          <LivePreview live={live} tools={unsavedTools} savedInHistory={savedInHistory} savedText={messages.at(-1)?.text} working={working} />
          {problem && !working && chat.item.state === 'active' && <AgentProblem reason={chat.status_reason} />}
          {asking && chat.item.state === 'active' && <AgentQuestion reason={chat.status_reason} />}
        </div>
      </div>
      <Button
        variant="secondary"
        size="icon-sm"
        aria-label="Scroll to latest"
        onClick={scrollToEnd}
        className={cn('absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full border shadow-md transition-[opacity,translate] duration-200', atBottom ? 'pointer-events-none translate-y-2 opacity-0' : 'opacity-100')}
        tabIndex={atBottom ? -1 : 0}
        aria-hidden={atBottom}
      >
        <ArrowDownIcon />
      </Button>
      <ReadTracker chat={chat} atBottom={atBottom} />
    </div>
  )
}

// ReadTracker marks the conversation read when it is opened, and when a new
// response arrives while the reader follows the bottom of a visible chat.
function ReadTracker({ chat, atBottom }: { chat: Chat; atBottom: boolean }) {
  useEffect(() => {
    const id = chat.item.id, leaf = chat.history.leaf_id
    if (!chat.unread || chat.item.state !== 'active' || document.visibilityState !== 'visible' || !atBottom || leaf === readMarks.get(id)) return
    readMarks.set(id, leaf)
    void markRead(id)
  }, [chat.unread, chat.item.state, chat.item.id, chat.history.leaf_id, atBottom])
  return null
}

// taskNotSent reports an active item whose agent never received anything:
// its startup stopped before delivering the task, say because the server
// restarted or the agent needed an approval nobody was there to give.
function taskNotSent(chat: Chat, working: boolean) {
  return chat.item.state === 'active' && !working && !chat.runtime.online && chat.status !== 'busy' && !!chat.description.trim() && !chat.history.messages.length
}

// TaskNotSent offers to send the item's task, its description, as the first
// message: what starting it would have sent. Nothing resends on its own.
function TaskNotSent({ chat }: { chat: Chat }) {
  const send = useSendMessage(chat.item.id, useConfirm(), useChoose())
  const submit = () =>
    send.mutate({ client_id: newClientID(), text: chat.description.trim(), images: [], known: [], delivery: 'prompt', runtime_id: '' })
  return (
    <div role="status" className="flex flex-col items-center gap-3 self-center text-center text-sm">
      <p>
        The task was never sent to the agent. <span className="text-muted-foreground">Send it to start the agent, or write your own first message below.</span>
      </p>
      <Button size="sm" variant="secondary" disabled={send.isPending} onClick={submit}>
        <SendIcon /> Send task
      </Button>
    </div>
  )
}
