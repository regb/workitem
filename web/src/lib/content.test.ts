import { describe, expect, it } from 'vitest'
import type { Live } from './api'
import { applyContent, type ContentEvent } from './content'
// Written by the Go test of driverprotocol.ApplyContent; both must agree.
import fixture from '../../../agentdriver/protocol/testdata/content_events.json'

// Go omits empty optional fields and the browser leaves them undefined, so
// compare what is set.
const set = (value: object) => JSON.parse(JSON.stringify(value, (_, field) => (field === false || field === '' || field === 0 || field === null ? undefined : field)))

describe('applyContent', () => {
  it('agrees with driverprotocol.ApplyContent on the shared fixture', () => {
    let value: Live = {}
    fixture.events.forEach((event: ContentEvent & { snapshot?: Live }, index: number) => {
      value = event.type === 'snapshot' ? { ...event.snapshot } : applyContent(value, event)
      expect(set(value), `after event ${event.sequence} (${event.type})`).toEqual(set(fixture.snapshots[index]))
    })
  })

  it('truncates at a character boundary and stops growing', () => {
    const limit = 256 << 10
    let value = applyContent({}, { sequence: 1, type: 'message.delta', text: 'x'.repeat(limit - 2) + '🌍 tail' })
    expect(value.truncated).toBe(true)
    expect(new TextEncoder().encode(value.text).length).toBe(limit - 2)
    value = applyContent(value, { sequence: 2, type: 'message.delta', text: 'more' })
    expect(value.text?.length).toBe(limit - 2)
  })
})
