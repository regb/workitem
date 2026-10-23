import { useEffect, useRef } from 'react'
import { useParams } from '@tanstack/react-router'
import { LoaderIcon, LockIcon } from 'lucide-react'
import { useConfirm } from '@/components/confirm'
import { Composer, type ComposerHandle } from '@/components/chat/composer'
import { Conversation } from '@/components/chat/conversation'
import { Button } from '@/components/ui/button'
import { readMarks, releaseControl, releaseOnExit, takeControl } from '@/lib/actions'
import type { Chat } from '@/lib/api'
import { useChat } from '@/lib/queries'
import { ui } from '@/lib/ui-state'
import { cn, sentence } from '@/lib/utils'

export function ChatPage() {
  const { id } = useParams({ from: '/items/$id' })
  const { data: chat, error, isPending } = useChat(id)
  const composer = useRef<ComposerHandle>(null)
  const closing = ui.use(state => state.closing.has(id) || state.waiting.has(id))

  // Opening a conversation counts as reading it, even if it was marked
  // unread earlier. Leaving it, or closing the tab, releases this tab's
  // control.
  const controls = useRef(false)
  controls.current = !!chat?.control?.you
  useEffect(() => {
    readMarks.delete(id)
    const exit = () => controls.current && releaseOnExit(id)
    window.addEventListener('pagehide', exit)
    return () => {
      window.removeEventListener('pagehide', exit)
      if (controls.current) void releaseControl(id)
    }
  }, [id])

  if (isPending || ['queued', 'starting'].includes(chat?.startup?.state ?? '')) {
    return (
      <div role="status" aria-label="Starting conversation" className="flex flex-1 items-center justify-center">
        <LoaderIcon className="size-6 animate-spin text-muted-foreground" />
      </div>
    )
  }
  if (!chat) return <p className="p-6 text-sm text-destructive">{error?.message}</p>
  // A startup problem matters until the conversation has begun.
  const startupProblem = chat.startup?.state === 'problem' && !chat.history.messages.length ? chat.startup.error : ''
  const warnings = [startupProblem, ...(chat.warnings ?? [])].filter(Boolean)
  return (
    <div className={cn('flex min-h-0 flex-1 flex-col', closing && 'pointer-events-none animate-out fill-mode-forwards fade-out-0 slide-out-to-left-6 duration-200')}>
      {!!warnings.length && <p className="border-b px-4 py-2 text-xs whitespace-pre-wrap text-amber-300">{warnings.join('\n')}</p>}
      {/* Keys reset the conversation's pages and the composer per item. */}
      <Conversation key={`conversation-${id}`} chat={chat} onRetry={message => composer.current?.restore(message)} />
      {/* The composer, or a panel in its place when this tab cannot send. */}
      {chat.item.state === 'closed' ? <ClosedPanel chat={chat} /> : readOnlyHolder(chat) || controlUnavailable(chat) ? <ReadOnlyBar chat={chat} /> : <Composer key={`composer-${id}`} ref={composer} chat={chat} />}
    </div>
  )
}

// readOnlyHolder names the other client that controls the agent, if any.
function readOnlyHolder(chat: Chat) {
  const control = chat.control
  if (!control || control.you || chat.item.state !== 'active') return ''
  return control.status.holder ?? ''
}

// controlUnavailable explains why this agent cannot be controlled from the
// browser at all: no runtime mode of its driver accepts structured input.
function controlUnavailable(chat: Chat) {
  return chat.item.state === 'active' ? (chat.control?.plan.unavailable ?? '') : ''
}

function ReadOnlyBar({ chat }: { chat: Chat }) {
  const confirm = useConfirm()
  const holder = readOnlyHolder(chat)
  const unavailable = controlUnavailable(chat)
  const where = holder === 'terminal' ? 'in a terminal' : 'in another browser tab'
  if (unavailable) {
    return (
      <ComposerNotice>
        This view is read-only. <span className="text-muted-foreground">{sentence(unavailable)}.</span>
      </ComposerNotice>
    )
  }
  return (
    <div className="mx-auto mb-3 flex w-full max-w-3xl flex-wrap items-center justify-between gap-3 rounded-xl border border-amber-500/30 bg-amber-500/5 px-4 py-3" role="status">
      <div className="flex min-w-0 items-center gap-2 text-sm">
        <LockIcon className="size-4 shrink-0 text-amber-300" aria-hidden />
        <span>
          This agent is controlled {where}. <span className="text-muted-foreground">This view is read-only.</span>
        </span>
      </div>
      <Button variant="secondary" size="sm" onClick={() => takeControl(confirm, chat.item.id, chat.control)}>
        Take control
      </Button>
    </div>
  )
}

// ClosedPanel stands in for the composer of a closed item: its
// conversation is read only. A backlog item keeps the composer, and sending
// starts it.
function ClosedPanel({ chat }: { chat: Chat }) {
  const m = chat.item
  return (
    <ComposerNotice>
      Closed{m.resolution ? ` as ${m.resolution}` : ''}. <span className="text-muted-foreground">The conversation is read only.</span>
    </ComposerNotice>
  )
}

// ComposerNotice stands in for the composer, saying why there is none.
function ComposerNotice({ children }: { children: React.ReactNode }) {
  return (
    <div className="mx-auto mb-3 flex w-full max-w-3xl items-center gap-2 rounded-xl border px-4 py-3 text-sm" role="status">
      <LockIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      <span>{children}</span>
    </div>
  )
}
