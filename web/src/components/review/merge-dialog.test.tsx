import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { Chat } from '@/lib/api'
import { reviewUI } from '@/lib/review'
import { MergeButton, mergeBlocker } from './merge-dialog'

const chat = { item: { id: 'item', title: 'Task', state: 'active', checkout: { claim: { id: 'claim', generation: 1 } } }, runtime: { online: false }, status: 'idle' } as Chat
let client: QueryClient
let merged: string[] = []
beforeEach(() => {
  merged = []
  reviewUI.set(state => ({ ...state, drafts: new Map(), mergeMessages: new Map() }))
})
afterEach(() => {
  cleanup()
  client?.clear()
  vi.unstubAllGlobals()
})
function mount(value = chat) {
  client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MergeButton chat={value} integrationBranch="main" onMerged={notice => merged.push(notice)} />
    </QueryClientProvider>,
  )
}
const open = () => fireEvent.click(screen.getByRole('button', { name: /Merge…/ }))

it('asks for the message only in the dialog, prefilled with the item title', async () => {
  const fetcher = vi.fn(async (_url: string, _options: RequestInit) => new Response(JSON.stringify({})))
  vi.stubGlobal('fetch', fetcher)
  mount()
  expect(screen.queryByRole('textbox')).toBeNull()
  open()
  const message = screen.getByRole('textbox', { name: 'Merge message' }) as HTMLTextAreaElement
  expect(message.value).toBe('Task')
  fireEvent.change(message, { target: { value: 'Reviewed result' } })
  fireEvent.click(screen.getByRole('button', { name: 'Merge' }))
  await waitFor(() => expect(merged).toEqual(['Merged into main.']))
  expect(JSON.parse(fetcher.mock.calls[0][1].body as string)).toEqual({ message: 'Reviewed result', rebase: false })
  expect(screen.queryByRole('dialog')).toBeNull()
})

it('keeps the individual commits without asking for a message', async () => {
  const fetcher = vi.fn(async (_url: string, _options: RequestInit) => new Response(JSON.stringify({})))
  vi.stubGlobal('fetch', fetcher)
  mount()
  open()
  fireEvent.click(screen.getByRole('switch', { name: 'Keep the individual commits' }))
  expect(screen.queryByRole('textbox', { name: 'Merge message' })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Merge' }))
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))
  expect(JSON.parse(fetcher.mock.calls[0][1].body as string)).toEqual({ message: '', rebase: true })
})

it('lets an idle running agent merge and refuses while it works or a capture runs', () => {
  expect(mergeBlocker({ ...chat, runtime: { ...chat.runtime, online: true } } as Chat, false)).toBe('')
  expect(mergeBlocker({ ...chat, status: 'busy' } as Chat, false)).toContain('running a turn')
  expect(mergeBlocker({ ...chat, capturing: true } as Chat, false)).toContain('snapshot')
  expect(mergeBlocker(chat, true)).toContain('Save or discard')
  expect(mergeBlocker({ ...chat, item: { ...chat.item, state: 'closed' } } as Chat, false)).toContain('active')
  mount({ ...chat, capturing: true } as Chat)
  expect((screen.getByRole('button', { name: /Merge…/ }) as HTMLButtonElement).disabled).toBe(true)
})

it('reports a lost connection as an unconfirmed result, keeping the dialog open', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      throw new TypeError('connection lost')
    }),
  )
  mount()
  open()
  fireEvent.click(screen.getByRole('button', { name: 'Merge' }))
  await screen.findByText('The merge result could not be confirmed. Check the branch before retrying.')
  expect(screen.getByRole('dialog')).toBeTruthy()
})

it('keeps an edited message when the panel remounts', () => {
  const first = mount()
  open()
  fireEvent.change(screen.getByRole('textbox', { name: 'Merge message' }), { target: { value: 'Keep this message' } })
  first.unmount()
  mount()
  open()
  expect((screen.getByRole('textbox', { name: 'Merge message' }) as HTMLTextAreaElement).value).toBe('Keep this message')
})
