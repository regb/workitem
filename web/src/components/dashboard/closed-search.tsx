import { useEffect, useState } from 'react'
import { SearchIcon } from 'lucide-react'
import { Input } from '@/components/ui/input'
import type { ListItem, WorkList } from '@/lib/api'
import { useSearch } from '@/lib/queries'
import { cn } from '@/lib/utils'
import { ItemRow } from './item-row'

// ClosedItems lists closed items, the most recently updated first, with a
// search over every closed item's title, labels, description and notes.
export function ClosedItems({ items, list, names }: { items: ListItem[]; list: WorkList; names: Map<string, string> }) {
  const [query, setQuery] = useState('')
  const searched = useDebounced(query.trim(), 200)
  const { data, isFetching, error } = useSearch(searched, 'closed')
  const searching = !!query.trim()
  const hits = searching ? (data?.items ?? []) : []
  // A match in the description or notes shows where it matched.
  const rows = searching ? hits.map(hit => ({ row: hit as ListItem, detail: hit.excerpt ? `${hit.field}: ${hit.excerpt}` : undefined })) : items.map(row => ({ row, detail: undefined }))
  return (
    <div className="flex flex-col gap-1">
      <div className="relative">
        <SearchIcon aria-hidden className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          type="search"
          aria-label="Search closed items"
          placeholder="Search closed items: title, description, notes…"
          value={query}
          onChange={event => setQuery(event.target.value)}
          onKeyDown={event => event.key === 'Escape' && setQuery('')}
          className="h-8 border-border/50 bg-transparent ps-8 text-sm dark:bg-transparent"
        />
      </div>
      {searching && error && <p className="text-xs text-destructive">{error.message}</p>}
      {searching && !error && data && !hits.length && !isFetching && <p className="px-1 text-sm text-muted-foreground">No closed items match.</p>}
      {rows.length > 0 && (
        <div className={cn('flex flex-col divide-y divide-border/50 rounded-lg border border-border/50', searching && isFetching && 'opacity-70')}>
          {rows.map(({ row, detail }) => (
            <ItemRow key={row.id} row={row} list={list} project={names.get(row.project_id) ?? 'Unknown project'} quiet detail={detail} />
          ))}
        </div>
      )}
      {searching && !!data && data.total > hits.length && (
        <p className="px-1 text-xs text-muted-foreground">
          Showing the best {hits.length} of {data.total} matches.
        </p>
      )}
    </div>
  )
}

function useDebounced<T>(value: T, delay: number) {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), delay)
    return () => clearTimeout(timer)
  }, [value, delay])
  return settled
}
