import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Usage } from '@/lib/api'

const calls: string[] = []
let current: Usage
vi.mock('@/lib/api', async original => ({
  ...(await original<object>()),
  api: async (path: string) => {
    calls.push(path)
    return { ...current, refreshing: path.includes('refresh=1') }
  },
}))
vi.mock('@/lib/queries', async original => ({
  ...(await original<object>()),
  useUsage: () => ({ data: current }),
}))

beforeEach(() => {
  calls.length = 0
  current = { enabled: true, results: [], checked_at: new Date(Date.now() - 3 * 60_000).toISOString() }
})
afterEach(cleanup)

describe('UsageSettings', () => {
  it('shows the last check at once and checks again behind it', async () => {
    const { UsageSettings } = await import('./usage-settings')
    render(<UsageSettings />)
    expect(screen.getByText('Last checked 3m ago.')).toBeTruthy()
    await waitFor(() => expect(calls).toEqual(['/api/usage?refresh=1']))
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
