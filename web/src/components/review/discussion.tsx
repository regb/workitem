import { useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { ReviewIntent } from './review-intent'
import { RoundNotify } from './round-notify'
import { newClientID } from '@/lib/session'
import { reviewKey, reviewUI, setCommentBuffer, setPanel, type CheckpointSelection } from '@/lib/review'
import {
  commentBufferKey,
  comparisonAnchor,
  getReviewDrafts,
  getReviewThreads,
  mutateReview,
  reviewDiscussionKey,
  sameComparison,
  type CommentBuffer,
  type ReviewDraft,
  type ReviewMutation,
  type ReviewThread,
} from '@/lib/review-discussion'

type Attempt = { action: Parameters<typeof mutateReview>[1]; request: object; complete?: (value: ReviewMutation) => void }
export function Discussion({ id, selected, paths, path, commit = '' }: { id: string; selected: CheckpointSelection; paths: string[]; path: string; commit?: string }) {
  const client = useQueryClient()
  const key = reviewDiscussionKey(id)
  const threads = useQuery({ queryKey: [...key, 'threads'], queryFn: ({ signal }) => getReviewThreads(id, signal), refetchInterval: 5000, retry: false })
  const drafts = useQuery({ queryKey: [...key, 'drafts'], queryFn: ({ signal }) => getReviewDrafts(id, signal), refetchInterval: 5000, retry: false })
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  const [notice, setNotice] = useState('')
  const attempt = useRef<Attempt | undefined>(undefined)
  const busy = useRef(false)
  const coherent = !!threads.data && !!drafts.data && threads.data.version === drafts.data.version
  const locked = pending || !!error || !coherent
  const visibleDrafts = (drafts.data?.drafts ?? []).filter(draft => sameComparison(draft, selected))
  const refresh = () => {
    void client.invalidateQueries({ queryKey: reviewKey(id) })
  }
  const execute = async () => {
    const sent = attempt.current
    if (!sent || busy.current) return
    busy.current = true
    setPending(true)
    setError('')
    try {
      const outcome = await mutateReview(id, sent.action, sent.request)
      if (outcome.error) throw new Error(outcome.error)
      if (!outcome.result) throw new Error('Review response was incomplete. Retry the same operation.')
      sent.complete?.(outcome.result)
      setNotice(outcome.result.round_id ? `Published ${outcome.result.round_id}.` : outcome.result.draft_id ? `Saved ${outcome.result.draft_id}. Publish feedback when ready.` : 'Draft removed.')
      attempt.current = undefined
      refresh()
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error))
      refresh()
    } finally {
      busy.current = false
      setPending(false)
    }
  }
  const run = (sent: Attempt) => {
    if (attempt.current || !coherent) return
    attempt.current = sent
    void execute()
  }
  const fences = () => ({ operation_id: newClientID(), expected_version: threads.data?.version ?? 0 })
  const save = (buffer: CommentBuffer, bufferKey: string, thread?: string, resolution?: string) => {
    const start = buffer.start ? Number(buffer.start) : 0
    const end = buffer.end ? Number(buffer.end) : 0
    if (![start, end].every(value => Number.isInteger(value) && value >= 0 && value <= 0xffffffff)) {
      setError('Use valid one-based line numbers.')
      return
    }
    const anchor = { ...comparisonAnchor(selected), ...(!thread && buffer.path ? { path: buffer.path, side: buffer.side, start_line: start, end_line: end } : {}) }
    run({
      action: 'review-draft',
      request: { ...fences(), draft: { thread_id: thread, anchor, body: buffer.body, resolution } },
      complete: () => {
        if (JSON.stringify(reviewUI.get().comments.get(bufferKey)) === JSON.stringify(buffer)) setCommentBuffer(bufferKey)
      },
    })
  }
  const locateDraft = (draft: ReviewDraft) => setPanel(id, { checkpoint: { revision: draft.anchor.revision_id, fromRevision: draft.anchor.from_revision_id }, path: draft.anchor.path ?? '' })
  return (
    <section aria-label="Revision feedback" className="space-y-4 border-t p-4 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="font-medium">Review {selected.revision}</h2>
        <span className="text-xs text-muted-foreground">{threads.data?.rounds?.length ?? 0} published rounds</span>
      </div>
      {(threads.error || drafts.error) && <p role="alert">{threads.error?.message ?? drafts.error?.message}</p>}
      {!coherent && !threads.error && !drafts.error && (
        <p role="status" className="text-xs text-muted-foreground">
          Refreshing review state…
        </p>
      )}
      {notice && (
        <p role="status" className="text-xs text-muted-foreground">
          {notice}
        </p>
      )}
      {error && (
        <div role="alert" className="space-y-2">
          <p>{error}</p>
          {attempt.current && (
            <Button size="sm" variant="outline" disabled={pending} onClick={() => void execute()}>
              Retry review operation
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            disabled={pending}
            onClick={() => {
              attempt.current = undefined
              setError('')
              refresh()
            }}
          >
            Keep text and refresh
          </Button>
        </div>
      )}
      {!!threads.data?.rounds?.length && (
        <details>
          <summary>Published rounds ({threads.data.rounds.length})</summary>
          <div className="space-y-2 py-2">
            {threads.data.rounds.map(round => (
              <RoundNotify key={round.id} id={id} round={round} />
            ))}
          </div>
        </details>
      )}
      <ReviewIntent id={id} selected={selected} commit={commit} />
      <CommentEditor id={id} selected={selected} paths={paths} path={path} disabled={locked} onSave={save} />
      {!!drafts.data?.drafts?.length && (
        <section aria-label="Saved review drafts" className="space-y-2 rounded border p-3">
          <h3 className="font-medium">Saved drafts</h3>
          {drafts.data.drafts.map(draft => (
            <article key={draft.id} className="space-y-1 border-b py-2 last:border-0">
              <button className="text-xs underline" onClick={() => locateDraft(draft)}>
                {draft.id} at {draft.anchor.revision_id}
                {draft.thread_id ? `, ${draft.thread_id}` : ''}
              </button>
              {draft.body && <p className="whitespace-pre-wrap break-words">{draft.body}</p>}
              {draft.resolution && <p className="text-xs">Reviewer decision: {draft.resolution}</p>}
              <Button size="sm" variant="ghost" disabled={locked} onClick={() => run({ action: 'review-drafts/delete', request: { ...fences(), draft_ids: [draft.id] } })}>
                Discard {draft.id}
              </Button>
            </article>
          ))}
          <Button
            size="sm"
            disabled={locked || !visibleDrafts.length}
            onClick={() =>
              run({ action: 'review-round', request: { ...fences(), revision_id: selected.revision, from_revision_id: selected.fromRevision, draft_ids: visibleDrafts.map(draft => draft.id) } })
            }
          >
            Publish feedback ({visibleDrafts.length})
          </Button>
          <p className="text-xs text-muted-foreground">
            Only drafts on this exact comparison publish together. A notification is sent once. Publication survives delivery failure. Use Notify agent on a published round, or wi review notify, to
            send explicitly.
          </p>
        </section>
      )}
      {(threads.data?.threads ?? []).map(thread => (
        <Thread key={thread.id} id={id} selected={selected} thread={thread} disabled={locked} onSave={save} />
      ))}
    </section>
  )
}
function CommentEditor({
  id,
  selected,
  paths,
  path,
  thread,
  disabled,
  onSave,
}: {
  id: string
  selected: CheckpointSelection
  paths: string[]
  path: string
  thread?: string
  disabled: boolean
  onSave: (buffer: CommentBuffer, key: string, thread?: string) => void
}) {
  const key = commentBufferKey(id, selected, thread)
  const buffer = reviewUI.use(state => state.comments.get(key)) ?? { body: '', path: thread ? '' : path, side: 'new', start: '', end: '' }
  const update = (patch: Partial<CommentBuffer>) => setCommentBuffer(key, { ...buffer, ...patch })
  return (
    <div className="space-y-2">
      {!thread && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <label>
            Comment on{' '}
            <select aria-label="Comment file" className="max-w-64 rounded border bg-background p-1" value={buffer.path} onChange={event => update({ path: event.target.value, start: '', end: '' })}>
              <option value="">Revision</option>
              {[...new Set([...paths, buffer.path])].filter(Boolean).map(path => (
                <option key={path} value={path}>
                  {path}
                </option>
              ))}
            </select>
          </label>
          {buffer.path && (
            <>
              <select aria-label="Comment side" className="rounded border bg-background p-1" value={buffer.side} onChange={event => update({ side: event.target.value })}>
                <option value="new">New side</option>
                <option value="old">Old side</option>
              </select>
              <input
                aria-label="Comment first line"
                className="w-20 rounded border px-2 py-1"
                inputMode="numeric"
                placeholder="File-level"
                value={buffer.start}
                onChange={event => update({ start: event.target.value })}
              />
              <input
                aria-label="Comment last line"
                className="w-20 rounded border px-2 py-1"
                inputMode="numeric"
                placeholder="Last line"
                value={buffer.end}
                onChange={event => update({ end: event.target.value })}
              />
            </>
          )}
        </div>
      )}
      <textarea
        aria-label={thread ? `Reply to ${thread}` : 'Review comment'}
        className="min-h-20 w-full rounded border bg-background p-2"
        value={buffer.body}
        onChange={event => update({ body: event.target.value })}
        placeholder={thread ? 'Reviewer follow-up…' : 'Feedback on this immutable revision…'}
      />
      <Button size="sm" variant="outline" disabled={disabled || !buffer.body.trim()} onClick={() => onSave(buffer, key, thread)}>
        Save {thread ? 'reply draft' : 'comment draft'}
      </Button>
    </div>
  )
}
function Thread({
  id,
  selected,
  thread,
  disabled,
  onSave,
}: {
  id: string
  selected: CheckpointSelection
  thread: ReviewThread
  disabled: boolean
  onSave: (buffer: CommentBuffer, key: string, thread?: string, resolution?: string) => void
}) {
  const a = thread.anchor
  const decision = (resolution: string) => onSave({ body: '', path: '', side: '', start: '', end: '' }, '', thread.id, resolution)
  return (
    <article aria-label={`Thread ${thread.id}`} className="space-y-3 rounded border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <button className="underline" onClick={() => setPanel(id, { checkpoint: { revision: a.revision_id, fromRevision: a.from_revision_id }, path: a.path ?? '' })}>
          {thread.id} anchored to {a.revision_id}
          {a.path ? ` · ${a.side} ${a.path}${a.start_line ? `:${a.start_line}-${a.end_line}` : ''}` : ''}
        </button>
        <span>{thread.status === 'addressed' ? 'Agent addressed, awaiting reviewer' : thread.status}</span>
      </div>
      {(thread.messages ?? []).map(message => (
        <div key={message.id} className="space-y-1">
          <p className="text-xs text-muted-foreground">
            {message.role} · {message.revision_id}
            {message.addressed ? ' · reported addressed' : ''}
            {message.resolution ? ` · ${message.resolution}` : ''}
          </p>
          {message.body && <p className="whitespace-pre-wrap break-words">{message.body}</p>}
        </div>
      ))}
      <CommentEditor id={id} selected={selected} paths={[]} path="" thread={thread.id} disabled={disabled} onSave={onSave} />
      <div className="flex flex-wrap gap-1">
        {thread.status !== 'resolved' && (
          <Button size="sm" variant="ghost" disabled={disabled} onClick={() => decision('resolved')}>
            Draft resolution
          </Button>
        )}
        {thread.status !== 'open' && (
          <Button size="sm" variant="ghost" disabled={disabled} onClick={() => decision('open')}>
            Draft reopen
          </Button>
        )}
        {thread.status !== 'withdrawn' && (
          <Button size="sm" variant="ghost" disabled={disabled} onClick={() => decision('withdrawn')}>
            Draft withdrawal
          </Button>
        )}
      </div>
    </article>
  )
}
