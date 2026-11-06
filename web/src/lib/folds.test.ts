import { describe, expect, it, vi } from 'vitest'

describe('folds', () => {
  it('keeps the folds opened in each conversation across a reload', async () => {
    sessionStorage.clear()
    vi.resetModules()
    const { foldOpen, saveFold } = await import('./folds')
    saveFold('a', 'tool-1', true)
    saveFold('a', 'fold-m1', true)
    saveFold('a', 'fold-m1', false)
    saveFold('b', 'tool-1', true)
    vi.resetModules()
    const reloaded = await import('./folds')
    expect(reloaded.foldOpen('a', 'tool-1')).toBe(true)
    expect(reloaded.foldOpen('a', 'fold-m1')).toBe(false)
    expect(reloaded.foldOpen('b', 'tool-1')).toBe(true)
    expect(foldOpen('c', 'tool-1')).toBe(false)
  })

  it('survives unreadable saved folds', async () => {
    sessionStorage.setItem('wi-folds', '{not json')
    vi.resetModules()
    const { foldOpen } = await import('./folds')
    expect(foldOpen('a', 'tool-1')).toBe(false)
  })
})
