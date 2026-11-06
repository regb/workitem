import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useConfirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { ApiError } from '@/lib/api'
import {
  acceptFileSave,
  draftKey,
  getWorkspaceFile,
  newFileDraft,
  reviewKey,
  reviewUI,
  saveFile,
  setFileDraft,
  sourceKey,
  workspaceDocumentURL,
  type FileDraft,
  type FileNavigation,
  type WorkspaceFile,
  type WorkspaceFiles,
} from '@/lib/review'
import { SourceView, unavailable } from './code-view'

export function FileEditor({ id, path, claim, editable, location }: { id: string; path: string; claim: WorkspaceFiles; editable: boolean; location?: FileNavigation }) {
  const client = useQueryClient()
  const confirm = useConfirm()
  const draft = reviewUI.use(state => state.drafts.get(draftKey(id, path)))
  const documentURL = /\.pdf$/i.test(path) && claim.claim_id ? workspaceDocumentURL(id, path, claim) : null
  const source = useQuery({
    queryKey: sourceKey(id, path, claim),
    queryFn: ({ signal }) => getWorkspaceFile(id, path, claim, signal),
    enabled: !!claim.claim_id && !draft && !documentURL,
    staleTime: 5000,
    refetchInterval: 5000,
    retry: false,
  })
  const [error, setError] = useState('')
  const [discarding, setDiscarding] = useState(false)
  const save = useMutation({
    onMutate: (sent: FileDraft) => client.cancelQueries({ queryKey: sourceKey(id, path, sent) }),
    mutationFn: (sent: FileDraft) => saveFile(id, path, sent),
    onSuccess: (result, sent) => {
      const file = result.result?.file
      if (!file) {
        setError('The save response did not contain the saved file. Your draft has been kept.')
        return
      }
      // Cancelled pre-save reads cannot replace this newer source snapshot.
      client.setQueryData<WorkspaceFile>(sourceKey(id, path, sent), { file, claim_id: sent.claim_id, claim_generation: sent.claim_generation })
      acceptFileSave(id, path, sent, file)
      setError('')
      void client.invalidateQueries({ queryKey: reviewKey(id), predicate: query => query.queryKey[2] !== 'source' })
    },
    onError: (error: Error) =>
      setError(
        !(error instanceof ApiError && error.outcome) ? 'The save could not be confirmed. Your draft has been kept. Check the file before retrying.' : `${error.message} Your draft has been kept.`,
      ),
  })
  const discard = async () => {
    if (
      draft &&
      !(await confirm({
        title: 'Discard unsaved changes?',
        description: `Reload ${path} from disk. This discards your local draft.`,
        action: 'Discard and reload',
        destructive: true,
      }))
    )
      return
    setDiscarding(true)
    try {
      if (claim.claim_id) {
        const result = await source.refetch()
        if (result.error) throw result.error
      }
      if (reviewUI.get().drafts.get(draftKey(id, path))?.session_id === draft?.session_id) setFileDraft(id, path)
      setError('')
    } catch (error) {
      setError(`Could not reload the file: ${(error as Error).message}. Your draft has been kept.`)
    } finally {
      setDiscarding(false)
    }
  }
  const busy = save.isPending || discarding
  const staleClaim = draft && (draft.claim_id !== claim.claim_id || draft.claim_generation !== claim.claim_generation)
  return (
    <div
      className="relative flex min-h-0 flex-1 flex-col"
      onKeyDownCapture={event => {
        if (busy) {
          event.preventDefault()
          event.stopPropagation()
          return
        }
        if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') {
          event.preventDefault()
          if (draft && !staleClaim && editable) save.mutate(draft)
        }
      }}
    >
      <div className="flex shrink-0 items-center gap-2 border-b px-3 py-2">
        <span className="min-w-0 flex-1 truncate font-mono text-xs" title={path}>
          {path}
          {location?.line ? `:${location.line}${location.column ? `:${location.column}` : ''}` : ''}
          {draft ? ' • unsaved' : ''}
        </span>
        {draft ? (
          <>
            <Button size="sm" variant="ghost" disabled={busy} onClick={discard}>
              Discard
            </Button>
            <Button size="sm" disabled={busy || !!staleClaim || !editable} onClick={() => save.mutate(draft)}>
              {save.isPending ? 'Saving…' : 'Save'}
            </Button>
          </>
        ) : documentURL ? (
          <a href={documentURL} target="_blank" rel="noopener noreferrer" className="inline-flex h-7 items-center rounded-md border px-2 text-xs hover:bg-muted">
            Open PDF in new tab
          </a>
        ) : (
          <Button
            size="sm"
            variant="outline"
            disabled={!editable || !source.data?.file.exists || !!unavailable(source.data.file)}
            onClick={() => {
              if (!source.data) return
              setFileDraft(id, path, newFileDraft(source.data))
              setError('')
            }}
          >
            Edit
          </Button>
        )}
      </div>
      {(error || source.error || staleClaim) && (
        <p role="alert" className="border-b p-3 text-sm text-destructive">
          {staleClaim ? 'This draft belongs to an older checkout claim. It cannot be saved here. Copy it before discarding.' : error || source.error?.message}
        </p>
      )}
      {draft ? (
        editable && !staleClaim ? (
          <DraftEditor
            key={`${draft.session_id}:${draft.file.sha256}`}
            draft={draft}
            location={location}
            onChange={content => {
              const current = reviewUI.get().drafts.get(draftKey(id, path))
              if (current?.session_id === draft.session_id) setFileDraft(id, path, { ...current, content })
            }}
          />
        ) : (
          <SourceView file={{ ...draft.file, content: draft.content, sha256: undefined }} location={location} />
        )
      ) : documentURL ? (
        <p className="p-4 text-sm text-muted-foreground">
          This PDF opens in your browser.{' '}
          <a href={`${documentURL}&download=1`} className="text-sky-300 underline">
            Download PDF
          </a>
        </p>
      ) : source.data ? (
        <SourceView file={source.data.file} location={location} />
      ) : source.isPending ? (
        <p className="p-4 text-sm">Loading source…</p>
      ) : null}
      {busy && <div className="absolute inset-0 z-10 cursor-wait" aria-hidden />}
    </div>
  )
}

// Pierre owns the active document. Only a new session installs external text;
// the read-only fallback above always shows the latest recoverable draft.
function DraftEditor({ draft, location, onChange }: { draft: FileDraft; location?: FileNavigation; onChange: (content: string) => void }) {
  const [file] = useState(() => ({ ...draft.file, content: draft.content, sha256: undefined }))
  return <SourceView file={file} location={location} edit onChange={onChange} />
}
