import { describe, expect, it } from 'vitest'
import { blockerShort, blockerText } from './blockers'

describe('usage blockers', () => {
  const later = new Date(Date.now() + 2 * 3600_000).toISOString()
  const past = new Date(Date.now() - 60_000).toISOString()

  it('name the provider and when its limit resets', () => {
    expect(blockerText({ id: 'b', kind: 'usage', provider: 'claude', until: later })).toMatch(/^Claude usage limit, resets /)
    expect(blockerText({ id: 'b', kind: 'usage', provider: 'opencode-go' })).toBe('opencode-go usage limit')
    expect(blockerShort({ id: 'b', kind: 'usage', until: later })).toMatch(/^usage, resets /)
  })

  it('stop promising a reset once it has passed', () => {
    expect(blockerText({ id: 'b', kind: 'usage', until: past })).toBe('usage limit')
    expect(blockerShort({ id: 'b', kind: 'usage', until: past })).toBe('usage limit')
  })
})
