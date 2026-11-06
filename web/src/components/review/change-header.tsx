import { useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CameraIcon, LoaderIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Chat } from '@/lib/views'
import { baseOptions, baseValue, selectBase, selectTarget, targetOptions, targetValue, type ChangeContext, type ChangeOption } from '@/lib/change-selection'
import { captureRevision, defaultPanel, getReviewRevisions, reviewKey, reviewUI, setPanel, type Commits, type Review } from '@/lib/review'
import { rpcMessage } from '@/lib/rpc'
import { newClientID } from '@/lib/session'
import { MergeButton } from './merge-dialog'

// ChangeHeader is one line: what to review, what to compare it to, and the
// two actions, capture and merge. A status line follows only when there is
// something to say.
export function ChangeHeader({ chat, active, history, review, onMerged }: { chat: Chat; active: boolean; history?: Commits; review?: Review; onMerged: () => void }) {
  const id = chat.item.id
  const client = useQueryClient()
  const panel = reviewUI.use(state => state.panels.get(id) ?? defaultPanel)
  const [notice, setNotice] = useState('')
  const records = useQuery({
    queryKey: [...reviewKey(id), 'checkpoints'],
    queryFn: ({ signal }) => getReviewRevisions(id, signal),
    enabled: active,
    refetchInterval: active ? 5000 : false,
    retry: false,
  })
  const context: ChangeContext = {
    start: chat.item.repository?.created_from_commit ?? '',
    hasCheckout: !!chat.item.checkout.claim,
    history,
    revisions: records.data?.revisions ?? [],
    merges: records.data?.merges ?? [],
  }
  const reviewBase = review?.revision.base_commit ?? ''
  const targets = targetOptions(panel, context)
  const bases = baseOptions(panel, context, reviewBase)
  const selected = context.revisions.find(revision => revision.id === panel.checkpoint?.revision)
  const merged = context.merges.find(merge => merge.revision_id === selected?.id)
  const capture = useCapture(chat, revision => {
    setPanel(id, { checkpoint: { revision } })
    void client.invalidateQueries({ queryKey: reviewKey(id) })
  })
  const gaps = records.data?.capture_gaps ?? []
  return (
    <section aria-label="Change header" className="border-b">
      <div className="flex flex-wrap items-center gap-2 px-3 py-2">
        <span className="max-w-36 truncate font-mono text-xs text-muted-foreground" title={chat.item.checkout.specification.branch}>
          {chat.item.checkout.specification.branch}
        </span>
        <Picker label="Revision" value={targetValue(panel, context)} onChange={value => setPanel(id, selectTarget(value, panel, reviewBase))}>
          <Group label="Live" options={targets.live} />
          <Group label="Checkpoints" options={targets.checkpoints} />
          <Group label="Commits" options={targets.commits} />
        </Picker>
        <span className="text-xs text-muted-foreground">vs</span>
        <Picker label="Compared to" value={baseValue(panel, context, reviewBase)} onChange={value => setPanel(id, selectBase(value, panel, context))}>
          <Group options={bases} />
        </Picker>
        <span className="flex-1" />
        <Button
          size={capture.failed ? 'sm' : 'icon-sm'}
          variant="ghost"
          aria-label={capture.failed ? 'Retry checkpoint' : 'Capture checkpoint'}
          title="Capture a checkpoint of the checkout now, without a branch commit"
          disabled={capture.pending || chat.capturing || !chat.item.checkout.claim?.id || chat.item.state !== 'active' || records.isPending || records.isError}
          onClick={() => void capture.run()}
        >
          {capture.pending || chat.capturing ? <LoaderIcon className="animate-spin" /> : <CameraIcon />}
          {capture.failed && 'Retry checkpoint'}
        </Button>
        {!panel.checkpoint && (
          <MergeButton
            chat={chat}
            integrationBranch={review?.integration_branch}
            onMerged={notice => {
              setNotice(notice)
              onMerged()
            }}
          />
        )}
      </div>
      <div className="space-y-1 px-3 pb-2 text-xs text-muted-foreground empty:hidden">
        {chat.capturing && <p role="status">Capturing a snapshot of the checkout…</p>}
        {selected && (
          <p>
            {selected.id} is a saved snapshot ({selected.commit.slice(0, 12)}, from{' '}
            {selected.detached_head ? `detached HEAD at ${selected.observed_head.slice(0, 12)}` : selected.observed_branch || 'the checkout'}). It does not change with the checkout, and merging always
            merges the current work.
          </p>
        )}
        {merged && (
          <p>
            Merged into {merged.target_ref.replace('refs/heads/', '')} as {merged.commit.slice(0, 12)}.
          </p>
        )}
        {!!selected?.excluded?.length && (
          <details className="text-amber-300">
            <summary>Partial snapshot: {selected.excluded.length} files left out</summary>
            <ul>
              {selected.excluded.map(value => (
                <li key={value.path}>
                  {value.path}: {value.reason}
                </li>
              ))}
            </ul>
          </details>
        )}
        {!!gaps.length && (
          <details className="text-amber-300">
            <summary>
              {gaps.length} {gaps.length === 1 ? 'capture' : 'captures'} failed
            </summary>
            <ul>
              {gaps.map(gap => (
                <li key={gap.id}>{gap.reason}</li>
              ))}
            </ul>
          </details>
        )}
        {records.error && <p role="alert">Checkpoints unavailable: {records.error.message}</p>}
        {capture.failure && (
          <div role="alert" className="flex items-center gap-2 text-destructive">
            <span>{capture.failure}</span>
            <Button size="xs" variant="ghost" onClick={capture.reset}>
              Start another capture
            </Button>
          </div>
        )}
        {notice && (
          <p role="status" className="whitespace-pre-wrap text-foreground">
            {notice}
          </p>
        )}
      </div>
    </section>
  )
}

function Picker({ label, value, onChange, children }: { label: string; value: string; onChange: (value: string) => void; children: React.ReactNode }) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger size="sm" aria-label={label} data-value={value} className="max-w-60 font-mono text-xs">
        <SelectValue />
      </SelectTrigger>
      <SelectContent position="popper" align="start" className="max-h-80">
        {children}
      </SelectContent>
    </Select>
  )
}

function Group({ label, options }: { label?: string; options: ChangeOption[] }) {
  if (!options.length) return null
  return (
    <SelectGroup>
      {label && <SelectLabel>{label}</SelectLabel>}
      {options.map(option => (
        <SelectItem key={option.value} value={option.value} data-value={option.value} className="font-mono text-xs">
          {option.label}
          {option.detail && <span className="max-w-72 truncate font-sans text-muted-foreground">{option.detail}</span>}
        </SelectItem>
      ))}
    </SelectGroup>
  )
}

// useCapture captures a checkpoint now. A failed or uncertain capture is
// retried with the same identity and checkout fence, so a capture that did
// land is not taken twice; only "Start another capture" allocates a new one.
export function useCapture(chat: Chat, onCaptured: (revision: string) => void) {
  const id = chat.item.id
  const claim = chat.item.checkout.claim
  const [pending, setPending] = useState(false)
  const [failure, setFailure] = useState('')
  const attempt = useRef<{ item: string; claim: string; generation: number; id: string } | undefined>(undefined)
  const run = async () => {
    if (!claim?.id || !claim.generation || pending) return
    if (!attempt.current || attempt.current.item !== id || attempt.current.claim !== claim.id || attempt.current.generation !== claim.generation) {
      attempt.current = { item: id, claim: claim.id, generation: claim.generation, id: newClientID() }
    }
    setPending(true)
    setFailure('')
    try {
      const outcome = await captureRevision(id, { id: claim.id, generation: claim.generation }, attempt.current.id)
      if (!outcome.result) throw new Error('The checkpoint response was incomplete. Retry the same capture.')
      attempt.current = undefined
      onCaptured(outcome.result.revision.id)
    } catch (error) {
      setFailure(rpcMessage(error))
    } finally {
      setPending(false)
    }
  }
  const reset = () => {
    attempt.current = undefined
    setFailure('')
  }
  return { run, reset, pending, failure, failed: !!failure }
}
