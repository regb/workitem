import { describe, expect, it } from 'vitest'
import type { ListItem } from './api'
import { placeStarting } from './work-list'

const item = (id: string): ListItem => ({ id, slug: id, title: id, state: 'active', labels: [], project_id: 'p' })

describe('placeStarting', () => {
  it('shows items whose agent is starting under IN PROGRESS, first', () => {
    const sections = [
      { name: 'NEEDS FIXING', items: [item('a'), item('b')] },
      { name: 'IN PROGRESS', items: [item('c')] },
    ]
    const placed = placeStarting(sections, new Map([['b', 1]]))
    expect(placed.map(section => [section.name, section.items.map(row => row.id)])).toEqual([
      ['NEEDS FIXING', ['a']],
      ['IN PROGRESS', ['b', 'c']],
    ])
  })

  it('adds IN PROGRESS before waiting work, and leaves the backlog alone', () => {
    const sections = [
      { name: 'NEEDS FIXING', items: [item('a')] },
      { name: 'WAITING', items: [] },
      { name: 'BACKLOG', items: [item('b')] },
    ]
    const placed = placeStarting(sections, new Map([['a', 1], ['b', 1]]))
    expect(placed.map(section => [section.name, section.items.map(row => row.id)])).toEqual([
      ['NEEDS FIXING', []],
      ['IN PROGRESS', ['a']],
      ['WAITING', []],
      ['BACKLOG', ['b']],
    ])
  })
})
