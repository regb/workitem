import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { Chat } from '@/lib/api'
import { reviewUI, setPanel } from '@/lib/review'
import { ChangeHeader } from './change-header'

const chat = {
  item: { id: 'item', title: 'Task', state: 'active', checkout: { specification: { branch: 'wi/task' }, claim: { id: 'claim', generation: 7 } } },
  runtime: { online: false },
  status: 'idle',
} as Chat
const revision = (id: string, patch: Record<string, unknown> = {}) => ({
  id,
  commit: 'a'.repeat(40),
  trigger: 'manual',
  observed_branch: 'wi/task',
  observed_head: 'b'.repeat(40),
  excluded: [],
  ...patch,
})
let records: Record<string, unknown> = { version: 1, revisions: [revision('r1'), revision('r2')] }
let client: QueryClient
function mount(value = chat) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ChangeHeader chat={value} active onMerged={() => {}} />
    </QueryClientProvider>,
  )
}
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
beforeEach(() => {
  records = { version: 1, revisions: [revision('r1'), revision('r2')] }
  reviewUI.set(state => ({ ...state, panels: new Map(), drafts: new Map(), mergeMessages: new Map() }))
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => reply(records)),
  )
})
afterEach(() => {
  cleanup()
  client?.clear()
  vi.unstubAllGlobals()
})
const captureButton = () => screen.getByRole('button', { name: /Capture checkpoint|Retry checkpoint/ }) as HTMLButtonElement

it('shows one line with the branch, both pickers, capture and merge, and no status when there is nothing to say', async () => {
  mount()
  await waitFor(() => expect(captureButton().disabled).toBe(false))
  expect(screen.getByText('wi/task')).toBeTruthy()
  expect(screen.getByRole('combobox', { name: 'Revision' })).toBeTruthy()
  expect(screen.getByRole('combobox', { name: 'Compared to' })).toBeTruthy()
  expect(screen.getByRole('button', { name: /Merge…/ })).toBeTruthy()
  expect(screen.queryByRole('status')).toBeNull()
  expect(screen.queryByRole('textbox')).toBeNull()
})

it('describes a selected snapshot, hides merge and lists left-out files', async () => {
  records = {
    revisions: [revision('r1', { excluded: [{ path: '.env', reason: 'sensitive path' }] })],
    merges: [{ revision_id: 'r1', commit: 'c'.repeat(40), tree: '', target_ref: 'refs/heads/main', before: '', after: '' }],
  }
  setPanel('item', { checkpoint: { revision: 'r1' } })
  mount()
  await screen.findByText(/r1 is a saved snapshot \(aaaaaaaaaaaa, from wi\/task\)/)
  expect(screen.getByText('Merged into main as cccccccccccc.')).toBeTruthy()
  expect(screen.getByText('Partial snapshot: 1 files left out')).toBeTruthy()
  expect(screen.getByText('.env: sensitive path')).toBeTruthy()
  expect(screen.queryByRole('button', { name: /Merge…/ })).toBeNull()
})

it('labels a detached snapshot by its head', async () => {
  records = { revisions: [revision('r1', { observed_branch: '', detached_head: true })] }
  setPanel('item', { checkpoint: { revision: 'r1' } })
  mount()
  await screen.findByText(/from detached HEAD at bbbbbbbbbbbb/)
})

it('keeps capture unavailable after the item closes and its checkout is released', async () => {
  mount({ ...chat, item: { ...chat.item, state: 'closed', checkout: { ...chat.item.checkout, claim: null } } } as Chat)
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(captureButton().disabled).toBe(true)
})

it('retries an uncertain capture with the original identity and checkout fence', async () => {
  const sent: Record<string, unknown>[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method !== 'POST') return reply(records)
      sent.push(JSON.parse(String(init.body)))
      if (sent.length === 1) throw new Error('connection lost')
      return reply({ result: { revision: revision('r3'), reused: true } })
    }),
  )
  mount()
  await waitFor(() => expect(captureButton().disabled).toBe(false))
  fireEvent.click(captureButton())
  await screen.findByText('connection lost')
  fireEvent.click(screen.getByRole('button', { name: 'Retry checkpoint' }))
  await waitFor(() => expect(reviewUI.get().panels.get('item')?.checkpoint).toEqual({ revision: 'r3' }))
  expect(sent).toHaveLength(2)
  expect(sent[1]).toEqual(sent[0])
  expect(sent[0]).toMatchObject({ claim_id: 'claim', claim_generation: 7 })
})

it('allocates a new capture identity only after starting another capture', async () => {
  const sent: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method !== 'POST') return reply(records)
      sent.push(JSON.parse(String(init.body)).capture_id)
      return reply({ error: 'capture was not completed' }, 409)
    }),
  )
  mount()
  await waitFor(() => expect(captureButton().disabled).toBe(false))
  fireEvent.click(captureButton())
  await screen.findByRole('alert')
  fireEvent.click(screen.getByRole('button', { name: 'Start another capture' }))
  fireEvent.click(captureButton())
  await screen.findByRole('alert')
  expect(sent).toHaveLength(2)
  expect(sent[0]).not.toBe(sent[1])
})
