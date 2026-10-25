import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { cn } from '@/lib/utils'

// An empty action makes a notice: the dialog only explains, with one button
// labelled cancel (default "Cancel").
export type ConfirmOptions = { title: string; description?: string; points?: string[]; action: string; destructive?: boolean; secondary?: string; cancel?: string }
type Confirm = (options: ConfirmOptions) => Promise<boolean>
// Choice is the answer to a dialog with a secondary action.
export type Choice = 'primary' | 'secondary' | null
type Choose = (options: ConfirmOptions & { secondary: string }) => Promise<Choice>

const ConfirmContext = createContext<{ confirm: Confirm; choose: Choose }>({ confirm: async () => false, choose: async () => null })

// useConfirm asks before an action, replacing window.confirm with a dialog.
export const useConfirm = () => useContext(ConfirmContext).confirm
// useChoose asks between two actions, or neither.
export const useChoose = () => useContext(ConfirmContext).choose

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [options, setOptions] = useState<ConfirmOptions | null>(null)
  const resolver = useRef<(value: Choice) => void>(undefined)
  const choose = useCallback<Choose>(next => {
    resolver.current?.(null)
    setOptions(next)
    return new Promise(resolve => {
      resolver.current = resolve
    })
  }, [])
  const value = useMemo(() => ({ choose, confirm: async (next: ConfirmOptions) => (await choose({ ...next, secondary: next.secondary ?? '' })) === 'primary' }), [choose])
  const settle = (value: Choice) => {
    resolver.current?.(value)
    resolver.current = undefined
    setOptions(null)
  }
  return (
    <ConfirmContext.Provider value={value}>
      {children}
      <AlertDialog open={options !== null} onOpenChange={open => !open && settle(null)}>
        {/* Three actions need more room than the default width. */}
        <AlertDialogContent className={cn(options?.secondary && 'data-[size=default]:sm:max-w-md')}>
          <AlertDialogHeader>
            <AlertDialogTitle>{options?.title}</AlertDialogTitle>
            {options?.description && <AlertDialogDescription className="[overflow-wrap:anywhere]">{options.description}</AlertDialogDescription>}
          </AlertDialogHeader>
          {!!options?.points?.length && (
            <ul className="list-disc space-y-1 ps-5 text-sm text-muted-foreground">
              {options.points.map(point => (
                <li key={point}>{point}</li>
              ))}
            </ul>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => settle(null)}>{options?.cancel ?? 'Cancel'}</AlertDialogCancel>
            {options?.secondary && (
              <AlertDialogAction variant="outline" onClick={() => settle('secondary')}>
                {options.secondary}
              </AlertDialogAction>
            )}
            {options?.action && (
              <AlertDialogAction variant={options.destructive ? 'destructive' : 'default'} onClick={() => settle('primary')}>
                {options.action}
              </AlertDialogAction>
            )}
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </ConfirmContext.Provider>
  )
}
