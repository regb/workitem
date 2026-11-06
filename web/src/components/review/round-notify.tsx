import { useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { itemAction, type Outcome } from '@/lib/api'
import type { ReviewRound } from '@/lib/review-discussion'
import { reviewKey } from '@/lib/review'
import { newClientID } from '@/lib/session'

export function RoundNotify({ id, round }: { id: string; round: ReviewRound }) {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [sent, setSent] = useState(false)
  const attempt = useRef<string | undefined>(undefined)
  const busy = useRef(false)
  const notify = async () => {
    if (busy.current) return
    busy.current = true
    attempt.current ??= newClientID()
    setPending(true)
    setError('')
    try {
      const outcome = await itemAction<Outcome<ReviewRound>>(id, 'review-notify', { round_id: round.id, client_message_id: attempt.current })
      if (outcome.error) throw new Error(outcome.error)
      if (!outcome.result?.command_id) throw new Error('Notification response incomplete. Inspect delivery or retry the same notification.')
      attempt.current = undefined
      setSent(true)
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error))
    } finally {
      busy.current = false
      setPending(false)
      void client.invalidateQueries({ queryKey: reviewKey(id) })
    }
  }
  return (
    <article aria-label={`Published round ${round.id}`} className="space-y-2 rounded border p-2 text-xs">
      <p>
        {round.id} · {round.revision_id} · {round.notified_at || sent ? 'Notification recorded' : 'No recorded notification'}
      </p>
      <Button size="sm" variant="outline" disabled={pending} onClick={() => void notify()}>
        {pending ? 'Notifying…' : error ? `Retry same notification for ${round.id}` : round.notified_at || sent ? `Notify agent again for ${round.id}` : `Notify agent for ${round.id}`}
      </Button>
      {sent && !error && <p role="status">Notification sent for {round.id}.</p>}
      {error && (
        <p role="alert">
          {round.id} remains published. {error} Retry keeps the same command identity; no notification is resent automatically.
        </p>
      )}
    </article>
  )
}
