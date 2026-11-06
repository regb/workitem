import { describe, expect, it } from 'vitest'
import { windowLabel } from './usage-settings'

describe('windowLabel', () => {
  it('names windows as wi usage does', () => {
    const window = { remaining_percent: 50, resets_at: null, duration_seconds: null }
    expect(windowLabel({ ...window, id: 'five_hour' })).toBe('5-hour')
    expect(windowLabel({ ...window, id: 'seven_day_opus' })).toBe('Weekly opus')
    expect(windowLabel({ ...window, id: 'codex/primary', duration_seconds: 18000 })).toBe('5-hour')
    expect(windowLabel({ ...window, id: 'other', duration_seconds: 604800, scope: { model: 'gpt-5' } })).toBe('Weekly · other · gpt-5')
  })
})
