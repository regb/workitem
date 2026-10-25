import { cleanup, render as renderPage, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { Chat } from '@/lib/api'

vi.mock('@tanstack/react-router', () => ({ useParams: () => ({ id: 'item' }), useNavigate: () => vi.fn() }))
vi.mock('@/components/chat/conversation', () => ({ Conversation: () => null }))
const chat = (holder: string | undefined, you = false): Chat => ({
  description: '',
  project: { id: 'p', name: 'p' },
  item: { id: 'item', slug: 'item', title: 'Item', state: 'active', labels: [], created_at: '', updated_at: '', repository: {}, checkout: { specification: {} } },
  runtime: { online: true, runtime: { id: 'r', mode: 'tui' }, capabilities: { prompt: true } },
  status: 'idle',
  history: { leaf_id: '', messages: [] },
  control: { you, status: { holder, channels: { control: 'emulated' }, busy: false }, plan: { needs_confirmation: true, reasons: ['the terminal becomes read-only'] } },
  unread: false,
  warnings: [],
})
let current: Chat
vi.mock('@/lib/queries', async original => ({ ...(await original<object>()), useChat: () => ({ data: current, isPending: false }), useAgentSettings: () => ({ data: undefined }) }))

afterEach(cleanup)

// The composer sends through react-query mutations.
const render = (page: ReactNode) =>
  renderPage(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider>{page}</TooltipProvider>
    </QueryClientProvider>,
  )

describe('ChatPage', () => {
  it('replaces the composer with a read-only bar while a terminal controls the agent', async () => {
    const { ChatPage } = await import('./chat-page')
    current = chat('terminal')
    render(<ChatPage />)
    expect(screen.getByText(/controlled in a terminal/i)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Take control' })).toBeTruthy()
    expect(screen.queryByRole('textbox', { name: 'Message' })).toBeNull()
  })

  it('shows the composer when this tab controls the agent or nobody does', async () => {
    const { ChatPage } = await import('./chat-page')
    for (const [holder, you] of [
      ['web', true],
      [undefined, false],
    ] as const) {
      current = chat(holder, you)
      const { unmount } = render(<ChatPage />)
      expect(screen.getByRole('textbox', { name: 'Message' })).toBeTruthy()
      expect(screen.queryByRole('button', { name: 'Take control' })).toBeNull()
      unmount()
    }
  })
})

describe('ChatPage when the browser cannot control the agent', () => {
  it('shows read-only without a takeover button', async () => {
    const { ChatPage } = await import('./chat-page')
    current = chat(undefined)
    current.control!.plan = { needs_confirmation: false, reasons: [], unavailable: 'no runtime mode of this agent accepts structured input; control it from a terminal' }
    render(<ChatPage />)
    expect(screen.getByText(/accepts structured input; control it from a terminal/i)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Take control' })).toBeNull()
    expect(screen.queryByRole('textbox', { name: 'Message' })).toBeNull()
  })
})
