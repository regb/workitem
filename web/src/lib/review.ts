import type { BranchKind, FileChange, RevisionTrigger } from '@/gen/wi/core/v1/reviews_pb'
import { ReviewComparison } from '@/gen/wi/core/v1/reviews_pb'
import * as rpc from './review-rpc'
import { createStore } from './store'
import { newClientID } from './session'
import type { FileTarget } from './file-references'
import type { CommentBuffer } from './review-discussion'

export type FileNavigation = FileTarget & { request: number }
let fileRequest = 0
const pendingFiles = new Map<string, number>()
export function beginFileOpen(id: string) {
  const request = ++fileRequest
  pendingFiles.set(id, request)
  return request
}
export const isCurrentFileOpen = (id: string, request: number) => pendingFiles.get(id) === request

export type Comparison = 'item' | 'committed' | 'uncommitted' | 'integration' | 'commit'
export type SourceFile = {
  path: string
  content?: string
  sha256?: string
  exists?: boolean
  binary?: boolean
  too_large?: boolean
}
export type Revision = {
  version: string
  base_commit: string
  head_commit: string
  claim_id?: string
  claim_generation?: number
  working_tree?: boolean
}
export type Review = {
  revision: Revision
  files?: { path: string; change: FileChange }[]
  truncated?: boolean
  branch: string
  integration_branch: string
}
export type ReviewFile = {
  revision: Revision
  old_file: SourceFile
  new_file: SourceFile
}
export type WorkspaceFiles = {
  paths?: string[]
  truncated?: boolean
  claim_id: string
  claim_generation: number
}
export type WorkspaceFile = {
  file: SourceFile
  claim_id: string
  claim_generation: number
}
export type Commit = {
  sha: string
  subject: string
  author: string
  authored_at: string
  parent_shas?: string[]
}
export type ReviewBranch = { name: string; sha?: string; kind: BranchKind }
export type Commits = { commits?: Commit[]; truncated?: boolean; branches?: ReviewBranch[]; warnings?: string[] }
export type FileDraft = {
  session_id: string
  file: SourceFile
  content: string
  claim_id: string
  claim_generation: number
}

export type CheckpointSelection = { revision: string; fromRevision?: string }
export type CodeRevision = {
  detached_head?: boolean
  id: string
  capture_id: string
  commit: string
  tree: string
  observed_head: string
  observed_branch?: string
  base_commit: string
  // The adapter sends the API's timestamp as is: { seconds, nanos }.
  created_at?: string | { seconds: number | string; nanos?: number }
  trigger: RevisionTrigger
  excluded?: { path: string; reason: string }[]
}
export type ReviewMerge = { revision_id: string; commit: string; tree: string; target_ref: string; before: string; after: string }
export type ReviewRevisions = { revisions?: CodeRevision[]; version?: number; merges?: ReviewMerge[]; capture_gaps?: { id: string; reason: string }[] }
export type CapturedRevision = { revision: CodeRevision; version?: number; reused?: boolean }

export type PanelState = {
  tab: 'changes' | 'files' | 'history'
  comparison: Comparison
  commit: string
  baseCommit: string
  path: string
  split: boolean
  wrap: boolean
  chatWidth: number
  fileTarget?: FileNavigation
  checkpoint?: CheckpointSelection
}

// Drafts survive panel closing and item navigation within the tab. They are
// never replaced by polling. A claim change leaves its old drafts intact.
export const minChatWidth = 320
export const maxChatWidth = 720
export const defaultPanel: PanelState = { tab: 'changes', comparison: 'item', commit: '', baseCommit: '', path: '', split: true, wrap: true, chatWidth: 600 }

export const reviewUI = createStore({
  open: new Set<string>(),
  drafts: new Map<string, FileDraft>(),
  mergeMessages: new Map<string, string>(),
  comments: new Map<string, CommentBuffer>(),
  panels: new Map<string, PanelState>(),
})
export function setCommentBuffer(key: string, value?: CommentBuffer) {
  reviewUI.set(state => {
    const comments = new Map(state.comments)
    if (value) comments.set(key, value)
    else comments.delete(key)
    return { ...state, comments }
  })
}
// setMergeMessage keeps an edited merge message across panel remounts; no
// value restores the default, the item title.
export function setMergeMessage(id: string, value?: string) {
  reviewUI.set(state => {
    const mergeMessages = new Map(state.mergeMessages)
    if (value === undefined) mergeMessages.delete(id)
    else mergeMessages.set(id, value)
    return { ...state, mergeMessages }
  })
}

export const draftKey = (id: string, path: string) => `${id}:${path}`
export function toggleReview(id: string) {
  reviewUI.set(state => {
    const open = new Set(state.open)
    if (open.has(id)) open.delete(id)
    else open.add(id)
    return { ...state, open }
  })
}
export function newFileDraft(source: WorkspaceFile): FileDraft {
  return { session_id: newClientID(), file: source.file, content: source.file.content ?? '', claim_id: source.claim_id, claim_generation: source.claim_generation }
}

export function setFileDraft(id: string, path: string, draft?: FileDraft) {
  reviewUI.set(state => {
    const drafts = new Map(state.drafts)
    if (draft) drafts.set(draftKey(id, path), draft)
    else drafts.delete(draftKey(id, path))
    return { ...state, drafts }
  })
}
// Keep panel state in one place rather than mirroring six local fields into
// the store after every render. Each patch preserves the other selections.
export function setPanel(id: string, patch: Partial<PanelState> | ((current: PanelState) => Partial<PanelState>)) {
  reviewUI.set(state => {
    const current = state.panels.get(id) ?? defaultPanel
    const update = typeof patch === 'function' ? patch(current) : patch
    const next = { ...current, ...update }
    if ('path' in update) {
      pendingFiles.delete(id)
      if (!('fileTarget' in update)) next.fileTarget = undefined
    }
    next.chatWidth = Math.round(Math.max(minChatWidth, Math.min(maxChatWidth, next.chatWidth)))
    return { ...state, panels: new Map(state.panels).set(id, next) }
  })
}

// File chips open the built-in panel only after a click. A later click or
// manual navigation wins over an older asynchronous file lookup.
export function openWorkspaceFile(id: string, target: FileTarget, request = beginFileOpen(id)) {
  if (!isCurrentFileOpen(id, request)) return
  pendingFiles.delete(id)
  reviewUI.set(state => ({
    ...state,
    open: new Set(state.open).add(id),
    panels: new Map(state.panels).set(id, {
      ...(state.panels.get(id) ?? defaultPanel),
      tab: 'files',
      path: target.directory ? '' : target.path,
      fileTarget: { ...target, request },
    }),
  }))
}

// A response belongs to the edit session that sent it, not whatever draft is
// currently open at that path. Late responses must not consume newer drafts.
export function acceptFileSave(id: string, path: string, sent: FileDraft, file: SourceFile) {
  const current = reviewUI.get().drafts.get(draftKey(id, path))
  if (!current || current.session_id !== sent.session_id || current.file.sha256 !== sent.file.sha256 || current.claim_id !== sent.claim_id || current.claim_generation !== sent.claim_generation) return
  setFileDraft(id, path, current.content === sent.content ? undefined : { ...current, file })
}

export const reviewKey = (id: string) => ['review', id] as const
export const sourceKey = (id: string, path: string, claim: Pick<WorkspaceFiles, 'claim_id' | 'claim_generation'>) => [...reviewKey(id), 'source', path, claim.claim_id, claim.claim_generation] as const
const comparisonValue = (comparison: Comparison) => ReviewComparison[comparison.toUpperCase() as keyof typeof ReviewComparison]
const checkpointRequest = (id: string, selection: CheckpointSelection) => ({ item_id: id, revision_id: selection.revision, from_revision_id: selection.fromRevision ?? '' })
export const getReview = (id: string, comparison: Comparison, commit: string, signal?: AbortSignal, baseCommit = '', checkpoint?: CheckpointSelection): Promise<Review> =>
  checkpoint ? rpc.getReview(checkpointRequest(id, checkpoint), true, signal) : rpc.getReview({ item_id: id, comparison: comparisonValue(comparison), commit, base_commit: baseCommit }, false, signal)
export const getReviewFile = (id: string, comparison: Comparison, commit: string, path: string, version: string, signal?: AbortSignal, baseCommit = '', checkpoint?: CheckpointSelection): Promise<ReviewFile> =>
  checkpoint
    ? rpc.getReviewFile({ comparison: checkpointRequest(id, checkpoint), path, expected_version: version }, true, signal)
    : rpc.getReviewFile({ item_id: id, comparison: comparisonValue(comparison), commit, base_commit: baseCommit, path, expected_version: version }, false, signal)
export const getReviewRevisions = (id: string, signal?: AbortSignal): Promise<ReviewRevisions> => rpc.listRevisions(id, signal)
export const captureRevision = (id: string, claim: { id: string; generation: number }, captureID: string): Promise<{ result: CapturedRevision }> =>
  rpc.captureRevision({ item_id: id, capture_id: captureID, expected_claim_id: claim.id, expected_claim_generation: claim.generation })
export const getCommits = (id: string, signal?: AbortSignal): Promise<Commits> => rpc.listCommits(id, signal)
export const workspaceDocumentURL = (id: string, path: string, claim: Pick<WorkspaceFiles, 'claim_id' | 'claim_generation'>) =>
  `/api/items/${encodeURIComponent(id)}/document?${new URLSearchParams({ path, claim_id: claim.claim_id, claim_generation: String(claim.claim_generation) })}`

export const getWorkspaceFiles = (id: string, signal?: AbortSignal): Promise<WorkspaceFiles> => rpc.listWorkspaceFiles(id, signal)
export const getWorkspaceFile = (id: string, path: string, claim: WorkspaceFiles, signal?: AbortSignal): Promise<WorkspaceFile> =>
  rpc.getWorkspaceFile({ item_id: id, path, expected_claim_id: claim.claim_id, expected_claim_generation: claim.claim_generation }, signal)
export const saveFile = (id: string, path: string, draft: FileDraft): Promise<{ result: { file: SourceFile } }> =>
  rpc.saveWorkspaceFile({ item_id: id, path, content: draft.content, expected_sha256: draft.file.sha256, expected_claim_id: draft.claim_id, expected_claim_generation: draft.claim_generation })
