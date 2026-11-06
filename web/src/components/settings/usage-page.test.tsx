import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'

type Usage = { enabled: boolean; quotas: unknown[]; checkedAt?: ReturnType<typeof timestampFromDate>; refreshing?: boolean; error?: string }
const calls: unknown[] = []
let current: Usage
vi.mock('@/lib/rpc', async original => ({
  ...(await original<object>()),
  system: {
    getUsage: async (request: { refresh?: boolean }) => {
      calls.push(request)
      return { ...current, refreshing: !!request.refresh }
    },
  },
}))
vi.mock('@/lib/queries', async original => ({
  ...(await original<object>()),
  useUsage: () => ({ data: current }),
}))

beforeEach(() => {
  calls.length = 0
  current = { enabled: true, quotas: [], checkedAt: timestampFromDate(new Date(Date.now() - 3 * 60_000)) }
})
afterEach(cleanup)

describe('UsageSettings', () => {
  it('shows the last check at once and checks again behind it', async () => {
    const { UsageSettings } = await import('./usage-settings')
    render(<UsageSettings />)
    expect(screen.getByText('Last checked 3m ago.')).toBeTruthy()
    await waitFor(() => expect(calls).toEqual([{ refresh: true }]))
  })

  it('says a check is running and reports a failed check with the earlier results', async () => {
    current = { ...current, refreshing: true, error: 'the usage plugin failed: provider unreachable' }
    const { UsageSettings } = await import('./usage-settings')
    render(<UsageSettings />)
    expect(screen.getByText('Checking now…')).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toContain('provider unreachable')
    expect((screen.getByRole('button', { name: /Refresh/ }) as HTMLButtonElement).disabled).toBe(true)
  })
})
