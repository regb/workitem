import type { ReactNode } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { Chat } from '@/lib/api'
import { defaultPanel, maxChatWidth, minChatWidth, reviewUI, setFileDraft, setPanel } from '@/lib/review'
import { ReviewPanel } from './review-panel'

vi.mock('@/lib/queries', () => ({ refreshChat: vi.fn() }))
vi.mock('./code-view', () => ({
  CodeProvider: ({ children }: { children: ReactNode }) => children,
  SourceView: ({ file }: { file: { content?: string } }) => <pre>{file.content}</pre>,
  DiffView: () => null,
  unavailable: () => '',
}))
vi.mock('@pierre/trees/react', () => ({
  FileTree: () => null,
  useFileTree: () => ({ model: { resetPaths: () => {}, getItem: () => null } }),
}))
const chat = {
  item: { id: 'item', state: 'active', title: 'Task', checkout: { specification: { branch: 'wi/task' }, claim: {} } },
  runtime: { online: false },
  status: 'idle',
} as Chat
let client: QueryClient
beforeEach(() => {
  reviewUI.set(state => ({ ...state, drafts: new Map(), panels: new Map() }))
  setPanel('item', { tab: 'files', path: 'a.ts' })
  setFileDraft('item', 'a.ts', {
    session_id: 'old-session',
    file: { path: 'a.ts', content: 'original', exists: true, sha256: 'original' },
    content: 'recover this draft',
    claim_id: 'old',
    claim_generation: 1,
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify({ error: 'checkout unavailable' }), { status: 409 })),
  )
})
afterEach(() => {
  cleanup()
  client?.clear()
  vi.unstubAllGlobals()
})

it('keeps drafts accessible even when file-list inspection fails', async () => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <ReviewPanel chat={chat} />
    </QueryClientProvider>,
  )
  expect(await screen.findByText('recover this draft')).toBeTruthy()
  expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
})

it('resizes the chat sidebar in pixels with bounded keyboard controls', () => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <ReviewPanel chat={chat} />
    </QueryClientProvider>,
  )
  const divider = screen.getByRole('separator', { name: 'Resize chat sidebar' })
  expect(divider.getAttribute('aria-valuenow')).toBe(String(defaultPanel.chatWidth))
  fireEvent.keyDown(divider, { key: 'End' })
  expect(reviewUI.get().panels.get('item')?.chatWidth).toBe(maxChatWidth)
  fireEvent.keyDown(divider, { key: 'ArrowRight' })
  expect(reviewUI.get().panels.get('item')?.chatWidth).toBe(maxChatWidth)
  fireEvent.keyDown(divider, { key: 'Home' })
  fireEvent.keyDown(divider, { key: 'ArrowLeft' })
  expect(reviewUI.get().panels.get('item')?.chatWidth).toBe(minChatWidth)
  fireEvent.keyDown(divider, { key: 'ArrowRight' })
  expect(reviewUI.get().panels.get('item')?.chatWidth).toBe(minChatWidth + 16)
})

it('offers an explicit return to chat without discarding file drafts', () => {
  reviewUI.set(state => ({ ...state, open: new Set(['item']) }))
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <ReviewPanel chat={chat} />
    </QueryClientProvider>,
  )
  fireEvent.click(screen.getByRole('button', { name: 'Back to chat' }))
  expect(reviewUI.get().open.has('item')).toBe(false)
  expect(reviewUI.get().drafts.size).toBe(1)
})

it('keeps drafts accessible after the checkout is released', async () => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <ReviewPanel chat={{ ...chat, item: { ...chat.item, checkout: { ...chat.item.checkout, claim: null } } }} />
    </QueryClientProvider>,
  )
  expect(await screen.findByText('recover this draft')).toBeTruthy()
})

it('shows the change as a compact header, files and diff, with no commit form', async () => {
  reviewUI.set(state => ({ ...state, drafts: new Map(), panels: new Map(), mergeMessages: new Map() }))
  const start = 'a'.repeat(40),
    head = 'b'.repeat(40)
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async (url: string) =>
        new Response(
          JSON.stringify(
            url.includes('/commits')
              ? { branches: [{ kind: 'item', name: 'wi/task', sha: head }], commits: [] }
              : url.includes('/review-file')
                ? { old_file: { exists: true, content: 'old' }, new_file: { exists: true, content: 'new' } }
                : url.includes('review-revisions')
                  ? { revisions: [] }
                  : {
                      revision: { version: 'v', base_commit: start, head_commit: head, working_tree: true },
                      files: [{ path: 'a.ts', change: 'modified' }],
                      branch: 'wi/task',
                      integration_branch: 'main',
                    },
          ),
        ),
    ),
  )
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <ReviewPanel chat={{ ...chat, item: { ...chat.item, repository: { created_from_commit: start } } }} />
    </QueryClientProvider>,
  )
  expect(screen.getByRole('button', { name: 'Change' })).toBeTruthy()
  const header = screen.getByRole('region', { name: 'Change header' })
  const files = await screen.findByRole('region', { name: 'Changed files' })
  const diff = await screen.findByRole('region', { name: 'File diff' })
  expect(header.compareDocumentPosition(files) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(files.compareDocumentPosition(diff) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(screen.getByRole('combobox', { name: 'Revision' })).toBeTruthy()
  expect(screen.getByRole('combobox', { name: 'Compared to' })).toBeTruthy()
  // The message is asked for only when merging.
  expect(screen.queryByRole('textbox')).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: /Merge…/ }))
  expect((screen.getByRole('textbox', { name: 'Merge message' }) as HTMLTextAreaElement).value).toBe('Task')
})
