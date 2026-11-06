import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { readLabelFilter, saveLabelFilter } from '@/lib/label-filter'
import { refreshList, useAgentProfiles, useGeneralSettings } from '@/lib/queries'

// GeneralSettingsSection describes this installation.
export function GeneralSettingsSection() {
  const { data, error } = useGeneralSettings()
  const profiles = useAgentProfiles().data?.profiles ?? []
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>
  return (
    <div className="flex flex-col gap-4">
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
        <dt className="text-muted-foreground">Version</dt>
        <dd className="font-mono break-all">{data.version}</dd>
        <dt className="text-muted-foreground">Data</dt>
        <dd className="font-mono break-all">{data.dataRoot}</dd>
        <dt className="text-muted-foreground">Default agent</dt>
        <dd>
          <Link to="/settings/$section" params={{ section: 'agents' }} className="hover:underline">
            {profiles.find(agent => agent.name === data.defaultAgentProfile)?.label || data.defaultAgentProfile || 'none'}
          </Link>
        </dd>
      </dl>
      <LabelFilter />
      <p className="text-xs text-muted-foreground">
        Other settings come from Wi&apos;s configuration file, <code>~/.config/wi/config.toml</code>, if you have one; inspect them with <code>wi config</code>.
      </p>
    </div>
  )
}

// LabelFilter sets which labels the dashboard shows, for this browser.
function LabelFilter() {
  const [text, setText] = useState(readLabelFilter)
  const [saved, setSaved] = useState(readLabelFilter)
  const [problem, setProblem] = useState('')
  const save = () => {
    try {
      saveLabelFilter(text)
      setSaved(text.trim())
      setProblem('')
      void refreshList()
    } catch (error) {
      setProblem((error as Error).message)
    }
  }
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={event => {
        event.preventDefault()
        save()
      }}
    >
      <label htmlFor="label-filter" className="text-sm font-medium">
        Dashboard labels
      </label>
      <div className="flex gap-2">
        <Input id="label-filter" value={text} onChange={event => setText(event.target.value)} placeholder="team, -personal" className="max-w-sm font-mono" />
        <Button type="submit" variant="outline" disabled={text.trim() === saved}>
          Save
        </Button>
      </div>
      {problem ? (
        <p role="alert" className="text-xs text-destructive">
          {problem}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          Comma-separated labels the dashboard requires; a leading minus excludes one. Empty shows everything. Kept in this browser, like the project filter; <code>wi list</code>&apos;s filters stay in the terminal.
        </p>
      )}
    </form>
  )
}
