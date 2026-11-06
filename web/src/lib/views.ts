// The page's view models: what its components read, built from the API's
// messages by the *-view modules and items.ts.

export type Blocker = {
  id: string
  kind: 'time' | 'item' | 'external' | 'usage'
  until?: string
  item_id?: string
  event?: string
  label?: string
  // provider is the usage provider a usage blocker waits on, such as claude.
  provider?: string
  // follow_up is sent to the agent once the item no longer waits.
  follow_up?: string
}

// FollowUp is a cleared blocker's message, about to be sent.
export type FollowUp = { id: string; message: string }

export type ListItem = {
  id: string
  slug: string
  title: string
  state: 'backlog' | 'active' | 'closed'
  labels: string[] | null
  capturing?: boolean
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
// Agent profiles, kept by the daemon; users counts the open items on one.
export type AgentProfileDefinition = { name: string; driver: string; label?: string; config?: Record<string, unknown> }
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
// driver is the coding agent behind the profile, such as pi or claude.

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
// A message's parts, in the order it was written: text, the model's
// reasoning (its text when the agent shares it, how long it took when known),
// a tool call in its current state, an image. A saved message and a live one
// are the same parts.
export type Thought = { text?: string; duration_ms?: number }
export type MessagePart = { text: string } | { thought: Thought } | { tool: HistoryTool } | { image: HistoryImage }
export type HistoryMessage = {
  id: string
  role: string
  // time is when the message was written, when known.
  time?: string
  // sender is who sent a message sent through wi: "user:…", or another
  // item's agent, "item:<id>".
  sender?: string
  parts: MessagePart[]
  // client_message_id is the ID a client sent this prompt with, when the
  // agent linked them: what this page sent is confirmed by it.
  client_message_id?: string
  // A reply the agent's harness wrote instead of the model, such as a usage
  // limit notice.
  error?: boolean
  // usage_provider names whose quota ran out, when the error reports a usage
  // limit.
  usage_provider?: string
  // A reply the operator stopped before it finished; the turn ended there.
  aborted?: boolean
  native_ids?: string[]
  // A subagent's result, reported to the agent when it finished (role
  // "subagent"); its text is the result.
  subagent?: HistorySubagent
  // What a "summary" message summarizes: a compaction replaced the
  // conversation before it in the agent's context; a branch summary covers
  // a branch the conversation left.
  summary?: { kind: 'compaction' | 'branch'; tokens_before?: number }
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
  // History entries that hold the message, once the driver knows them.
  saved_as?: string[]
  // The message being written, as history will hold it.
  parts?: MessagePart[]
  // Recently sent prompts, by the request ID their sender gave, with the
  // history entries holding them.
  prompts?: PromptLink[]
  activity?: string
  leaf_id?: string
  error?: string
  // The question the agent waits on, while it can be answered here.
  question?: HistoryTool
}

export type ControlView = {
  you: boolean
  status: { holder?: string; lease?: { id?: string; label?: string }; channels: { control?: string }; busy: boolean }
  plan: { plan_token?: string; needs_confirmation: boolean; reasons: string[] | null; replace?: string; start?: string; abort?: boolean; unavailable?: string }
}

export type Manifest = {
  id: string
  slug: string
  title: string
  state: 'backlog' | 'active' | 'closed'
  labels: string[] | null
  resolution?: string
  agent?: string
  blockers?: Blocker[]
  follow_ups?: FollowUp[]
  created_at: string
  updated_at: string
  repository: { remote_url?: string; created_from_commit?: string }
  checkout: { specification: { kind?: string; branch?: string }; claim?: { id?: string; generation?: number; path?: string } | null }
  primary_conversation?: { id: string }
  // owner is who the item is for: "user:…", or "item:<id>" when another
  // item's agent delegated it.
  owner?: string
}

export type Chat = {
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
  capturing?: boolean
  // Why the agent has its status: what needs attention for 'problem' or a
  // question, and for 'busy' what it does, such as starting the agent.
  status_reason?: string
  // The turn is paused on a question answered in the agent's terminal.
  asking?: boolean
  history: History
  control?: ControlView
  unread: boolean
  // background is the background work the idle agent waits on, such as a
  // command or subagent it started; each re-activates it when it ends.
  background?: { id: string; label: string; started_at?: string }[]
  // The checkout's setup while it runs, or after it failed: the agent starts
  // only once it succeeds.
  setup?: WorkspaceSetup
  warnings: string[] | null
}

export type WorkspaceSetup = {
  current: boolean
  run?: { status: 'running' | 'succeeded' | 'failed' | 'skipped'; error?: string; warnings?: string[]; log?: string }
  log_tail?: string
}

export type ModelOption = { value: string; resolved?: string; label?: string; description?: string; reasoning: boolean; levels?: string[]; image_input?: boolean }
export type ModelCatalog = { reasoning_levels?: string[]; models: ModelOption[]; default?: string; default_reasoning?: string }
export type AgentSettings = {
  agent: string
  driver?: string
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
