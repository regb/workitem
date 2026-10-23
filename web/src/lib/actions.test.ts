import { describe, expect, it } from 'vitest'
import type { ListItem } from './api'
import { closeCheck } from './actions'

const row = (agent: string, worktree: ListItem['worktree']): ListItem => ({
  id: 'i', slug: 'i', title: 'Item', state: 'active', labels: [], project_id: 'p',
  agent: { status: agent },
  worktree,
})

describe('closeCheck', () => {
  it('closes a safe item without asking, even with new commits on its branch', () => {
    expect(closeCheck(row('idle', { status: 'changed', dirty: false }))).toEqual({ warn: [], force: false })
    expect(closeCheck(undefined)).toEqual({ warn: [], force: false })
  })

  it('asks before stopping a busy agent and then forces the stop', () => {
    const check = closeCheck(row('busy', { status: 'clean' }))
    expect(check.force).toBe(true)
    expect(check.warn).toHaveLength(1)
  })

  it('refuses uncommitted changes, which the server would refuse too', () => {
    expect(closeCheck(row('idle', { status: 'changed', dirty: true })).refuse).toMatch(/uncommitted/)
  })

  it('warns about a checkout problem', () => {
    expect(closeCheck(row('idle', { status: 'problem', reason: 'branch differs' })).warn[0]).toMatch(/branch differs/)
  })
})
