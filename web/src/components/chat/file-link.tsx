import { useContext, useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { FileIcon, FolderIcon, LoaderIcon } from 'lucide-react'
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover'
import { useChat } from '@/lib/queries'
import { lookupFileReference, referenceExists, workspaceFilePath, type FileReference, type FileTarget } from '@/lib/file-references'
import { beginFileOpen, getWorkspaceFiles, isCurrentFileOpen, openWorkspaceFile, reviewKey, workspaceDocumentURL } from '@/lib/review'
import { ItemContext } from './item-context'
import { copyText } from '@/lib/clipboard'

export function FileLink({ reference, children }: { reference: FileReference; children: ReactNode }) {
  const item = useContext(ItemContext)
  // Markdown is also used outside an item conversation. Do not turn a local
  // file URL into browser navigation when there is no owning workspace.
  return item ? <WorkspaceFileLink item={item} reference={reference}>{children}</WorkspaceFileLink> : <span>{children}</span>
}

// rootedOutside reports an absolute or home path outside the workspace, or
// any such path when the item has no checkout.
export function rootedOutside(path: string, workspace: string | undefined) {
  if (!/^(?:~\/|\/|[A-Za-z]:\/)/.test(path.replaceAll('\\', '/'))) return false
  return !workspace || workspaceFilePath(path, workspace) === null
}

function WorkspaceFileLink({ item, reference, children }: { item: string; reference: FileReference; children: ReactNode }) {
  const client = useQueryClient()
  const { data: chat } = useChat(item)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [menu, setMenu] = useState(false)
  const [matches, setMatches] = useState<FileTarget[]>([])
  const label = reference.path + (reference.line ? reference.endLine ? `#L${reference.line}${reference.column ? `C${reference.column}` : ''}-L${reference.endLine}` : `:${reference.line}${reference.column ? `:${reference.column}` : ''}` : '')
  const claim = chat?.item.checkout.claim
  // The checkout's file list, one read shared by every path in the
  // conversation and by opening one; it lists names, never reads files.
  const { data: files } = useQuery({ queryKey: [...reviewKey(item), 'files'], queryFn: ({ signal }) => getWorkspaceFiles(item, signal), enabled: !!claim?.path, staleTime: 3000 })
  // A rooted path names a file only inside this item's checkout; elsewhere,
  // such as an API route like /v1/status, it stays plain code rather than a
  // link that can only fail. So does a path the checkout does not have, once
  // its complete list says so; until then, or when the list is truncated,
  // it stays a link.
  if (rootedOutside(reference.path, claim?.path)) return <>{children}</>
  const listed = files && !files.truncated && files.claim_id === claim?.id && files.claim_generation === claim?.generation
  if (listed && claim?.path && !referenceExists(reference, claim.path, files.paths ?? [])) return <>{children}</>
  const documentPath = /\.pdf$/i.test(reference.path) && reference.path.includes('/') && claim?.path ? workspaceFilePath(reference.path, claim.path) : null
  if (documentPath && claim?.id && claim.generation) {
    return <a href={workspaceDocumentURL(item, documentPath, { claim_id: claim.id, claim_generation: claim.generation })} target="_blank" rel="noopener noreferrer" data-file-link={reference.path} title={`Open ${label} in a new tab`} className="inline-flex max-w-full items-baseline gap-1 rounded border border-sky-500/25 bg-sky-500/10 px-1.5 py-0.5 text-sky-300 hover:bg-sky-500/20 [&_code]:bg-transparent [&_code]:p-0">
      <FileIcon className="size-3 shrink-0 self-center" aria-hidden /><span className="min-w-0 truncate">{children}</span>
    </a>
  }
  const open = async () => {
    const request = beginFileOpen(item)
    setMenu(false)
    setError('')
    setMatches([])
    const claim = chat?.item.checkout.claim
    const workspace = claim?.path
    if (!workspace) { setError('This item has no workspace checkout.'); return }
    setPending(true)
    try {
      const files = await client.fetchQuery({ queryKey: [...reviewKey(item), 'files'], queryFn: () => getWorkspaceFiles(item), staleTime: 3000 })
      if (!isCurrentFileOpen(item, request)) return
      if ((claim?.id && claim.id !== files.claim_id) || (claim?.generation && claim.generation !== files.claim_generation)) {
        void client.invalidateQueries({ queryKey: [...reviewKey(item), 'files'] })
        throw new Error('The workspace changed. Click the reference again after it refreshes.')
      }
      const found = lookupFileReference(reference, workspace, files.paths ?? [], files.truncated)
      if (found.length === 1) openWorkspaceFile(item, found[0], request)
      else { setMatches(found); setMenu(true) }
    } catch (error) {
      if (isCurrentFileOpen(item, request)) setError((error as Error).message)
    } finally { setPending(false) }
  }
  const copy = async (text: string) => {
    try { await copyText(text); setMenu(false) }
    catch { setError('The browser could not copy this path.'); setMenu(false) }
  }
  return <Popover open={menu} onOpenChange={setMenu}>
    <PopoverAnchor asChild>
      <button type="button" data-file-link={reference.path} aria-label={`Open ${label}`} aria-busy={pending || undefined}
        title={error || `Open ${label} in this item's workspace`} onClick={() => void open()}
        onContextMenu={event => { event.preventDefault(); setMenu(true) }}
        className="inline-flex max-w-full items-baseline gap-1 rounded border border-sky-500/25 bg-sky-500/10 px-1.5 py-0.5 align-baseline text-sky-300 hover:bg-sky-500/20 [&_code]:bg-transparent [&_code]:p-0">
        {pending ? <LoaderIcon className="size-3 shrink-0 self-center animate-spin" aria-hidden /> : reference.path.endsWith('/') ? <FolderIcon className="size-3 shrink-0 self-center" aria-hidden /> : <FileIcon className="size-3 shrink-0 self-center" aria-hidden />}
        <span className="min-w-0 truncate">{children}</span>
      </button>
    </PopoverAnchor>
    {error && <span role="status" className="ms-1 text-xs text-amber-300">{error}</span>}
    <PopoverContent align="start" className="max-h-80 w-80 max-w-[90vw] gap-1 overflow-auto">
      {!!matches.length && <>
        <p className="px-2 py-1 text-xs text-muted-foreground">Choose a file</p>
        {matches.map(target => <button type="button" key={target.path} className="rounded px-2 py-1.5 text-left font-mono text-xs break-all hover:bg-muted"
          onClick={() => { openWorkspaceFile(item, target); setMenu(false) }}>{target.path}</button>)}
      </>}
      <button type="button" className="rounded px-2 py-1.5 text-left hover:bg-muted" onClick={() => void open()}>Open in file panel</button>
      <button type="button" className="rounded px-2 py-1.5 text-left hover:bg-muted" onClick={() => void copy(reference.path)}>Copy path</button>
      <button type="button" className="rounded px-2 py-1.5 text-left hover:bg-muted" onClick={() => void copy(label)}>Copy reference</button>
    </PopoverContent>
  </Popover>
}
