import { useMemo, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { ArrowLeftIcon, BellIcon, ChevronRightIcon, CircleCheckIcon, ClockIcon, CloudOffIcon, InfoIcon, MenuIcon } from 'lucide-react'
import { useChoose, useConfirm } from '@/components/confirm'
import { TicketSheet } from '@/components/chat/ticket-sheet'
import { tone } from '@/components/tones'
import { useWaitMenu } from '@/components/wait-menu'
import { useMoveOn, useWait } from '@/hooks/use-move-on'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { closeItem, markUnread, nextItem, shelveItem, startItem, type Resolution } from '@/lib/actions'
import { useChat, useCreations, useWorkList } from '@/lib/queries'
import { attentionCount, ui } from '@/lib/ui-state'
import { cn } from '@/lib/utils'

export function AppHeader({ item }: { item?: string }) {
  return (
    <header className="relative z-10 flex min-h-13 shrink-0 items-center gap-1.5 border-b bg-card px-2 shadow-sm sm:gap-2 sm:px-4">
      {item ? (
        <>
          {/* Phones keep only a short title; the arrow returns to the list. */}
          <Button asChild variant="ghost" size="icon-sm" className="sm:hidden">
            <Link to="/" aria-label="Dashboard">
              <ArrowLeftIcon />
            </Link>
          </Button>
          <Link to="/" className="hidden font-semibold text-foreground/90 hover:text-foreground sm:inline">
            Dashboard
          </Link>
          <ChatTitle id={item} />
          <LiveStatus />
          <ChatActions id={item} />
        </>
      ) : (
        <>
          <Link to="/" className="flex-1 font-semibold text-foreground/90 hover:text-foreground">
            Dashboard
          </Link>
          <LiveStatus />
          <AttentionButton />
        </>
      )}
    </header>
  )
}

// LiveStatus is a quiet sign that what the page shows may be stale: the
// event stream is reconnecting, or the server cannot read the list or this
// conversation. Hovering, or a screen reader, gives the reason.
function LiveStatus() {
  const problems = ui.use(state => state.liveProblems)
  const reasons = [problems.stream, problems.list, problems.chat].filter(Boolean)
  if (!reasons.length) return null
  const text = `Live updates paused. ${reasons.join(' ')}`
  return (
    <span role="status" title={text} className="flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-xs text-amber-300">
      <CloudOffIcon className="size-4" aria-hidden />
      <span className="hidden md:inline">Live updates paused</span>
      <span className="sr-only md:hidden">{text}</span>
      <span className="sr-only max-md:hidden">{reasons.join(' ')}</span>
    </span>
  )
}

function ChatTitle({ id }: { id: string }) {
  const { data } = useChat(id)
  return (
    <h1 className="flex min-w-0 flex-1 items-center gap-2 text-sm font-medium sm:text-base">
      <ChevronRightIcon className="hidden size-4 shrink-0 text-muted-foreground sm:block" aria-hidden />
      <span className="max-w-[55vw] truncate sm:max-w-none">{data?.item.title ?? 'Loading conversation'}</span>
    </h1>
  )
}

// AttentionButton opens the next item needing attention in the selected
// projects; it is a cue, never automatic navigation. The count is always
// shown; wide screens add the label.
function AttentionButton({ current }: { current?: string }) {
  const navigate = useNavigate()
  const { data: list } = useWorkList()
  const { data: jobs } = useCreations()
  const selected = ui.use(state => state.selectedProjects)
  const pending = ui.use(state => state.pendingLaunches)
  const closing = ui.use(state => state.closing)
  const waiting = ui.use(state => state.waiting)
  const leaving = useMemo(() => new Set([...closing, ...waiting]), [closing, waiting])
  const count = attentionCount(list, jobs ?? [], selected, pending, leaving)
  const open = async () => {
    const next = await nextItem(current ?? '', selected)
    if (next) await navigate({ to: '/items/$id', params: { id: next } })
  }
  return (
    <Button
      variant="outline"
      size="sm"
      disabled={count === 0}
      onClick={open}
      aria-label={count ? `Next item needing attention (${count})` : 'Nothing needs attention'}
      className={cn('gap-1.5', count ? 'border-amber-500/50 text-amber-300 hover:bg-amber-500/15 hover:text-amber-200 dark:hover:bg-amber-500/15' : 'text-muted-foreground disabled:opacity-60')}
    >
      <BellIcon className={count ? 'fill-amber-300/20' : undefined} />
      <span className="hidden md:inline">Attention</span>
      <span className="tabular-nums">{count}</span>
    </Button>
  )
}


// ChatActions puts the common actions on the bar and keeps everything in the
// menu. Bar buttons appear as the screen widens; the menu hides the items
// the bar already shows.
function ChatActions({ id }: { id: string }) {
  const confirm = useConfirm()
  const choose = useChoose()
  const navigate = useNavigate()
  const { data } = useChat(id)
  const { data: list } = useWorkList()
  const [details, setDetails] = useState(false)
  const state = data?.item.state
  const busy = !!data?.runtime.online && data?.status === 'busy'
  const title = data?.item.title ?? ''
  // Closing and waiting run in the background while the page moves on.
  const canMoveOn = !!data && state !== 'closed'
  const leave = (done: boolean) => done && navigate({ to: '/' })
  const { reopen, goNext } = useMoveOn(id)
  const close = async (resolution: Resolution = 'completed') => {
    const row = list?.display_sections.flatMap(section => section.items).find(item => item.id === id)
    if (await closeItem(confirm, id, title, row, resolution, reopen)) await goNext()
  }
  const waiting = useWaitMenu(id, useWait(id, title))
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="sm" className={cn('hidden lg:inline-flex', tone.wait)} disabled={!canMoveOn}>
            <ClockIcon /> Wait
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {waiting.items}
        </DropdownMenuContent>
      </DropdownMenu>
      <Button variant="ghost" size="sm" className={cn('hidden xl:inline-flex', tone.neutral)} onClick={() => setDetails(true)} disabled={!data}>
        <InfoIcon /> Details
      </Button>
      <Button
        variant="ghost"
        size="sm"
        // Icon only on phones, the size of the other icon buttons.
        className={cn(tone.done, 'max-md:size-7 max-md:px-0')}
        onClick={() => close()}
        disabled={!canMoveOn}
        aria-label="Done"
        title={busy ? 'Done: stop the agent and close as completed' : 'Done: close as completed'}
      >
        <CircleCheckIcon /> <span className="hidden md:inline">Done</span>
      </Button>
      <AttentionButton current={id} />
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" className={tone.neutral} aria-label="Conversation actions">
            <MenuIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-48">
          <DropdownMenuItem className="xl:hidden" onSelect={() => setDetails(true)}>
            <InfoIcon /> Details
          </DropdownMenuItem>
          <DropdownMenuItem disabled={state !== 'active'} onSelect={() => markUnread(id, data?.history.leaf_id ?? '')}>
            Mark unread
          </DropdownMenuItem>
          <DropdownMenuSub>
            <DropdownMenuSubTrigger className="lg:hidden" disabled={!canMoveOn}>
              <ClockIcon /> Wait
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent>
              {waiting.items}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
          <DropdownMenuSeparator />
          {state === 'backlog' && <DropdownMenuItem onSelect={() => startItem(choose, id)}>Start</DropdownMenuItem>}
          <DropdownMenuItem disabled={state !== 'active' || busy} onSelect={async () => leave(await shelveItem(confirm, id, title))}>
            Shelve
          </DropdownMenuItem>
          <DropdownMenuItem disabled={!canMoveOn} onSelect={() => close()}>
            <CircleCheckIcon /> Close as completed
          </DropdownMenuItem>
          <DropdownMenuItem variant="destructive" disabled={!canMoveOn} onSelect={() => close('abandoned')}>
            Close as abandoned
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {data && <TicketSheet open={details} onOpenChange={setDetails} chat={data} />}
      {waiting.dialogs}
    </>
  )
}

