import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RoundNotify } from './round-notify'
let client: QueryClient
function mount() {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <RoundNotify id="item" round={{ id: 'q1', revision_id: 'r2' }} />
    </QueryClientProvider>,
  )
}
afterEach(() => {
  cleanup()
  client?.clear()
  vi.unstubAllGlobals()
})
it('sends only on explicit action and uses a new identity for an explicit resend', async () => {
  const sent: Record<string, string>[] = []
  const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
    expect(url).toContain('/review-notify')
    sent.push(JSON.parse(String(init?.body)))
    return new Response(JSON.stringify({ result: { id: 'q1', revision_id: 'r2', command_id: 'command' } }))
  })
  vi.stubGlobal('fetch', fetcher)
  mount()
  expect(fetcher).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Notify agent for q1' }))
  await screen.findByText('Notification sent for q1.')
  expect(sent[0]).toMatchObject({ round_id: 'q1' })
  expect(Object.keys(sent[0]).sort()).toEqual(['client_message_id', 'round_id'])
  fireEvent.click(screen.getByRole('button', { name: 'Notify agent again for q1' }))
  await waitFor(() => expect(sent).toHaveLength(2))
  expect(sent[1].client_message_id).not.toBe(sent[0].client_message_id)
})
it('keeps uncertain delivery identity and never retries automatically', async () => {
  const sent: object[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => {
      sent.push(JSON.parse(String(init?.body)))
      if (sent.length === 1) throw new Error('connection lost')
      return new Response(JSON.stringify({ result: { command_id: 'confirmed' } }))
    }),
  )
  mount()
  fireEvent.click(screen.getByRole('button', { name: 'Notify agent for q1' }))
  await screen.findByRole('alert')
  expect(sent).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: 'Retry same notification for q1' }))
  await screen.findByText('Notification sent for q1.')
  expect(sent).toHaveLength(2)
  expect(sent[1]).toEqual(sent[0])
})
it('retains published-round identity when no agent can receive a notification', async () => {
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ error: 'No agent is running' })))
  vi.stubGlobal('fetch', fetcher)
  mount()
  fireEvent.click(screen.getByRole('button', { name: 'Notify agent for q1' }))
  expect((await screen.findByRole('alert')).textContent).toContain('q1 remains published. No agent is running')
  expect(screen.getByRole('article', { name: 'Published round q1' })).toBeTruthy()
  expect(fetcher).toHaveBeenCalledTimes(1)
})
