import { QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const calls: Array<{ method: string; request: unknown }> = []
const listed = { revision: 7, defaultProfile: 'pi', profiles: [{ name: 'pi', driver: 'pi', label: 'Pi', users: 2 }, { name: 'claude', driver: 'claude', label: '', users: 0 }] }
vi.mock('@/lib/rpc', async original => ({
  ...(await original<object>()),
  profiles: new Proxy(
    {},
    {
      get: (_, method: string) => async (request: unknown) => {
        calls.push({ method, request })
        return method === 'listAgentProfiles' ? listed : {}
      },
    },
  ),
}))
vi.mock('@/components/confirm', () => ({ useConfirm: () => async () => true }))

afterEach(cleanup)

describe('AgentSettings', () => {
  it('reads profiles from the API and makes each change fenced on what it shows', async () => {
    const { queryClient } = await import('@/lib/queries')
    const { AgentSettings } = await import('./agent-settings')
    render(
      <QueryClientProvider client={queryClient}>
        <AgentSettings />
      </QueryClientProvider>,
    )
    await screen.findByLabelText('Make claude the default')
    expect(screen.getByText('default')).toBeTruthy()
    fireEvent.click(screen.getByLabelText('Make claude the default'))
    await waitFor(() => expect(calls.find(call => call.method === 'setDefaultAgentProfile')?.request).toEqual({ name: 'claude', expectedRevision: 7 }))
    fireEvent.click(screen.getByLabelText('Remove claude'))
    await waitFor(() => expect(calls.find(call => call.method === 'deleteAgentProfile')?.request).toEqual({ name: 'claude', expectedRevision: 7 }))
  })
})
