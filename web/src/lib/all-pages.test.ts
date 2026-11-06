import { expect, it } from 'vitest'
import { allPages } from './rpc'

it('reads a paged list to its end, in order', async () => {
  const pages: Record<string, { entries: number[]; version: number; nextPageToken: string }> = {
    '': { entries: [1, 2], version: 7, nextPageToken: 'b' },
    b: { entries: [3, 4], version: 8, nextPageToken: 'c' },
    c: { entries: [5], version: 9, nextPageToken: '' },
  }
  const read: string[] = []
  const all = await allPages(
    async token => {
      read.push(token)
      return { ...pages[token], entries: [...pages[token].entries] }
    },
    page => page.entries,
  )
  expect(read).toEqual(['', 'b', 'c'])
  expect(all).toEqual({ entries: [1, 2, 3, 4, 5], version: 7, nextPageToken: '' })
})
