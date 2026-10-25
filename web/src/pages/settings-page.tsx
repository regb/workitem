import { Link, useParams } from '@tanstack/react-router'
import { BotIcon, ChevronRightIcon, FolderGitIcon, GaugeIcon, SlidersHorizontalIcon } from 'lucide-react'
import { AddRepository } from '@/components/settings/add-repository'
import { AgentSettings } from '@/components/settings/agent-settings'
import { GeneralSettingsSection } from '@/components/settings/general-settings'
import { RemoteLine } from '@/components/settings/repository-settings'
import { SettingsLayout } from '@/components/settings/settings-layout'
import { UsageSettings } from '@/components/settings/usage-settings'
import { useRepositories } from '@/lib/queries'

// SettingsPage is Wi's own settings, for the whole installation; each
// repository has its settings page apart, reached from Repositories.
export function SettingsPage() {
  const { section } = useParams({ from: '/settings/$section' })
  const current = section === 'repositories' || section === 'agents' || section === 'usage' ? section : 'general'
  const titles = { general: 'General', agents: 'Agents', usage: 'Usage', repositories: 'Repositories' }
  const nav = [
    { label: 'General', icon: SlidersHorizontalIcon, link: { to: '/settings/$section', params: { section: 'general' } }, current: current === 'general' },
    { label: 'Agents', icon: BotIcon, link: { to: '/settings/$section', params: { section: 'agents' } }, current: current === 'agents' },
    { label: 'Usage', icon: GaugeIcon, link: { to: '/settings/$section', params: { section: 'usage' } }, current: current === 'usage' },
    { label: 'Repositories', icon: FolderGitIcon, link: { to: '/settings/$section', params: { section: 'repositories' } }, current: current === 'repositories' },
  ] as const
  return (
    <SettingsLayout header={<h1 className="text-xl font-semibold">Settings</h1>} nav={[...nav]} title={titles[current]}>
      {current === 'general' ? (
        <GeneralSettingsSection />
      ) : current === 'agents' ? (
        <AgentSettings />
      ) : current === 'usage' ? (
        <UsageSettings />
      ) : (
        <>
          <AddRepository />
          <RepositoryList />
        </>
      )}
    </SettingsLayout>
  )
}

// RepositoryList lists registered repositories; each opens its own settings.
function RepositoryList() {
  const { data, error } = useRepositories()
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (!data.length) return <p className="text-sm text-muted-foreground">No repositories are registered. Add one with wi repo add.</p>
  return (
    <ul className="divide-y rounded-lg border">
      {data.map(repository => (
        <li key={repository.id} className="flex items-center gap-3 px-4 py-3" data-repository={repository.id}>
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <Link to="/repositories/$repo/settings/$section" params={{ repo: repository.id, section: 'general' }} className="w-fit font-medium hover:text-sky-300 hover:underline">
              {repository.name}
            </Link>
            {repository.remote ? <RemoteLine remote={repository.remote} /> : <span className="text-sm text-muted-foreground">Local only</span>}
          </div>
          <Link
            to="/repositories/$repo/settings/$section"
            params={{ repo: repository.id, section: 'general' }}
            aria-label={`${repository.name} settings`}
            className="shrink-0 text-muted-foreground hover:text-foreground"
          >
            <ChevronRightIcon className="size-4" />
          </Link>
        </li>
      ))}
    </ul>
  )
}
