import { timestampDate } from '@bufbuild/protobuf/wkt'
import { AgentStatus, ItemState, Resolution, WorktreeStatus } from '@/gen/wi/core/v1/common_pb'
import { BlockerKind, ListOrder, ListSection, type Blocker as BlockerMessage, type ItemSummary } from '@/gen/wi/core/v1/items_pb'
import type { Blocker, ListItem, Project, SearchHit, SearchResult, WorkList } from './views'
import { labelRules } from './label-filter'
import { listProjects } from './repositories'
import { items } from './rpc'

// Work-list rows built from the API's item summaries, as wi list shows them.

// enumName is an enum value's name in lower case, without its prefix: the
// generated enums already drop it (ITEM_STATE_ACTIVE is ACTIVE).
const enumName = (names: Record<number, string>, value: number) => (names[value] ?? '').toLowerCase()

export function blockerView(blocker: BlockerMessage): Blocker {
  return {
    id: blocker.id,
    kind: enumName(BlockerKind, blocker.kind) as Blocker['kind'],
    until: blocker.until ? timestampDate(blocker.until).toISOString() : undefined,
    item_id: blocker.itemId || undefined,
    event: blocker.event || undefined,
    label: blocker.label || undefined,
    provider: blocker.provider || undefined,
    follow_up: blocker.followUp || undefined,
  }
}

export function listItemView(summary: ItemSummary): ListItem {
  const row: ListItem = {
    id: summary.id,
    slug: summary.slug,
    title: summary.title,
    state: enumName(ItemState, summary.state) as ListItem['state'],
    resolution: summary.resolution ? enumName(Resolution, summary.resolution) : undefined,
    labels: summary.labels,
    blockers: summary.blockers.map(blockerView),
    project_id: summary.repositoryId,
  }
  const o = summary.observation
  if (o) {
    row.unread = o.unread
    row.capturing = o.capturing
    if (o.agentStatus) row.agent = { status: enumName(AgentStatus, o.agentStatus) }
    if (o.worktreeStatus) row.worktree = { status: enumName(WorktreeStatus, o.worktreeStatus), reason: o.worktreeReason, dirty: o.worktreeDirty, branch_mismatch: o.branchMismatch }
  }
  return row
}

// sectionName is a dashboard section's name, as wi list shows it: the
// daemon places every row of an attention-ordered list in one.
const sectionName = (section: ListSection) => (ListSection[section || ListSection.NEEDS_ATTENTION] ?? '').replaceAll('_', ' ')

// workListView groups rows into the dashboard's sections, in the daemon's
// order of sections and of rows within them.
export function workListView(rows: ItemSummary[], projects: Project[], warnings: string[] = []): WorkList {
  const sections = new Map<string, ListItem[]>()
  for (const row of [...rows].sort((a, b) => (a.section || ListSection.NEEDS_ATTENTION) - (b.section || ListSection.NEEDS_ATTENTION))) {
    const name = sectionName(row.section)
    sections.set(name, [...(sections.get(name) ?? []), listItemView(row)])
  }
  return { display_sections: [...sections].map(([name, items]) => ({ name, items })), projects, warnings }
}

// readWorkList reads the dashboard: open work, and the latest closed items.
// It answers from the daemon's observations, which it renews while watched.
export async function readWorkList(signal?: AbortSignal): Promise<WorkList> {
  const [listed, projects] = await Promise.all([
    items.listItems({ order: ListOrder.ATTENTION, states: [ItemState.ACTIVE, ItemState.BACKLOG, ItemState.CLOSED], closedLimit: 50, labelRules: labelRules() }, { signal }),
    listProjects(),
  ])
  return workListView(listed.items, projects, listed.warnings)
}

// searchItems finds items by text, in one lifecycle state when given.
export async function searchItems(query: string, state: string): Promise<SearchResult> {
  const states = state ? [ItemState[state.toUpperCase() as keyof typeof ItemState]].filter(value => value !== undefined) : []
  const value = await items.searchItems({ query, states })
  return { total: value.total, items: value.hits.filter(hit => hit.item).map(hit => ({ ...listItemView(hit.item!), field: hit.field as SearchHit['field'], excerpt: hit.excerpt || undefined })) }
}
