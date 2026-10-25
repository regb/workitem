// Types and client for the wi web API (internal/web). The server owns all
// policy; these types mirror its JSON.
import { clientID } from './session'

export type Blocker = {
  id: string
  kind: 'time' | 'item' | 'external'
  until?: string
  item_id?: string
  event?: string
  label?: string
}

export type ListItem = {
  id: string
  slug: string
  title: string
  state: 'backlog' | 'active' | 'closed'
  labels: string[] | null
  agent?: { status: string }
  worktree?: { status: string; reason?: string; dirty?: boolean; branch_mismatch?: boolean }
  blockers?: Blocker[]
  unread?: boolean
  // How a closed item ended.
  resolution?: string
  project_id: string
}

// SearchHit is an item a search found: the strongest field it matched in
// and, for a description or notes, an excerpt around the match.
export type SearchHit = ListItem & { field: 'title' | 'labels' | 'description' | 'notes'; excerpt?: string }
export type SearchResult = { items: SearchHit[]; total: number }

export type Project = { id: string; name: string }

// Settings: the installation, registered repositories with the files Wi
// copies into their checkouts, and each repository's environment variables.
export type GeneralSettings = { version: string; default_agent: string; agents: AgentProfile[]; data_root: string }
// Usage is how much of each subscription's quota is left, from the usage
// plugin; enabled is false while the plugin is not enabled.
export type QuotaWindow = { id: string; scope?: { model?: string; surface?: string }; remaining_percent: number | null; resets_at: string | null; duration_seconds: number | null }
export type QuotaResult = {
  provider: string
  account?: string
  observed_at: string | null
  windows: QuotaWindow[]
  included_usage_allowed?: boolean
  credits?: { remaining: number | null; unlimited: boolean | null; unit: string }
  stale?: boolean
  error?: { code: string; message: string }
}
export type Usage = { enabled: boolean; results: QuotaResult[] }
// Agent profiles, kept by the daemon; users counts the open items on one.
export type AgentProfileDefinition = { name: string; driver: string; label?: string; config?: Record<string, unknown> }
export type AgentProfiles = { revision: number; default: string; profiles: (AgentProfileDefinition & { users: number })[] }
export type RepositoryFile = { path: string; size: number; sha256: string; updated_at: string }
// remote is the upstream, without credentials; web_url is its page on a known
// host (github, gitlab, bitbucket).
export type Remote = { url: string; branch: string; web_url?: string; host?: 'github' | 'gitlab' | 'bitbucket' }
export type Repository = { id: string; name: string; integration_branch: string; created_at: string; remote?: Remote; files: RepositoryFile[] }
// How a repository's checkouts are set up; declared is the command its
// default branch declares, which runs only with trust 'always'.
export type RepositorySetup = { revision: number; run: string; trust: 'never' | 'always'; timeout_seconds: number; skip: string[]; declared?: string }
export type EnvVar = { key: string; value: string }
// A repository's environment variables; revision fences a stale save.
// Agents are the agents running in the repository's checkouts; stale ones
// started before the variables last changed.
export type EnvironmentAgent = { id: string; title: string; started_at: string; busy: boolean; stale: boolean }
export type Environment = { project: Project; vars: EnvVar[]; revision: number; agents?: EnvironmentAgent[] }
export type AgentProfile = { name: string; label: string }

export type WorkList = {
  display_sections: { name: string; items: ListItem[] }[]
  projects: Project[]
  warnings?: string[]
}

export type HistoryImage = { mime_type: string; data: string; alt?: string }
// PromptLink ties a prompt the page sent, by its request ID, to the history
// entries that hold it.
export type PromptLink = { request_id: string; native_ids: string[] }
// HistorySubagent is an agent a tool call delegated to; its own conversation
// is read by ID. Status is running, completed or failed.
export type HistorySubagent = { id: string; tool_id?: string; type?: string; description?: string; status?: string }
export type HistoryTool = { id?: string; name: string; input?: string; output?: string; status: string; images?: HistoryImage[]; subagent?: HistorySubagent }
export type HistoryMessage = {
  id: string
  role: string
  text: string
  // A reply the agent's harness wrote instead of the model, such as a usage
  // limit notice.
  error?: boolean
  // A reply the operator stopped before it finished; the turn ended there.
  aborted?: boolean
  tools?: HistoryTool[]
  images?: HistoryImage[]
  native_ids?: string[]
  // A subagent's result, reported to the agent when it finished (role
  // "subagent"); text is the result.
  subagent?: HistorySubagent
  // What a "summary" message summarizes: a compaction replaced the
  // conversation before it in the agent's context; a branch summary covers
  // a branch the conversation left.
  summary?: { kind: 'compaction' | 'branch'; tokens_before?: number }
  // reasoning marks a message the model reasoned before: its text and how
  // long it took, when the agent saved them.
  reasoning?: { text?: string; duration_ms?: number }
}
// An item's log: short notes, oldest first, some recording an attachment;
// and each attachment name's current content.
export type NoteAttachment = { name: string; sha256: string; size: number; type: string }
export type ItemNote = { time: string; text: string; head?: string; attachment?: NoteAttachment }
export type ItemNotes = { notes: ItemNote[]; attachments: (NoteAttachment & { time: string })[] }
export type History = {
  messages: HistoryMessage[]
  native_entry_ids?: string[]
  has_more?: boolean
  before_id?: string
  leaf_id: string
  truncated?: boolean
}

export type Live = {
  sequence?: number
  message_id?: string
  truncated?: boolean
  // History entries that hold text, once the driver knows them.
  saved_as?: string[]
  thought?: string
  tools?: HistoryTool[]
  // Recently sent prompts, by the request ID their sender gave, with the
  // history entries holding them.
  prompts?: PromptLink[]
  text?: string
  activity?: string
  leaf_id?: string
  error?: string
  tool?: HistoryTool
}

export type ControlView = {
  you: boolean
  status: { holder?: string; lease?: { label?: string }; channels: { control?: string }; busy: boolean }
  plan: { needs_confirmation: boolean; reasons: string[] | null; replace?: string; start?: string; abort?: boolean; unavailable?: string }
}

export type Creation = { id: string; title: string; state: 'queued' | 'starting' | 'sent' | 'problem'; error?: string; warnings?: string[]; created_at?: string }

export type Manifest = {
  id: string
  slug: string
  title: string
  state: 'backlog' | 'active' | 'closed'
  labels: string[] | null
  resolution?: string
  agent?: string
  blockers?: Blocker[]
  created_at: string
  updated_at: string
  repository: { remote_url?: string; created_from_commit?: string }
  checkout: { specification: { kind?: string; branch?: string }; claim?: { path?: string } | null }
  primary_conversation?: { id: string }
}

export type Chat = {
  startup?: Creation
  description: string
  project: Project
  item: Manifest
  runtime: {
    online: boolean
    // settings are the model and reasoning level the agent reports running with.
    runtime?: { id: string; mode: string; settings?: Record<string, string> }
    capabilities: { prompt?: boolean; steering?: boolean; follow_up?: boolean; image_input?: boolean }
  }
  status: string
  // Why the agent needs attention, when status is 'problem' or it is asking.
  status_reason?: string
  // The turn is paused on a question answered in the agent's terminal.
  asking?: boolean
  history: History
  control?: ControlView
  unread: boolean
  // The checkout's setup while it runs, or after it failed: the agent starts
  // only once it succeeds.
  setup?: WorkspaceSetup
  warnings: string[] | null
  // When the server began reading this snapshot, in Unix microseconds.
  read_at?: number
}

export type WorkspaceSetup = {
  current: boolean
  run?: { status: 'running' | 'succeeded' | 'failed' | 'skipped'; error?: string; warnings?: string[]; log?: string }
  log_tail?: string
}

// approval is a question the server needs answered before it can go on,
// such as trusting a worktree's .envrc.
// Outcome is an action's answer; R is what its result holds.
export type Outcome<R = unknown> = { error?: string; warnings?: string[]; result?: R; control?: ControlView; observed?: Manifest; creation_uncertain?: boolean; approval?: { question: string; path?: string } }

// ApiError carries the server's outcome; a missing outcome means the
// connection failed and the result is unknown.
export class ApiError extends Error {
  outcome?: Outcome
  constructor(message: string, outcome?: Outcome) {
    super(message)
    this.outcome = outcome
  }
}

export function headers(json = false): Record<string, string> {
  const result: Record<string, string> = { 'X-Wi-Client': clientID }
  if (json) result['Content-Type'] = 'application/json'
  return result
}

export async function api<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: headers(body !== undefined),
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })
  // The server answers in JSON, except for refusals before a request reaches
  // the API (a foreign origin, a missing proxy user), which are plain text.
  const raw = await response.text()
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    const message = raw.trim() || `${response.status} ${response.statusText}`.trim()
    throw new ApiError(message, { error: message })
  }
  if (!response.ok) {
    const outcome = value as Outcome
    throw new ApiError([outcome.error, ...(outcome.warnings ?? [])].filter(Boolean).join('\n'), outcome)
  }
  return value as T
}

export const itemAction = <T = Outcome>(id: string, action: string, body: unknown = {}) => api<T>(`/api/items/${id}/${action}`, body)

export type ModelOption = { value: string; resolved?: string; label?: string; description?: string; reasoning: boolean; levels?: string[]; image_input?: boolean }
export type ModelCatalog = { reasoning_levels?: string[]; models: ModelOption[]; default?: string; default_reasoning?: string }
export type AgentSettings = {
  agent: string
  values: Record<string, string>
  overrides: Record<string, string>
  catalog?: ModelCatalog
  catalog_error?: string
  runtime_online: boolean
  runtime_mode?: string
  live: string[]
  running?: Record<string, string>
}
export type SettingsResult = { changed: string[]; pending: string[]; restarted?: boolean; warnings: string[] }
