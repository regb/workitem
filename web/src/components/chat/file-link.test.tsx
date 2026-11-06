import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ItemContext } from './item-context'
import { Markdown } from './markdown'
import { AttachmentLinksContext } from './attachment-context'
import { reviewUI } from '@/lib/review'

const chat = { item: { checkout: { claim: { id: 'claim', generation: 1, path: '/repo' } } } }
vi.mock('@/lib/queries', () => ({ useChat: () => ({ data: chat }) }))
let client: QueryClient
let fetcher: ReturnType<typeof vi.fn>
function message(text: string, attachments: string[] = [], external: Record<string, string> = {}) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><ItemContext value="item"><AttachmentLinksContext value={{ names: attachments, open: vi.fn(), external }}><Markdown text={text} /></AttachmentLinksContext></ItemContext></QueryClientProvider>)
}
beforeEach(() => {
  reviewUI.set(state => ({ ...state, open: new Set(), panels: new Map(), drafts: new Map() }))
  fetcher = vi.fn(async () => new Response(JSON.stringify({ paths: ['src/auth.ts', 'src/only.ts'], claim_id: 'claim', claim_generation: 1 })))
  vi.stubGlobal('fetch', fetcher)
})
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals() })

describe('message file chips', () => {
  it('links PDFs to a claim-fenced browser tab without probing files', () => {
    message('`reports/final.pdf`')
    const link = screen.getByRole('link')
    const url = new URL(link.getAttribute('href')!, 'http://localhost')
    expect(url.pathname).toBe('/api/items/item/document')
    expect(url.searchParams.get('path')).toBe('reports/final.pdf')
    expect(url.searchParams.get('claim_id')).toBe('claim')
    expect(url.searchParams.get('claim_generation')).toBe('1')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toContain('noopener')
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('links attached PDFs directly instead of treating their outside path as a workspace file', () => {
    message('`~/Downloads/report.pdf`', ['report.pdf'], { 'report.pdf': '/api/items/item/attachments/digest' })
    const link = screen.getByRole('link')
    expect(link.getAttribute('href')).toBe('/api/items/item/attachments/digest')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.hasAttribute('download')).toBe(false)
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('recognises only Markdown links and eligible inline code, without probing files at render time', () => {
    const { container } = message('See `src/auth.ts:42`, [auth](src/auth.ts#L2C3), and src/plain.ts. `auth.ts`, `src/`, `npm install`, `example.com/path`\n\n```ts\nsrc/fenced.ts\n```')
    expect([...container.querySelectorAll('[data-file-link]')].map(e => e.getAttribute('data-file-link'))).toEqual(['src/auth.ts', 'src/auth.ts'])
    expect(fetcher).not.toHaveBeenCalled()
    expect(reviewUI.get().open.size).toBe(0)
  })

  it('opens the owning item file panel at a line and column only on click', async () => {
    message('[auth source](src/auth.ts#L42C3)')
    fireEvent.click(screen.getByRole('button', { name: 'Open src/auth.ts:42:3' }))
    await waitFor(() => expect(reviewUI.get().open.has('item')).toBe(true))
    expect(reviewUI.get().panels.get('item')).toMatchObject({ tab: 'files', path: 'src/auth.ts', fileTarget: { path: 'src/auth.ts', line: 42, column: 3, directory: false } })
  })

  it('resolves an abbreviated filename and asks instead of guessing when ambiguous', async () => {
    fetcher.mockImplementation(async () => new Response(JSON.stringify({ paths: ['src/auth.ts', 'tests/auth.ts'], claim_id: 'claim', claim_generation: 1 })))
    message('`auth.ts:42`')
    fireEvent.click(screen.getByRole('button', { name: 'Open auth.ts:42' }))
    await screen.findByText('Choose a file')
    expect(reviewUI.get().open.size).toBe(0)
    fireEvent.click(screen.getByRole('button', { name: 'tests/auth.ts' }))
    expect(reviewUI.get().panels.get('item')).toMatchObject({ path: 'tests/auth.ts', fileTarget: { line: 42 } })
  })

  it('opens directory links in the file tree', async () => {
    message('[source folder](src/)')
    fireEvent.click(screen.getByRole('button', { name: 'Open src/' }))
    await waitFor(() => expect(reviewUI.get().panels.get('item')?.fileTarget?.directory).toBe(true))
    expect(reviewUI.get().panels.get('item')).toMatchObject({ tab: 'files', path: '', fileTarget: { path: 'src' } })
  })

  it('does not produce nested buttons for inline code already inside a Markdown link', () => {
    const { container } = message('[`src/auth.ts`](src/auth.ts#L2)')
    expect(container.querySelectorAll('[data-file-link]')).toHaveLength(1)
    expect(container.querySelector('button button')).toBeNull()
  })

  it('preserves external links, unsafe-URL filtering, and attachment precedence', () => {
    const { container } = message('[web](https://example.com/a.ts) [bad](javascript:alert(1)) [local](file:///repo/src/auth.ts#L2) `out/REPORT.md`', ['REPORT.md'])
    expect(container.querySelector('a')?.getAttribute('href')).toBe('https://example.com/a.ts')
    expect(container.innerHTML).not.toContain('href="javascript:')
    expect(container.innerHTML).not.toContain('href="file:')
    expect(container.querySelectorAll('[data-file-link]')).toHaveLength(1)
    expect(container.querySelectorAll('[data-attachment-link]')).toHaveLength(1)
  })

  it('refuses files outside the workspace instead of navigating the browser to them', async () => {
    const { container } = message('[secret](file:///etc/passwd)')
    fireEvent.click(screen.getByRole('button', { name: 'Open /etc/passwd' }))
    await screen.findByText('This reference is outside the item workspace.')
    expect(reviewUI.get().open.size).toBe(0)
    expect(container.querySelector('a')).toBeNull()
  })

  it('refuses a lookup whose checkout identity no longer matches the path context', async () => {
    fetcher.mockImplementation(async () => new Response(JSON.stringify({ paths: ['src/auth.ts'], claim_id: 'replacement', claim_generation: 2 })))
    message('`src/auth.ts:42`')
    fireEvent.click(screen.getByRole('button', { name: 'Open src/auth.ts:42' }))
    await screen.findByText('The workspace changed. Click the reference again after it refreshes.')
    expect(reviewUI.get().open.size).toBe(0)
  })

  it('offers workspace-open and copy actions through the chip context menu', async () => {
    message('`src/auth.ts:42`')
    fireEvent.contextMenu(screen.getByRole('button', { name: 'Open src/auth.ts:42' }))
    expect(await screen.findByRole('button', { name: 'Open in file panel' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Copy reference' })).toBeTruthy()
  })
})
