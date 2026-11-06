import { BranchKind, RevisionTrigger } from '@/gen/wi/core/v1/reviews_pb'
import { enumWord } from './enums'
import type { CodeRevision, Commit, Commits, PanelState, ReviewMerge } from './review'
import { itemRevisions, mergeBase } from './review-revisions'

// The Change header picks what to review (the target) and what to compare it
// to (the base) from two lists. Values are strings for the select controls;
// these functions map them to and from the panel state, which the queries use.

export type ChangeOption = { value: string; label: string; detail?: string }
export type ChangeOptions = { live: ChangeOption[]; checkpoints: ChangeOption[]; commits: ChangeOption[] }

export type ChangeContext = {
  start: string
  hasCheckout: boolean
  history?: Commits
  revisions: CodeRevision[]
  merges: ReviewMerge[]
}

const short = (sha: string) => sha.slice(0, 8)
// timeOf reads a timestamp the adapter sends either as an RFC 3339 string or
// as a protobuf Timestamp object ({ seconds, nanos }).
export function timeOf(value: unknown): Date | undefined {
  if (typeof value === 'string') return value ? new Date(value) : undefined
  if (value && typeof value === 'object' && 'seconds' in value) return new Date(Number((value as { seconds: number | string }).seconds) * 1000)
  return undefined
}
const when = (value: unknown) => timeOf(value)?.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) ?? ''

const headOf = (history?: Commits) => history?.branches?.find(branch => branch.kind === BranchKind.ITEM)?.sha

// commitsOf lists the item's own commits, newest first, plus a commit picked
// from History that is not one of them.
function commitsOf(context: ChangeContext, panel: PanelState): Commit[] {
  const own = itemRevisions(context.history, context.start).reverse()
  const picked = panel.commit && !own.some(commit => commit.sha === panel.commit) ? context.history?.commits?.find(commit => commit.sha === panel.commit) : undefined
  return picked ? [picked, ...own] : own
}

export function targetValue(panel: PanelState, context: ChangeContext): string {
  if (panel.checkpoint) return `rev:${panel.checkpoint.revision}`
  const head = headOf(context.history)
  switch (panel.comparison) {
    case 'item':
      return context.hasCheckout ? 'working' : 'tip'
    case 'uncommitted':
      return 'working'
    case 'commit':
      return panel.commit && panel.commit !== head ? `commit:${panel.commit}` : 'tip'
    default:
      return 'tip'
  }
}

export function targetOptions(panel: PanelState, context: ChangeContext): ChangeOptions {
  const head = headOf(context.history)
  const live: ChangeOption[] = []
  if (context.hasCheckout) live.push({ value: 'working', label: 'Working tree', detail: 'live' })
  live.push({ value: 'tip', label: 'Branch tip', detail: head ? short(head) : undefined })
  const merged = new Set(context.merges.map(merge => merge.revision_id))
  const checkpoints = [...context.revisions].reverse().map(revision => ({
    value: `rev:${revision.id}`,
    label: revision.id,
    detail: [enumWord(RevisionTrigger, revision.trigger), when(revision.created_at), merged.has(revision.id) ? 'merged' : ''].filter(Boolean).join(' · '),
  }))
  const commits = commitsOf(context, panel)
    .filter(commit => commit.sha !== head)
    .map(commit => ({ value: `commit:${commit.sha}`, label: short(commit.sha), detail: commit.subject }))
  return { live, checkpoints, commits }
}

// selectTarget keeps the chosen base when the target changes, as the old
// revision strip did; a commit compared to its parent keeps no explicit base.
export function selectTarget(value: string, panel: PanelState, reviewBase: string): Partial<PanelState> {
  if (value.startsWith('rev:')) return { checkpoint: { revision: value.slice(4) }, path: '' }
  const parentBase = panel.comparison === 'commit' && !panel.baseCommit
  const baseCommit = panel.baseCommit || (parentBase ? '' : reviewBase || '')
  // Leaving a checkpoint drops its base: a snapshot commit is not on the branch.
  if (panel.checkpoint) return selectTarget(value, { ...panel, checkpoint: undefined, comparison: 'item', baseCommit: '' }, '')
  if (value === 'working') return { checkpoint: undefined, comparison: 'item', commit: '', baseCommit, path: '' }
  if (value === 'tip') return { checkpoint: undefined, comparison: 'committed', baseCommit, path: '' }
  return { checkpoint: undefined, comparison: 'commit', commit: value.slice(7), baseCommit, path: '' }
}

export function baseValue(panel: PanelState, context: ChangeContext, reviewBase: string): string {
  if (panel.checkpoint) return panel.checkpoint.fromRevision ? `rev:${panel.checkpoint.fromRevision}` : 'capture'
  if (panel.comparison === 'integration' && !panel.baseCommit) return 'integration'
  if (panel.comparison === 'commit' && !panel.baseCommit) return 'parent'
  const base = panel.baseCommit || reviewBase
  if (!base || base === context.start) return 'start'
  return `commit:${base}`
}

export function baseOptions(panel: PanelState, context: ChangeContext, reviewBase: string): ChangeOption[] {
  if (panel.checkpoint) {
    const selected = panel.checkpoint.revision
    return [
      { value: 'capture', label: 'Its base', detail: 'what it was captured against' },
      ...[...context.revisions]
        .reverse()
        .filter(revision => revision.id !== selected)
        .map(revision => ({ value: `rev:${revision.id}`, label: revision.id, detail: enumWord(RevisionTrigger, revision.trigger) })),
    ]
  }
  const head = headOf(context.history)
  const integration = context.history?.branches?.find(branch => branch.kind === BranchKind.INTEGRATION)
  const name = integration?.name || 'integration branch'
  const base = mergeBase(context.history)
  // Against the integration branch first: what merging would add, which for
  // an item that merged before is what it did since.
  const options: ChangeOption[] = [{ value: 'integration', label: name, detail: base ? `merge base ${short(base)}` : 'merge base' }]
  const add = (option: ChangeOption) => !options.some(other => other.value === option.value) && options.push(option)
  // Each merge the item made, newest first: the item continued from its
  // result.
  for (const merge of [...context.merges].reverse()) {
    if (merge.after) add({ value: `commit:${merge.after}`, label: 'Merged', detail: `into ${merge.target_ref.replace(/^refs\/heads\//, '') || name} · ${short(merge.after)}` })
  }
  add({ value: 'start', label: 'Item start', detail: context.start ? short(context.start) : undefined })
  if (panel.comparison === 'commit') add({ value: 'parent', label: 'Parent commit' })
  // The branch tip is a base only for the working tree in front of it.
  if (head && head !== context.start && panel.comparison === 'item' && context.hasCheckout) add({ value: `commit:${head}`, label: 'Branch tip', detail: short(head) })
  for (const commit of commitsOf(context, panel)) {
    if (commit.sha !== context.start && commit.sha !== head) add({ value: `commit:${commit.sha}`, label: short(commit.sha), detail: commit.subject })
  }
  const current = baseValue(panel, context, reviewBase)
  if (!options.some(option => option.value === current)) options.push({ value: current, label: short(current.slice(7)) })
  return options
}

export function selectBase(value: string, panel: PanelState, context: ChangeContext): Partial<PanelState> {
  if (panel.checkpoint) return { checkpoint: { revision: panel.checkpoint.revision, fromRevision: value.startsWith('rev:') ? value.slice(4) : undefined }, path: '' }
  if (value === 'integration') return { comparison: 'integration', baseCommit: '', path: '' }
  if (value === 'parent') return { baseCommit: '', path: '' }
  if (value === 'start') return { baseCommit: context.start, path: '' }
  return { baseCommit: value.slice(7), path: '' }
}
