import { ApprovalKind } from '@/gen/wi/core/v1/reviews_pb'
import { enumWord } from '@/lib/enums'
import { useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { getIntent, setIntent } from '@/lib/review-rpc'
import { newClientID } from '@/lib/session'
import type { ReviewMutation } from '@/lib/review-discussion'
import { reviewKey, type CheckpointSelection } from '@/lib/review'
import { rpcMessage } from '@/lib/rpc'

type Intent = { version?: number; approved?: boolean; approvals?: { id: string; kind: ApprovalKind; revision_id: string }[] }
export function ReviewIntent({ id, selected, commit }: { id: string; selected: CheckpointSelection; commit: string }) {
  const client = useQueryClient()
  const intent = useQuery({
    queryKey: [...reviewKey(id), 'intent', selected.revision],
    queryFn: ({ signal }): Promise<Intent> => getIntent(id, selected.revision, signal),
    refetchInterval: 5000,
    retry: false,
  })
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const attempt = useRef<object | undefined>(undefined)
  const busy = useRef(false)
  const send = async () => {
    if (!attempt.current || busy.current) return
    busy.current = true
    setPending(true)
    setError('')
    try {
      const outcome: { result: ReviewMutation } = await setIntent(id, attempt.current)
      if (!outcome.result) throw new Error('Decision response incomplete; retry the same operation.')
      attempt.current = undefined
      setNotice('Reviewer intent recorded for this revision.')
      void client.invalidateQueries({ queryKey: reviewKey(id) })
    } catch (error) {
      setError(rpcMessage(error))
    } finally {
      busy.current = false
      setPending(false)
    }
  }
  const decide = (kind: ApprovalKind) => {
    if (attempt.current || !intent.data) return
    attempt.current = { operation_id: newClientID(), revision_id: selected.revision, kind, expected_version: intent.data.version ?? 0 }
    void send()
  }
  const ready = !pending && !error && !!commit && !!intent.data
  const latest = intent.data?.approvals?.at(-1)
  return (
    <section aria-label="Reviewer intent" className="space-y-2 rounded border p-3 text-xs">
      <p>{intent.data?.approved ? 'Approved revision.' : latest ? `Reviewer intent: ${enumWord(ApprovalKind, latest.kind)}` : 'Checkpoint capture does not request review or grant approval.'}</p>
      <p className="text-muted-foreground">Approval is optional and never blocks merge or close.</p>
      <div className="flex flex-wrap gap-1">
        <Button size="sm" variant="outline" disabled={!ready} onClick={() => decide(ApprovalKind.REQUEST_REVIEW)}>
          Request review
        </Button>
        <Button size="sm" variant="outline" disabled={!ready || !!intent.data?.approved} onClick={() => decide(ApprovalKind.APPROVE)}>
          Approve revision
        </Button>
        <Button size="sm" variant="ghost" disabled={!ready} onClick={() => decide(ApprovalKind.REQUEST_CHANGES)}>
          Request changes
        </Button>
        {intent.data?.approved && (
          <Button size="sm" variant="ghost" disabled={!ready} onClick={() => decide(ApprovalKind.WITHDRAW_APPROVAL)}>
            Withdraw approval
          </Button>
        )}
      </div>
      {notice && <p role="status">{notice}</p>}
      {intent.error && <p role="alert">{intent.error.message}</p>}
      {error && (
        <div role="alert">
          <p>{error}</p>
          <Button size="sm" variant="outline" disabled={pending} onClick={() => void send()}>
            Retry decision
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={pending}
            onClick={() => {
              attempt.current = undefined
              setError('')
              void client.invalidateQueries({ queryKey: reviewKey(id) })
            }}
          >
            Refresh decision context
          </Button>
        </div>
      )}
    </section>
  )
}
