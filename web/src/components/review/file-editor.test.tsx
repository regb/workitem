import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { FileEditor } from './file-editor'
import { draftKey, reviewKey, reviewUI, setFileDraft } from '@/lib/review'

vi.mock('@/components/confirm', () => ({ useConfirm: () => async () => true }))
vi.mock('./code-view', () => ({
  unavailable: (file: { binary?: boolean; too_large?: boolean }) => (file.binary || file.too_large ? 'Not source' : ''),
  SourceView: ({ file, edit, onChange }: { file: { content?: string }; edit: boolean; onChange: (text: string) => void }) =>
    edit ? <textarea aria-label="Source editor" defaultValue={file.content ?? ''} onChange={event => onChange(event.target.value)} /> : <pre>{file.content}</pre>,
}))

const claim = { claim_id: 'claim', claim_generation: 2 }
const disk = {
  file: {
    path: 'a.ts',
    content: 'original',
    sha256: 'original-hash',
    exists: true,
  },
  ...claim,
}
let client: QueryClient
let fetcher: ReturnType<typeof vi.fn>
function mount(currentClaim = claim, path = 'a.ts') {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <FileEditor id="item" path={path} claim={currentClaim} editable />
    </QueryClientProvider>,
  )
}
beforeEach(() => {
  reviewUI.set(state => ({ ...state, drafts: new Map() }))
  fetcher = vi.fn(async () => new Response(JSON.stringify(disk), { status: 200 }))
  vi.stubGlobal('fetch', fetcher)
})
afterEach(() => {
  cleanup()
  client?.clear()
  vi.unstubAllGlobals()
})

async function edit(text: string) {
  await waitFor(() => expect((screen.getByRole('button', { name: 'Edit' }) as HTMLButtonElement).disabled).toBe(false))
  fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
  fireEvent.change(screen.getByRole('textbox', { name: 'Source editor' }), {
    target: { value: text },
  })
}

describe('file editing', () => {
  it('offers native PDF links without loading binary bytes into the editor', () => {
    mount(claim, 'reports/final.pdf')
    const link = screen.getByRole('link', { name: 'Open PDF in new tab' })
    const url = new URL(link.getAttribute('href')!, 'http://localhost')
    expect(url.pathname).toBe('/api/items/item/document')
    expect(url.searchParams.get('path')).toBe('reports/final.pdf')
    expect(url.searchParams.get('claim_id')).toBe('claim')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(screen.getByRole('link', { name: 'Download PDF' }).getAttribute('href')).toContain('download=1')
    expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull()
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('saves the original digest and claim through the core-backed endpoint', async () => {
    mount()
    await edit('replacement')
    fetcher.mockImplementation(
      async (_url: string, options: RequestInit) =>
        new Response(
          JSON.stringify(
            options.method === 'POST'
              ? {
                  result: {
                    file: {
                      ...disk.file,
                      content: 'replacement',
                      sha256: 'new',
                    },
                  },
                }
              : disk,
          ),
        ),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(reviewUI.get().drafts.size).toBe(0))
    const call = fetcher.mock.calls.find(([, options]) => (options as RequestInit).method === 'POST')!
    expect(JSON.parse((call[1] as RequestInit).body as string)).toEqual({
      path: 'a.ts',
      content: 'replacement',
      expected_sha256: 'original-hash',
      ...claim,
    })
  })

  it('does not replace an active editor document when disk data refreshes', async () => {
    mount()
    await edit('my draft')
    client.setQueryData([...reviewKey('item'), 'source', 'a.ts', claim.claim_id, claim.claim_generation], {
      ...disk,
      file: { ...disk.file, content: 'agent edit', sha256: 'agent-hash' },
    })
    await waitFor(() =>
      expect(
        (
          screen.getByRole('textbox', {
            name: 'Source editor',
          }) as HTMLTextAreaElement
        ).value,
      ).toBe('my draft'),
    )
    expect(reviewUI.get().drafts.get(draftKey('item', 'a.ts'))?.file.sha256).toBe('original-hash')
  })

  it('retains the draft after a save conflict and restores it after reopening', async () => {
    const view = mount()
    await edit('keep this draft')
    fetcher.mockResolvedValue(new Response(JSON.stringify({ error: 'file changed since it was opened' }), { status: 409 }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByRole('alert')
    expect(screen.getByRole('alert').textContent).toContain('Your draft has been kept')
    view.unmount()
    mount()
    expect(
      (
        screen.getByRole('textbox', {
          name: 'Source editor',
        }) as HTMLTextAreaElement
      ).value,
    ).toBe('keep this draft')
  })

  it('shows the latest draft, not the original source, when a claim changes', async () => {
    const view = mount()
    await edit('latest unsaved content')
    view.rerender(
      <QueryClientProvider client={client}>
        <FileEditor id="item" path="a.ts" claim={{ claim_id: 'replacement', claim_generation: 3 }} editable />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('latest unsaved content')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('installs the saved file in the source cache before leaving edit mode', async () => {
    mount()
    await edit('saved content')
    const saved = { ...disk.file, content: 'saved content', sha256: 'saved-hash' }
    fetcher.mockImplementation(async (_url: string, options: RequestInit) => (options.method === 'POST' ? new Response(JSON.stringify({ result: { file: saved } })) : new Promise(() => {})))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(reviewUI.get().drafts.size).toBe(0))
    expect(client.getQueryData([...reviewKey('item'), 'source', 'a.ts', claim.claim_id, claim.claim_generation])).toEqual({ ...claim, file: saved })
    expect(screen.getByText('saved content')).toBeTruthy()
  })

  it('does not clear a replacement draft when an earlier save finishes', async () => {
    mount()
    await edit('same text')
    let finish!: (response: Response) => void
    fetcher.mockImplementation((_url: string, options: RequestInit) =>
      options.method === 'POST'
        ? new Promise<Response>(resolve => {
            finish = resolve
          })
        : Promise.resolve(new Response(JSON.stringify(disk))),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(finish).toBeDefined())
    const replacement = { ...reviewUI.get().drafts.get(draftKey('item', 'a.ts'))!, session_id: 'new-session-on-the-same-source' }
    setFileDraft('item', 'a.ts', replacement)
    finish(new Response(JSON.stringify({ result: { file: { ...disk.file, content: 'same text', sha256: 'saved-hash' } } })))
    await waitFor(() => expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).textContent).toBe('Save'))
    expect(reviewUI.get().drafts.get(draftKey('item', 'a.ts'))).toBe(replacement)
  })

  it('reloads disk before discarding, so a new editor cannot reuse stale source', async () => {
    mount()
    await edit('my draft')
    let finish!: (response: Response) => void
    fetcher.mockImplementation(
      () =>
        new Promise<Response>(resolve => {
          finish = resolve
        }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
    await waitFor(() => expect(finish).toBeDefined())
    expect(reviewUI.get().drafts.size).toBe(1)
    expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull()
    finish(new Response(JSON.stringify({ ...disk, file: { ...disk.file, content: 'disk changed', sha256: 'disk-hash' } })))
    await screen.findByRole('button', { name: 'Edit' })
    expect(screen.getByText('disk changed')).toBeTruthy()
  })

  it('keeps a draft if discard cannot reload the current file', async () => {
    mount()
    await edit('keep until reload works')
    fetcher.mockImplementation(async () => {
      throw new TypeError('offline')
    })
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
    await screen.findByRole('alert')
    expect(reviewUI.get().drafts.get(draftKey('item', 'a.ts'))?.content).toBe('keep until reload works')
  })

  it('never saves an old checkout draft into a new claim', () => {
    setFileDraft('item', 'a.ts', {
      session_id: 'old-session',
      file: disk.file,
      content: 'old draft',
      claim_id: 'old-claim',
      claim_generation: 1,
    })
    mount()
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByRole('alert').textContent).toContain('older checkout claim')
  })
})
