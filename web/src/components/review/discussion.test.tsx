import { ReviewRole, ReviewSide, ThreadResolution, ThreadStatus } from '@/gen/wi/core/v1/reviews_pb'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Discussion } from './discussion'
import { reviewUI, type CheckpointSelection } from '@/lib/review'
import type { ReviewDrafts, ReviewThreads } from '@/lib/review-discussion'

const rpc = vi.hoisted(() => ({ getThreads: vi.fn(), getDrafts: vi.fn(), mutateReview: vi.fn() }))
vi.mock('@/lib/review-rpc', () => rpc)

const selected: CheckpointSelection = { revision: 'r2', fromRevision: 'r1' }
let client: QueryClient
let threads: ReviewThreads
let drafts: ReviewDrafts
let sent: { item: string; action: string; body: Record<string, any> }[]
// record keeps what a mutation was sent, as it was at the time of the call.
const record = (item: string, action: string, input: object) => {
  const body = structuredClone(input) as Record<string, any>
  sent.push({ item, action, body })
  return body
}
function mount() {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <Discussion id="item" selected={selected} paths={['a.ts']} path="a.ts" />
    </QueryClientProvider>,
  )
}
beforeEach(() => {
  reviewUI.set(state => ({ ...state, comments: new Map(), panels: new Map() }))
  threads = { version: 1, threads: [], rounds: [] }
  drafts = { version: 1, drafts: [] }
  sent = []
  for (const fn of Object.values(rpc)) fn.mockReset()
  rpc.getThreads.mockImplementation(async () => structuredClone(threads))
  rpc.getDrafts.mockImplementation(async () => structuredClone(drafts))
  rpc.mutateReview.mockImplementation(async (item: string, action: string, input: object) => {
    record(item, action, input)
    return { result: { draft_id: 'd1' } }
  })
})
afterEach(() => {
  cleanup()
  client?.clear()
})

it('saves a comment against immutable comparison endpoints and an explicit side/range', async () => {
  mount()
  fireEvent.change(screen.getByRole('textbox', { name: 'Review comment' }), { target: { value: 'Check this case' } })
  fireEvent.change(screen.getByRole('textbox', { name: 'Comment first line' }), { target: { value: '2' } })
  fireEvent.change(screen.getByRole('textbox', { name: 'Comment last line' }), { target: { value: '3' } })
  await waitFor(() => expect((screen.getByRole('button', { name: 'Save comment draft' }) as HTMLButtonElement).disabled).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: 'Save comment draft' }))
  await screen.findByText('Saved d1. Publish feedback when ready.')
  expect(sent[0]).toMatchObject({ item: 'item', action: 'review-draft' })
  expect(sent[0].body).toMatchObject({
    expected_version: 1,
    draft: { body: 'Check this case', anchor: { revision_id: 'r2', from_revision_id: 'r1', path: 'a.ts', side: ReviewSide.NEW, start_line: 2, end_line: 3 } },
  })
  expect((screen.getByRole('textbox', { name: 'Review comment' }) as HTMLTextAreaElement).value).toBe('')
})

it('retries uncertain operations with the exact original payload and keeps newer typing', async () => {
  rpc.mutateReview.mockImplementation(async (item: string, action: string, input: object) => {
    record(item, action, input)
    if (sent.length === 1) throw new Error('network lost')
    return { result: { draft_id: 'd1' } }
  })
  mount()
  const input = screen.getByRole('textbox', { name: 'Review comment' }) as HTMLTextAreaElement
  fireEvent.change(input, { target: { value: 'original comment' } })
  await waitFor(() => expect((screen.getByRole('button', { name: 'Save comment draft' }) as HTMLButtonElement).disabled).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: 'Save comment draft' }))
  await screen.findByText('network lost')
  fireEvent.change(input, { target: { value: 'newer comment while waiting' } })
  fireEvent.click(screen.getByRole('button', { name: 'Retry review operation' }))
  await screen.findByText('Saved d1. Publish feedback when ready.')
  expect(sent).toHaveLength(2)
  expect(sent[1]).toEqual(sent[0])
  expect(input.value).toBe('newer comment while waiting')
})

it('preserves unsaved comment buffers when the code panel unmounts', () => {
  const first = mount()
  fireEvent.change(screen.getByRole('textbox', { name: 'Review comment' }), { target: { value: 'keep this unsaved thought' } })
  first.unmount()
  client.clear()
  mount()
  expect((screen.getByRole('textbox', { name: 'Review comment' }) as HTMLTextAreaElement).value).toBe('keep this unsaved thought')
})

it('treats addressed as unresolved and publishes a reviewer decision only as a round', async () => {
  threads.threads = [
    {
      id: 't1',
      anchor: { revision_id: 'r1', path: 'a.ts', side: ReviewSide.NEW, start_line: 1, end_line: 1 },
      status: ThreadStatus.ADDRESSED,
      messages: [
        { id: 'm1', role: ReviewRole.REVIEWER, body: 'Check this', revision_id: 'r1' },
        { id: 'm2', role: ReviewRole.AGENT, body: 'Fixed', addressed: true, revision_id: 'r2' },
      ],
    },
  ]
  rpc.mutateReview.mockImplementation(async (item: string, action: string, input: object) => {
    const body = record(item, action, input)
    if (action === 'review-draft') {
      drafts.drafts = [{ id: 'd2', thread_id: 't1', anchor: body.draft.anchor, resolution: ThreadResolution.RESOLVED }]
      return { result: { draft_id: 'd2' } }
    }
    threads.threads![0].status = ThreadStatus.RESOLVED
    drafts.drafts = []
    return { result: { round_id: 'q2' } }
  })
  mount()
  await screen.findByText('Agent addressed, awaiting reviewer')
  fireEvent.click(screen.getByRole('button', { name: 'Draft resolution' }))
  await screen.findByRole('button', { name: 'Publish feedback (1)' })
  expect(screen.getByText('Agent addressed, awaiting reviewer')).toBeTruthy()
  expect(sent).toHaveLength(1)
  expect(sent[0].action).toBe('review-draft')
  expect(sent[0].body.draft).toMatchObject({ thread_id: 't1', resolution: ThreadResolution.RESOLVED, anchor: { revision_id: 'r2', from_revision_id: 'r1' } })
  fireEvent.click(screen.getByRole('button', { name: 'Publish feedback (1)' }))
  await screen.findByText('Published q2.')
  expect(sent[1].action).toBe('review-round')
  expect(sent[1].body).toMatchObject({ draft_ids: ['d2'], revision_id: 'r2', from_revision_id: 'r1' })
  expect(threads.threads![0].anchor.revision_id).toBe('r1')
})

it('refreshes a durably published round after notification fails', async () => {
  drafts.drafts = [{ id: 'd1', anchor: { revision_id: 'r2', from_revision_id: 'r1' }, body: 'Saved concern' }]
  rpc.mutateReview.mockImplementation(async () => {
    drafts.drafts = []
    threads.rounds = [{ id: 'q1', revision_id: 'r2' }]
    throw new Error('Published q1; no agent is running')
  })
  mount()
  fireEvent.click(await screen.findByRole('button', { name: 'Publish feedback (1)' }))
  await screen.findByText('Published q1; no agent is running')
  expect(await screen.findByRole('article', { name: 'Published round q1' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Publish feedback (1)' })).toBeNull()
})
it('does not publish drafts anchored to another comparison', async () => {
  drafts.drafts = [{ id: 'd1', anchor: { revision_id: 'r1' }, body: 'Older feedback' }]
  mount()
  const button = (await screen.findByRole('button', { name: 'Publish feedback (0)' })) as HTMLButtonElement
  expect(button.disabled).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: 'd1 at r1' }))
  expect(reviewUI.get().panels.get('item')?.checkpoint).toMatchObject({ revision: 'r1' })
})
