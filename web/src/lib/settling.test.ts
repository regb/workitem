import { beforeEach, describe, expect, it } from 'vitest'
import { forgetSettling, noteLiveChange, pruneSettling, settling } from './settling'

describe('settling messages', () => {
  beforeEach(() => settling.set({}))

  it('holds saved text that left the live view until history shows it', () => {
    noteLiveChange('item', { text: 'Answer', saved_as: ['entry'] }, { text: '' })
    expect(settling.get().item).toEqual([expect.objectContaining({ ids: ['entry'], text: 'Answer' })])
    pruneSettling('item', ids => ids.includes('other'))
    expect(settling.get().item).toHaveLength(1)
    pruneSettling('item', ids => ids.includes('entry'))
    expect(settling.get().item).toEqual([])
  })

  it('forgets a conversation once it is closed', () => {
    noteLiveChange('item', { text: 'Answer', saved_as: ['entry'] }, { text: '' })
    forgetSettling('item')
    expect('item' in settling.get()).toBe(false)
  })

  it('ignores unsaved text and unchanged text', () => {
    noteLiveChange('item', { text: 'Draft' }, { text: '' })
    noteLiveChange('item', { text: 'Answer', saved_as: ['entry'] }, { text: 'Answer', saved_as: ['entry'] })
    expect(settling.get().item).toBeUndefined()
  })
})

describe('content frames', () => {
  beforeEach(() => settling.set({}))

  it('holds a message saved and replaced within one frame', async () => {
    const { applyFrame } = await import('./events')
    const { keys, queryClient } = await import('./queries')
    queryClient.setQueryData(keys.live('frame-item'), { sequence: 1, text: 'Answer' })
    applyFrame(
      {
        type: 'content',
        events: [
          { sequence: 2, type: 'message.saved', native_ids: ['entry'] },
          { sequence: 3, type: 'message.started', message_id: 'live-3' },
        ],
      },
      'frame-item',
    )
    expect(settling.get()['frame-item']).toEqual([expect.objectContaining({ ids: ['entry'], text: 'Answer' })])
  })
})
