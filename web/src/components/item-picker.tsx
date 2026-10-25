import { useEffect, useRef, useState } from 'react'
import { SearchIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { sectionTone } from '@/components/tones'
import { matchesItem, useItemRows } from '@/hooks/use-item-rows'
import { cn } from '@/lib/utils'

// ItemPicker chooses another item: a search over title, project and labels,
// and a condensed dashboard list with each item's status. Arrow keys and
// Enter or a click pick one.
export function ItemPicker({ open, onOpenChange, title, exclude, onPick }: { open: boolean; onOpenChange: (open: boolean) => void; title: string; exclude: string; onPick: (id: string) => void }) {
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const search = useRef<HTMLInputElement>(null)
  const shown = useItemRows(exclude).filter(row => matchesItem(query, row))
  const list = useRef<HTMLUListElement>(null)
  // The active item stays in view as arrow keys move through the list.
  // Its result is not a cleanup: Chrome's scrollIntoView returns a promise.
  useEffect(() => {
    list.current?.children[active]?.scrollIntoView?.({ block: 'nearest' })
  }, [active])
  const pick = (id: string) => {
    onOpenChange(false)
    setQuery('')
    setActive(0)
    onPick(id)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="gap-0 p-0 sm:max-w-xl"
        showCloseButton={false}
        // The search takes focus, also when a closing menu returns it elsewhere.
        onOpenAutoFocus={event => {
          event.preventDefault()
          requestAnimationFrame(() => search.current?.focus())
        }}
      >
        <DialogTitle className="sr-only">{title}</DialogTitle>
        <DialogDescription className="sr-only">Search by title, project or label, then pick an item.</DialogDescription>
        <div className="flex items-center gap-2 border-b px-3">
          <SearchIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <input
            ref={search}
            aria-label="Search items"
            placeholder={title}
            value={query}
            className="h-11 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            onChange={event => {
              setQuery(event.target.value)
              setActive(0)
            }}
            onKeyDown={event => {
              if (event.key === 'ArrowDown') {
                event.preventDefault()
                setActive(index => Math.max(Math.min(index + 1, shown.length - 1), 0))
              } else if (event.key === 'ArrowUp') {
                event.preventDefault()
                setActive(index => Math.max(index - 1, 0))
              } else if (event.key === 'Enter' && shown[active]) {
                event.preventDefault()
                pick(shown[active].row.id)
              }
            }}
          />
        </div>
        <ul ref={list} role="listbox" aria-label="Items" className="max-h-96 overflow-y-auto p-1">
          {shown.map(({ row, status, project }, index) => (
            <li key={row.id} role="option" aria-selected={index === active}>
              <button type="button" onMouseEnter={() => setActive(index)} onClick={() => pick(row.id)} className={cn('flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm', index === active && 'bg-muted')}>
                <span aria-hidden className={cn('size-1.5 shrink-0 rounded-full', row.unread ? 'bg-sky-400' : 'bg-transparent')} />
                <span className={cn('min-w-0 flex-1 truncate', row.unread && 'font-semibold')}>{row.title}</span>
                {(row.labels ?? []).map(label => (
                  <Badge key={label} variant="outline" className="text-[0.65rem] text-violet-300">
                    {label}
                  </Badge>
                ))}
                <span className="max-w-28 shrink-0 truncate text-xs text-muted-foreground">{project}</span>
                <span className={cn('w-28 shrink-0 text-right text-[0.65rem] font-semibold tracking-wider', sectionTone[status] ?? 'text-muted-foreground')}>{status}</span>
              </button>
            </li>
          ))}
          {!shown.length && <li className="px-2 py-6 text-center text-sm text-muted-foreground">No matching item.</li>}
        </ul>
      </DialogContent>
    </Dialog>
  )
}
