import { useState, type FormEvent } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { LoaderIcon, PlusIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, type Project } from '@/lib/api'
import { keys, queryClient } from '@/lib/queries'

// suggestedName is a repository name from its source: the last path part
// without .git.
export const suggestedName = (source: string) => (source.trim().replace(/\/+$/, '').split(/[/:]/).at(-1) ?? '').replace(/\.git$/, '').replace(/[^A-Za-z0-9._-]/g, '-')

// AddRepository imports a repository, as wi repo add does, then opens its
// settings. Cloning can take a while; the form waits for it.
export function AddRepository() {
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const [source, setSource] = useState('')
  const [name, setName] = useState('')
  const [branch, setBranch] = useState('')
  const [adding, setAdding] = useState(false)
  const [error, setError] = useState('')
  const shownName = name || suggestedName(source)
  if (!open)
    return (
      <Button variant="outline" size="sm" className="w-fit" onClick={() => setOpen(true)}>
        <PlusIcon /> Add repository
      </Button>
    )
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setAdding(true)
    setError('')
    try {
      const out = await api<{ result: Project }>('/api/repositories', { name: shownName, source: source.trim(), branch: branch.trim() || undefined })
      void queryClient.invalidateQueries({ queryKey: keys.repositories })
      void queryClient.invalidateQueries({ queryKey: keys.projects })
      void navigate({ to: '/repositories/$repo/settings/$section', params: { repo: out.result.id, section: 'general' } })
    } catch (failure) {
      setError((failure as Error).message)
    } finally {
      setAdding(false)
    }
  }
  return (
    <form onSubmit={submit} className="flex flex-col gap-3 rounded-lg border p-4" aria-label="Add repository">
      <label className="flex flex-col gap-1 text-sm">
        Source
        <Input
          value={source}
          onChange={event => setSource(event.target.value)}
          placeholder="git@github.com:owner/repo.git or /path/to/checkout"
          className="font-mono"
          autoFocus
          required
          disabled={adding}
        />
        <span className="text-xs text-muted-foreground">A Git URL, or the absolute path of a checkout on this machine. Wi imports it into its own store; the source is not changed.</span>
      </label>
      <div className="flex flex-col gap-3 sm:flex-row">
        <label className="flex flex-1 flex-col gap-1 text-sm">
          Name
          <Input value={shownName} onChange={event => setName(event.target.value)} placeholder="name" required disabled={adding} />
        </label>
        <label className="flex flex-1 flex-col gap-1 text-sm">
          Integration branch
          <Input value={branch} onChange={event => setBranch(event.target.value)} placeholder="the source's default" disabled={adding} />
        </label>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)} disabled={adding}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={adding || !source.trim() || !shownName}>
          {adding ? (
            <>
              <LoaderIcon className="animate-spin" /> Importing…
            </>
          ) : (
            'Add repository'
          )}
        </Button>
      </div>
    </form>
  )
}
