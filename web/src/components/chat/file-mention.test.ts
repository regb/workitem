import { describe, expect, it } from 'vitest'
import { mentionAt } from './file-mention'

describe('mentionAt', () => {
  it('finds the mention being typed before the caret', () => {
    expect(mentionAt('look at @src/ap', 15)).toEqual({ start: 8, query: 'src/ap' })
    expect(mentionAt('@', 1)).toEqual({ start: 0, query: '' })
    // A finished mention followed by a space, or an e-mail address, is not one.
    expect(mentionAt('look at @src/app.ts ', 20)).toBeNull()
    expect(mentionAt('mail me@example.com', 19)).toBeNull()
    // Only the text before the caret counts.
    expect(mentionAt('@docs and more', 5)).toEqual({ start: 0, query: 'docs' })
  })
})
