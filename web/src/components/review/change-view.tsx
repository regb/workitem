import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { Chat } from '@/lib/views'
import { defaultPanel, getCommits, getReview, getReviewFile, reviewKey, reviewUI, setPanel, type Review } from '@/lib/review'
import { Button } from '@/components/ui/button'
import { DiffView } from './code-view'
import { ChangeHeader } from './change-header'
import { PathTree } from './path-tree'
import { Discussion } from './discussion'

// ChangeView reviews one revision against a base: a one-line header, the
// changed files, then the diff, which gets the rest of the space. Merging is
// a dialog behind the header's Merge… button; there is no commit form here.
export function ChangeView({ chat, active, refresh }: { chat: Chat; active: boolean; refresh: () => void }) {
  const id = chat.item.id
  const state = reviewUI.use(value => value.panels.get(id) ?? defaultPanel)
  const { comparison, commit, baseCommit, path, split, checkpoint } = state
  const hasCheckout = !!chat.item.checkout.claim
  const review = useQuery({
    queryKey: [...reviewKey(id), 'changes', comparison, commit, baseCommit, checkpoint],
    queryFn: ({ signal }) => getReview(id, comparison, commit, signal, baseCommit, checkpoint),
    enabled: active && (!!checkpoint || comparison !== 'commit' || !!commit),
    refetchInterval: checkpoint ? false : 5000,
    retry: false,
  })
  const history = useQuery({ queryKey: [...reviewKey(id), 'commits'], queryFn: ({ signal }) => getCommits(id, signal), enabled: active, refetchInterval: 10000, retry: false })
  const paths = useMemo(() => review.data?.files?.map(file => file.path) ?? [], [review.data?.files])
  const selected = paths.includes(path) ? path : (paths[0] ?? '')
  const treeHeight = Math.min(180, Math.max(64, paths.length * 28 + 16))
  return (
    <section aria-label="Change review" hidden={!active} className={active ? 'flex min-h-0 flex-1 flex-col overflow-y-auto' : 'hidden'}>
      {/* The header stays mounted across tabs, keeping a pending merge or capture. */}
      <ChangeHeader chat={chat} active={active} history={history.data} review={review.data} onMerged={refresh} />
      {active && (
        <>
          {history.error && <p className="px-3 py-1 text-xs text-amber-300">Commit history unavailable: {history.error.message}</p>}
          {review.error ? (
            <p role="status" className="p-4 text-sm text-muted-foreground">
              {review.error.message}
            </p>
          ) : review.isPending ? (
            <p role="status" className="p-4 text-sm text-muted-foreground">
              Loading change…
            </p>
          ) : (
            <>
              <section aria-label="Changed files" className="shrink-0 border-b">
                <h2 className="px-3 pt-2 text-xs font-medium text-muted-foreground">
                  {paths.length} {paths.length === 1 ? 'file' : 'files'} changed{review.data?.truncated ? ' (list limited to 10,000)' : ''}
                </h2>
                {paths.length ? (
                  <div style={{ height: treeHeight }}>
                    <PathTree paths={paths} selectedPath={selected} changes={review.data?.files} expanded onSelect={path => setPanel(id, { path })} />
                  </div>
                ) : (
                  <p className="px-3 pb-3 text-sm text-muted-foreground">
                    {comparison === 'integration' && !baseCommit && !checkpoint
                      ? `Everything on this branch is already in ${review.data?.integration_branch || 'the integration branch'}.`
                      : 'No changes between these revisions.'}
                  </p>
                )}
              </section>
              {review.data && selected && (
                <section aria-label="File diff" className="flex min-h-[28rem] flex-1 flex-col">
                  <ChangedFile
                    key={`${id}:${comparison}:${commit}:${baseCommit}:${JSON.stringify(checkpoint)}:${selected}`}
                    id={id}
                    path={selected}
                    review={review.data}
                    canOpen={hasCheckout}
                    split={split}
                  />
                </section>
              )}
              {checkpoint && <Discussion key={`${id}:${JSON.stringify(checkpoint)}`} id={id} selected={checkpoint} paths={paths} path={selected} commit={review.data?.revision.head_commit ?? ''} />}
            </>
          )}
        </>
      )}
    </section>
  )
}

function ChangedFile({ id, path, review, split, canOpen }: { id: string; path: string; review: Review; split: boolean; canOpen: boolean }) {
  const { comparison, commit, baseCommit, checkpoint } = reviewUI.use(state => state.panels.get(id) ?? defaultPanel)
  const result = useQuery({
    queryKey: [...reviewKey(id), 'diff', comparison, commit, baseCommit, review.revision.version, path, checkpoint],
    queryFn: ({ signal }) => getReviewFile(id, comparison, commit, path, review.revision.version, signal, baseCommit, checkpoint),
    refetchInterval: review.revision.working_tree ? 5000 : false,
    retry: false,
  })
  return (
    <>
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-1.5">
        <span className="min-w-0 flex-1 truncate font-mono text-xs" title={path}>
          {path}
        </span>
        <Button size="sm" variant="ghost" aria-pressed={split} onClick={() => setPanel(id, current => ({ split: !current.split }))}>
          {split ? 'Split' : 'Unified'}
        </Button>
        {canOpen && (
          <Button size="sm" variant="ghost" onClick={() => setPanel(id, { tab: 'files', path })}>
            {review.revision.working_tree ? 'Open file' : 'Open working file'}
          </Button>
        )}
      </div>
      {result.error ? (
        <p role="status" className="p-4 text-sm text-muted-foreground">
          {result.error.message} Refresh the comparison to retry.
        </p>
      ) : !result.data ? (
        <p role="status" className="p-4 text-sm text-muted-foreground">
          Loading diff…
        </p>
      ) : (
        <DiffView oldFile={result.data.old_file} newFile={result.data.new_file} split={split} />
      )}
    </>
  )
}
