import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const calls: Array<{ path: string; body: unknown }> = []
vi.mock('@/lib/api', async original => ({
  ...(await original<object>()),
  api: async (path: string, body: unknown) => {
    calls.push({ path, body })
    return { id: 'new-item' }
  },
}))
vi.mock('@/lib/queries', async original => ({
  ...(await original<object>()),
  useProjects: () => ({ data: [{ id: 'repo', name: 'repo' }], isPending: false }),
  useAgents: () => ({ data: [{ name: 'claude', label: 'Claude' }], isPending: false }),
  useWorkList: () => ({ data: undefined }),
  useCreations: () => ({ data: [] }),
}))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => () => {}, Link: () => null }))

beforeEach(() => (calls.length = 0))
afterEach(cleanup)

describe('BacklogAdd', () => {
  it('saves a title, and a description only when added', async () => {
    const { BacklogAdd } = await import('./backlog-add')
    render(<BacklogAdd />)
    expect(screen.queryByLabelText('Backlog description')).toBeNull()
    fireEvent.change(screen.getByLabelText('Backlog title'), { target: { value: '  Fix the flaky test ' } })
    fireEvent.click(screen.getByRole('button', { name: /Description/ }))
    fireEvent.change(screen.getByLabelText('Backlog description'), { target: { value: ' Seen in CI. ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(calls).toEqual([{ path: '/api/backlog', body: { project_id: 'repo', title: 'Fix the flaky test', description: 'Seen in CI.' } }]))
    await waitFor(() => expect((screen.getByLabelText('Backlog title') as HTMLInputElement).value).toBe(''))
  })
})

describe('QuickCreate', () => {
  it('starts the task on Enter and clears for the next one', async () => {
    const { QuickCreate } = await import('./quick-create')
    render(<QueryClientProvider client={new QueryClient()}><QuickCreate /></QueryClientProvider>)
    const box = screen.getByLabelText('New task') as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: 'Upgrade the linter' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(calls).toEqual([{ path: '/api/items', body: { project_id: 'repo', agent: 'claude', prompt: 'Upgrade the linter' } }]))
    await waitFor(() => expect(box.value).toBe(''))
  })
})
