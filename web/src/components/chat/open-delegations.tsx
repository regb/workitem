import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { AgentStatus, ItemState } from '@/gen/wi/core/v1/common_pb'
import type { ItemSummary } from '@/gen/wi/core/v1/items_pb'
import { items } from '@/lib/rpc'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { takeOwnership } from '@/lib/actions'

// delegationState is how a delegation reads at a glance: its agent working
// (or waiting on work that will re-activate it), asking, in trouble, done
// for now, or not started.
export function delegationState(row: ItemSummary): { label: string; tone: string } {
  const o = row.observation
  if (row.state === ItemState.BACKLOG) return { label: 'not started', tone: 'bg-muted-foreground/40' }
  if (o?.agentStatus === AgentStatus.PROBLEM) return { label: 'problem', tone: 'bg-red-400' }
  if (o?.asking) return { label: 'asking', tone: 'bg-amber-300' }
  if (o?.agentStatus === AgentStatus.BUSY || o?.backgroundTasks || o?.workingDelegations) return { label: 'working', tone: 'bg-sky-400 animate-pulse' }
  return { label: o?.unread ? 'replied' : 'idle', tone: o?.unread ? 'bg-emerald-400' : 'bg-muted-foreground/60' }
}

// OpenDelegations shows, above the composer, the unfinished items this
// item's agent delegated: the work it has going on, each a link to its
// conversation. The item's details list every delegation, closed ones too.
export function OpenDelegations({ id }: { id: string }) {
  const { data } = useQuery({
    queryKey: ['delegations', id, 'open'],
    queryFn: ({ signal }) => items.listItems({ owner: `item:${id}`, states: [ItemState.BACKLOG, ItemState.ACTIVE] }, { signal }),
    refetchInterval: 10_000,
  })
  const open = data?.items ?? []
  if (!open.length) return null
  return (
    <nav aria-label="Open delegations" className="mx-auto flex w-full max-w-3xl flex-wrap items-center gap-1.5 px-4 pb-2 text-xs">
      <span className="text-muted-foreground">Delegated:</span>
      {open.map(row => {
        const state = delegationState(row)
        return (
          <Link
            key={row.id}
            to="/items/$id"
            params={{ id: row.id }}
            title={`${row.title} · ${state.label}`}
            className="flex max-w-56 items-center gap-1.5 rounded-full border px-2 py-0.5 hover:bg-muted"
          >
            <span aria-hidden className={cn('size-1.5 shrink-0 rounded-full', state.tone)} />
            <span className="truncate">{row.title}</span>
            <span className="sr-only">{state.label}</span>
          </Link>
        )
      })}
    </nav>
  )
}

// DelegatedBy says, above a delegation's composer, which item's agent follows
// it up, and lets you take it over. Its owner is its agent's user: what you
// send here reaches it in an envelope naming you, a person, whose word
// prevails.
export function DelegatedBy({ id, owner }: { id: string; owner: string }) {
  const { data } = useQuery({
    queryKey: ['item-title', owner],
    queryFn: ({ signal }) => items.getItem({ itemId: owner }, { signal }),
    staleTime: 60_000,
  })
  const title = data?.item?.title ?? owner
  return (
    <div role="note" aria-label="Delegation" className="mx-auto flex w-full max-w-3xl flex-wrap items-center gap-1.5 px-4 pb-2 text-xs text-muted-foreground">
      <span>Delegated by</span>
      <Link to="/items/$id" params={{ id: owner }} className="max-w-56 truncate rounded-full border px-2 py-0.5 text-foreground hover:bg-muted">
        {title}
      </Link>
      <span>: its agent follows this up. Messages you send here come from you.</span>
      <Button size="xs" variant="outline" className="ml-auto" onClick={() => void takeOwnership(id)}>
        Take it
      </Button>
    </div>
  )
}

// BackgroundWork shows, above the composer, the background work the idle
// agent waits on: commands, subagents or monitors it started, each of which
// re-activates it when it ends.
export function BackgroundWork({ work }: { work: { id: string; label: string; started_at?: string }[] }) {
  if (!work.length) return null
  return (
    <nav aria-label="Background work" className="mx-auto flex w-full max-w-3xl flex-wrap items-center gap-1.5 px-4 pb-2 text-xs">
      <span className="text-muted-foreground">Background:</span>
      {work.map(task => (
        <span
          key={task.id}
          title={task.started_at ? `${task.label || task.id} · started ${new Date(task.started_at).toLocaleTimeString()}` : task.label || task.id}
          className="flex max-w-56 items-center gap-1.5 rounded-full border px-2 py-0.5"
        >
          <span aria-hidden className="size-1.5 shrink-0 animate-pulse rounded-full bg-sky-400" />
          <span className="truncate">{task.label || task.id}</span>
        </span>
      ))}
    </nav>
  )
}
