import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { AgentSettings, Chat } from '@/lib/api'
import { Composer, startsWithImages } from './composer'

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => navigate }))
const sendMessage = vi.fn()
vi.mock('@/lib/actions', async original => ({ ...(await original<object>()), sendMessage: (...args: unknown[]) => sendMessage(...args), nextItem: async () => 'other' }))
vi.mock('@/lib/queries', async original => ({ ...(await original<object>()), refreshChat: async () => {}, useAgentSettings: () => ({ data: undefined }) }))

afterEach(() => {
  cleanup()
  localStorage.clear()
  navigate.mockReset()
  sendMessage.mockReset()
})

const chat = (status: string): Chat => ({
  description: '',
  project: { id: 'p', name: 'p' },
  item: { id: 'item', slug: 'item', title: 'Item', state: 'active', labels: [], created_at: '', updated_at: '', repository: {}, checkout: { specification: {} } },
  runtime: { online: true, runtime: { id: 'r', mode: 'headless' }, capabilities: { prompt: true, steering: true, follow_up: true } },
  status,
  history: { leaf_id: '', messages: [] },
  control: { you: true, status: { holder: 'web', channels: { control: 'structured' }, busy: status === 'busy' }, plan: { needs_confirmation: false, reasons: [] } },
  unread: false,
  warnings: [],
})

const open = (status: string) => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider>
        <Composer chat={chat(status)} />
      </TooltipProvider>
    </QueryClientProvider>,
  )
  const box = screen.getByRole('textbox', { name: 'Message' })
  fireEvent.change(box, { target: { value: 'hello' } })
  return box
}

const sent = () => sendMessage.mock.calls.map(call => (call[3] as { delivery: string }).delivery)

describe('Composer', () => {
  it('sends to an idle agent, and sends and moves on with ⌘⌥Enter', async () => {
    sendMessage.mockResolvedValue({})
    const box = open('idle')
    expect(screen.getByRole('button', { name: 'Send' })).toBeTruthy()
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true, altKey: true })
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/items/$id', params: { id: 'other' } }))
    expect(sent()).toEqual(['prompt'])
  })

  it('steers a working agent, and queues with ⌘Enter', async () => {
    sendMessage.mockResolvedValue({})
    const box = open('busy')
    expect(screen.getByRole('button', { name: 'Steer now' })).toBeTruthy()
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(sent()).toEqual(['follow_up']))
    fireEvent.change(box, { target: { value: 'again' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(sent()).toEqual(['follow_up', 'steer']))
    expect(navigate).not.toHaveBeenCalled()
  })

  it('stays on the item when sending before moving on fails', async () => {
    sendMessage.mockRejectedValue(new Error('refused'))
    const box = open('idle')
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true, altKey: true })
    await waitFor(() => expect(sendMessage).toHaveBeenCalled())
    await new Promise(resolve => setTimeout(resolve, 300))
    expect(navigate).not.toHaveBeenCalled()
  })

  it('shows the action a click would take while modifiers are held', () => {
    open('busy')
    fireEvent.keyDown(window, { key: 'Meta', metaKey: true })
    expect(screen.getByRole('button', { name: 'Queue next' })).toBeTruthy()
    fireEvent.keyDown(window, { key: 'Alt', metaKey: true, altKey: true })
    expect(screen.getByRole('button', { name: 'Send and go to next' })).toBeTruthy()
    fireEvent.keyUp(window, { key: 'Meta' })
    expect(screen.getByRole('button', { name: 'Steer now' })).toBeTruthy()
  })

  it('clicks in the mode chosen from the caret, which Enter ignores and the browser keeps', async () => {
    sendMessage.mockResolvedValue({})
    const box = open('busy')
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Choose what the send button does' }), { button: 0, pointerType: 'mouse' })
    fireEvent.click(await screen.findByRole('menuitemradio', { name: /Queue next/ }))
    expect(localStorage.getItem('wi.send-mode')).toBe('queue')
    fireEvent.click(screen.getByRole('button', { name: 'Queue next' }))
    await waitFor(() => expect(sent()).toEqual(['follow_up']))
    fireEvent.change(box, { target: { value: 'again' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(sent()).toEqual(['follow_up', 'steer']))
  })

  it('sends instead of queueing to an idle agent', () => {
    localStorage.setItem('wi.send-mode', 'queue')
    open('idle')
    expect(screen.getByRole('button', { name: 'Send' })).toBeTruthy()
  })

  it('moves on after a click in send-and-go-to-next mode', async () => {
    localStorage.setItem('wi.send-mode', 'next')
    sendMessage.mockResolvedValue({})
    open('idle')
    fireEvent.click(screen.getByRole('button', { name: 'Send and go to next' }))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/items/$id', params: { id: 'other' } }))
  })
})

describe('images without a running agent', () => {
  it('offers attachments on a backlog item, as on an active one', () => {
    const backlog: Chat = { ...chat('idle'), item: { ...chat('idle').item, state: 'backlog' }, runtime: { online: false, capabilities: {} } }
    render(
      <QueryClientProvider client={new QueryClient()}>
        <TooltipProvider>
          <Composer chat={backlog} />
        </TooltipProvider>
      </QueryClientProvider>,
    )
    expect(screen.getByRole('button', { name: 'Attach images' })).toBeTruthy()
  })
  const settings = (model: string): AgentSettings => ({
    agent: 'pi',
    values: { model },
    overrides: {},
    runtime_online: false,
    live: [],
    catalog: { models: [{ value: 'seeing', reasoning: false, image_input: true }, { value: 'blind', reasoning: false }] },
  })
  it('follows the model the agent will start with', () => {
    expect(startsWithImages(settings('seeing'))).toBe(true)
    expect(startsWithImages(settings('blind'))).toBe(false)
    expect(startsWithImages(settings('unknown'))).toBe(true)
    expect(startsWithImages(undefined)).toBe(true)
  })
})
