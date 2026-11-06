import { useState, type FormEvent } from 'react'
import { PencilIcon, PlusIcon, StarIcon, Trash2Icon } from 'lucide-react'
import { useConfirm } from '@/components/confirm'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api, type AgentProfileDefinition, type AgentProfiles } from '@/lib/api'
import { keys, queryClient, useAgentProfiles } from '@/lib/queries'

// The settings the form edits; any others a profile has are kept.
const fields = [
  { key: 'executable', label: 'Executable', placeholder: '/absolute/path/to/the/agent' },
  { key: 'model', label: 'Model', placeholder: "the agent's default" },
  { key: 'reasoning', label: 'Reasoning', placeholder: "the agent's default" },
] as const

const drivers = [
  { id: 'pi', label: 'Pi' },
  { id: 'claude', label: 'Claude Code' },
]

// AgentSettings lists the agent profiles the daemon keeps, the same list as
// wi agent profile, and adds, edits, removes them or changes the default.
export function AgentSettings() {
  const { data, error } = useAgentProfiles()
  const confirm = useConfirm()
  const [editing, setEditing] = useState<string | null>(null)
  const [failure, setFailure] = useState('')
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>
  const change = async (body: Record<string, unknown>) => {
    setFailure('')
    try {
      const out = await api<{ result: AgentProfiles }>('/api/agent-profiles', { revision: data.revision, ...body })
      queryClient.setQueryData(keys.agentProfiles, out.result)
      void queryClient.invalidateQueries({ queryKey: keys.agents })
      setEditing(null)
      return true
    } catch (problem) {
      setFailure((problem as Error).message)
      return false
    }
  }
  const remove = async (profile: AgentProfiles['profiles'][number]) => {
    const ok = await confirm({
      title: `Remove ${profile.label || profile.name}?`,
      description: profile.users
        ? `${profile.users} open ${profile.users === 1 ? 'item uses' : 'items use'} it. They keep its name and ask to move to another profile when they next start.`
        : 'No open item uses it.',
      action: 'Remove',
    })
    if (ok) await change({ remove: profile.name })
  }
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        A profile is a coding agent and the settings it starts with. New items use the default profile. Agents log in on their own; profiles hold no credentials.
      </p>
      {failure && (
        <p role="alert" className="text-sm text-destructive">
          {failure}
        </p>
      )}
      {data.profiles.length ? (
        <ul className="divide-y rounded-lg border">
          {data.profiles.map(profile => (
            <li key={profile.name} className="flex flex-col gap-3 px-4 py-3" data-profile={profile.name}>
              {editing === profile.name ? (
                <ProfileForm profile={profile} onCancel={() => setEditing(null)} onSave={put => change({ put })} />
              ) : (
                <div className="flex items-start gap-3">
                  <div className="flex min-w-0 flex-1 flex-col gap-1">
                    <span className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{profile.label || profile.name}</span>
                      <span className="font-mono text-xs text-muted-foreground">{profile.name}</span>
                      <Badge variant="outline">{drivers.find(driver => driver.id === profile.driver)?.label ?? profile.driver}</Badge>
                      {profile.name === data.default && <Badge className="bg-sky-500/15 text-sky-300">default</Badge>}
                    </span>
                    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5 text-xs">
                      {Object.entries(profile.config ?? {}).map(([key, value]) => (
                        <div key={key} className="contents">
                          <dt className="text-muted-foreground">{key}</dt>
                          <dd className="min-w-0 truncate font-mono">{Array.isArray(value) ? value.join(', ') : String(value)}</dd>
                        </div>
                      ))}
                    </dl>
                    <span className="text-xs text-muted-foreground">
                      {profile.users} open {profile.users === 1 ? 'item' : 'items'}
                    </span>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    {profile.name !== data.default && (
                      <Button variant="ghost" size="icon-sm" aria-label={`Make ${profile.name} the default`} title="Make default" onClick={() => void change({ default: profile.name })}>
                        <StarIcon />
                      </Button>
                    )}
                    <Button variant="ghost" size="icon-sm" aria-label={`Edit ${profile.name}`} title="Edit" onClick={() => setEditing(profile.name)}>
                      <PencilIcon />
                    </Button>
                    <Button variant="ghost" size="icon-sm" aria-label={`Remove ${profile.name}`} title="Remove" onClick={() => void remove(profile)}>
                      <Trash2Icon />
                    </Button>
                  </div>
                </div>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">No agent profiles. Install Pi or Claude Code and run any wi command to add one, or add one here.</p>
      )}
      {editing === '' ? (
        <div className="rounded-lg border px-4 py-3">
          <ProfileForm onCancel={() => setEditing(null)} onSave={put => change({ put })} />
        </div>
      ) : (
        <Button variant="outline" size="sm" className="w-fit" onClick={() => setEditing('')}>
          <PlusIcon /> Add profile
        </Button>
      )}
    </div>
  )
}

// ProfileForm adds a profile, or edits one: its label and common settings.
function ProfileForm({ profile, onSave, onCancel }: { profile?: AgentProfileDefinition; onSave: (profile: AgentProfileDefinition) => Promise<boolean>; onCancel: () => void }) {
  const [name, setName] = useState(profile?.name ?? '')
  const [driver, setDriver] = useState(profile?.driver ?? 'pi')
  const [label, setLabel] = useState(profile?.label ?? '')
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(fields.map(field => [field.key, String(profile?.config?.[field.key] ?? '')])))
  const [saving, setSaving] = useState(false)
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setSaving(true)
    const config: Record<string, unknown> = { ...profile?.config }
    for (const field of fields) {
      const value = values[field.key].trim()
      if (value) config[field.key] = value
      else delete config[field.key]
    }
    await onSave({ name: name.trim(), driver, label: label.trim() || undefined, config })
    setSaving(false)
  }
  return (
    <form onSubmit={submit} className="flex flex-col gap-3" aria-label={profile ? `Edit ${profile.name}` : 'Add profile'}>
      <div className="grid gap-3 sm:grid-cols-3">
        <label className="flex flex-col gap-1 text-sm">
          Name
          <Input value={name} onChange={event => setName(event.target.value)} placeholder="pi-work" required disabled={!!profile || saving} className="font-mono" />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Driver
          <Select value={driver} onValueChange={setDriver} disabled={!!profile || saving}>
            <SelectTrigger aria-label="Driver">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {drivers.map(entry => (
                <SelectItem key={entry.id} value={entry.id}>
                  {entry.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Label
          <Input value={label} onChange={event => setLabel(event.target.value)} placeholder="Pi (work)" disabled={saving} />
        </label>
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        {fields.map(field => (
          <label key={field.key} className="flex flex-col gap-1 text-sm">
            {field.label}
            <Input
              value={values[field.key]}
              onChange={event => setValues({ ...values, [field.key]: event.target.value })}
              placeholder={field.placeholder}
              className="font-mono text-xs"
              disabled={saving}
            />
          </label>
        ))}
      </div>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={saving || !name.trim()}>
          {saving ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}
