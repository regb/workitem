import { ApprovalKind } from '@/gen/wi/core/v1/reviews_pb'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ReviewIntent } from './review-intent'

const rpc = vi.hoisted(() => ({ getIntent: vi.fn(), setIntent: vi.fn() }))
vi.mock('@/lib/review-rpc', () => rpc)
let client: QueryClient
beforeEach(() => {
  for (const fn of Object.values(rpc)) fn.mockReset()
})
afterEach(() => {
  cleanup()
  client?.clear()
})
it('approves the displayed revision, retaining uncertainty identity', async () => {
  const sent: object[] = []
  rpc.getIntent.mockResolvedValue({ version: 1, approvals: [] })
  rpc.setIntent.mockImplementation(async (item: string, input: object) => {
    expect(item).toBe('item')
    sent.push(structuredClone(input))
    if (sent.length === 1) throw new Error('decision response lost')
    return { result: { decision_id: 'a1', reused: true } }
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <ReviewIntent id="item" selected={{ revision: 'r2' }} commit="immutable-candidate" />
    </QueryClientProvider>,
  )
  await waitFor(() => expect((screen.getByRole('button', { name: 'Approve revision' }) as HTMLButtonElement).disabled).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: 'Approve revision' }))
  await screen.findByText('decision response lost')
  fireEvent.click(screen.getByRole('button', { name: 'Retry decision' }))
  await screen.findByText('Reviewer intent recorded for this revision.')
  expect(rpc.getIntent.mock.calls[0].slice(0, 2)).toEqual(['item', 'r2'])
  expect(sent).toHaveLength(2)
  expect(sent[1]).toEqual(sent[0])
  expect(sent[0]).toMatchObject({ revision_id: 'r2', kind: ApprovalKind.APPROVE, expected_version: 1 })
})
it('can withdraw a recorded approval after a later informational intent', async () => {
  rpc.getIntent.mockResolvedValue({
    version: 2,
    approved: true,
    approvals: [
      { id: 'a1', kind: ApprovalKind.APPROVE, revision_id: 'r2' },
      { id: 'a2', kind: ApprovalKind.REQUEST_REVIEW, revision_id: 'r2' },
    ],
  })
  client = new QueryClient()
  render(
    <QueryClientProvider client={client}>
      <ReviewIntent id="item" selected={{ revision: 'r2' }} commit="candidate" />
    </QueryClientProvider>,
  )
  const button = (await screen.findByRole('button', { name: 'Withdraw approval' })) as HTMLButtonElement
  expect(button.disabled).toBe(false)
})
it('keeps approval informational despite unresolved feedback or target movement', async () => {
  rpc.getIntent.mockResolvedValue({ version: 1, approved: true, approvals: [{ id: 'a1', kind: ApprovalKind.APPROVE, revision_id: 'r2' }] })
  client = new QueryClient()
  render(
    <QueryClientProvider client={client}>
      <ReviewIntent id="item" selected={{ revision: 'r2' }} commit="candidate" />
    </QueryClientProvider>,
  )
  expect(await screen.findByText('Approved revision.')).toBeTruthy()
})
