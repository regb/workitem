import { describe, expect, it } from 'vitest'
import type { WorkList } from './views'
import { attentionCount } from './ui-state'

const list: WorkList = {
  projects: [{ id: 'a', name: 'a' }, { id: 'b', name: 'b' }],
  display_sections: [
    { name: 'NEEDS ATTENTION', items: [row('ready', 'a'), row('other', 'b')] },
    { name: 'NEEDS FIXING', items: [row('broken', 'a')] },
    { name: 'IN PROGRESS', items: [row('busy', 'a')] },
  ],
}
function row(id: string, project: string) {
  return { id, slug: id, title: id, state: 'active' as const, labels: [], project_id: project }
}

describe('attentionCount', () => {
  it('counts attention and fixing rows in the selected projects', () => {
    expect(attentionCount(list, null, new Map())).toBe(3)
    expect(attentionCount(list, new Set(['b']), new Map())).toBe(1)
  })

  it('excludes items this tab is starting or leaving', () => {
    expect(attentionCount(list, null, new Map([['broken', Date.now()]]), new Set(['ready']))).toBe(1)
  })
})

describe('takeoverEffect', () => {
  it('explains a terminal takeover with and without a runtime restart', async () => {
    const { takeoverEffect } = await import('./actions')
    const control = (replace?: string) => ({ you: false, status: { holder: 'terminal', channels: {}, busy: false }, plan: { needs_confirmation: true, reasons: [], replace } })
    expect(takeoverEffect(control('headless'))).toMatch(/stopped and restarted here in headless mode/)
    expect(takeoverEffect(control())).toMatch(/pane becomes read-only/)
  })
})

describe('project filters and the last project', () => {
  it('adds filters from items, drops the bar with the last one, and remembers the project', async () => {
    const { addProjectFilter, removeProjectFilter, setLastProject, ui } = await import('./ui-state')
    ui.set(state => ({ ...state, selectedProjects: null }))
    addProjectFilter('a')
    addProjectFilter('b')
    expect([...(ui.get().selectedProjects ?? [])]).toEqual(['a', 'b'])
    // Filtering to a project also makes it the default for new work.
    expect(ui.get().lastProject).toBe('b')
    expect(localStorage.getItem('wi.lastProject')).toBe('b')
    removeProjectFilter('a')
    removeProjectFilter('b')
    expect(ui.get().selectedProjects).toBeNull()
    // Choosing a project for new work does not filter the dashboard.
    setLastProject('c')
    expect(ui.get().lastProject).toBe('c')
    expect(ui.get().selectedProjects).toBeNull()
  })
})
