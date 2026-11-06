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
  useAgents: () => ({ data: [{ name: 'claude', label: 'Claude', driver: 'claude' }], isPending: false }),
  useProfileModels: () => ({
    data: {
      agent: 'claude',
      driver: 'claude',
      values: { model: 'opus' },
      overrides: {},
      live: [],
      runtime_online: false,
      catalog: {
        models: [
          { value: 'opus', label: 'Opus', reasoning: true },
          { value: 'sonnet', label: 'Sonnet', reasoning: true },
        ],
        reasoning_levels: ['low', 'high'],
        default_reasoning: 'low',
      },
    },
  }),
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
    await waitFor(() => expect(calls).toEqual([{ path: '/api/backlog', body: { project_id: 'repo', title: 'Fix the flaky test', description: 'Seen in CI.', request_id: expect.any(String) } }]))
    await waitFor(() => expect((screen.getByLabelText('Backlog title') as HTMLInputElement).value).toBe(''))
  })
})

describe('QuickCreate', () => {
  it('starts the task on Enter and clears for the next one', async () => {
    const { QuickCreate } = await import('./quick-create')
    render(
      <QueryClientProvider client={new QueryClient()}>
        <QuickCreate />
      </QueryClientProvider>,
    )
    const box = screen.getByLabelText('New task') as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: 'Upgrade the linter' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(calls).toEqual([{ path: '/api/items', body: { project_id: 'repo', agent: 'claude', prompt: 'Upgrade the linter', request_id: expect.any(String) } }]))
    await waitFor(() => expect(box.value).toBe(''))
  })
})

describe('AgentPicker', () => {
  it('shows the profile, model and thinking level, and sends only what differs from the profile', async () => {
    const { drafts } = await import('@/lib/drafts')
    drafts.choices.clear()
    const { QuickCreate } = await import('./quick-create')
    render(
      <QueryClientProvider client={new QueryClient()}>
        <QuickCreate />
      </QueryClientProvider>,
    )
    const chip = screen.getByRole('button', { name: 'Agent: Claude · Opus · low' })
    expect(chip.querySelector('[data-driver="claude"]')).toBeTruthy()
    fireEvent.click(chip)
    fireEvent.click(screen.getByRole('option', { name: 'Sonnet' }))
    fireEvent.click(screen.getByRole('radio', { name: 'high' }))
    expect(screen.getByRole('button', { name: 'Agent: Claude · Sonnet · high' })).toBeTruthy()
    const box = screen.getByLabelText('New task') as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: 'Review the parser' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(calls).toEqual([{ path: '/api/items', body: { project_id: 'repo', agent: 'claude', prompt: 'Review the parser', model: 'sonnet', reasoning: 'high', request_id: expect.any(String) } }]))
    drafts.choices.clear()
  })
})
