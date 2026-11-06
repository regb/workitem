import { useState, type FormEvent } from 'react'
import { ExternalLinkIcon, FileIcon, GitForkIcon, PlusIcon, Trash2Icon } from 'lucide-react'
import { useConfirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, type Remote, type Repository } from '@/lib/api'
import { keys, queryClient } from '@/lib/queries'

// RepositoryGeneral is the repository's identity: its upstream, integration
// branch and ID.
export function RepositoryGeneral({ repository }: { repository: Repository }) {
  return (
    <dl className="grid grid-cols-[auto_1fr] items-center gap-x-6 gap-y-3 text-sm" data-repository={repository.id}>
      <dt className="text-muted-foreground">Name</dt>
      <dd>{repository.name}</dd>
      <dt className="text-muted-foreground">Upstream</dt>
      <dd className="min-w-0">{repository.remote ? <RemoteLine remote={repository.remote} /> : <span className="text-muted-foreground">None: local only</span>}</dd>
      <dt className="text-muted-foreground">Integration branch</dt>
      <dd className="font-mono">{repository.integration_branch}</dd>
      <dt className="text-muted-foreground">ID</dt>
      <dd className="font-mono text-xs break-all text-muted-foreground">{repository.id}</dd>
    </dl>
  )
}

// RepositoryFiles lists the files Wi copies into each of the repository's
// checkouts, and adds or removes them, as wi repo files does.
export function RepositoryFiles({ repository }: { repository: Repository }) {
  const confirm = useConfirm()
  const [adding, setAdding] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState<{ error?: string; warnings?: string[] }>({})
  const change = async (path: string, body: Record<string, unknown>) => {
    setBusy(true)
    setMessage({})
    try {
      const out = await api<{ warnings?: string[] }>(`/api/projects/${repository.id}/repository-files${path}`, body)
      setMessage({ warnings: out.warnings })
      setAdding(false)
      await queryClient.invalidateQueries({ queryKey: keys.repositories })
      void queryClient.invalidateQueries({ queryKey: keys.repositorySetup(repository.id) })
    } catch (failure) {
      setMessage({ error: (failure as Error).message })
    } finally {
      setBusy(false)
    }
  }
  const remove = async (path: string) => {
    const ok = await confirm({
      title: `Remove ${path}?`,
      description: 'Checkouts no item claims lose their copy now, the others at their next claim. A copy edited since it was placed is kept.',
      action: 'Remove',
    })
    if (ok) await change('/remove', { path })
  }
  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">
        Untracked files, such as a <code>.env</code>, that Wi keeps instead of Git and copies into every new checkout, where Git ignores them. A file the branch tracks at the same path wins.
      </p>
      {repository.files.length ? (
        <ul className="divide-y rounded-lg border text-sm">
          {repository.files.map(file => (
            <li key={file.path} className="flex items-center gap-2 px-3 py-2" data-file={file.path}>
              <FileIcon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 truncate font-mono">{file.path}</span>
              <span className="ms-auto shrink-0 text-xs text-muted-foreground tabular-nums">{file.size} bytes</span>
              <Button variant="ghost" size="icon-sm" aria-label={`Remove ${file.path}`} onClick={() => void remove(file.path)} disabled={busy}>
                <Trash2Icon />
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">No files.</p>
      )}
      {adding ? (
        <AddFile busy={busy} onCancel={() => setAdding(false)} onAdd={(path, content) => change('', { path, content })} />
      ) : (
        <Button variant="outline" size="sm" className="w-fit" onClick={() => setAdding(true)} disabled={busy}>
          <PlusIcon /> Add file
        </Button>
      )}
      {message.error && (
        <p role="alert" className="text-sm text-destructive">
          {message.error}
        </p>
      )}
      {message.warnings?.map(warning => (
        <p key={warning} className="text-sm text-amber-300">
          {warning}
        </p>
      ))}
    </div>
  )
}

// AddFile uploads a file to a path in the checkout; the path starts as the
// file's name.
function AddFile({ busy, onAdd, onCancel }: { busy: boolean; onAdd: (path: string, content: string) => Promise<void>; onCancel: () => void }) {
  const [file, setFile] = useState<File | null>(null)
  const [path, setPath] = useState('')
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!file) return
    await onAdd(path.trim(), await base64(file))
  }
  return (
    <form onSubmit={submit} className="flex flex-col gap-3 rounded-lg border p-3" aria-label="Add file">
      <label className="flex flex-col gap-1 text-sm">
        File
        <Input
          type="file"
          onChange={event => {
            const chosen = event.target.files?.[0] ?? null
            setFile(chosen)
            if (chosen && !path) setPath(chosen.name)
          }}
          disabled={busy}
        />
      </label>
      <label className="flex flex-col gap-1 text-sm">
        Path in the checkout
        <Input value={path} onChange={event => setPath(event.target.value)} placeholder=".env" className="font-mono" disabled={busy} required />
        <span className="text-xs text-muted-foreground">Relative to the repository root, such as .env or config/local.json. Adding a path that exists replaces its file.</span>
      </label>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={busy || !file || !path.trim()}>
          {busy ? 'Adding…' : 'Add file'}
        </Button>
      </div>
    </form>
  )
}

// base64 encodes a file's bytes, as the server takes them.
async function base64(file: Blob) {
  const bytes = new Uint8Array(await file.arrayBuffer())
  let binary = ''
  for (let at = 0; at < bytes.length; at += 0x8000) binary += String.fromCharCode(...bytes.subarray(at, at + 0x8000))
  return btoa(binary)
}

// RemoteLine names the upstream a repository follows, linking to its page
// on a known host.
export function RemoteLine({ remote }: { remote: Remote }) {
  const Icon = remote.host === 'github' ? GitHubMark : remote.host === 'gitlab' ? GitLabMark : GitForkIcon
  const label = (
    <>
      <Icon className="size-4 shrink-0" aria-hidden />
      <span className="min-w-0 truncate font-mono">{remote.url}</span>
      {remote.branch && <span className="shrink-0 text-xs text-muted-foreground">{remote.branch}</span>}
    </>
  )
  return remote.web_url ? (
    <a href={remote.web_url} target="_blank" rel="noreferrer" className="group flex min-w-0 items-center gap-2 text-sm hover:text-sky-300" title={`Open on ${remote.host}`} data-remote={remote.host}>
      {label}
      <ExternalLinkIcon className="size-3.5 shrink-0 text-muted-foreground group-hover:text-sky-300" aria-hidden />
    </a>
  ) : (
    <span className="flex min-w-0 items-center gap-2 text-sm" data-remote="">
      {label}
    </span>
  )
}

// Brand marks: Lucide no longer ships them.
function GitHubMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" fill="currentColor" className={className}>
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
    </svg>
  )
}

function GitLabMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" className={className}>
      <path d="M23.6 9.6l-.03-.09L20.3 1.01a.85.85 0 00-1.62.06l-2.2 6.78H7.52L5.32 1.07a.85.85 0 00-1.62-.06L.43 9.51l-.03.09a6.07 6.07 0 002.01 7.01l.01.01.03.02 4.98 3.73 2.47 1.87 1.5 1.13a1.01 1.01 0 001.22 0l1.5-1.13 2.47-1.87 5.01-3.75.01-.01a6.07 6.07 0 002-7.01z" />
    </svg>
  )
}
