import { beforeEach, describe, expect, it, vi } from 'vitest'

const { api, itemAction } = vi.hoisted(() => ({
  api: vi.fn(),
  itemAction: vi.fn(),
}))
vi.mock('./api', async original => ({
  ...(await original<typeof import('./api')>()),
  api,
  itemAction,
}))
vi.mock('./queries', () => ({ refreshChat: vi.fn(), refreshList: vi.fn() }))
vi.mock('sonner', () => ({
  toast: {
    loading: vi.fn(),
    success: vi.fn(),
    dismiss: vi.fn(),
    error: vi.fn(),
  },
}))

import { closeCheck, closeItem, shelveItem } from './actions'
import type { ListItem } from './api'

beforeEach(() => {
  api.mockReset().mockResolvedValue(1)
  itemAction.mockReset().mockResolvedValue({})
})

describe('Done with nullable uncommitted lists', () => {
  const dirty: ListItem = { id: 'i', slug: 'i', title: 'Item', state: 'active', labels: [], project_id: 'p', agent: { status: 'idle' }, worktree: { status: 'changed', dirty: true } }
  it.each([{ files: null, attached: ['ACCESS_DESIGN.md'] }, { attached: ['ACCESS_DESIGN.md'] }, { files: null, attached: null }])('closes attachment-only or empty responses without a commit: %j', async changes => {
    api.mockResolvedValueOnce(changes).mockResolvedValueOnce(0)
    const confirm = vi.fn()
    expect(await closeItem(confirm, 'i', 'Item', dirty)).toBe(true)
    expect(confirm).not.toHaveBeenCalled()
    expect(itemAction).toHaveBeenCalledExactlyOnceWith('i', 'close', { resolution: 'completed', force: false, commit_changes: false, confirm_attached_terminal: false })
  })
  it('still asks before committing real changes when attached is null', async () => {
    api.mockResolvedValueOnce({ files: ['changed.ts'], attached: null }).mockResolvedValueOnce(0)
    const confirm = vi.fn().mockResolvedValue(false)
    expect(await closeItem(confirm, 'i', 'Item', dirty)).toBe(false)
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ action: 'Commit and close', points: [expect.stringContaining('changed.ts')] }))
    expect(itemAction).not.toHaveBeenCalled()
  })
})

describe('attached terminal confirmation', () => {
  it('warns even for an otherwise safe close', () => {
    expect(closeCheck(undefined, undefined, 1).warn).toEqual([expect.stringContaining('terminal is attached')])
  })
  it('does not close or shelve when cancelled', async () => {
    const confirm = vi.fn().mockResolvedValue(false)
    expect(await closeItem(confirm, 'i', 'Item', undefined)).toBe(false)
    expect(await shelveItem(confirm, 'i', 'Item')).toBe(false)
    expect(itemAction).not.toHaveBeenCalled()
    expect(confirm).toHaveBeenLastCalledWith(
      expect.objectContaining({
        points: [expect.stringContaining('terminal is attached')],
      }),
    )
  })
  it('sends explicit consent only after confirmation', async () => {
    const confirm = vi.fn().mockResolvedValue(true)
    await closeItem(confirm, 'i', 'Item', undefined)
    await shelveItem(confirm, 'i', 'Item')
    expect(itemAction).toHaveBeenCalledWith('i', 'close', expect.objectContaining({ confirm_attached_terminal: true }))
    expect(itemAction).toHaveBeenCalledWith('i', 'shelve', {
      confirm_attached_terminal: true,
    })
  })
  it('does not act when terminal inspection fails', async () => {
    api.mockRejectedValue(new Error('Cannot inspect terminal'))
    expect(await closeItem(vi.fn(), 'i', 'Item', undefined)).toBe(false)
    expect(await shelveItem(vi.fn(), 'i', 'Item')).toBe(false)
    expect(itemAction).not.toHaveBeenCalled()
  })
  it('does not authorize a newly attached terminal based on an empty preflight', async () => {
    api.mockResolvedValue(0)
    await closeItem(vi.fn(), 'i', 'Item', undefined)
    expect(itemAction).toHaveBeenCalledWith('i', 'close', expect.objectContaining({ confirm_attached_terminal: false }))
  })
})
