import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt'
import { SetupTrust, type Repository as RepositoryMessage, type StaleAgent } from '@/gen/wi/core/v1/repositories_pb'
import type { Environment, Project, Repository, RepositorySetup } from './views'
import { repositories } from './rpc'

// The repository settings' view models, built from the API's messages. The
// compiler checks each mapping; nothing here restates a message by hand.

const iso = (at?: Timestamp) => (at ? timestampDate(at).toISOString() : '')

// listProjects names the registered repositories new work can start in.
export async function listProjects(): Promise<Project[]> {
  return (await repositories.listRepositories({})).repositories.map(repo => ({ id: repo.id, name: repo.name }))
}

// listRepositories reads the repositories with the files each copies into
// its checkouts.
export async function listRepositories(): Promise<Repository[]> {
  const listed = (await repositories.listRepositories({})).repositories
  return Promise.all(listed.map(async repo => repositoryView(repo, (await repositories.listRepositoryFiles({ repositoryId: repo.id })).files)))
}

function repositoryView(repo: RepositoryMessage, files: Awaited<ReturnType<typeof repositories.listRepositoryFiles>>['files']): Repository {
  const upstream = repo.upstream
  return {
    id: repo.id,
    name: repo.name,
    integration_branch: repo.integrationBranch,
    created_at: iso(repo.createdAt),
    remote: upstream?.url ? { url: upstream.url, branch: upstream.branch, web_url: upstream.webUrl || undefined, host: (upstream.host || undefined) as 'github' | 'gitlab' | 'bitbucket' | undefined } : undefined,
    files: files.map(file => ({ path: file.path, size: file.size, sha256: file.sha256, updated_at: iso(file.modifiedAt) })),
  }
}

// readSetup reads how a repository's checkouts are set up.
export async function readSetup(project: string): Promise<RepositorySetup> {
  const value = await repositories.getCheckoutSetup({ repositoryId: project })
  const setup = value.setup
  return { revision: value.revision, run: setup?.run ?? '', trust: setup?.trust === SetupTrust.ALWAYS ? 'always' : 'never', timeout_seconds: setup?.timeoutSeconds ?? 0, skip: setup?.skip ?? [], declared: setup?.declared || undefined }
}

// saveSetup changes it, fenced on the revision read, and reads it again.
export async function saveSetup(project: string, setup: RepositorySetup): Promise<RepositorySetup> {
  await repositories.updateCheckoutSetup({ repositoryId: project, expectedRevision: setup.revision, setup: { run: setup.run, trust: setup.trust === 'always' ? SetupTrust.ALWAYS : SetupTrust.NEVER, timeoutSeconds: setup.timeout_seconds, skip: setup.skip } })
  return readSetup(project)
}

// readEnvironment reads a repository's variables and the agents still
// running with older ones.
export async function readEnvironment(project: string): Promise<Environment> {
  const [value, repo] = await Promise.all([repositories.getEnvironment({ repositoryId: project }), repositories.getRepository({ repositoryId: project })])
  return {
    project: { id: project, name: repo.repository?.name ?? project },
    revision: value.revision,
    vars: value.variables.map(variable => ({ key: variable.key, value: variable.value })),
    agents: value.staleAgents.map((agent: StaleAgent) => ({ id: agent.itemId, title: agent.title, started_at: iso(agent.startedAt), busy: agent.busy, stale: true })),
  }
}

// saveEnvironment replaces the variables, fenced on the revision read.
export async function saveEnvironment(project: string, vars: Environment['vars'], revision: number): Promise<Environment> {
  await repositories.updateEnvironment({ repositoryId: project, expectedRevision: revision, replace: true, set: vars })
  return readEnvironment(project)
}
