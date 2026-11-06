import { describe, expect, it } from 'vitest'
import { windowLabel } from './usage-settings'

describe('windowLabel', () => {
  it('names windows as wi usage does', () => {
    const window = { scopeModel: '', scopeSurface: '' }
    expect(windowLabel({ ...window, id: 'five_hour' })).toBe('5-hour')
    expect(windowLabel({ ...window, id: 'seven_day_opus' })).toBe('Weekly opus')
    expect(windowLabel({ ...window, id: 'codex/primary', durationSeconds: 18000n })).toBe('5-hour')
    expect(windowLabel({ ...window, id: 'other', durationSeconds: 604800n, scopeModel: 'gpt-5' })).toBe('Weekly · other · gpt-5')
  })
})
