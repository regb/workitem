import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { LiveContentSchema, ToolCallPhase, ToolCallStatus, WatchLiveContentResponseSchema } from '@/gen/wi/core/v1/agents_pb'
import type { Live } from './views'
import { applyContent, contentEvent, liveView, type ContentEvent } from './content'
import { partsText } from './parts'
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
    const text = (live: Live) => partsText(live.parts)
    expect(value.truncated).toBe(true)
    expect(new TextEncoder().encode(text(value)).length).toBe(limit - 2)
    value = applyContent(value, { sequence: 2, type: 'message.delta', text: 'more' })
    expect(text(value).length).toBe(limit - 2)
  })

  it('keeps a message in the order it was written, each call in place', () => {
    let value: Live = {}
    const events: Omit<ContentEvent, 'sequence'>[] = [
      { type: 'thought.delta', text: 'Look ' },
      { type: 'thought.delta', text: 'first' },
      { type: 'message.delta', text: 'Reading.' },
      { type: 'tool.started', tool: { id: 't', name: 'read', status: 'running' } },
      { type: 'message.delta', text: 'Done.' },
      { type: 'tool.completed', tool: { id: 't', name: 'read', status: 'completed', output: 'x' } },
    ]
    events.forEach((event, index) => (value = applyContent(value, { ...event, sequence: index + 1 })))
    expect(value.parts).toEqual([{ thought: { text: 'Look first' } }, { text: 'Reading.' }, { tool: { id: 't', name: 'read', status: 'completed', output: 'x' } }, { text: 'Done.' }])
  })
})

describe('API live content', () => {
  it('becomes the driver’s snapshot, without empty fields', () => {
    const live = liveView(
      create(LiveContentSchema, {
        messageId: 'm',
        activity: 'Tool running',
        savedAs: ['e1'],
        parts: [
          { kind: { case: 'thought', value: { text: 'Hm', durationMs: 0 } } },
          { kind: { case: 'text', value: 'Hi' } },
          { kind: { case: 'tool', value: { id: 't1', name: 'bash', input: 'ls', status: ToolCallStatus.RUNNING } } },
        ],
        prompts: [{ clientMessageId: 'r1', nativeIds: ['p1'] }],
        question: { id: 'q', name: 'AskUserQuestion', input: '{}', output: 'x', status: ToolCallStatus.RUNNING },
      }),
      7,
    )
    expect(live).toEqual({
      sequence: 7,
      message_id: 'm',
      activity: 'Tool running',
      saved_as: ['e1'],
      parts: [{ thought: { text: 'Hm' } }, { text: 'Hi' }, { tool: { id: 't1', name: 'bash', input: 'ls', status: 'running' } }],
      prompts: [{ request_id: 'r1', native_ids: ['p1'] }],
      question: { id: 'q', name: 'AskUserQuestion', input: '{}', status: 'running' },
    })
  })

  it('becomes the driver’s events', () => {
    const event = (value: Parameters<typeof create<typeof WatchLiveContentResponseSchema>>[1]) => contentEvent(create(WatchLiveContentResponseSchema, value), 3)
    expect(event({ event: { case: 'tool', value: { phase: ToolCallPhase.COMPLETED, tool: { id: 't', name: 'bash', status: ToolCallStatus.COMPLETED } } } })).toEqual({ sequence: 3, type: 'tool.completed', tool: { id: 't', name: 'bash', status: 'completed' } })
    expect(event({ event: { case: 'promptSaved', value: { link: { clientMessageId: 'r', nativeIds: ['n'] } } } })).toEqual({ sequence: 3, type: 'prompt.saved', request_id: 'r', native_ids: ['n'] })
    expect(event({ event: { case: 'snapshot', value: {} } })).toBeUndefined()
  })
})
