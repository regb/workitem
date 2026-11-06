import { keepPreviousData, QueryClient, useQuery } from '@tanstack/react-query'
import { readChat } from './chat-view'
import { readWorkList, searchItems } from './items'
import { listProjects, listRepositories, readEnvironment, readSetup } from './repositories'
import { profiles, rpcMessage, schedules, system } from './rpc'
import type { Live } from './views'
import { readAgentSettings, readProfileModels } from './settings-view'
import { reviewKey } from './review'
import { setLiveProblem } from './ui-state'

// The watch stream says when the list and the open conversation change, and
// they are read again then (see watch.ts), so by default queries never
// refetch on their own: each is read once on first use and refreshed after
// actions and changes.
export const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: Infinity, refetchOnWindowFocus: false, retry: 1 } },
})

export const keys = {
  list: ['list'] as const,
  projects: ['projects'] as const,
  agents: ['agents'] as const,
  chat: (id: string) => ['chat', id] as const,
  live: (id: string) => ['live', id] as const,
  settings: (id: string) => ['settings', id] as const,
  general: ['general'] as const,
  agentProfiles: ['agent-profiles'] as const,
  profileModels: (profile: string) => ['profile-models', profile] as const,
  schedules: ['schedules'] as const,
  usage: ['usage'] as const,
  repositories: ['repositories'] as const,
  environment: (project: string) => ['environment', project] as const,
  repositorySetup: (project: string) => ['repository-setup', project] as const,
}

// A failed read keeps what is shown, and says why live updates paused.
async function watched<T>(source: 'list' | 'chat', signal: AbortSignal, read: (signal: AbortSignal) => Promise<T>) {
  try {
    const value = await read(signal)
    setLiveProblem(source, '')
    return value
  } catch (error) {
    if (!signal.aborted) setLiveProblem(source, rpcMessage(error))
    throw error
  }
}

export const useWorkList = () => useQuery({ queryKey: keys.list, queryFn: ({ signal }) => watched('list', signal, readWorkList) })
export const useProjects = () => useQuery({ queryKey: keys.projects, queryFn: listProjects, staleTime: 0 })
export const useGeneralSettings = () => useQuery({ queryKey: keys.general, queryFn: () => system.getInfo({}), staleTime: 0 })
export const useRepositorySetup = (project: string) => useQuery({ queryKey: keys.repositorySetup(project), queryFn: () => readSetup(project), staleTime: 0 })
// The daemon answers usage at once from its last check; while a check runs,
// the page reads again every two seconds for its result.
export const useUsage = () => useQuery({ queryKey: keys.usage, queryFn: () => system.getUsage({}), staleTime: 0, refetchInterval: query => (query.state.data?.refreshing ? 2_000 : 60_000) })
export const useAgentProfiles = () => useQuery({ queryKey: keys.agentProfiles, queryFn: () => profiles.listAgentProfiles({}), staleTime: 0 })
// Next and last runs change as schedules fire, so the list refreshes.
export const useSchedules = () => useQuery({ queryKey: keys.schedules, queryFn: async () => (await schedules.listSchedules({})).schedules, staleTime: 0, refetchInterval: 30_000 })
export const useRepositories = () => useQuery({ queryKey: keys.repositories, queryFn: listRepositories, staleTime: 0 })
export const useEnvironment = (project: string) => useQuery({ queryKey: keys.environment(project), queryFn: () => readEnvironment(project), enabled: !!project, staleTime: 0 })
// A profile's models change rarely; the daemon caches them for ten minutes.
export const useProfileModels = (profile: string) =>
  useQuery({ queryKey: keys.profileModels(profile), queryFn: ({ signal }) => readProfileModels(profile, signal), enabled: !!profile, staleTime: 5 * 60_000 })
// The agents new work can use: the profiles, in the daemon's order.
export const useAgents = () => useQuery({ queryKey: keys.agents, queryFn: async () => (await profiles.listAgentProfiles({})).profiles, staleTime: 0 })
export const useChat = (id: string) => useQuery({ queryKey: keys.chat(id), queryFn: ({ signal }) => watched('chat', signal, signal => readChat(id, signal)) })
// Live output arrives only on the watch stream, which writes it to the cache.
export const useLive = (id: string) => useQuery<Live | null>({ queryKey: keys.live(id), queryFn: () => null, enabled: false })

// refresh reads a query again, after an action or a change. A read under way
// is cancelled, so the last read begun is what shows. A query nothing shows
// is only marked stale, and read when next shown.
async function refresh(key: readonly unknown[]) {
  if (!queryClient.getQueryCache().find({ queryKey: key })?.getObserversCount()) return queryClient.invalidateQueries({ queryKey: key })
  return queryClient.refetchQueries({ queryKey: key, exact: true })
}
// A conversation read again may name files the agent just wrote: the
// checkout's file list, which decides what is a file link, is read again too.
export const refreshChat = (id: string) => Promise.all([refresh(keys.chat(id)), refresh([...reviewKey(id), 'files'])]).then(([chat]) => chat)
export const refreshList = () => refresh(keys.list)
// Settings change rarely and listing models may start the backend's catalog
// command, so they are kept for a minute.
// useSearch finds items by text in one lifecycle state. Results stay shown
// while the next query runs, and are read again after a short while: the
// stream does not update them.
export const useSearch = (query: string, state: string) =>
  useQuery({
    queryKey: ['search', state, query],
    queryFn: () => searchItems(query, state),
    enabled: !!query.trim(),
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  })
export const useAgentSettings = (id: string) => useQuery({ queryKey: keys.settings(id), queryFn: ({ signal }) => readAgentSettings(id, signal), staleTime: 60_000 })
export const refreshSettings = (id: string) => queryClient.invalidateQueries({ queryKey: keys.settings(id) })
