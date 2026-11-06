import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { waitInstant } from '@/lib/wait-time'

// FollowUpField is the optional message a blocker sends the agent once the
// item no longer waits.
export function FollowUpField({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return (
    <label className="flex flex-col gap-1.5 text-sm">
      <span className="text-muted-foreground">Then send the agent (optional)</span>
      <Textarea
        aria-label="Then send the agent"
        rows={3}
        maxLength={16384}
        placeholder="A message sent once the item no longer waits"
        value={value}
        onChange={change => onChange(change.target.value)}
      />
    </label>
  )
}

export function TimeDialog({ onOpenChange, onWait }: { onOpenChange: (open: boolean) => void; onWait: (until: string, followUp: string) => void }) {
  const [value, setValue] = useState('')
  const [followUp, setFollowUp] = useState('')
  const [error, setError] = useState('')
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={event => {
            event.preventDefault()
            try {
              onWait(waitInstant(value), followUp.trim())
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
          <FollowUpField value={followUp} onChange={setFollowUp} />
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
