import { useState, type FormEvent } from 'react'
import { Link } from '@tanstack/react-router'
import { PencilIcon, PlayIcon, PlusIcon, Trash2Icon } from 'lucide-react'
import { useConfirm } from '@/components/confirm'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { api, type Schedule } from '@/lib/api'
import { keys, queryClient, useAgentProfiles, useRepositories, useSchedules } from '@/lib/queries'

// when shows a schedule time in the browser's locale.
export const when = (at?: string) => (at ? new Date(at).toLocaleString(undefined, { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '')

// parseLabels reads a comma- or space-separated list of labels.
export const parseLabels = (text: string) =>
  text
    .split(/[\s,]+/)
    .map(label => label.trim())
    .filter(Boolean)

// ScheduleSettings lists the schedules the daemon keeps, the same list as
// wi schedule, and adds, edits, toggles, runs and removes them.
export function ScheduleSettings() {
  const { data, error } = useSchedules()
  const repositories = useRepositories().data ?? []
  const profiles = useAgentProfiles().data
  const confirm = useConfirm()
  const [editing, setEditing] = useState<string | null>(null)
  const [failure, setFailure] = useState('')
  const [notice, setNotice] = useState<{ item: string } | null>(null)
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>
  const repositoryName = (id: string) => repositories.find(repository => repository.id === id)?.name ?? id
  const profileLabel = (name: string) => {
    const profile = profiles?.profiles.find(entry => entry.name === name)
    return profile?.label || name
  }
  const attempt = async (act: () => Promise<unknown>) => {
    setFailure('')
    setNotice(null)
    try {
      await act()
      await queryClient.invalidateQueries({ queryKey: keys.schedules })
      return true
    } catch (problem) {
      setFailure((problem as Error).message)
      return false
    }
  }
  const save = async (schedule: Schedule) => {
    const ok = await attempt(() => api('/api/schedules', schedule))
    if (ok) setEditing(null)
    return ok
  }
  const run = (schedule: Schedule) =>
    attempt(async () => {
      const out = await api<{ result: { item_id: string } }>(`/api/schedules/${schedule.id}/run`, {})
      void queryClient.invalidateQueries({ queryKey: keys.list })
      setNotice({ item: out.result.item_id })
    })
  const remove = async (schedule: Schedule) => {
    const ok = await confirm({ title: 'Remove this schedule?', description: 'It stops firing. Items it already started are unaffected.', action: 'Remove' })
    if (ok) await attempt(() => api(`/api/schedules/${schedule.id}/delete`, {}))
  }
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        A schedule starts a new item in a repository on a cron schedule, as if you had sent its prompt from the dashboard. Each run is an ordinary item with its own agent.
      </p>
      {failure && (
        <p role="alert" className="text-sm whitespace-pre-wrap text-destructive">
          {failure}
        </p>
      )}
      {notice && (
        <p role="status" className="text-sm text-muted-foreground">
          Started{' '}
          <Link to="/items/$id" params={{ id: notice.item }} className="text-sky-300 hover:underline">
            a new item
          </Link>
          .
        </p>
      )}
      {data.length ? (
        <ul className="divide-y rounded-lg border">
          {data.map(schedule => (
            <li key={schedule.id} className="flex flex-col gap-3 px-4 py-3" data-schedule={schedule.id}>
              {editing === schedule.id ? (
                <ScheduleForm schedule={schedule} onCancel={() => setEditing(null)} onSave={save} />
              ) : (
                <div className="flex items-start gap-3">
                  <Switch
                    checked={schedule.enabled}
                    onCheckedChange={enabled => void save({ ...schedule, enabled })}
                    aria-label={schedule.enabled ? 'Disable schedule' : 'Enable schedule'}
                    className="mt-0.5"
                  />
                  <div className="flex min-w-0 flex-1 flex-col gap-1">
                    <span className="flex flex-wrap items-center gap-2">
                      <span className={schedule.title ? 'font-medium' : 'text-muted-foreground italic'}>{schedule.title || 'Title generated from the prompt'}</span>
                      <Badge variant="outline" className="font-mono">
                        {schedule.cron}
                      </Badge>
                      <span className="text-xs text-muted-foreground">
                        {repositoryName(schedule.repository_id)} · {profileLabel(schedule.agent_profile)}
                      </span>
                      {schedule.labels.map(label => (
                        <Badge key={label} variant="secondary">
                          {label}
                        </Badge>
                      ))}
                    </span>
                    <p className="line-clamp-2 text-sm whitespace-pre-wrap text-muted-foreground">{schedule.prompt}</p>
                    <span className="flex flex-wrap gap-x-4 gap-y-0.5 text-xs text-muted-foreground">
                      <span>{schedule.enabled ? `Next: ${when(schedule.next_run_at) || 'never'}` : 'Disabled'}</span>
                      {schedule.last_run_at && (
                        <span>
                          Last: {when(schedule.last_run_at)}
                          {schedule.last_item_id && (
                            <>
                              {' · '}
                              <Link to="/items/$id" params={{ id: schedule.last_item_id }} className="text-sky-300 hover:underline">
                                item
                              </Link>
                            </>
                          )}
                        </span>
                      )}
                    </span>
                    {schedule.last_error && <span className="text-xs text-destructive">Last run failed: {schedule.last_error}</span>}
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button variant="ghost" size="icon-sm" aria-label="Run now" title="Run now" onClick={() => void run(schedule)}>
                      <PlayIcon />
                    </Button>
                    <Button variant="ghost" size="icon-sm" aria-label="Edit schedule" title="Edit" onClick={() => setEditing(schedule.id ?? null)}>
                      <PencilIcon />
                    </Button>
                    <Button variant="ghost" size="icon-sm" aria-label="Remove schedule" title="Remove" onClick={() => void remove(schedule)}>
                      <Trash2Icon />
                    </Button>
                  </div>
                </div>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">No schedules.</p>
      )}
      {editing === '' ? (
        <div className="rounded-lg border px-4 py-3">
          <ScheduleForm onCancel={() => setEditing(null)} onSave={save} />
        </div>
      ) : (
        <Button variant="outline" size="sm" className="w-fit" onClick={() => setEditing('')}>
          <PlusIcon /> Add schedule
        </Button>
      )}
    </div>
  )
}

// ScheduleForm adds a schedule, or edits one.
export function ScheduleForm({ schedule, onSave, onCancel }: { schedule?: Schedule; onSave: (schedule: Schedule) => Promise<boolean>; onCancel: () => void }) {
  const repositories = useRepositories().data ?? []
  const profiles = useAgentProfiles().data
  const [repository, setRepository] = useState(schedule?.repository_id ?? (repositories.length === 1 ? repositories[0].id : ''))
  const [cron, setCron] = useState(schedule?.cron ?? '')
  const [agent, setAgent] = useState(schedule?.agent_profile ?? profiles?.default ?? '')
  const [title, setTitle] = useState(schedule?.title ?? '')
  const [prompt, setPrompt] = useState(schedule?.prompt ?? '')
  const [labels, setLabels] = useState((schedule?.labels ?? []).join(', '))
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true)
  const [saving, setSaving] = useState(false)
  const chosenAgent = agent || profiles?.default || ''
  const ready = !!repository && !!cron.trim() && !!chosenAgent && !!prompt.trim()
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    await onSave({ ...schedule, repository_id: repository, cron: cron.trim(), agent_profile: chosenAgent, title: title.trim(), prompt, labels: parseLabels(labels), enabled })
    setSaving(false)
  }
  return (
    <form onSubmit={submit} className="flex flex-col gap-3" aria-label={schedule ? 'Edit schedule' : 'Add schedule'}>
      <div className="grid gap-3 sm:grid-cols-3">
        <label className="flex flex-col gap-1 text-sm">
          Repository
          <Select value={repository} onValueChange={setRepository} disabled={saving}>
            <SelectTrigger aria-label="Repository">
              <SelectValue placeholder="Choose a repository" />
            </SelectTrigger>
            <SelectContent>
              {repositories.map(entry => (
                <SelectItem key={entry.id} value={entry.id}>
                  {entry.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Schedule
          <Input value={cron} onChange={event => setCron(event.target.value)} placeholder="0 9 * * 1-5" aria-label="Cron expression" className="font-mono" disabled={saving} />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Agent
          <Select value={chosenAgent} onValueChange={setAgent} disabled={saving}>
            <SelectTrigger aria-label="Agent">
              <SelectValue placeholder="Choose an agent" />
            </SelectTrigger>
            <SelectContent>
              {(profiles?.profiles ?? []).map(entry => (
                <SelectItem key={entry.name} value={entry.name}>
                  {entry.label || entry.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </label>
      </div>
      <p className="-mt-1 text-xs text-muted-foreground">
        Cron: minute hour day-of-month month day-of-week, in the daemon&apos;s local time, or @daily, @hourly. <span className="font-mono">0 9 * * 1-5</span> is 09:00 on weekdays.
      </p>
      <label className="flex flex-col gap-1 text-sm">
        Title
        <Input value={title} onChange={event => setTitle(event.target.value)} placeholder="Generated from the prompt" aria-label="Title" disabled={saving} />
      </label>
      <label className="flex flex-col gap-1 text-sm">
        Prompt
        <Textarea value={prompt} onChange={event => setPrompt(event.target.value)} rows={5} aria-label="Prompt" placeholder="What the agent should do on each run" disabled={saving} />
      </label>
      <p className="-mt-1 text-xs text-muted-foreground">
        Title and prompt may use <span className="font-mono">{'{{date}}'}</span>, <span className="font-mono">{'{{time}}'}</span>, <span className="font-mono">{'{{datetime}}'}</span>,{' '}
        <span className="font-mono">{'{{weekday}}'}</span> and <span className="font-mono">{'{{repo}}'}</span>.
      </p>
      <label className="flex flex-col gap-1 text-sm">
        Labels
        <Input value={labels} onChange={event => setLabels(event.target.value)} placeholder="optional, comma-separated" aria-label="Labels" disabled={saving} />
      </label>
      <div className="flex items-center justify-between gap-2">
        <label className="flex items-center gap-2 text-sm">
          <Switch checked={enabled} onCheckedChange={setEnabled} aria-label="Enabled" disabled={saving} />
          Enabled
        </label>
        <div className="flex gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
            Cancel
          </Button>
          <Button type="submit" size="sm" disabled={saving || !ready}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </div>
    </form>
  )
}
