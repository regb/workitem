import { useMemo, useState, type ClipboardEvent } from 'react'
import { Link } from '@tanstack/react-router'
import { EyeIcon, EyeOffIcon, PlusIcon, RotateCwIcon, Trash2Icon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, itemAction, type Environment, type EnvironmentAgent, type EnvVar } from '@/lib/api'
import { keys, queryClient, useEnvironment } from '@/lib/queries'

type Row = EnvVar & { id: number; shown: boolean }

let nextRow = 0
const rows = (vars: EnvVar[]): Row[] => vars.map(v => ({ ...v, id: nextRow++, shown: false }))

const keyPattern = /^[A-Za-z_][A-Za-z0-9_]*$/
const reserved = (key: string) => key.startsWith('WI_') || key === 'TMUX' || key === 'TMUX_PANE'

// EnvironmentSettings edits a repository's environment variables as a list
// of names and values, which Wi sets for the agents and setup commands it
// starts in the repository's checkouts.
export function EnvironmentSettings({ project }: { project: string }) {
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        Wi sets these variables for every agent and setup command it starts in this repository&apos;s checkouts. An agent already running keeps the variables it started with until it restarts. Wi does
        not write them to a file.
      </p>
      <EnvironmentEditor key={project} project={project} />
      <StaleAgents project={project} />
    </div>
  )
}

function EnvironmentEditor({ project }: { project: string }) {
  const { data, error, isPending } = useEnvironment(project)
  const [edited, setEdited] = useState<Row[] | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const saved = useMemo(() => rows(data?.vars ?? []), [data])
  // The saved list shows until it is edited; edits stay until saved or
  // discarded.
  const current = edited ?? saved

  const problems = useMemo(() => {
    const counts = new Map<string, number>()
    for (const row of current) counts.set(row.key, (counts.get(row.key) ?? 0) + 1)
    // A blank row is ignored, not invalid.
    const problem = (row: Row) =>
      !row.key && !row.value
        ? ''
        : !keyPattern.test(row.key)
          ? 'Letters, digits and _, not starting with a digit'
          : reserved(row.key)
            ? 'Reserved for Wi and tmux'
            : counts.get(row.key)! > 1
              ? 'Set twice'
              : ''
    return new Map(current.map(row => [row.id, problem(row)]))
  }, [current])
  const invalid = [...problems.values()].some(Boolean)

  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>

  const update = (id: number, change: Partial<Row>) => setEdited(current.map(row => (row.id === id ? { ...row, ...change } : row)))
  // A new variable shows its value while it is typed.
  const add = () => setEdited([...current, { key: '', value: '', id: nextRow++, shown: true }])
  // Pasting a .env (several KEY=VALUE lines) into a name adds them all.
  const paste = (id: number, event: ClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData.getData('text')
    const pasted = parsePasted(text)
    if (!pasted.length || (!text.includes('\n') && !text.includes('='))) return
    event.preventDefault()
    const row = current.find(entry => entry.id === id)
    const next = row && !row.key && !row.value ? current.filter(entry => entry.id !== id) : [...current]
    for (const v of pasted) {
      const existing = next.findIndex(entry => entry.key === v.key)
      if (existing >= 0) next[existing] = { ...next[existing], value: v.value }
      else next.push(...rows([v]))
    }
    setEdited(next)
  }
  const save = async () => {
    setSaving(true)
    setSaveError('')
    try {
      const vars = current.filter(row => row.key || row.value).map(({ key, value }) => ({ key, value }))
      const out = await api<{ result: Environment }>(`/api/projects/${project}/env`, { vars, revision: data?.revision ?? 0 })
      queryClient.setQueryData(keys.environment(project), out.result)
      setEdited(null)
    } catch (failure) {
      setSaveError((failure as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex flex-col gap-3" data-environment={project}>
      {current.length ? (
        <ul className="flex flex-col gap-2">
          {current.map(row => (
            <li key={row.id} className="flex flex-col gap-1" data-var={row.key}>
              <div className="flex items-center gap-2">
                <Input
                  aria-label="Name"
                  placeholder="NAME"
                  value={row.key}
                  onChange={event => update(row.id, { key: event.target.value.trim() })}
                  onPaste={event => paste(row.id, event)}
                  aria-invalid={!!problems.get(row.id) || undefined}
                  className="w-2/5 font-mono text-sm sm:w-56"
                  autoComplete="off"
                  spellCheck={false}
                />
                <Input
                  aria-label={`Value of ${row.key || 'new variable'}`}
                  placeholder="value"
                  type={row.shown ? 'text' : 'password'}
                  value={row.value}
                  onChange={event => update(row.id, { value: event.target.value })}
                  className="min-w-0 flex-1 font-mono text-sm"
                  autoComplete="off"
                  spellCheck={false}
                />
                <Button variant="ghost" size="icon-sm" aria-label={row.shown ? `Hide ${row.key}` : `Show ${row.key}`} onClick={() => update(row.id, { shown: !row.shown })}>
                  {row.shown ? <EyeOffIcon /> : <EyeIcon />}
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label={`Remove ${row.key || 'variable'}`} onClick={() => setEdited(current.filter(entry => entry.id !== row.id))}>
                  <Trash2Icon />
                </Button>
              </div>
              {problems.get(row.id) && <span className="text-xs text-destructive">{problems.get(row.id)}</span>}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">No variables yet.</p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" onClick={() => add()}>
          <PlusIcon /> Add variable
        </Button>
        <span className="text-xs text-muted-foreground">Tip: paste a whole .env into a name to add every line.</span>
        <span className="ms-auto flex gap-2">
          {edited && (
            <Button variant="ghost" size="sm" onClick={() => setEdited(null)} disabled={saving}>
              Discard
            </Button>
          )}
          <Button size="sm" onClick={save} disabled={!edited || invalid || saving}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
        </span>
      </div>
      {saveError && (
        <p role="alert" className="text-sm text-destructive">
          {saveError}
        </p>
      )}
    </div>
  )
}

// StaleAgents lists the agents still running with the variables as they were
// before the last change, and restarts the idle ones. A working agent is
// left alone; restart it once it finishes.
function StaleAgents({ project }: { project: string }) {
  const { data } = useEnvironment(project)
  const [restarting, setRestarting] = useState(false)
  const [failures, setFailures] = useState<{ title: string; message: string }[]>([])
  const { stale, idle, summary, action } = staleAgents(data?.agents ?? [])
  if (!stale.length && !failures.length) return null
  const restart = async (agents: EnvironmentAgent[]) => {
    setRestarting(true)
    setFailures([])
    const failed: { title: string; message: string }[] = []
    // One at a time: each waits for its agent to be ready again.
    for (const agent of agents) {
      try {
        await itemAction(agent.id, 'restart')
      } catch (failure) {
        failed.push({ title: agent.title, message: (failure as Error).message })
      }
    }
    setFailures(failed)
    setRestarting(false)
    void queryClient.invalidateQueries({ queryKey: keys.environment(project) })
  }
  return (
    <section aria-label="Agents using previous variables" className="flex flex-col gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm">
      {!!stale.length && (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <span className="min-w-0 flex-1">{summary}</span>
            <Button size="sm" variant="outline" disabled={!idle.length || restarting} onClick={() => void restart(idle)}>
              <RotateCwIcon className={restarting ? 'animate-spin' : undefined} />
              {restarting ? 'Restarting…' : action}
            </Button>
          </div>
          <ul className="flex flex-col gap-1">
            {stale.map(agent => (
              <li key={agent.id} data-stale-agent={agent.id} className="flex items-center gap-2">
                <Link to="/items/$id" params={{ id: agent.id }} className="min-w-0 truncate hover:underline">
                  {agent.title}
                </Link>
                {agent.busy && <span className="shrink-0 text-xs text-muted-foreground">working; restart it once it finishes</span>}
              </li>
            ))}
          </ul>
        </>
      )}
      {failures.map(failure => (
        <p key={failure.title} role="alert" className="text-destructive">
          Could not restart {failure.title}: {failure.message}
        </p>
      ))}
    </section>
  )
}

// staleAgents picks the agents that still run with the previous variables,
// the idle ones a click can restart, and how to say so.
export function staleAgents(agents: EnvironmentAgent[]) {
  const stale = agents.filter(agent => agent.stale)
  const idle = stale.filter(agent => !agent.busy)
  const summary = `${stale.length === 1 ? '1 running agent still uses' : `${stale.length} running agents still use`} the previous variables.`
  const action = idle.length === stale.length ? (stale.length === 1 ? 'Restart it' : 'Restart them') : `Restart ${idle.length} idle`
  return { stale, idle, summary, action }
}

// parsePasted reads pasted KEY=VALUE lines, as a .env holds them.
export function parsePasted(text: string): EnvVar[] {
  const vars: EnvVar[] = []
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim().replace(/^export\s+/, '')
    if (!line || line.startsWith('#')) continue
    const at = line.indexOf('=')
    if (at <= 0) continue
    const key = line.slice(0, at).trim()
    let value = line.slice(at + 1).trim()
    if (value.length >= 2 && (value[0] === '"' || value[0] === "'") && value.endsWith(value[0])) value = value.slice(1, -1)
    if (keyPattern.test(key)) vars.push({ key, value })
  }
  return vars
}
