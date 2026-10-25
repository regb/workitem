import { useEffect, useRef, useState } from 'react'
import { PlusIcon } from 'lucide-react'
import { QuickCreate } from '@/components/create/quick-create'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

// CreateDock floats the create box at the bottom of the dashboard. While it
// is in use, a backdrop blurs the page behind; clicking the backdrop or
// Escape returns to the page. On a desktop it is always there; on a phone it
// opens from the + button, which keeps the list readable.
export function CreateDock() {
  const [opened, setOpened] = useState(false)
  const [focused, setFocused] = useState(false)
  const dock = useRef<HTMLDivElement>(null)
  const active = opened || focused
  const leave = () => {
    setOpened(false)
    setFocused(false)
    if (dock.current?.contains(document.activeElement)) (document.activeElement as HTMLElement).blur()
  }
  // Opening on a phone puts the cursor in the box.
  useEffect(() => {
    if (opened) dock.current?.querySelector('textarea')?.focus()
  }, [opened])
  return (
    <>
      {active && <div aria-hidden className="fixed inset-0 z-30 bg-background/50 backdrop-blur-sm duration-150 animate-in fade-in-0" onMouseDown={leave} />}
      <div
        ref={dock}
        className={cn('fixed inset-x-0 bottom-0 z-40 px-3 pb-[calc(0.75rem+env(safe-area-inset-bottom))] sm:px-6 sm:pb-6', !opened && 'hidden sm:block')}
        onFocus={() => setFocused(true)}
        onBlur={event => {
          // Pickers open in a layer of their own; focus there is still the dock's.
          const next = event.relatedTarget as HTMLElement | null
          if (!next || dock.current?.contains(next) || next.closest('[data-radix-popper-content-wrapper], [data-slot="select-content"], [role="listbox"]')) return
          setFocused(false)
        }}
        // Escape closes the dock, unless an open list in it took the key.
        onKeyDown={event => event.key === 'Escape' && !event.defaultPrevented && leave()}
      >
        <QuickCreate className="mx-auto max-w-3xl" onCreated={leave} />
      </div>
      {!opened && (
        <Button size="icon" aria-label="New work item" onClick={() => setOpened(true)} className="fixed right-4 bottom-[calc(1rem+env(safe-area-inset-bottom))] z-20 size-12 rounded-full shadow-lg sm:hidden">
          <PlusIcon className="size-5" />
        </Button>
      )}
    </>
  )
}
