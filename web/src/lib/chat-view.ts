import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt'
import { ControlChannel, type Agent, type ControlStatus } from '@/gen/wi/core/v1/agents_pb'
import { AgentMode, AgentStatus, ItemState, Resolution } from '@/gen/wi/core/v1/common_pb'
import type { Item } from '@/gen/wi/core/v1/items_pb'
import type { GetItemViewResponse } from '@/gen/wi/core/v1/views_pb'
import { SetupState } from '@/gen/wi/core/v1/workspaces_pb'
import type { Chat, ControlView, Manifest } from './views'
import { historyView } from './history-view'
import { blockerView } from './items'
import { views } from './rpc'

// The conversation page's view of an item, built from the API's item view.

const lower = (names: Record<number, string>, value: number) => (names[value] ?? '').toLowerCase()
const iso = (at?: Timestamp) => (at ? timestampDate(at).toISOString() : '')
const mode = (value: AgentMode) => (value === AgentMode.TUI ? 'tui' : value === AgentMode.HEADLESS ? 'headless' : '')

function manifestView(item: Item | undefined): Manifest {
  if (!item) return { id: '', slug: '', title: '', state: 'backlog', labels: [], created_at: '', updated_at: '', repository: {}, checkout: { specification: {} } }
  const claim = item.checkout?.claim
  return {
    id: item.id,
    slug: item.slug,
    title: item.title,
    state: lower(ItemState, item.state) as Manifest['state'],
    labels: item.labels,
    resolution: item.resolution ? lower(Resolution, item.resolution) : undefined,
    agent: item.agentProfile || undefined,
    blockers: item.blockers.map(blockerView),
    follow_ups: item.followUps.length ? item.followUps.map(followUp => ({ id: followUp.id, message: followUp.message })) : undefined,
    created_at: iso(item.createdAt),
    updated_at: iso(item.updatedAt),
    repository: { created_from_commit: item.createdFromCommit || undefined },
    checkout: {
      specification: { kind: item.checkout?.kind || undefined, branch: item.checkout?.branch || undefined },
      claim: claim ? { id: claim.id, generation: claim.generation, path: claim.path } : null,
    },
    primary_conversation: item.conversation ? { id: item.conversation.id } : undefined,
    owner: item.owner || undefined,
  }
}

// controlChannel is how the agent takes input: what its driver reports, or
// for a driver that reports none, what its capabilities imply.
function controlChannel(agent: Agent) {
  const c = agent.capabilities
  if (!c) return ''
  if (c.control === ControlChannel.STRUCTURED) return 'structured'
  if (c.control === ControlChannel.EMULATED) return 'emulated'
  if (!c.prompt) return ''
  return agent.mode === AgentMode.TUI && !c.atomicIdle ? 'emulated' : 'structured'
}

function controlView(control: ControlStatus, agent: Agent | undefined): ControlView {
  const lease = control.lease
  const plan = control.plan
  return {
    you: control.yours,
    status: {
      // An attached terminal controls the agent without holding a lease.
      holder: lease ? lease.holder : control.implicit ? 'terminal' : undefined,
      lease: lease ? { id: lease.id, label: lease.label || undefined } : undefined,
      channels: { control: (agent && controlChannel(agent)) || undefined },
      busy: agent?.status === AgentStatus.BUSY,
    },
    plan: {
      plan_token: plan?.planToken || undefined,
      needs_confirmation: !!plan?.needsConfirmation,
      reasons: plan?.reasons ?? [],
      replace: (plan && mode(plan.restartsIn)) || undefined,
      start: (plan && mode(plan.startsIn)) || undefined,
      abort: plan?.abortsTurn || undefined,
      unavailable: plan?.unavailable || undefined,
    },
  }
}

function runtimeView(agent: Agent | undefined): Chat['runtime'] {
  if (!agent) return { online: false, capabilities: {} }
  const c = agent.capabilities
  return {
    online: agent.online,
    runtime: agent.generation ? { id: String(agent.generation), mode: mode(agent.mode), settings: agent.runningSettings } : undefined,
    capabilities: c ? { prompt: c.prompt, steering: c.steering, follow_up: c.followUp, image_input: c.imageInput } : {},
  }
}

export function chatView(view: GetItemViewResponse): Chat {
  const item = manifestView(view.item)
  const agent = view.agent
  const chat: Chat = {
    description: view.description,
    project: { id: view.item?.repository?.id ?? '', name: view.item?.repository?.name ?? '' },
    item,
    runtime: runtimeView(agent),
    status: agent ? (agent.status ? lower(AgentStatus, agent.status) : 'unknown') : '',
    status_reason: agent?.reason || undefined,
    asking: agent?.asking || undefined,
    history: historyView(view.history ?? { messages: [], nextBefore: '', truncated: false }),
    unread: !!view.observation?.unread,
    capturing: view.observation?.capturing || undefined,
    background: view.observation?.background.length
      ? view.observation.background.map(work => ({ id: work.id, label: work.label, started_at: work.startedAt ? timestampDate(work.startedAt).toISOString() : undefined }))
      : undefined,
    warnings: view.warnings,
  }
  if (view.control) chat.control = controlView(view.control, agent)
  // A setup that succeeded no longer explains anything.
  const setup = view.setup
  if (setup && setup.state !== SetupState.SUCCEEDED) {
    chat.setup = { current: true, run: { status: setup.state === SetupState.RUNNING ? 'running' : 'failed', error: setup.error || undefined, warnings: setup.warnings }, log_tail: setup.logTail || undefined }
  }
  return chat
}

// readChat reads what the conversation page shows for an item.
export const readChat = async (id: string, signal?: AbortSignal) => chatView(await views.getItemView({ itemId: id }, { signal }))
