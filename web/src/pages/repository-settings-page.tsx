import { Link, useParams } from '@tanstack/react-router'
import { FileIcon, FolderGitIcon, KeyRoundIcon, SlidersHorizontalIcon, WrenchIcon } from 'lucide-react'
import { EnvironmentSettings } from '@/components/settings/environment-settings'
import { RemoteLine, RepositoryFiles, RepositoryGeneral } from '@/components/settings/repository-settings'
import { RepositorySetupSettings } from '@/components/settings/repository-setup'
import { SettingsLayout } from '@/components/settings/settings-layout'
import { useRepositories } from '@/lib/queries'

const sections = [
  { id: 'general', label: 'General', icon: SlidersHorizontalIcon },
  { id: 'setup', label: 'Setup', icon: WrenchIcon },
  { id: 'files', label: 'Files', icon: FileIcon },
  { id: 'environment', label: 'Environment variables', icon: KeyRoundIcon },
] as const

// RepositorySettingsPage is one repository's settings: everything on it is
// scoped to that repository.
export function RepositorySettingsPage() {
  const { repo, section } = useParams({ from: '/repositories/$repo/settings/$section' })
  const { data, error } = useRepositories()
  const repository = data?.find(entry => entry.id === repo)
  const current = sections.find(entry => entry.id === section) ?? sections[0]
  const header = (
    <div className="flex flex-col gap-1.5">
      <h1 className="flex items-center gap-2 text-xl font-semibold">
        <FolderGitIcon className="size-5 text-muted-foreground" aria-hidden />
        <Link to="/settings/$section" params={{ section: 'repositories' }} className="font-normal text-muted-foreground hover:text-foreground hover:underline">
          Repositories
        </Link>
        <span className="text-muted-foreground">/</span>
        <span className="truncate">{repository?.name ?? 'Repository'}</span>
      </h1>
      {repository?.remote && <RemoteLine remote={repository.remote} />}
    </div>
  )
  const nav = sections.map(entry => ({
    label: entry.label,
    icon: entry.icon,
    link: { to: '/repositories/$repo/settings/$section', params: { repo, section: entry.id } } as const,
    current: entry.id === current.id,
  }))
  return (
    <SettingsLayout header={header} nav={nav} title={current.label}>
      {repository ? (
        current.id === 'environment' ? (
          <EnvironmentSettings project={repository.id} />
        ) : current.id === 'setup' ? (
          <RepositorySetupSettings repository={repository} />
        ) : current.id === 'files' ? (
          <RepositoryFiles repository={repository} />
        ) : (
          <RepositoryGeneral repository={repository} />
        )
      ) : error ? (
        <p className="text-sm text-destructive">{(error as Error).message}</p>
      ) : data ? (
        <p className="text-sm text-muted-foreground">This repository is not registered.</p>
      ) : (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}
    </SettingsLayout>
  )
}
