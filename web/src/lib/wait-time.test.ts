import { afterEach, describe, expect, it } from 'vitest'
import { waitInstant } from './wait-time'

const originalTZ = process.env.TZ
afterEach(() => {
  process.env.TZ = originalTZ
})

describe('browser wait times', () => {
  it('resolves every preset and custom input in the client timezone', () => {
    process.env.TZ = 'Pacific/Auckland'
    const now = new Date('2026-11-02T12:00:00+13:00')
    expect(waitInstant('1h', now)).toBe('2026-11-02T13:00:00+13:00')
    expect(waitInstant('4h', now)).toBe('2026-11-02T16:00:00+13:00')
    expect(waitInstant('tomorrow', now)).toBe('2026-11-03T09:00:00+13:00')
    expect(waitInstant('monday', now)).toBe('2026-11-09T09:00:00+13:00')
    expect(waitInstant('2026-11-04T10:30', now)).toBe('2026-11-04T10:30:00+13:00')
  })

  it('uses calendar days across DST but elapsed hours for durations', () => {
    process.env.TZ = 'America/New_York'
    const now = new Date('2026-03-07T12:00:00-05:00')
    expect(waitInstant('tomorrow', now)).toBe('2026-03-08T09:00:00-04:00')
    expect(waitInstant('4h', new Date('2026-03-08T00:00:00-05:00'))).toBe('2026-03-08T05:00:00-04:00')
    expect(waitInstant('monday', new Date('2026-03-08T12:00:00-04:00'))).toBe('2026-03-09T09:00:00-04:00')
  })

  it('rejects invalid and past custom times', () => {
    const now = new Date('2026-11-02T12:00:00Z')
    expect(() => waitInstant('invalid', now)).toThrow()
    expect(() => waitInstant('2020-01-01T12:00', now)).toThrow()
  })
})
