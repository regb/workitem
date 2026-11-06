import * as React from 'react'
import { cn } from 'cn'

// Switch is an on/off toggle: a button with the switch role.
function Switch({ checked, onCheckedChange, className, ...props }: Omit<React.ComponentProps<'button'>, 'onChange'> & { checked: boolean; onCheckedChange: (checked: boolean) => void }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      data-slot="switch"
      data-state={checked ? 'on' : 'off'}
      onClick={() => onCheckedChange(!checked)}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'bg-sky-500' : 'bg-input',
        className,
      )}
      {...props}
    >
      <span className={cn('pointer-events-none block size-4 rounded-full bg-background shadow-sm transition-transform', checked ? 'translate-x-4.5' : 'translate-x-0.5')} />
    </button>
  )
}

export { Switch }
