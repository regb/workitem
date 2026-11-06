import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ReviewIntent } from './review-intent'
let client: QueryClient
afterEach(() => {
  cleanup()
  client?.clear()
  vi.unstubAllGlobals()
})
it('approves the displayed revision, retaining uncertainty identity', async () => {
  const sent: object[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method !== 'POST') return new Response(JSON.stringify({ version: 1, approvals: [] }))
      sent.push(JSON.parse(String(init.body)))
      if (sent.length === 1) throw new Error('decision response lost')
      return new Response(JSON.stringify({ result: { decision_id: 'a1', reused: true } }))
    }),
  )
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
  expect(sent).toHaveLength(2)
  expect(sent[1]).toEqual(sent[0])
  expect(sent[0]).toMatchObject({ revision_id: 'r2', kind: 'approve', expected_version: 1 })
})
it('can withdraw a recorded approval after a later informational intent', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            version: 2,
            approved: true,
            approvals: [
              { id: 'a1', kind: 'approve', revision_id: 'r2' },
              { id: 'a2', kind: 'request_review', revision_id: 'r2' },
            ],
          }),
        ),
    ),
  )
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
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify({ version: 1, approved: true, approvals: [{ id: 'a1', kind: 'approve', revision_id: 'r2' }] }))),
  )
  client = new QueryClient()
  render(
    <QueryClientProvider client={client}>
      <ReviewIntent id="item" selected={{ revision: 'r2' }} commit="candidate" />
    </QueryClientProvider>,
  )
  expect(await screen.findByText('Approved revision.')).toBeTruthy()
})
