import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { AgentMode } from '@/gen/wi/core/v1/common_pb'
import { calls, resetRPC, rpcReplies } from '@/lib/rpc-mock'

vi.mock('@/lib/rpc', async original => (await import('@/lib/rpc-mock')).mockRPC(await original()))
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
}))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => () => {}, Link: () => null }))

beforeEach(() => {
  resetRPC()
  rpcReplies.set('lifecycle.createAndStartItem', () => ({ item: { id: 'new-item', title: '' } }))
  rpcReplies.set('items.createItem', () => ({ item: { id: 'new-item' }, warnings: [] }))
})
afterEach(cleanup)

// started is the request starting new work from a task: the task is the
// description and the first prompt, and the item's settings override the
// profile only where chosen.
const started = (prompt: string, agentSettings: Record<string, string>) => ({
  item: { repositoryId: 'repo', description: prompt, generateTitle: true, agentProfile: 'claude', agentSettings, requestId: expect.stringMatching(/^web-/) },
  start: { prompt, mode: AgentMode.HEADLESS, clientMessageId: expect.stringMatching(/^web-.*-prompt$/) },
})

describe('BacklogAdd', () => {
  it('saves a title, and a description only when added', async () => {
    const { BacklogAdd } = await import('./backlog-add')
    render(<BacklogAdd />)
    expect(screen.queryByLabelText('Backlog description')).toBeNull()
    fireEvent.change(screen.getByLabelText('Backlog title'), { target: { value: '  Fix the flaky test ' } })
    fireEvent.click(screen.getByRole('button', { name: /Description/ }))
    fireEvent.change(screen.getByLabelText('Backlog description'), { target: { value: ' Seen in CI. ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(calls('items.createItem')).toEqual([{ repositoryId: 'repo', title: 'Fix the flaky test', description: 'Seen in CI.', requestId: expect.stringMatching(/^web-/) }]))
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
    await waitFor(() => expect(calls('lifecycle.createAndStartItem')).toEqual([started('Upgrade the linter', {})]))
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
    await waitFor(() => expect(calls('lifecycle.createAndStartItem')).toEqual([started('Review the parser', { model: 'sonnet', reasoning: 'high' })]))
    drafts.choices.clear()
  })
})
