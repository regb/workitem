import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { api, type Repository, type RepositorySetup } from '@/lib/api'
import { keys, queryClient, useRepositorySetup } from '@/lib/queries'

// RepositorySetupSettings edits how the repository's checkouts are set up:
// the setup command, whether the command the repository declares runs, the
// time limit, and which files are not copied. It sets the same repository
// settings as wi repo settings.
export function RepositorySetupSettings({ repository }: { repository: Repository }) {
  const { data, error } = useRepositorySetup(repository.id)
  // The form starts again from each saved revision; its outcome outlives it.
  const [message, setMessage] = useState<{ error?: string; saved?: boolean }>({})
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>
  return <SetupForm key={data.revision} repository={repository} setup={data} message={message} setMessage={setMessage} />
}

type Message = { error?: string; saved?: boolean }

function SetupForm({ repository, setup, message, setMessage }: { repository: Repository; setup: RepositorySetup; message: Message; setMessage: (message: Message) => void }) {
  const [run, setRun] = useState(setup.run)
  const [trusted, setTrusted] = useState(setup.trust === 'always')
  const [timeout, setTimeLimit] = useState(String(setup.timeout_seconds))
  const [skip, setSkip] = useState(new Set(setup.skip))
  const [saving, setSaving] = useState(false)
  const seconds = Number(timeout)
  const changed = run !== setup.run || trusted !== (setup.trust === 'always') || seconds !== setup.timeout_seconds || skip.size !== setup.skip.length || setup.skip.some(path => !skip.has(path))
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    setMessage({})
    try {
      const out = await api<{ result: RepositorySetup }>(`/api/projects/${repository.id}/setup`, {
        revision: setup.revision,
        run: run.trim(),
        trust: trusted ? 'always' : 'never',
        timeout_seconds: seconds,
        skip: [...skip],
      })
      queryClient.setQueryData(keys.repositorySetup(repository.id), out.result)
      setMessage({ saved: true })
    } catch (failure) {
      setMessage({ error: (failure as Error).message })
    } finally {
      setSaving(false)
    }
  }
  return (
    <form onSubmit={submit} className="flex flex-col gap-6" aria-label="Setup">
      <p className="text-sm text-muted-foreground">
        Every new checkout is set up before an agent starts in it: the repository&apos;s files are copied in, then the setup command runs there with <code>/bin/sh</code>. A failed setup keeps the
        agent from starting until you retry it.
      </p>
      <label className="flex flex-col gap-1.5 text-sm">
        <span className="font-medium">Setup command</span>
        <Textarea value={run} onChange={event => setRun(event.target.value)} placeholder={'cp "$WI_FILES/.env" .env && npm install'} rows={3} className="font-mono text-xs" disabled={saving} />
        <span className="text-xs text-muted-foreground">
          It runs in the checkout with <code>WI_FILES</code> (a private copy of the repository files), <code>WI_SLOT_REUSED</code> and the repository&apos;s environment variables. It replaces the
          command the repository declares.
        </span>
      </label>
      <div className="flex flex-col gap-1.5 text-sm">
        <span className="font-medium">Command the repository declares</span>
        {setup.declared ? (
          <>
            <code className="rounded-md border bg-muted/30 px-2 py-1.5 font-mono text-xs break-all">{setup.declared}</code>
            <label className="flex items-center gap-2">
              <Checkbox checked={trusted} onCheckedChange={value => setTrusted(value === true)} disabled={saving} />
              Run it when no setup command is set here
            </label>
            <span className="text-xs text-muted-foreground">
              Read from <code>.config/wi.toml</code> on {repository.integration_branch}, so a branch cannot change it. It is code from the repository: run it only if you trust it.
            </span>
          </>
        ) : (
          <span className="text-muted-foreground">
            None: {repository.integration_branch} has no <code>[workspace.setup] run</code> in <code>.config/wi.toml</code>.
          </span>
        )}
      </div>
      <label className="flex flex-col gap-1.5 text-sm">
        <span className="font-medium">Time limit</span>
        <span className="flex items-center gap-2">
          <Input type="number" min={10} max={7200} value={timeout} onChange={event => setTimeLimit(event.target.value)} className="w-28" disabled={saving} /> seconds
        </span>
      </label>
      {repository.files.length > 0 && (
        <fieldset className="flex flex-col gap-1.5 text-sm">
          <legend className="mb-1.5 font-medium">Files to copy</legend>
          {repository.files.map(file => (
            <label key={file.path} className="flex items-center gap-2">
              <Checkbox
                checked={!skip.has(file.path)}
                onCheckedChange={value => {
                  const next = new Set(skip)
                  if (value === true) next.delete(file.path)
                  else next.add(file.path)
                  setSkip(next)
                }}
                disabled={saving}
              />
              <span className="font-mono text-xs">{file.path}</span>
            </label>
          ))}
          <span className="text-xs text-muted-foreground">A file not copied stays readable from $WI_FILES during setup.</span>
        </fieldset>
      )}
      <div className="flex items-center justify-end gap-3">
        {message.error && (
          <p role="alert" className="mr-auto text-sm text-destructive">
            {message.error}
          </p>
        )}
        {message.saved && !changed && !saving && <span className="text-sm text-muted-foreground">Saved. New checkouts use it.</span>}
        <Button type="submit" size="sm" disabled={!changed || saving || !(seconds >= 10 && seconds <= 7200)}>
          {saving ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}
