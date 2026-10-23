import type { ReactNode } from 'react'
import { ClockIcon } from 'lucide-react'
import { blockerText } from '@/components/blockers'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { useWaitMenu } from '@/components/wait-menu'
import { useWait } from '@/hooks/use-move-on'
import { clearBlocker } from '@/lib/actions'
import type { Chat } from '@/lib/api'
import { useWorkList } from '@/lib/queries'

// TicketSheet shows the item's metadata and what it waits for.
export function TicketSheet({ open, onOpenChange, chat }: { open: boolean; onOpenChange: (open: boolean) => void; chat: Chat }) {
  const { data: list } = useWorkList()
  const m = chat.item
  const live = chat.runtime.runtime
  const project = list?.projects.find(p => p.id === chat.project.id)?.name || chat.project.name
  const fields: [string, ReactNode, boolean?][] = [
    ['Project', project],
    ['Origin', m.repository.remote_url, true],
    ['Item', m.id, true],
    ['Slug', m.slug, true],
    ['Branch', m.checkout.specification.branch, true],
    ['Checkout', m.checkout.specification.kind],
    ['Workspace', m.checkout.claim?.path || 'Not materialized', true],
    ['Resolution', m.resolution],
    ['Agent', m.agent],
    ['Runtime', live ? `${live.mode} · ${live.id}` : 'No runtime', true],
    ['Conversation', m.primary_conversation?.id, true],
    ['Created', new Date(m.created_at).toLocaleString()],
    ['Updated', new Date(m.updated_at).toLocaleString()],
    ['Created from', m.repository.created_from_commit, true],
  ]
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{m.title}</SheetTitle>
          <SheetDescription className="flex gap-1.5">
            <Badge variant="outline">{m.state}</Badge>
            <Badge variant="outline">{chat.status}</Badge>
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 px-4 pb-6">
          <section className="flex flex-col gap-1.5">
            <h3 className="text-sm font-medium">Description</h3>
            <p className="text-sm whitespace-pre-wrap text-muted-foreground">{chat.description || 'No description.'}</p>
          </section>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            {fields
              .filter(([, value]) => value)
              .map(([name, value, code]) => (
                <div key={name} className="contents">
                  <dt className="text-muted-foreground">{name}</dt>
                  <dd className={code ? 'font-mono text-xs break-all' : undefined}>{value}</dd>
                </div>
              ))}
          </dl>
          <section className="flex flex-col gap-1.5">
            <h3 className="text-sm font-medium">Labels</h3>
            <div className="flex flex-wrap gap-1.5">
              {m.labels?.length ? (
                m.labels.map(label => (
                  <Badge key={label} variant="outline" className="text-violet-300">
                    {label}
                  </Badge>
                ))
              ) : (
                <span className="text-sm text-muted-foreground">No labels</span>
              )}
            </div>
          </section>
          <Waiting chat={chat} onWait={() => onOpenChange(false)} />
        </div>
      </SheetContent>
    </Sheet>
  )
}

// Waiting lists what the item waits for. Adding a wait is the same menu and
// action as the header's Wait: the item waits and the page moves on.
function Waiting({ chat, onWait }: { chat: Chat; onWait: () => void }) {
  const { data: list } = useWorkList()
  const m = chat.item
  const wait = useWait(m.id, m.title)
  const menu = useWaitMenu(m.id, request => {
    onWait()
    wait(request)
  })
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-medium">Waiting</h3>
      <ul className="flex flex-col gap-1">
        {(m.blockers ?? []).map(blocker => (
          <li key={blocker.id} className="flex items-center justify-between gap-2 text-sm">
            <span>{blocker.label ? `${blocker.label} · ${blockerText(blocker, list)}` : blockerText(blocker, list)}</span>
            <Button variant="outline" size="xs" aria-label={`Clear blocker ${blockerText(blocker, list)}`} onClick={() => clearBlocker(m.id, blocker.id)}>
              Clear
            </Button>
          </li>
        ))}
        {!m.blockers?.length && <li className="text-sm text-muted-foreground">Not waiting</li>}
      </ul>
      {m.state !== 'closed' && (
        <>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="secondary" size="sm" className="self-start">
                <ClockIcon /> Wait
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">{menu.items}</DropdownMenuContent>
          </DropdownMenu>
          {menu.dialogs}
        </>
      )}
    </section>
  )
}
