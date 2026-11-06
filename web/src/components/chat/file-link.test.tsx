import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ItemContext } from './item-context'
import { Markdown } from './markdown'
import { AttachmentLinksContext } from './attachment-context'
import { reviewUI } from '@/lib/review'

const chat = { item: { checkout: { claim: { id: 'claim', generation: 1, path: '/repo' } } } }
vi.mock('@/lib/queries', () => ({ useChat: () => ({ data: chat }) }))
const rpc = vi.hoisted(() => ({ listWorkspaceFiles: vi.fn() }))
vi.mock('@/lib/review-rpc', () => rpc)
let client: QueryClient
let fetcher: ReturnType<typeof vi.fn>
function message(text: string, attachments: string[] = [], external: Record<string, string> = {}) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><ItemContext value="item"><AttachmentLinksContext value={{ names: attachments, open: vi.fn(), external }}><Markdown text={text} /></AttachmentLinksContext></ItemContext></QueryClientProvider>)
}
beforeEach(() => {
  reviewUI.set(state => ({ ...state, open: new Set(), panels: new Map(), drafts: new Map() }))
  rpc.listWorkspaceFiles.mockReset()
  rpc.listWorkspaceFiles.mockResolvedValue({ paths: ['src/auth.ts', 'src/only.ts', 'reports/final.pdf'], claim_id: 'claim', claim_generation: 1 })
  // Media links are real URLs; nothing should fetch them at render time.
  fetcher = vi.fn(async () => new Response('{}'))
  vi.stubGlobal('fetch', fetcher)
})
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals() })

describe('message file chips', () => {
  it('links PDFs to a claim-fenced browser tab without reading files', () => {
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

  it('links a web address written as inline code, keeping it code', () => {
    const { container } = message('API (`https://tt-api.dev.example.ch`), Argo `https://argo.example.ch/workflows?ns=a` but not `https://` or `see https://x.ch now`, `javascript:alert(1)`, `ftp://files.example.ch`')
    const links = [...container.querySelectorAll('a')]
    expect(links.map(link => link.getAttribute('href'))).toEqual(['https://tt-api.dev.example.ch', 'https://argo.example.ch/workflows?ns=a'])
    expect(links.every(link => link.querySelector('code') && link.getAttribute('target') === '_blank')).toBe(true)
    expect([...container.querySelectorAll('code')].map(code => code.textContent)).toContain('javascript:alert(1)')
  })

  it('links attached PDFs directly instead of treating their outside path as a workspace file', () => {
    message('`~/Downloads/report.pdf`', ['report.pdf'], { 'report.pdf': '/api/items/item/attachments/digest' })
    const link = screen.getByRole('link')
    expect(link.getAttribute('href')).toBe('/api/items/item/attachments/digest')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.hasAttribute('download')).toBe(false)
    expect(fetcher).not.toHaveBeenCalled()
    expect(rpc.listWorkspaceFiles).not.toHaveBeenCalled()
  })

  it('recognises only Markdown links and eligible inline code, without reading files at render time', () => {
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
    rpc.listWorkspaceFiles.mockResolvedValue({ paths: ['src/auth.ts', 'tests/auth.ts'], claim_id: 'claim', claim_generation: 1 })
    message('`auth.ts:42`')
    fireEvent.click(screen.getByRole('button', { name: 'Open auth.ts:42' }))
    await screen.findByText('Choose a file')
    expect(rpc.listWorkspaceFiles.mock.calls[0][0]).toBe('item')
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

  it('leaves paths outside the workspace as plain text instead of navigating the browser to them', () => {
    const { container } = message('[secret](file:///etc/passwd)')
    expect(screen.queryByRole('button')).toBeNull()
    expect(container.querySelector('a')).toBeNull()
    expect(container.textContent).toContain('secret')
    expect(reviewUI.get().open.size).toBe(0)
  })

  it('turns a path the checkout does not have into plain code once its file list says so', async () => {
    const { container } = message('`src/auth.ts:42`, `only.ts:3`, `src/missing.ts:1` and `docs/gone.md`')
    const links = () => [...container.querySelectorAll('[data-file-link]')].map(e => e.getAttribute('data-file-link'))
    // Until the list arrives, every eligible path is a link.
    expect(links()).toEqual(['src/auth.ts', 'only.ts', 'src/missing.ts', 'docs/gone.md'])
    await waitFor(() => expect(links()).toEqual(['src/auth.ts', 'only.ts']))
    // One listing serves every path in the conversation.
    expect(rpc.listWorkspaceFiles).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('docs/gone.md')
  })

  it('keeps links when the file list is truncated or from another claim, where absence proves nothing', async () => {
    rpc.listWorkspaceFiles.mockResolvedValue({ paths: ['src/auth.ts'], truncated: true, claim_id: 'claim', claim_generation: 1 })
    const { container } = message('`src/missing.ts:1`')
    await waitFor(() => expect(rpc.listWorkspaceFiles).toHaveBeenCalled())
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(container.querySelector('[data-file-link]')?.getAttribute('data-file-link')).toBe('src/missing.ts')
  })

  it('keeps rooted paths that are not in the checkout, such as API routes, as plain code', () => {
    const { container } = message('`/v1/cities`, `/health`, `~/notes/todo.md` and `/repo/src/auth.ts:3`')
    expect([...container.querySelectorAll('[data-file-link]')].map(e => e.getAttribute('data-file-link'))).toEqual(['/repo/src/auth.ts'])
    expect([...container.querySelectorAll('code')].map(e => e.textContent)).toEqual(['/v1/cities', '/health', '~/notes/todo.md', '/repo/src/auth.ts:3'])
  })

  it('refuses a lookup whose checkout identity no longer matches the path context', async () => {
    rpc.listWorkspaceFiles.mockResolvedValue({ paths: ['src/auth.ts'], claim_id: 'replacement', claim_generation: 2 })
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
