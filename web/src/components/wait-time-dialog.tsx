import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { waitInstant } from '@/lib/wait-time'

export function TimeDialog({ onOpenChange, onWait }: { onOpenChange: (open: boolean) => void; onWait: (until: string) => void }) {
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={event => {
            event.preventDefault()
            try {
              onWait(waitInstant(value))
            } catch (error) {
              setError((error as Error).message)
            }
          }}
        >
          <DialogHeader>
            <DialogTitle>Wait until</DialogTitle>
            <DialogDescription>Choose a time in your local timezone.</DialogDescription>
          </DialogHeader>
          <Input aria-label="Wait until" type="datetime-local" value={value} onChange={event => setValue(event.target.value)} required />
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="submit">Wait</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
