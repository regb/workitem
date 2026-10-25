import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { ArrowDownIcon, SendIcon } from 'lucide-react'
import { useChoose, useConfirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { useExpectingReply } from '@/hooks/use-expecting-reply'
import { useFollowBottom } from '@/hooks/use-follow-bottom'
import { markRead, readMarks } from '@/lib/actions'
import { newClientID } from '@/lib/session'
import { api, type Chat, type NoteAttachment, type History, type HistoryMessage, type HistoryTool, type Live, type PromptLink } from '@/lib/api'
import { addOlderPage, foldTurns, follow, matchOutbox, maxShown, toolMessages, trimShown, visible, type OutboxMessage, type Shown } from '@/lib/history'
import { cachedShown, cacheShown, placeOf, savePlace } from '@/lib/places'
import { forgetMessages, shownMessages, usePendingMessages, useSendMessage } from '@/lib/send'
import { useLive } from '@/lib/queries'
import { forgetSettling, pruneSettling, useSettling } from '@/lib/settling'
import { reportError, ui } from '@/lib/ui-state'
import { cn } from '@/lib/utils'
import { AgentProblem, AgentQuestion, ChatMessage, LivePreview, SentMessage } from './chat-message'
import { AttachmentLinksContext, type AttachmentLinks } from './attachment-context'
import { AttachmentViewer, useItemNotes, viewable } from './item-notes'
import { TurnFold } from './turn-fold'

// useShown keeps what the conversation displays: each newer latest page is
// folded in, so the list only grows at the end while the branch is the same.
// Going back to a conversation starts from what it showed when left.
function useShown(id: string, latest: History) {
  const [state, setState] = useState(() => ({ latest, shown: follow(cachedShown(id), latest) }))
  let current = state
  if (state.latest !== latest) {
    current = { latest, shown: follow(state.shown, latest) }
    setState(current)
  }
  const setShown = useCallback((update: (shown: Shown) => Shown) => setState(previous => ({ ...previous, shown: update(previous.shown) })), [])
  useEffect(() => cacheShown(id, current.shown), [id, current.shown])
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
  const [shown, setShown] = useShown(id, chat.history)
  const [loadingOlder, setLoadingOlder] = useState(false)
  const { viewport, content, atBottom, scrollToEnd, keepPlace, startAt, place } = useFollowBottom()
  const { overlays, unsavedTools } = useLiveTools(shown.messages, live)
  const messages = useMemo(() => shown.messages.filter(visible), [shown.messages])
  // Entries history shows, by id and folded native id, to match saved live text.
  const entries = useMemo(() => new Set(shown.messages.flatMap(message => [message.id, ...(message.native_ids ?? [])])), [shown.messages])
  const held = useSettling(id)
  useEffect(() => {
    pruneSettling(id, ids => ids.some(entry => entries.has(entry)))
  }, [id, entries, held])
  const savedInHistory = !!live?.saved_as?.some(entry => entries.has(entry))

  const { pending, keyOf } = useOutbox(id, shown.messages, live?.prompts)
  // A follow-up waits for the turn to end: it is not in the conversation
  // yet, so it waits above the composer until history shows it.
  const queued = pending.filter(message => message.delivery === 'follow_up' && (message.status === 'sending' || message.status === 'sent'))
  const inline = queued.length ? pending.filter(message => !queued.includes(message)) : pending

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

  // An unread reply opens on the operator's last message, so it reads from
  // the question and the top of the answer. A read one opens where the
  // reader left it, or at the end.
  const [jumped, setJumped] = useState(false)
  const unreadOnOpen = useRef(chat.unread)
  useLayoutEffect(() => {
    if (!unreadOnOpen.current) {
      const left = placeOf(id)
      if (!left || left === 'end') return
      const entry = [...entryElements(content.current)].find(element => element.dataset.entry === left.entry)
      const element = entry?.firstElementChild
      // A place inside a fold, open then and folded now, opens on the fold.
      if (element instanceof HTMLElement) startAt(element, Math.max(left.offset, -element.offsetHeight + 48))
      return
    }
    if (messages.at(-1)?.role === 'user') return
    const asked = [...(content.current?.querySelectorAll<HTMLElement>('[data-role="user"]:not([data-pending])') ?? [])].at(-1)
    if (!asked) return
    startAt(asked)
    setJumped(true)
  }, [])

  // The reader's place is kept as they scroll, and as they leave.
  useLayoutEffect(() => {
    const el = viewport.current
    const inner = content.current
    if (!el || !inner) return
    const save = () => {
      const at = place([...entryElements(inner)].map(entry => entry.firstElementChild).filter(element => element instanceof HTMLElement))
      savePlace(id, at === 'end' ? at : { entry: (at.element.parentElement as HTMLElement).dataset.entry!, offset: Math.round(at.offset) })
    }
    let frame = 0
    const onScroll = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(save)
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    return () => {
      cancelAnimationFrame(frame)
      el.removeEventListener('scroll', onScroll)
      // Still attached: a layout effect is cleaned up before its DOM goes.
      if (el.isConnected) save()
    }
  }, [id, place])

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
  // The agent's own Idle, once its reply is in history, ends the turn even
  // while the status still says busy: it is observed a moment later.
  const finished = live?.activity === 'Idle' && !pending.length && messages.at(-1)?.role === 'assistant'
  const busy = chat.status === 'busy' && !finished
  const problem = chat.status === 'problem'
  // A new reply in history ends the expectation even if the turn was too
  // quick to be seen working.
  const answers = useMemo(() => messages.filter(message => message.role === 'assistant').length, [messages])
  const { expecting, started } = useExpectingReply(pending, live, busy, problem, answers)
  const starting = ui.use(state => state.pendingLaunches.has(id))
  // While a message is being sent; after delivery the expectation of a reply
  // takes over, and ends on any problem.
  const awaiting = inline.some(message => message.status === 'sending')
  // A question pauses the turn: the agent waits on the operator, not the
  // other way round.
  const asking = !!chat.asking
  const working = (busy || awaiting || expecting || starting) && !asking && chat.item.state !== 'closed'
  // Until the agent itself shows work, the page says what it is waiting on
  // rather than claiming the agent is working.
  // A finished turn folds its work behind its answer. The last turn stays
  // whole while the agent may still add to it: a message just sent starts a
  // new turn, so the one before it is final.
  const settling = held.filter(message => !message.ids.some(entry => entries.has(entry)))
  const segments = useMemo(() => foldTurns(messages, (working && !inline.length) || !!settling.length), [messages, working, inline.length, settling.length])
  const waitingOn = awaiting ? 'Sending' : starting || (expecting && !started && !busy) ? 'Waiting for agent' : undefined
  return (
    <ConversationAttachments id={id} leaf={chat.history.leaf_id}>
      <div className="flex min-h-0 flex-1 flex-col">
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
                  without being redrawn. Each entry is marked, without a box
                  of its own, to find the reader's place again. */}
              {[
                ...segments.map(segment => {
                  const entry = segment.kind === 'fold' ? segment.key : segment.message.id
                  return (
                    <div key={segment.kind === 'message' && segment.message.role === 'user' ? keyOf(segment.message) : entry} data-entry={entry} className="contents">
                      {segment.kind === 'fold' ? (
                        <TurnFold tools={segment.tools} texts={segment.texts}>
                          {segment.messages.map(message => (
                            <ChatMessage key={message.id} message={message} liveTools={overlays.get(message.id)} />
                          ))}
                        </TurnFold>
                      ) : segment.message.role === 'user' ? (
                        <SentMessage text={segment.message.text} images={segment.message.images} tools={segment.message.tools} />
                      ) : (
                        <ChatMessage message={segment.message} liveTools={overlays.get(segment.message.id)} />
                      )}
                    </div>
                  )
                }),
                ...settling.map(message => <ChatMessage key={`held-${message.ids[0]}`} message={{ id: message.ids[0], role: 'assistant', text: message.text }} />),
                // Wrapped like history entries, so the message keeps its element.
                ...inline.map(message => (
                  <div key={message.client_id} className="contents">
                    <SentMessage text={message.text} images={message.images} status={message.status} onRetry={() => onRetry(message)} />
                  </div>
                )),
              ]}
              <LivePreview live={live} tools={unsavedTools} savedInHistory={savedInHistory} savedText={messages.at(-1)?.text} working={working} waitingOn={waitingOn} />
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
        </div>
        {!!queued.length && <QueuedMessages messages={queued} />}
        <ReadTracker chat={chat} atBottom={atBottom} jumped={jumped} />
      </div>
    </ConversationAttachments>
  )
}

// entryElements are the conversation's marked entries, in order.
const entryElements = (content: HTMLElement | null) => content?.querySelectorAll<HTMLElement>(':scope > [data-entry]') ?? []

// ConversationAttachments lets the conversation link the item's image and
// Markdown attachments it names, opening them in the attachment viewer. An
// agent attaches as it works, so they are read again as history grows.
function ConversationAttachments({ id, leaf, children }: { id: string; leaf: string; children: React.ReactNode }) {
  const { data, refetch } = useItemNotes(id)
  useEffect(() => {
    void refetch()
  }, [leaf, refetch])
  const [open, setOpen] = useState<NoteAttachment | null>(null)
  // The links change only with the names, so re-reading the same
  // attachments redraws no message.
  const attachments = useRef<NoteAttachment[]>([])
  attachments.current = (data?.attachments ?? []).filter(viewable)
  const names = attachments.current.map(attachment => attachment.name).join('\n')
  const links = useMemo<AttachmentLinks>(
    () => ({ names: names ? names.split('\n') : [], open: name => setOpen(attachments.current.find(attachment => attachment.name === name) ?? null) }),
    [names],
  )
  return (
    <>
      <AttachmentLinksContext value={links}>{children}</AttachmentLinksContext>
      <AttachmentViewer id={id} attachment={open} onClose={() => setOpen(null)} />
    </>
  )
}

// QueuedMessages lists follow-ups the agent takes when its turn ends, in
// light gray above the composer.
function QueuedMessages({ messages }: { messages: OutboxMessage[] }) {
  return (
    <ul aria-label="Queued follow-ups" className="mx-auto flex w-full max-w-3xl flex-col gap-0.5 px-4 pb-2 text-xs text-muted-foreground">
      {messages.map(message => (
        <li key={message.client_id} className="truncate" title={message.text}>
          <span className="font-medium">Follow up:</span> {message.text.replace(/\s+/g, ' ').trim() || `${message.images.length} image${message.images.length === 1 ? '' : 's'}`}
          {message.status === 'sending' && <span className="ms-1 opacity-70">(sending…)</span>}
        </li>
      ))}
    </ul>
  )
}

// ReadTracker marks the conversation read when it is opened, and when a new
// response arrives while the reader follows the bottom of a visible chat.
// The server reports a reply that just finished as unread a moment later, so
// leaving a chat whose end the reader followed marks it read too, if anything
// arrived while it was open. A turn still running ends after that mark and
// stays unread; a chat marked unread here is left so. A chat opened on its
// unread reply, rather than the end, is read as it opens.
function ReadTracker({ chat, atBottom, jumped }: { chat: Chat; atBottom: boolean; jumped: boolean }) {
  const id = chat.item.id, leaf = chat.history.leaf_id
  const openedLeaf = useRef(leaf)
  const reading = atBottom || (jumped && leaf === openedLeaf.current)
  useEffect(() => {
    if (!chat.unread || chat.item.state !== 'active' || document.visibilityState !== 'visible' || !reading || leaf === readMarks.get(id)) return
    readMarks.set(id, leaf)
    void markRead(id)
  }, [chat.unread, chat.item.state, id, leaf, reading])

  const seen = useRef({ leaf, following: false, active: false })
  seen.current = { ...seen.current, leaf, following: atBottom && document.visibilityState === 'visible', active: chat.item.state === 'active' }
  useEffect(() => {
    const opened = seen.current.leaf
    return () => {
      const { leaf, following, active } = seen.current
      if (!active || !following || leaf === opened || leaf === readMarks.get(id)) return
      readMarks.set(id, leaf)
      void markRead(id)
    }
  }, [id])
  return null
}

// taskNotSent reports an active item whose agent never received anything:
// its startup stopped before delivering the task, say because the server
// restarted, the agent needed an approval nobody was there to give, or the
// agent started but refused the task. Its runtime may still be up and idle.
function taskNotSent(chat: Chat, working: boolean) {
  return chat.item.state === 'active' && !working && chat.status !== 'busy' && !!chat.description.trim() && !chat.history.messages.length
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
