import { fromJson, toJson, type DescMessage, type JsonValue, type MessageShape } from '@bufbuild/protobuf'
import * as pb from '@/gen/wi/core/v1/reviews_pb'
import * as workspacePb from '@/gen/wi/core/v1/workspaces_pb'
import { allPages, awaitJob, repositories, reviews, workspaces } from './rpc'

// The review panel's calls. Its views read the API's JSON mapping with proto
// field names and enums as numbers, which toJson produces from the generated
// messages; requests are built from that mapping with fromJson, so a field
// the API does not have is an error rather than silently dropped.

const json = { useProtoFieldName: true, enumAsInteger: true } as const

export function plain<S extends DescMessage>(schema: S, message: MessageShape<S> | undefined) {
  return (message ? toJson(schema, message, json) : {}) as never
}

function request<S extends DescMessage>(schema: S, value: object) {
  return fromJson(schema, value as JsonValue)
}

// A mutation answers its result as the panel reads it.
type Result<T> = { result: T }

export const getReview = async (input: object, revision: boolean, signal?: AbortSignal) =>
  revision
    ? plain(pb.GetReviewResponseSchema, (await reviews.getRevisionReview(request(pb.GetRevisionReviewRequestSchema, input), { signal })).review)
    : plain(pb.GetReviewResponseSchema, await reviews.getReview(request(pb.GetReviewRequestSchema, input), { signal }))

export const getReviewFile = async (input: object, revision: boolean, signal?: AbortSignal) =>
  revision
    ? plain(pb.GetReviewFileResponseSchema, (await reviews.getRevisionReviewFile(request(pb.GetRevisionReviewFileRequestSchema, input), { signal })).file)
    : plain(pb.GetReviewFileResponseSchema, await reviews.getReviewFile(request(pb.GetReviewFileRequestSchema, input), { signal }))

export const listRevisions = async (itemId: string, signal?: AbortSignal) =>
  plain(pb.ListReviewRevisionsResponseSchema, await allPages(pageToken => reviews.listReviewRevisions({ itemId, pageToken }, { signal }), page => page.revisions))

export const captureRevision = async (input: object): Promise<Result<never>> => ({
  result: plain(pb.CaptureReviewRevisionResponseSchema, await reviews.captureReviewRevision(request(pb.CaptureReviewRevisionRequestSchema, input))),
})

export const listCommits = async (itemId: string, signal?: AbortSignal) => plain(pb.ListReviewCommitsResponseSchema, await reviews.listReviewCommits({ itemId, includeContext: true }, { signal }))

export const listWorkspaceFiles = async (itemId: string, signal?: AbortSignal) => plain(workspacePb.ListWorkspaceFilesResponseSchema, await workspaces.listWorkspaceFiles({ itemId }, { signal }))

export const getWorkspaceFile = async (input: object, signal?: AbortSignal) => plain(workspacePb.GetWorkspaceFileResponseSchema, await workspaces.getWorkspaceFile(request(workspacePb.GetWorkspaceFileRequestSchema, input), { signal }))

export const saveWorkspaceFile = async (input: object): Promise<Result<never>> => ({
  result: plain(workspacePb.SaveWorkspaceFileResponseSchema, await workspaces.saveWorkspaceFile(request(workspacePb.SaveWorkspaceFileRequestSchema, input))),
})

export const commitWorkspace = async (itemId: string, message: string): Promise<Result<never>> => ({
  result: plain(workspacePb.CommitWorkspaceResponseSchema, await workspaces.commitWorkspace({ itemId, message })),
})

// mergeItem integrates the item's branch, in a job it waits for.
export async function mergeItem(itemId: string, message: string, rebase: boolean) {
  const out = await repositories.integrateItem({ itemId, message, rebase })
  return { result: await awaitJob(out.job) }
}

export const getThreads = async (itemId: string, signal?: AbortSignal) =>
  plain(pb.GetReviewThreadsResponseSchema, await allPages(pageToken => reviews.getReviewThreads({ itemId, pageToken }, { signal }), page => page.threads))
export const getDrafts = async (itemId: string, signal?: AbortSignal) => plain(pb.GetReviewDraftsResponseSchema, await reviews.getReviewDrafts({ itemId }, { signal }))

// mutateReview saves or deletes drafts, publishes a round or replies.
export async function mutateReview(itemId: string, action: 'review-draft' | 'review-drafts/delete' | 'review-round' | 'review-replies', input: object): Promise<Result<never>> {
  const value = { ...input, item_id: itemId }
  switch (action) {
    case 'review-draft':
      return { result: plain(pb.ReviewMutationSchema, (await reviews.saveReviewDraft(request(pb.SaveReviewDraftRequestSchema, value))).result) }
    case 'review-drafts/delete':
      return { result: plain(pb.ReviewMutationSchema, (await reviews.deleteReviewDrafts(request(pb.DeleteReviewDraftsRequestSchema, value))).result) }
    case 'review-round':
      return { result: plain(pb.ReviewMutationSchema, (await reviews.publishReviewRound(request(pb.PublishReviewRoundRequestSchema, value))).result) }
    case 'review-replies':
      return { result: plain(pb.ReviewMutationSchema, (await reviews.replyReviewThreads(request(pb.ReplyReviewThreadsRequestSchema, value))).result) }
  }
}

export const getIntent = async (itemId: string, revisionId: string, signal?: AbortSignal) => plain(pb.GetReviewIntentResponseSchema, await reviews.getReviewIntent({ itemId, revisionId }, { signal }))

export const setIntent = async (itemId: string, input: object): Promise<Result<never>> => ({
  result: plain(pb.ReviewMutationSchema, (await reviews.setReviewIntent(request(pb.SetReviewIntentRequestSchema, { ...input, item_id: itemId }))).result),
})

export const notifyRound = async (itemId: string, input: object): Promise<Result<never>> => ({
  result: plain(pb.ReviewRoundSchema, (await reviews.notifyReviewRound(request(pb.NotifyReviewRoundRequestSchema, { ...input, item_id: itemId }))).round),
})
