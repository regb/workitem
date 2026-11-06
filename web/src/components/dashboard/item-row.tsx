import { Link, useNavigate } from '@tanstack/react-router'
import { CircleCheckIcon, ClockIcon, LoaderIcon, RotateCcwIcon } from 'lucide-react'
import { blockerShort } from '@/components/blockers'
import { useChoose, useConfirm } from '@/components/confirm'
import { tone } from '@/components/tones'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { useWaitMenu } from '@/components/wait-menu'
import { closeItem, reopenItem, startItem, waitItem } from '@/lib/actions'
import type { ListItem, WorkList } from '@/lib/api'
import { addProjectFilter, ui } from '@/lib/ui-state'
import { cn } from '@/lib/utils'

// ItemRow is a dashboard item: a card on narrow screens and a table-like
// row on wide ones.
export function ItemRow({ row, list, project, quiet, detail }: { row: ListItem; list: WorkList; project: string; quiet: boolean; detail?: string }) {
  const confirm = useConfirm()
  const navigate = useNavigate()
  const choose = useChoose()
  const waiting = row.blockers?.length ? `waiting · ${row.blockers.map(blocker => blockerShort(blocker, list)).join(', ')}` : ''
  const starting = ui.use(state => state.pendingLaunches.has(row.id))
  const waitMenu = useWaitMenu(row.id, request => waitItem(row.id, row.title, request))
  // Reopening is for going back to the work, so it opens the conversation.
  const reopen = async () => {
    await reopenItem(choose, row.id, () => void navigate({ to: '/items/$id', params: { id: row.id } }))
  }
  return (
    <div className={cn('flex items-center gap-3 px-3 py-2.5 transition-opacity hover:bg-muted/40', quiet && 'text-muted-foreground opacity-60 focus-within:opacity-100 hover:opacity-100')} data-item={row.id} data-quiet={quiet || undefined}>
      <Link to="/items/$id" params={{ id: row.id }} className="flex min-w-0 flex-1 flex-col gap-0.5 sm:flex-row sm:items-center sm:gap-4">
        <span className="flex min-w-0 flex-col sm:flex-1">
          {/* Unread replies: a dot and a bold title, like a mail inbox. */}
          <span className="flex min-w-0 items-center gap-2">
            <span aria-hidden className={cn('size-2 shrink-0 rounded-full', row.unread ? 'bg-sky-400' : 'bg-transparent')} />
            <span className={cn('truncate', row.unread ? 'font-semibold text-foreground' : 'text-foreground/85')}>{row.title}</span>
            {row.unread && <span className="sr-only">(unread)</span>}
          </span>
          {waiting && <span className="truncate ps-4 text-xs text-amber-300/90">{waiting}</span>}
          {detail && <span className="line-clamp-2 ps-4 text-xs text-muted-foreground">{detail}</span>}
        </span>
        <span className="flex min-w-0 flex-wrap items-center gap-1.5 text-xs text-muted-foreground sm:w-auto sm:justify-end">
          {starting && (
            <Badge variant="outline" className="gap-1 border-sky-500/40 text-sky-300">
              <LoaderIcon className="size-3 animate-spin" /> starting
            </Badge>
          )}
          {row.resolution && <span className="italic">{row.resolution}</span>}
          {(row.labels ?? []).map(label => (
            <Badge key={label} variant="outline" className="text-violet-300">
              {label}
            </Badge>
          ))}
        </span>
      </Link>
      <button type="button" className="max-w-28 shrink-0 truncate text-xs text-muted-foreground hover:text-foreground hover:underline sm:max-w-40" onClick={() => addProjectFilter(row.project_id)} title={`Show only ${project}`}>
        {project}
      </button>
      {row.state === 'closed' ? (
        // Icon only on phones, like Done.
        <Button variant="outline" size="xs" className={cn(tone.neutral, 'max-sm:size-7 max-sm:px-0')} onClick={reopen} aria-label={`Reopen ${row.title}`} title="Reopen: start it again and open its conversation">
          <RotateCcwIcon /> <span className="hidden sm:inline">Reopen</span>
        </Button>
      ) : row.state === 'backlog' ? (
        <Button variant="outline" size="xs" className={tone.start} disabled={starting} onClick={() => startItem(choose, row.id)} aria-label={`Start ${row.title}`}>
          Start
        </Button>
      ) : (
        // Wait and Done, as icons with room around them; the same actions as
        // a conversation's header, without leaving the dashboard. An item
        // already waiting has no Wait; its place stays, so Done lines up.
        <span className="ms-1 flex shrink-0 items-center gap-1.5">
          {row.blockers?.length ? (
            <span aria-hidden className="size-7" />
          ) : (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon-sm" className={tone.wait} aria-label={`Wait: ${row.title}`} title="Wait">
                  <ClockIcon />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">{waitMenu.items}</DropdownMenuContent>
            </DropdownMenu>
          )}
          {waitMenu.dialogs}
          <Button variant="ghost" size="icon-sm" className={tone.done} onClick={() => closeItem(confirm, row.id, row.title, row)} aria-label={`Done: close ${row.title} as completed`} title="Done">
            <CircleCheckIcon />
          </Button>
        </span>
      )}
    </div>
  )
}
