import { describe, expect, it } from 'vitest'
import { displaySegments, fromWire, itemMentionAt, mentionBeside, toWire, wireMention } from './item-mentions'

const id = '01J0ABCDEFGHJKMNPQRSTVWXYZ'
const login = { id, title: 'Fix [the] login', slug: 'fix-login' }

describe('item mentions', () => {
  it('sends a compact link with the ID, slug and title', () => {
    expect(wireMention(login)).toBe(`@@[Fix \\[the\\] login](wi:${id} "fix-login")`)
    expect(wireMention({ id, title: 'Plain' })).toBe(`@@[Plain](wi:${id})`)
  })

  it('turns picked mentions into links and back', () => {
    const text = 'see @@Fix [the] login, then @@Fix [the] loginx and a@@Fix [the] login'
    const wire = toWire(text, [login])
    expect(wire).toBe(`see ${wireMention(login)}, then @@Fix [the] loginx and a@@Fix [the] login`)
    expect(fromWire(wire)).toEqual({ text, refs: [login] })
  })

  it('prefers the longest picked title', () => {
    const short = { id, title: 'Fix' }
    expect(displaySegments('@@Fix [the] login now', [short, login]).map(segment => segment.ref?.title)).toEqual(['Fix [the] login', undefined])
  })

  it('finds the @@ mention being typed', () => {
    expect(itemMentionAt('ask @@fix log', 13)).toEqual({ start: 4, query: 'fix log' })
    expect(itemMentionAt('@@', 2)).toEqual({ start: 0, query: '' })
    expect(itemMentionAt('mail@@x', 7)).toBeNull()
    expect(itemMentionAt('@@a\nb', 5)).toBeNull()
  })
})

describe('mentionBeside', () => {
  const ref = { id, title: 'Fix login' }
  const text = 'see @@Fix login now'
  it('finds the whole mention a deletion would cut into', () => {
    expect(mentionBeside(text, [ref], 15, true)).toEqual({ start: 4, end: 15 })
    expect(mentionBeside(text, [ref], 4, false)).toEqual({ start: 4, end: 15 })
  })
  it('leaves plain text and the inside of a mention alone', () => {
    expect(mentionBeside(text, [ref], 16, true)).toBeNull()
    expect(mentionBeside(text, [ref], 4, true)).toBeNull()
    expect(mentionBeside(text, [ref], 10, true)).toBeNull()
    expect(mentionBeside(text, [], 15, true)).toBeNull()
  })
})
