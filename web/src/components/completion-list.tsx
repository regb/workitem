import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

// useCompletionList is the list of choices a field offers while a mention is
// typed: arrow keys move through it, Enter or Tab picks, Escape dismisses,
// and the active choice stays in view. field holds the attributes that tie
// the text field to the list for assistive technology. hold keeps Enter from
// submitting while the choices for what was typed are still being found.
// The list opens above its field, as for a message box at the bottom of the
// page, or below.
export function useCompletionList<T>({
  id,
  label,
  open,
  options,
  query,
  hold = false,
  below = false,
  notice,
  render,
  onPick,
  onDismiss,
}: {
  id: string
  label: string
  open: boolean
  options: T[]
  query: string
  hold?: boolean
  below?: boolean
  notice?: string
  render: (option: T) => ReactNode
  onPick: (option: T) => void
  onDismiss: () => void
}) {
  const [active, setActive] = useState(0)
  const list = useRef<HTMLUListElement>(null)
  const index = Math.min(active, Math.max(options.length - 1, 0))
  const visible = open && (options.length > 0 || !!notice)
  useEffect(() => {
    setActive(0)
  }, [query])
  useEffect(() => {
    if (visible) list.current?.querySelector(`#${id}-${index}`)?.scrollIntoView?.({ block: 'nearest' })
  }, [visible, index, id])

  // onKeyDown handles the keys of a shown list and reports whether it did.
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement | HTMLInputElement>) => {
    if (!open || event.nativeEvent.isComposing) return false
    if (event.key === 'Enter' && !event.shiftKey && hold) return true
    if (!visible) return false
    if (event.key === 'Escape') {
      onDismiss()
      return true
    }
    if (!options.length) return false
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      setActive((index + (event.key === 'ArrowDown' ? 1 : options.length - 1)) % options.length)
      return true
    }
    if ((event.key === 'Enter' && !event.shiftKey) || event.key === 'Tab') {
      onPick(options[index])
      return true
    }
    return false
  }

  const field = visible && options.length ? { 'aria-expanded': true, 'aria-controls': id, 'aria-activedescendant': `${id}-${index}`, 'aria-autocomplete': 'list' as const } : {}
  const element = visible && (
    <ul ref={list} id={id} role="listbox" aria-label={label} className={cn('absolute inset-x-0 z-20 max-h-72 overflow-y-auto rounded-lg border bg-popover p-1 shadow-lg', below ? 'top-full mt-1' : 'bottom-full mb-1')}>
      {notice && <li className="px-2 py-1.5 text-xs text-muted-foreground">{notice}</li>}
      {options.map((option, at) => (
        <li key={at} id={`${id}-${at}`} role="option" aria-selected={at === index}>
          <button
            type="button"
            tabIndex={-1}
            onMouseDown={event => event.preventDefault()}
            onMouseEnter={() => setActive(at)}
            onClick={() => onPick(option)}
            className={cn('flex w-full items-center gap-2 rounded-md px-2 py-1 text-left', at === index && 'bg-muted')}
          >
            {render(option)}
          </button>
        </li>
      ))}
    </ul>
  )
  return { onKeyDown, element, field }
}
