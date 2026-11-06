import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { AccessLevel } from '@/gen/wi/core/v1/items_pb'
import { ItemState } from '@/gen/wi/core/v1/common_pb'
import { wirePattern, type ItemRef } from '@/lib/item-mentions'
import { items, rpcMessage } from '@/lib/rpc'
import { reportError } from '@/lib/ui-state'

// What an item's agent may do to other items: each item it holds its own
// access entry for, whatever granted it, and each item the draft mentions,
// which sending the message lets it read. The writer lets it do more here,
// such as steer an item they are about to ask it to steer.
const choices = [
  { level: AccessLevel.VIEW, name: 'read it' },
  { level: AccessLevel.MESSAGE, name: 'message and steer it' },
  { level: AccessLevel.CONTROL, name: 'drive its agent' },
]

// mentionedItems are the items a draft mentions, once each, in order.
export function mentionedItems(text: string, self: string): { id: string; title: string }[] {
  const seen = new Set([self])
  const out: { id: string; title: string }[] = []
  for (const match of text.matchAll(wirePattern())) {
    const id = match[2]
    if (seen.has(id)) continue
    seen.add(id)
    out.push({ id, title: match[1].replace(/\\(.)/g, '$1') })
  }
  return out
}

// levelText says what a level lets the agent do, as the strip shows it.
export function levelText(level: AccessLevel | undefined) {
  if (level === undefined || level < AccessLevel.VIEW) return 'can read it once you send'
  if (level >= AccessLevel.INTEGRATE) return level >= AccessLevel.MANAGE ? 'manages it' : 'can merge it'
  return `can ${choices.find(choice => choice.level === level)?.name ?? 'read it'}`
}

// MentionNote says, in the bubble above a mention being written, what the
// agent may do to the mentioned item, and offers more. Sending the message
// lets it read the item; what you choose is granted with the message. For a
// new item, subject is empty: its agent will read the item, and what you
// choose is granted once the item is created.
export function MentionNote({ subject, item, pending, onPending }: { subject: string; item: ItemRef; pending: Record<string, AccessLevel>; onPending: (pending: Record<string, AccessLevel>) => void }) {
  const { data } = useQuery({
    queryKey: ['access', subject, item.id],
    queryFn: ({ signal }) => items.checkAccess({ subject, itemIds: [item.id] }, { signal }),
    enabled: !!subject,
    staleTime: 0,
  })
  if (subject && !data) return <span className="text-muted-foreground">Checking access…</span>
  const current = data?.access[0]?.level ?? AccessLevel.UNSPECIFIED
  const offered = choices.filter(choice => choice.level >= AccessLevel.VIEW && choice.level > current)
  const chosen = pending[item.id]
  const agent = subject ? 'this agent' : "the new item's agent"
  let text: string
  if (chosen) text = `Sending lets ${agent} ${choices.find(choice => choice.level === chosen)?.name ?? 'read it'}`
  else if (current >= AccessLevel.VIEW) text = `${agent === 'this agent' ? 'This agent' : agent} ${levelText(current)}`
  else text = `Mentioning ${item.title} lets ${agent} read it`
  const choose = (value: string) => {
    const next = { ...pending }
    const level = Number(value) as AccessLevel
    if (level <= Math.max(current, AccessLevel.VIEW)) delete next[item.id]
    else next[item.id] = level
    onPending(next)
  }
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      <span>{text}</span>
      {offered.length > 0 && (
        <Select value={String(chosen ?? Math.max(current, AccessLevel.VIEW))} onValueChange={choose}>
          <SelectTrigger size="sm" className="h-6 px-2 text-xs" aria-label={`What ${agent} may do to ${item.title}`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {choices
              .filter(choice => choice.level >= Math.max(current, AccessLevel.VIEW))
              .map(choice => (
                <SelectItem key={choice.level} value={String(choice.level)}>
                  {choice.level === current ? `It can ${choice.name}` : `Let it ${choice.name}`}
                </SelectItem>
              ))}
          </SelectContent>
        </Select>
      )}
    </span>
  )
}

// pendingFor keeps the choices for items the text still mentions.
export function pendingFor(text: string, self: string, pending: Record<string, AccessLevel>) {
  const mentioned = new Set(mentionedItems(text, self).map(item => item.id))
  return Object.fromEntries(Object.entries(pending).filter(([id]) => mentioned.has(id))) as Record<string, AccessLevel>
}

const allLevels = [
  { level: AccessLevel.VIEW, name: 'read' },
  { level: AccessLevel.MESSAGE, name: 'message and steer' },
  { level: AccessLevel.CONTROL, name: 'drive its agent' },
  { level: AccessLevel.INTEGRATE, name: 'merge' },
  { level: AccessLevel.MANAGE, name: 'manage' },
]

// AgentAccess lists, in an item's details, everything its agent may do to
// items: what it has through ownership (its own item, and the items it
// delegated, however far down), the entries granted to it on other items,
// with the level to change or revoke at once, and the entries for every item
// or every item's agent that cover it, which Settings changes.
export function AgentAccess({ itemId }: { itemId: string }) {
  const subject = `item:${itemId}`
  const client = useQueryClient()
  const { data } = useQuery({ queryKey: ['access'], queryFn: ({ signal }) => items.listAccess({}, { signal }), staleTime: 5_000 })
  const { data: delegations } = useQuery({ queryKey: ['delegations', itemId], queryFn: ({ signal }) => items.listItems({ owner: subject, states: [1, 2, 3] }, { signal }), staleTime: 5_000 })
  const open = (delegations?.items ?? []).filter(row => row.state !== ItemState.CLOSED).length
  const all = data?.entries ?? []
  const entries = all.filter(entry => entry.subject === subject && entry.itemId !== '*' && entry.itemId !== itemId)
  const wide = all.filter(entry => (entry.subject === subject || entry.subject === 'item:*') && !entries.includes(entry) && entry.itemId !== itemId)
  const targets = [...entries, ...wide].map(entry => entry.itemId).filter(id => id !== '*')
  const titles = useQueries({
    queries: targets.map(id => ({ queryKey: ['item-title', id], queryFn: ({ signal }: { signal: AbortSignal }) => items.getItem({ itemId: id }, { signal }), staleTime: 60_000 })),
  })
  const titleOf = (id: string) => (id === '*' ? 'Every item' : (titles[targets.indexOf(id)]?.data?.item?.title ?? id))
  const act = async (action: () => Promise<unknown>) => {
    try {
      await action()
    } catch (error) {
      reportError(rpcMessage(error))
    }
    await client.invalidateQueries({ queryKey: ['access'] })
  }
  return (
    <section className="flex flex-col gap-2 text-sm">
      <h3 className="font-medium">Access</h3>
      <ul className="flex flex-col gap-1">
        <li className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate">This item</span>
          <span className="text-xs text-muted-foreground">May merge: its own; its owner closes it</span>
        </li>
        {open > 0 && (
          <li className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate">
              {open === 1 ? 'The item it delegated' : `The ${open} items it delegated`}, and theirs
            </span>
            <span className="text-xs text-muted-foreground">May manage: it owns them</span>
          </li>
        )}
        {entries.map(entry => {
          const title = titleOf(entry.itemId)
          return (
            <li key={entry.id} className="flex items-center gap-2">
              <span className="min-w-0 flex-1 truncate">{title}</span>
              <Select value={String(entry.level)} onValueChange={value => void act(() => items.grantAccess({ subject, onItemId: entry.itemId, level: Number(value) as AccessLevel }))}>
                <SelectTrigger size="sm" className="h-7 w-44 text-xs" aria-label={`What this agent may do to ${title}`}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {allLevels.map(choice => (
                    <SelectItem key={choice.level} value={String(choice.level)}>
                      May {choice.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button size="xs" variant="ghost" aria-label={`Revoke access to ${title}`} onClick={() => void act(() => items.revokeAccess({ entryId: entry.id }))}>
                Revoke
              </Button>
            </li>
          )
        })}
        {wide.map(entry => (
          <li key={entry.id} className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate">{titleOf(entry.itemId)}</span>
            <span className="text-xs text-muted-foreground">
              May {allLevels.find(choice => choice.level === entry.level)?.name ?? 'view'}: {entry.subject === 'item:*' ? "every item's agent, in Settings" : 'in Settings'}
            </span>
          </li>
        ))}
      </ul>
      <p className="text-xs text-muted-foreground">
        {entries.length || wide.length ? 'Reading an item covers the items it delegated.' : 'No access to other items. Mentioning an item in a message lets it read it, and the items it delegated.'}
      </p>
    </section>
  )
}

// grantPending grants what the writer chose for the message being sent. A
// grant that fails is reported; the message is sent all the same.
export async function grantPending(itemId: string, pending: Record<string, AccessLevel>) {
  for (const [id, level] of Object.entries(pending)) {
    try {
      await items.grantAccess({ subject: `item:${itemId}`, onItemId: id, level })
    } catch (error) {
      reportError(rpcMessage(error))
    }
  }
}

// levelAsk says what a level lets an agent do, as a request asks it.
const levelAsk: Partial<Record<AccessLevel, string>> = {
  [AccessLevel.VIEW]: 'read',
  [AccessLevel.MESSAGE]: 'message and steer',
  [AccessLevel.CONTROL]: 'drive the agent of',
  [AccessLevel.INTEGRATE]: 'merge',
  [AccessLevel.MANAGE]: 'close and reopen',
}

// AccessRequests shows, above the message box, the access this item's agent
// was refused and you may grant: approving grants it at once, as you would
// with wi access grant; dismissing forgets the request.
export function AccessRequests({ itemId }: { itemId: string }) {
  const subject = `item:${itemId}`
  const client = useQueryClient()
  const { data } = useQuery({
    queryKey: ['access', 'requests', subject],
    queryFn: ({ signal }) => items.listAccessRequests({ subject }, { signal }),
    refetchInterval: 5_000,
  })
  const requests = data?.requests ?? []
  const titles = useQueries({
    queries: requests.map(request => ({ queryKey: ['item-title', request.itemId], queryFn: ({ signal }: { signal: AbortSignal }) => items.getItem({ itemId: request.itemId }, { signal }), staleTime: 60_000 })),
  })
  if (!requests.length) return null
  const act = async (action: () => Promise<unknown>) => {
    try {
      await action()
    } catch (error) {
      reportError(rpcMessage(error))
    }
    await client.invalidateQueries({ queryKey: ['access'] })
  }
  return (
    <ul aria-label="Access this agent asked for" className="mx-auto flex w-full max-w-3xl flex-col gap-1 px-4 pb-2 text-xs">
      {requests.map((request, at) => {
        const title = titles[at]?.data?.item?.title ?? request.itemId
        return (
          <li key={request.itemId} className="flex flex-wrap items-center gap-1.5 rounded-lg border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5">
            <span className="min-w-0 flex-1">
              This agent asked to {levelAsk[request.needed] ?? 'act on'} <span className="font-medium">{title}</span>
              {request.refusals > 1 ? ` (refused ${request.refusals} times)` : ''}.
            </span>
            <Button size="xs" onClick={() => void act(() => items.grantAccess({ subject, onItemId: request.itemId, level: request.needed }))}>
              Allow
            </Button>
            <Button size="xs" variant="ghost" onClick={() => void act(() => items.dismissAccessRequest({ subject, itemId: request.itemId }))}>
              Dismiss
            </Button>
          </li>
        )
      })}
    </ul>
  )
}
