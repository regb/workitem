import { keepPreviousData, QueryClient, useQuery } from '@tanstack/react-query'
import {
  api,
  type AgentProfile,
  type AgentProfiles,
  type AgentSettings,
  type Chat,
  type Creation,
  type Environment,
  type GeneralSettings,
  type Live,
  type Project,
  type Repository,
  type RepositorySetup,
  type SearchResult,
  type Usage,
  type WorkList,
} from './api'

// The event stream keeps the list, creations, chats and live output current,
// so by default queries never refetch on their own: each is read once on
// first use and refreshed after actions.
export const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: Infinity, refetchOnWindowFocus: false, retry: 1 } },
})

export const keys = {
  list: ['list'] as const,
  creations: ['creations'] as const,
  projects: ['projects'] as const,
  agents: ['agents'] as const,
  chat: (id: string) => ['chat', id] as const,
  live: (id: string) => ['live', id] as const,
  settings: (id: string) => ['settings', id] as const,
  general: ['general'] as const,
  agentProfiles: ['agent-profiles'] as const,
  usage: ['usage'] as const,
  repositories: ['repositories'] as const,
  environment: (project: string) => ['environment', project] as const,
  repositorySetup: (project: string) => ['repository-setup', project] as const,
}

export const useWorkList = () => useQuery({ queryKey: keys.list, queryFn: () => api<WorkList>('/api/items') })
export const useCreations = () => useQuery({ queryKey: keys.creations, queryFn: () => api<Creation[]>('/api/creations') })
export const useProjects = () => useQuery({ queryKey: keys.projects, queryFn: () => api<Project[]>('/api/projects'), staleTime: 0 })
export const useGeneralSettings = () => useQuery({ queryKey: keys.general, queryFn: () => api<GeneralSettings>('/api/settings'), staleTime: 0 })
export const useRepositorySetup = (project: string) => useQuery({ queryKey: keys.repositorySetup(project), queryFn: () => api<RepositorySetup>(`/api/projects/${project}/setup`), staleTime: 0 })
export const useUsage = () => useQuery({ queryKey: keys.usage, queryFn: () => api<Usage>('/api/usage'), staleTime: 60_000 })
export const useAgentProfiles = () => useQuery({ queryKey: keys.agentProfiles, queryFn: () => api<AgentProfiles>('/api/agent-profiles'), staleTime: 0 })
export const useRepositories = () => useQuery({ queryKey: keys.repositories, queryFn: () => api<Repository[]>('/api/repositories'), staleTime: 0 })
export const useEnvironment = (project: string) => useQuery({ queryKey: keys.environment(project), queryFn: () => api<Environment>(`/api/projects/${project}/env`), enabled: !!project, staleTime: 0 })
export const useAgents = () => useQuery({ queryKey: keys.agents, queryFn: () => api<AgentProfile[]>('/api/agents'), staleTime: 0 })
export const useChat = (id: string) => useQuery({ queryKey: keys.chat(id), queryFn: async () => latestChat(id, await api<Chat>(`/api/items/${id}/chat`)) })
// Live output arrives only on the event stream, which writes it to the cache.
export const useLive = (id: string) => useQuery<Live | null>({ queryKey: keys.live(id), queryFn: () => null, enabled: false })

// streamed records when the event stream last wrote a query, by key.
const streamed = new Map<string, number>()
export const noteStreamed = (key: readonly unknown[]) => streamed.set(JSON.stringify(key), performance.now())

// refresh reads a query again after an action. The stream may deliver a
// newer snapshot while the read is under way; the read then loses, as the
// stream only resends what changes and a stale value would stay.
// A query nothing shows is only marked stale, as before: reading a chat
// renews the tab's control lease, so unopened chats are left unread.
async function refresh<T>(key: readonly unknown[], path: string) {
  if (!queryClient.getQueryCache().find({ queryKey: key })?.getObserversCount()) return queryClient.invalidateQueries({ queryKey: key })
  const started = performance.now()
  try {
    const data = await api<T>(path)
    if ((streamed.get(JSON.stringify(key)) ?? -1) < started) queryClient.setQueryData(key, data)
  } catch {
    // The stream brings the state once the server can report it.
  }
}

// The stream and the page's own reads of a chat overlap, so a snapshot can
// arrive after a newer one: the server stamps each with when its read began,
// and the latest stays. Otherwise a reply's working state blinks out when a
// read begun before the agent came online lands last.
const latestChat = (id: string, chat: Chat) => {
  const current = queryClient.getQueryData<Chat>(keys.chat(id))
  return current && (current.read_at ?? 0) > (chat.read_at ?? 0) ? current : chat
}

// setChat shows a chat snapshot unless a newer one is shown, and reports
// whether it did.
export function setChat(id: string, chat: Chat) {
  if (latestChat(id, chat) !== chat) return false
  queryClient.setQueryData(keys.chat(id), chat)
  return true
}

// refreshChat reads a chat again after an action. A chat nothing shows is
// only marked stale: reading a chat renews the tab's control lease, so
// unopened chats are left unread.
export async function refreshChat(id: string) {
  if (
    !queryClient
      .getQueryCache()
      .find({ queryKey: keys.chat(id) })
      ?.getObserversCount()
  )
    return queryClient.invalidateQueries({ queryKey: keys.chat(id) })
  try {
    setChat(id, await api<Chat>(`/api/items/${id}/chat`))
  } catch {
    // The stream brings the state once the server can report it.
  }
}
export const refreshList = () => refresh<WorkList>(keys.list, '/api/items')
// Settings change rarely and listing models may start the backend's catalog
// command, so they are kept for a minute.
// useSearch finds items by text in one lifecycle state. Results stay shown
// while the next query runs, and are read again after a short while: the
// stream does not update them.
export const useSearch = (query: string, state: string) =>
  useQuery({
    queryKey: ['search', state, query],
    queryFn: () => api<SearchResult>(`/api/search?${new URLSearchParams({ q: query, state })}`),
    enabled: !!query.trim(),
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  })
export const useAgentSettings = (id: string) => useQuery({ queryKey: keys.settings(id), queryFn: () => api<AgentSettings>(`/api/items/${id}/settings`), staleTime: 60_000 })
export const refreshSettings = (id: string) => queryClient.invalidateQueries({ queryKey: keys.settings(id) })
