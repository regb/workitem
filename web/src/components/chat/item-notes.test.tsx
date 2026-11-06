import { fireEvent, render, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ItemNotesSection } from './item-notes'

afterEach(() => vi.unstubAllGlobals())

const report = { name: 'REPORT.md', sha256: 'b'.repeat(64), size: 2048, type: 'text/markdown; charset=utf-8' }

describe('ItemNotesSection', () => {
  it('lists notes newest first and opens a markdown attachment in the page', async () => {
    const fetch = vi.fn(async (url: string) =>
      url.endsWith('/notes')
        ? new Response(
            JSON.stringify({
              notes: [
                { time: '2026-10-25T10:00:00Z', text: 'Dropped the cache approach', head: 'a'.repeat(40) },
                { time: '2026-10-25T11:00:00Z', text: 'Attached REPORT.md', attachment: report },
              ],
              attachments: [{ ...report, time: '2026-10-25T11:00:00Z' }],
            }),
          )
        : new Response('# Findings\n\nThe **answer**'),
    )
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
    const posts: [string, unknown][] = []
    const fetch = vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        posts.push([url, JSON.parse(String(init.body))])
        return new Response('{}')
      }
      return new Response(JSON.stringify({ notes: [], attachments: [] }))
    })
    vi.stubGlobal('fetch', fetch)
    const { container } = render(
      <QueryClientProvider client={new QueryClient()}>
        <ItemNotesSection id="01ITEM" />
      </QueryClientProvider>,
    )
    const input = within(container).getByLabelText('Add a note')
    fireEvent.change(input, { target: { value: 'Chose TTL over LRU' } })
    fireEvent.submit(container.querySelector('[data-note-input]')!)
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]).toEqual(['/api/items/01ITEM/notes', { text: 'Chose TTL over LRU' }])
    await waitFor(() => expect((input as HTMLInputElement).value).toBe(''))
    const file = new File(['hi'], 'REPORT.md', { type: 'text/markdown' })
    fireEvent.change(container.querySelector('[data-attach-input]')!, { target: { files: [file] } })
    await waitFor(() => expect(posts).toHaveLength(2))
    expect(posts[1]).toEqual(['/api/items/01ITEM/attachments', { name: 'REPORT.md', content: 'aGk=' }])
    // The log is read again after an addition.
    await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init?.method === 'GET').length).toBeGreaterThan(1))
  })
})
