import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { loadFresh, reloadForNewBuild, staleBuild } from './stale-build'

const reload = vi.fn()
beforeEach(() => {
  sessionStorage.clear()
  reload.mockReset()
  vi.stubGlobal('location', { reload })
})
afterEach(() => vi.unstubAllGlobals())

const missing = new TypeError('Failed to fetch dynamically imported module: http://wi/assets/review-panel-DWIjUfPL.js')

describe('a tab older than the build served', () => {
  it('recognises a part of the page it can no longer load, in each browser', () => {
    expect(staleBuild(missing)).toBe(true)
    expect(staleBuild(new TypeError('Importing a module script failed.'))).toBe(true)
    expect(staleBuild(new Error('network down'))).toBe(false)
  })

  it('reloads once, and not again right away, so a broken build cannot loop', () => {
    expect(reloadForNewBuild(missing, 1_000_000)).toBe(true)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(reloadForNewBuild(missing, 1_010_000)).toBe(false)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(reloadForNewBuild(missing, 1_040_000)).toBe(true)
    expect(reloadForNewBuild(new Error('other'), 2_000_000)).toBe(false)
  })

  it('loads normally, reloads a stale tab instead of failing, and passes other errors on', async () => {
    expect(await loadFresh(async () => 'module')()).toBe('module')
    let settled = false
    void loadFresh(() => Promise.reject(missing))().finally(() => (settled = true))
    await new Promise(resolve => setTimeout(resolve, 10))
    expect(reload).toHaveBeenCalledTimes(1)
    expect(settled).toBe(false)
    await expect(loadFresh(() => Promise.reject(new Error('syntax error')))()).rejects.toThrow('syntax error')
  })
})
