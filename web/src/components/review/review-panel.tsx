import { useEffect, useMemo, useRef } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeftIcon, RefreshCwIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { Chat } from '@/lib/views'
import { refreshChat } from '@/lib/queries'
import { getCommits, getWorkspaceFiles, draftKey, defaultPanel, minChatWidth, maxChatWidth, setPanel, reviewKey, reviewUI, toggleReview } from '@/lib/review'
import { CodeProvider } from './code-view'
import { FileEditor } from './file-editor'
import { CommitHistory } from './commit-history'
import { ChangeView } from './change-view'
import { PathTree } from './path-tree'

const emptyPaths: string[] = []

export function ReviewPanel({ chat }: { chat: Chat }) {
  const id = chat.item.id
  const client = useQueryClient()
  const { tab, path, chatWidth, fileTarget, wrap } = reviewUI.use(state => state.panels.get(id) ?? defaultPanel)
  const panel = useRef<HTMLElement>(null)
  const hasCheckout = !!chat.item.checkout.claim
  const drafts = reviewUI.use(state => state.drafts)
  const draftPaths = useMemo(() => [...drafts.keys()].filter(key => key.startsWith(`${id}:`)).map(key => key.slice(id.length + 1)), [drafts, id])
  const files = useQuery({
    queryKey: [...reviewKey(id), 'files'],
    queryFn: ({ signal }) => getWorkspaceFiles(id, signal),
    enabled: hasCheckout && tab === 'files',
    refetchInterval: 5000,
    retry: false,
  })
  const history = useQuery({
    queryKey: [...reviewKey(id), 'commits'],
    queryFn: ({ signal }) => getCommits(id, signal),
    enabled: tab === 'history',
    refetchInterval: 10000,
    retry: false,
  })
  const paths = hasCheckout ? (files.data?.paths ?? emptyPaths) : draftPaths
  const selectedDraft = drafts.get(draftKey(id, path))
  const refresh = () => {
    void client.invalidateQueries({ queryKey: reviewKey(id) })
    void refreshChat(id)
  }
  useEffect(() => {
    void client.invalidateQueries({ queryKey: reviewKey(id) })
  }, [id, chat.status, client])
  return (
    <aside ref={panel} aria-label="Code review" className="relative flex min-h-0 min-w-0 w-full flex-1 flex-col border-l bg-background">
      <div
        role="separator"
        aria-label="Resize chat sidebar"
        aria-orientation="vertical"
        aria-valuemin={minChatWidth}
        aria-valuemax={maxChatWidth}
        aria-valuenow={chatWidth}
        aria-valuetext={`${chatWidth} pixels`}
        tabIndex={0}
        className="absolute inset-y-0 -left-1 z-20 hidden w-2 cursor-col-resize touch-none lg:block"
        onKeyDown={event => {
          if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
            event.preventDefault()
            setPanel(id, current => ({ chatWidth: current.chatWidth + (event.key === 'ArrowLeft' ? -16 : 16) }))
          } else if (event.key === 'Home' || event.key === 'End') {
            event.preventDefault()
            setPanel(id, { chatWidth: event.key === 'Home' ? minChatWidth : maxChatWidth })
          }
        }}
        onPointerDown={event => event.currentTarget.setPointerCapture(event.pointerId)}
        onPointerMove={event => {
          if (!event.currentTarget.hasPointerCapture(event.pointerId)) return
          const rect = panel.current?.parentElement?.getBoundingClientRect()
          if (rect) setPanel(id, { chatWidth: event.clientX - rect.left })
        }}
        onPointerUp={event => {
          if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
        }}
      />
      <div className="flex shrink-0 items-center gap-1 border-b px-2 py-1.5">
        {(['changes', 'files', 'history'] as const).map(value => (
          <Button
            key={value}
            size="sm"
            variant={tab === value ? 'secondary' : 'ghost'}
            onClick={() => {
              if (value !== tab) setPanel(id, { tab: value, path: '' })
            }}
          >
            {value === 'changes' ? 'Change' : value === 'files' ? 'Files' : 'History'}
          </Button>
        ))}
        <span className="flex-1" />
        <Button size="sm" variant={wrap ? 'secondary' : 'ghost'} aria-pressed={wrap} onClick={() => setPanel(id, { wrap: !wrap })}>
          Wrap lines
        </Button>
        <Button size="icon-sm" variant="ghost" aria-label="Refresh code" onClick={refresh}>
          <RefreshCwIcon />
        </Button>
        <Button size="sm" variant="ghost" className="lg:hidden" aria-label="Back to chat" onClick={() => toggleReview(id)}>
          <ArrowLeftIcon /> Chat
        </Button>
        <Button size="icon-sm" variant="ghost" className="hidden lg:inline-flex" aria-label="Close code panel" onClick={() => toggleReview(id)}>
          <XIcon />
        </Button>
      </div>
      {tab !== 'changes' && (
        <div className="shrink-0 truncate border-b px-3 py-2 font-mono text-xs text-muted-foreground" title={chat.item.checkout.specification.branch}>
          {chat.item.checkout.specification.branch}
        </div>
      )}
      {!!draftPaths.length && (
        <div className="flex flex-wrap items-center gap-1 border-b px-3 py-1 text-xs text-amber-300">
          Unsaved drafts:{' '}
          {draftPaths.map(path => (
            <button key={path} className="max-w-48 truncate rounded px-1.5 py-1 hover:bg-muted" title={path} onClick={() => setPanel(id, { tab: 'files', path })}>
              {path}
            </button>
          ))}
        </div>
      )}
      <CodeProvider wrap={wrap}>
        <ChangeView chat={chat} active={tab === 'changes'} refresh={refresh} />
        {tab === 'history' && (
          <div className="min-h-0 flex-1 overflow-auto">
            {history.error ? (
              <Problem text={history.error.message} />
            ) : history.isPending ? (
              <Problem text="Loading commits…" />
            ) : (
              <CommitHistory history={history.data ?? {}} onSelect={commit => setPanel(id, { commit, baseCommit: '', comparison: 'commit', path: '', tab: 'changes', checkpoint: undefined })} />
            )}
          </div>
        )}
        {tab === 'files' &&
          (!hasCheckout && !draftPaths.includes(path) ? (
            <Problem text="This item has no checkout. Committed changes and history remain available." />
          ) : (
            <div className="flex min-h-0 flex-1">
              <div className="w-40 min-w-28 shrink-0 overflow-auto border-r xl:w-52">
                {paths.length > 0 && <PathTree key={id} paths={paths} target={fileTarget} onSelect={path => setPanel(id, { path })} />}
                {files.data?.truncated && <Problem text="File list limited to 10,000 paths." />}
              </div>
              <div className="flex min-h-0 min-w-0 flex-1 flex-col">
                {files.error && !selectedDraft ? (
                  <Problem text={files.error.message} />
                ) : path && (files.data || selectedDraft) ? (
                  <FileEditor
                    key={`${id}:${path}`}
                    id={id}
                    path={path}
                    location={fileTarget?.path === path ? fileTarget : undefined}
                    claim={hasCheckout && files.data ? files.data : { claim_id: '', claim_generation: 0 }}
                    editable={hasCheckout && !!files.data && chat.item.state === 'active'}
                  />
                ) : (
                  <Problem text={files.isPending ? 'Loading files…' : 'Select a file to view or edit.'} />
                )}
              </div>
            </div>
          ))}
      </CodeProvider>
    </aside>
  )
}

function Problem({ text }: { text: string }) {
  return (
    <p role="status" className="p-4 text-sm text-muted-foreground">
      {text}
    </p>
  )
}
