import { beforeEach, describe, expect, it } from 'vitest'
import { forgetSettling, noteLiveChange, pruneSettling, settling } from './settling'

describe('settling messages', () => {
  beforeEach(() => settling.set({}))

  it('holds saved text that left the live view until history shows it', () => {
    noteLiveChange('item', { message_id: 'm1', parts: [{ text: 'Answer' }], saved_as: ['entry'] }, { message_id: 'm2' })
    expect(settling.get().item).toEqual([expect.objectContaining({ ids: ['entry'], parts: [{ text: 'Answer' }] })])
    pruneSettling('item', ids => ids.includes('other'))
    expect(settling.get().item).toHaveLength(1)
    pruneSettling('item', ids => ids.includes('entry'))
    expect(settling.get().item).toEqual([])
  })

  it('forgets a conversation once it is closed', () => {
    noteLiveChange('item', { message_id: 'm1', parts: [{ text: 'Answer' }], saved_as: ['entry'] }, { message_id: 'm2' })
    forgetSettling('item')
    expect('item' in settling.get()).toBe(false)
  })

  it('ignores an unsaved message and one still live', () => {
    noteLiveChange('item', { message_id: 'm1', parts: [{ text: 'Draft' }] }, { message_id: 'm2' })
    const saved = { message_id: 'm1', parts: [{ text: 'Answer' }], saved_as: ['entry'] }
    noteLiveChange('item', saved, { ...saved, parts: [...saved.parts, { tool: { id: 't', name: 'bash', status: 'running' } }] })
    expect(settling.get().item).toBeUndefined()
  })
})

describe('content events', () => {
  beforeEach(() => settling.set({}))

  it('hold a message saved and replaced in quick succession', async () => {
    const { create } = await import('@bufbuild/protobuf')
    const { WatchLiveContentResponseSchema } = await import('@/gen/wi/core/v1/agents_pb')
    const { applyContentEvent } = await import('./watch')
    const { keys, queryClient } = await import('./queries')
    queryClient.setQueryData(keys.live('frame-item'), { sequence: 1, message_id: 'live-2', parts: [{ text: 'Answer' }] })
    applyContentEvent(create(WatchLiveContentResponseSchema, { event: { case: 'messageSaved', value: { nativeIds: ['entry'] } } }), 'frame-item')
    applyContentEvent(create(WatchLiveContentResponseSchema, { event: { case: 'messageStarted', value: { messageId: 'live-3' } } }), 'frame-item')
    expect(settling.get()['frame-item']).toEqual([expect.objectContaining({ ids: ['entry'], parts: [{ text: 'Answer' }] })])
  })
})
