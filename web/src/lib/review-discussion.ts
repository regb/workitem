import { api, itemAction, type Outcome } from './api'
import type { CheckpointSelection } from './review'
export type ReviewAnchor = { revision_id: string; from_revision_id?: string; path?: string; side?: string; start_line?: number; end_line?: number; blob_sha256?: string }
export type ReviewMessage = { id: string; role: 'reviewer' | 'agent'; body?: string; revision_id: string; round_id?: string; addressed?: boolean; resolution?: string }
export type ReviewThread = { id: string; anchor: ReviewAnchor; messages?: ReviewMessage[]; status: 'open' | 'addressed' | 'resolved' | 'withdrawn' }
export type ReviewDraft = { id: string; thread_id?: string; anchor: ReviewAnchor; body?: string; resolution?: string }
export type ReviewRound = { id: string; revision_id: string; notified_at?: string; command_id?: string }
export type ReviewThreads = { threads?: ReviewThread[]; rounds?: ReviewRound[]; version?: number }
export type ReviewDrafts = { drafts?: ReviewDraft[]; version?: number }
export type ReviewMutation = { round_id?: string; draft_id?: string; thread_ids?: string[]; version?: number; reused?: boolean }
export type CommentBuffer = { body: string; path: string; side: string; start: string; end: string }
export const reviewDiscussionKey = (id: string) => ['review', id, 'discussion'] as const
export const getReviewThreads = (id: string, signal?: AbortSignal) => api<ReviewThreads>(`/api/items/${id}/review-threads`, undefined, signal)
export const getReviewDrafts = (id: string, signal?: AbortSignal) => api<ReviewDrafts>(`/api/items/${id}/review-drafts`, undefined, signal)
export const mutateReview = (id: string, action: 'review-draft' | 'review-drafts/delete' | 'review-round', request: object) => itemAction<Outcome<ReviewMutation>>(id, action, request)
export const comparisonAnchor = (selected: CheckpointSelection): ReviewAnchor => ({ revision_id: selected.revision, from_revision_id: selected.fromRevision })
export const sameComparison = (draft: ReviewDraft, selected: CheckpointSelection) =>
  draft.anchor.revision_id === selected.revision && (draft.anchor.from_revision_id ?? '') === (selected.fromRevision ?? '')
export const commentBufferKey = (id: string, selected: CheckpointSelection, thread = '') => JSON.stringify([id, selected.revision, selected.fromRevision ?? '', thread])
