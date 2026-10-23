import { useRef, useState } from 'react'
import { ItemPicker } from '@/components/item-picker'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'

export type WaitRequest = Record<string, string>

const waitChoices = [
  ['1h', '1 hour'],
  ['4h', '4 hours'],
  ['tomorrow', 'Tomorrow 9:00'],
  ['monday', 'Monday 9:00'],
] as const

// useWaitMenu is every Wait menu: items to put in a dropdown (a time to wait
// until, another item to wait on, an event) and the dialogs the last two
// open, to render once beside the menu. id is the item that waits.
export function useWaitMenu(id: string, onWait: (request: WaitRequest) => void) {
  const [dialog, setDialog] = useState<'item' | 'event' | null>(null)
  const close = (open: boolean) => !open && setDialog(null)
  const items = (
    <>
      <DropdownMenuLabel className="text-xs text-muted-foreground">Wait until</DropdownMenuLabel>
      {waitChoices.map(([until, label]) => (
        <DropdownMenuItem key={until} onSelect={() => onWait({ kind: 'time', until })}>
          {label}
        </DropdownMenuItem>
      ))}
      <DropdownMenuSeparator />
      {/* A dialog opens once the menu has closed and returned focus. */}
      <DropdownMenuItem onSelect={() => setTimeout(() => setDialog('item'))}>On another item…</DropdownMenuItem>
      <DropdownMenuItem onSelect={() => setTimeout(() => setDialog('event'))}>On an event…</DropdownMenuItem>
    </>
  )
  const dialogs = (
    <>
      {dialog === 'item' && <ItemPicker open onOpenChange={close} title="Wait on another item" exclude={id} onPick={other => onWait({ kind: 'item', item_id: other })} />}
      {dialog === 'event' && (
        <EventDialog
          onOpenChange={close}
          onWait={event => {
            setDialog(null)
            onWait({ kind: 'external', event })
          }}
        />
      )}
    </>
  )
  return { items, dialogs }
}

// EventDialog names an event the item waits for, such as a review or a PR.
function EventDialog({ onOpenChange, onWait }: { onOpenChange: (open: boolean) => void; onWait: (event: string) => void }) {
  const [event, setEvent] = useState('')
  const input = useRef<HTMLInputElement>(null)
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent
        className="sm:max-w-md"
        // The field takes focus, also when a closing menu returns it elsewhere.
        onOpenAutoFocus={focus => {
          focus.preventDefault()
          requestAnimationFrame(() => input.current?.focus())
        }}
      >
        <form
          className="flex flex-col gap-4"
          onSubmit={submit => {
            submit.preventDefault()
            if (event.trim()) onWait(event.trim())
          }}
        >
          <DialogHeader>
            <DialogTitle>Wait on an event</DialogTitle>
            <DialogDescription>The item waits until you clear the event in its details.</DialogDescription>
          </DialogHeader>
          <Input ref={input} aria-label="Event it waits for" maxLength={200} placeholder="Event, e.g. API review or a PR URL" value={event} onChange={change => setEvent(change.target.value)} />
          <DialogFooter>
            <Button type="submit" disabled={!event.trim()}>
              Wait
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
