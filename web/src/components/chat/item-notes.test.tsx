import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { fireEvent, render, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ItemNotesSection } from './item-notes'

const calls: Array<{ method: string; request: Record<string, unknown> }> = []
let listed: unknown = { notes: [], attachments: [] }
vi.mock('@/lib/rpc', async original => ({
  ...(await original<object>()),
  items: new Proxy(
    {},
    {
      get: (_, method: string) => async (request: Record<string, unknown>) => {
        calls.push({ method, request })
        return method === 'listNotes' ? listed : {}
      },
    },
  ),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  calls.length = 0
})

const at = (iso: string) => timestampFromDate(new Date(iso))
const report = { name: 'REPORT.md', sha256: 'b'.repeat(64), size: 2048, contentType: 'text/markdown; charset=utf-8', path: '' }

describe('ItemNotesSection', () => {
  it('lists notes newest first and opens a markdown attachment in the page', async () => {
    listed = {
      notes: [
        { time: at('2026-10-25T10:00:00Z'), text: 'Dropped the cache approach', head: 'a'.repeat(40) },
        { time: at('2026-10-25T11:00:00Z'), text: 'Attached REPORT.md', head: '', attachment: report },
      ],
      attachments: [{ attachment: report, time: at('2026-10-25T11:00:00Z') }],
    }
    // Attachments are served at a URL, for the browser to show.
    const fetch = vi.fn(async () => new Response('# Findings\n\nThe **answer**'))
    vi.stubGlobal('fetch', fetch)
    const { container } = render(
      <QueryClientProvider client={new QueryClient()}>
        <ItemNotesSection id="01ITEM" />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(container.querySelectorAll('[data-notes] li')).toHaveLength(2))
    const notes = [...container.querySelectorAll('[data-notes] li p')].map(p => p.textContent)
    expect(notes).toEqual(['Attached REPORT.md', 'Dropped the cache approach'])
    expect(container.querySelector('[data-notes]')?.textContent).toContain('aaaaaaaaa')
    fireEvent.click(within(container.querySelector('[data-attachments]') as HTMLElement).getByText('REPORT.md'))
    await waitFor(() => expect(document.querySelector('[data-attachment-markdown] strong')?.textContent).toBe('answer'))
    expect(fetch).toHaveBeenCalledWith(`/api/items/01ITEM/attachments/${report.sha256}`, expect.anything())
  })

  it('adds a note and attaches a file, then reads the log again', async () => {
    listed = { notes: [], attachments: [] }
    const { container } = render(
      <QueryClientProvider client={new QueryClient()}>
        <ItemNotesSection id="01ITEM" />
      </QueryClientProvider>,
    )
    const input = within(container).getByLabelText('Add a note')
    fireEvent.change(input, { target: { value: 'Chose TTL over LRU' } })
    fireEvent.submit(container.querySelector('[data-note-input]')!)
    await waitFor(() => expect(calls.find(call => call.method === 'addNote')?.request).toEqual({ itemId: '01ITEM', text: 'Chose TTL over LRU' }))
    await waitFor(() => expect((input as HTMLInputElement).value).toBe(''))
    const file = new File(['hi'], 'REPORT.md', { type: 'text/markdown' })
    fireEvent.change(container.querySelector('[data-attach-input]')!, { target: { files: [file] } })
    await waitFor(() => expect(calls.find(call => call.method === 'attach')).toBeTruthy())
    const attached = calls.find(call => call.method === 'attach')!.request
    expect([attached.itemId, attached.name, new TextDecoder().decode(attached.content as Uint8Array)]).toEqual(['01ITEM', 'REPORT.md', 'hi'])
    // The log is read again after an addition.
    await waitFor(() => expect(calls.filter(call => call.method === 'listNotes').length).toBeGreaterThan(1))
  })
})
