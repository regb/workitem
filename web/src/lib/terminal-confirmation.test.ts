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

beforeEach(() => {
  api.mockReset().mockResolvedValue(1)
  itemAction.mockReset().mockResolvedValue({})
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
