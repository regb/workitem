import { describe, expect, it, vi } from 'vitest'

describe('places', () => {
  it('keeps where each conversation was left across a reload', async () => {
    sessionStorage.clear()
    const { placeOf, savePlace } = await import('./places')
    savePlace('a', { entry: 'm1', offset: -40 })
    savePlace('b', 'end')
    expect(placeOf('a')).toEqual({ entry: 'm1', offset: -40 })
    vi.resetModules()
    const reloaded = await import('./places')
    expect(reloaded.placeOf('a')).toEqual({ entry: 'm1', offset: -40 })
    expect(reloaded.placeOf('b')).toBe('end')
    expect(reloaded.placeOf('c')).toBeUndefined()
  })

  it('survives unreadable saved places', async () => {
    sessionStorage.setItem('wi-places', '{not json')
    vi.resetModules()
    const { placeOf } = await import('./places')
    expect(placeOf('a')).toBeUndefined()
  })
})
