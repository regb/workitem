import { Link } from '@tanstack/react-router'
import { useGeneralSettings } from '@/lib/queries'

// GeneralSettingsSection describes this installation.
export function GeneralSettingsSection() {
  const { data, error } = useGeneralSettings()
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>
  return (
    <div className="flex flex-col gap-4">
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
        <dt className="text-muted-foreground">Version</dt>
        <dd className="font-mono break-all">{data.version}</dd>
        <dt className="text-muted-foreground">Data</dt>
        <dd className="font-mono break-all">{data.data_root}</dd>
        <dt className="text-muted-foreground">Default agent</dt>
        <dd>
          <Link to="/settings/$section" params={{ section: 'agents' }} className="hover:underline">
            {data.agents.find(agent => agent.name === data.default_agent)?.label ?? (data.default_agent || 'none')}
          </Link>
        </dd>
      </dl>
      <p className="text-xs text-muted-foreground">
        Other settings come from Wi&apos;s configuration file, <code>~/.config/wi/config.toml</code>, if you have one; inspect them with <code>wi config</code>.
      </p>
    </div>
  )
}
