import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RoundNotify } from './round-notify'

const rpc = vi.hoisted(() => ({ notifyRound: vi.fn() }))
vi.mock('@/lib/review-rpc', () => rpc)
let client: QueryClient
function mount() {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <RoundNotify id="item" round={{ id: 'q1', revision_id: 'r2' }} />
    </QueryClientProvider>,
  )
}
beforeEach(() => {
  rpc.notifyRound.mockReset()
})
afterEach(() => {
  cleanup()
  client?.clear()
})
it('sends only on explicit action and uses a new identity for an explicit resend', async () => {
  const sent: Record<string, string>[] = []
  rpc.notifyRound.mockImplementation(async (item: string, input: Record<string, string>) => {
    expect(item).toBe('item')
    sent.push(structuredClone(input))
    return { result: { id: 'q1', revision_id: 'r2', command_id: 'command' } }
  })
  mount()
  expect(rpc.notifyRound).not.toHaveBeenCalled()
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
  rpc.notifyRound.mockImplementation(async (_item: string, input: object) => {
    sent.push(structuredClone(input))
    if (sent.length === 1) throw new Error('connection lost')
    return { result: { command_id: 'confirmed' } }
  })
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
  rpc.notifyRound.mockRejectedValue(new Error('No agent is running'))
  mount()
  fireEvent.click(screen.getByRole('button', { name: 'Notify agent for q1' }))
  expect((await screen.findByRole('alert')).textContent).toContain('q1 remains published. No agent is running')
  expect(screen.getByRole('article', { name: 'Published round q1' })).toBeTruthy()
  expect(rpc.notifyRound).toHaveBeenCalledTimes(1)
})
