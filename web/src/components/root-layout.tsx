import { useEffect } from 'react'
import { Outlet, useRouterState } from '@tanstack/react-router'
import { XIcon } from 'lucide-react'
import { AppHeader } from '@/components/app-header'
import { Button } from '@/components/ui/button'
import { useWatch } from '@/lib/watch'
import { useWorkList } from '@/lib/queries'
import { clearError, ui } from '@/lib/ui-state'
import { reviewUI } from '@/lib/review'

// RootLayout holds the header, the error bar and the one watch stream for
// the current view.
export function RootLayout() {
  const path = useRouterState({ select: state => state.location.pathname })
  const item = path.match(/^\/items\/([^/]+)$/)?.[1]
  useWatch(item)
  useEffect(() => {
    clearError()
  }, [path])
  const unsavedReview = reviewUI.use(state => state.drafts.size > 0 || state.mergeMessages.size > 0 || [...state.comments.values()].some(value => !!value.body.trim()))
  useEffect(() => {
    if (!unsavedReview) return
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [unsavedReview])
  useSettledClosings()
  return (
    <div className="flex h-dvh flex-col overflow-hidden bg-background text-foreground">
      <AppHeader item={item} settings={path.startsWith('/settings') || /^\/repositories\/[^/]+\/settings/.test(path)} />
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

