import { create } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { GetReviewResponseSchema, ReviewComparison, ReviewMutationSchema, SaveReviewDraftResponseSchema } from '@/gen/wi/core/v1/reviews_pb'
import { SaveWorkspaceFileResponseSchema, SourceFileSchema } from '@/gen/wi/core/v1/workspaces_pb'

const sent: Record<string, unknown> = {}
vi.mock('./rpc', async original => ({
  ...(await original<object>()),
  reviews: {
    getReview: async (request: unknown) => ((sent.getReview = request), create(GetReviewResponseSchema, { branch: 'wi/x', integrationBranch: 'main' })),
    saveReviewDraft: async (request: unknown) => ((sent.saveReviewDraft = request), create(SaveReviewDraftResponseSchema, { result: create(ReviewMutationSchema, { draftId: 'd1', version: 3 }) })),
  },
  workspaces: {
    saveWorkspaceFile: async (request: unknown) => ((sent.saveWorkspaceFile = request), create(SaveWorkspaceFileResponseSchema, { file: create(SourceFileSchema, { path: 'a.ts', sha256: 'new' }) })),
  },
}))

// The panel's snake_case requests become API messages, and the answers come
// back in the shape its views read: proto field names, enums as numbers.
describe('review calls', () => {
  it('build API requests from the panel and answer in its shapes', async () => {
    const rpc = await import('./review-rpc')
    expect(await rpc.getReview({ item_id: 'i', comparison: ReviewComparison.COMMITTED, commit: '', base_commit: 'b' }, false)).toEqual({ branch: 'wi/x', integration_branch: 'main' })
    expect(sent.getReview).toMatchObject({ itemId: 'i', comparison: ReviewComparison.COMMITTED, baseCommit: 'b' })
    expect(await rpc.saveWorkspaceFile({ item_id: 'i', path: 'a.ts', content: 'x', expected_sha256: 'old', expected_claim_id: 'c', expected_claim_generation: 2 })).toEqual({ result: { file: { path: 'a.ts', sha256: 'new' } } })
    expect(sent.saveWorkspaceFile).toMatchObject({ itemId: 'i', expectedSha256: 'old', expectedClaimId: 'c', expectedClaimGeneration: 2 })
    expect(await rpc.mutateReview('i', 'review-draft', { operation_id: 'op-1', expected_version: 2 })).toEqual({ result: { draft_id: 'd1', version: 3 } })
    expect(sent.saveReviewDraft).toMatchObject({ itemId: 'i', operationId: 'op-1', expectedVersion: 2 })
  })

  it('refuses a request field the API does not have', async () => {
    const rpc = await import('./review-rpc')
    await expect(rpc.getReview({ item_id: 'i', comparsion: 1 }, false)).rejects.toThrow()
  })
})
