import { useEffect } from 'react'
import { Outlet, useRouterState } from '@tanstack/react-router'
import { XIcon } from 'lucide-react'
import { AppHeader } from '@/components/app-header'
import { Button } from '@/components/ui/button'
import { useEventStream } from '@/lib/events'
import { useCreations, useWorkList } from '@/lib/queries'
import { clearError, ui } from '@/lib/ui-state'

// RootLayout holds the header, the error bar and the one event stream for
// the current view.
export function RootLayout() {
  const path = useRouterState({ select: state => state.location.pathname })
  const item = path.match(/^\/items\/([^/]+)$/)?.[1]
  useEventStream(item)
  useEffect(() => {
    clearError()
  }, [path])
  useSettledLaunches()
  useSettledClosings()
  return (
    <div className="flex h-dvh flex-col overflow-hidden bg-background text-foreground">
      <AppHeader item={item} />
      <ErrorBar />
      <main className="flex min-h-0 flex-1 flex-col">
        <Outlet />
      </main>
    </div>
  )
}

function ErrorBar() {
  const error = ui.use(state => state.actionError)
  if (!error) return null
  return (
    <div role="alert" className="flex items-start gap-2 border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-sm whitespace-pre-wrap text-destructive">
      <p className="min-w-0 flex-1">{error}</p>
      <Button variant="ghost" size="icon-xs" aria-label="Dismiss" onClick={clearError}>
        <XIcon />
      </Button>
    </div>
  )
}

// useSettledClosings forgets closings once the list no longer shows the item
// open; a failed close forgets it at once.
function useSettledClosings() {
  const { data: list } = useWorkList()
  useEffect(() => {
    if (!list) return
    const listed = new Set(list.display_sections.flatMap(section => section.items.filter(item => item.state !== 'closed').map(item => item.id)))
    ui.set(state => {
      const closing = new Set([...state.closing].filter(id => listed.has(id)))
      return closing.size === state.closing.size ? state : { ...state, closing }
    })
  }, [list])
}

// useSettledLaunches forgets launches whose background start finished, or
// that are older than the start timeout.
function useSettledLaunches() {
  const { data: jobs } = useCreations()
  useEffect(() => {
    const settled = new Set((jobs ?? []).filter(job => job.state !== 'queued' && job.state !== 'starting').map(job => job.id))
    ui.set(state => {
      const pending = new Map([...state.pendingLaunches].filter(([id, started]) => !settled.has(id) && Date.now() - started < 150_000))
      return pending.size === state.pendingLaunches.size ? state : { ...state, pendingLaunches: pending }
    })
  }, [jobs])
}
