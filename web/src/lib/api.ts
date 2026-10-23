// Types and client for the wi web API (internal/web). The server owns all
// policy; these types mirror its JSON.
import { clientID, token } from './session'

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
  project_id: string
}

export type Project = { id: string; name: string }
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
export type HistoryTool = { id?: string; name: string; input?: string; output?: string; status: string; images?: HistoryImage[] }
export type HistoryMessage = {
  id: string
  role: string
  text: string
  // A reply the agent's harness wrote instead of the model, such as a usage
  // limit notice.
  error?: boolean
  tools?: HistoryTool[]
  images?: HistoryImage[]
  native_ids?: string[]
}
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
  status: { holder?: string; channels: { control?: string }; busy: boolean }
  plan: { needs_confirmation: boolean; reasons: string[] | null; replace?: string; start?: string; abort?: boolean; unavailable?: string }
}

export type Creation = { id: string; title: string; state: 'queued' | 'starting' | 'sent' | 'problem'; error?: string }

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
    runtime?: { id: string; mode: string }
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
  warnings: string[] | null
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
  if (token) result.Authorization = `Bearer ${token}`
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
  // the API (a missing token, a foreign origin), which are plain text.
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

export type ModelOption = { value: string; label?: string; description?: string; reasoning: boolean; levels?: string[]; image_input?: boolean }
export type ModelCatalog = { model_setting: string; reasoning_setting?: string; reasoning_levels?: string[]; models: ModelOption[] }
export type AgentSettings = {
  agent: string
  values: Record<string, string>
  overrides: Record<string, string>
  catalog?: ModelCatalog
  catalog_error?: string
  runtime_online: boolean
  runtime_mode?: string
  live: string[]
}
export type SettingsResult = { changed: string[]; pending: string[]; restarted?: boolean; warnings: string[] }
