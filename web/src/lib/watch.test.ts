import { create } from '@bufbuild/protobuf'
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WatchLiveContentResponseSchema, type LiveContent } from '@/gen/wi/core/v1/agents_pb'
import { WatchChangesResponseSchema, WatchResponseSchema, type WatchResponse } from '@/gen/wi/core/v1/events_pb'
import type { Live } from './views'

// Each stream the page opens: what it asked for, and a way to feed it.
type Stream = { request: { after: string; itemId: string }; signal: AbortSignal; push: (response: WatchResponse) => void }
const streams: Stream[] = []

vi.mock('./rpc', async original => ({
  ...(await original<object>()),
  events: {
    watch: (request: Stream['request'], options: { signal: AbortSignal }) => {
      const queue: WatchResponse[] = []
      let wake = () => {}
      streams.push({ request, signal: options.signal, push: response => (queue.push(response), wake()) })
      return (async function* () {
        while (!options.signal.aborted) {
          if (queue.length) yield queue.shift()!
          else await new Promise<void>(resolve => ((wake = resolve), options.signal.addEventListener('abort', () => resolve())))
        }
      })()
    },
  },
}))
vi.mock('./queries', async original => ({ ...(await original<object>()), refreshChat: vi.fn(async () => {}), refreshList: vi.fn(async () => {}) }))

const { applyChanges, applyContentEvent, useWatch } = await import('./watch')
const { keys, queryClient, refreshChat, refreshList } = await import('./queries')
const { settling } = await import('./settling')

const changes = (value: { cursor?: string; itemIds?: string[]; all?: boolean }) => create(WatchChangesResponseSchema, { cursor: 'c', ...value })
const snapshot = (value: Partial<LiveContent>) => create(WatchLiveContentResponseSchema, { event: { case: 'snapshot', value: value as LiveContent } })

describe('changes', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.mocked(refreshList).mockClear()
    vi.mocked(refreshChat).mockClear()
  })
  afterEach(async () => {
    await vi.runAllTimersAsync()
    vi.useRealTimers()
  })

  it('read the list again, a few times a second at most', async () => {
    applyChanges(changes({ itemIds: ['a'] }), undefined)
    applyChanges(changes({ itemIds: ['b'] }), undefined)
    applyChanges(changes({ itemIds: ['c'] }), undefined)
    expect(refreshList).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(400)
    expect(refreshList).toHaveBeenCalledTimes(2)
    expect(refreshChat).not.toHaveBeenCalled()
  })

  it('read the open conversation when they name it, or all', async () => {
    applyChanges(changes({ itemIds: ['other'] }), 'open')
    expect(refreshChat).not.toHaveBeenCalled()
    applyChanges(changes({ itemIds: ['open'] }), 'open')
    expect(refreshChat).toHaveBeenCalledWith('open')
    await vi.advanceTimersByTimeAsync(400)
    applyChanges(changes({ all: true }), 'open')
    expect(refreshChat).toHaveBeenCalledTimes(2)
  })

  it('leave quiet answers alone', () => {
    applyChanges(changes({}), 'open')
    expect(refreshList).not.toHaveBeenCalled()
    expect(refreshChat).not.toHaveBeenCalled()
  })
})

describe('live content', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.mocked(refreshChat).mockClear()
  })
  afterEach(async () => {
    await vi.runAllTimersAsync()
    vi.useRealTimers()
  })

  it('ignores a repeated snapshot and reads the conversation when the activity changes', async () => {
    applyContentEvent(snapshot({ activity: 'Idle' }), 'live-item')
    const first = queryClient.getQueryData<{ sequence: number }>(keys.live('live-item'))
    applyContentEvent(snapshot({ activity: 'Idle' }), 'live-item')
    expect(queryClient.getQueryData(keys.live('live-item'))).toBe(first)
    await vi.advanceTimersByTimeAsync(400)
    vi.mocked(refreshChat).mockClear()
    applyContentEvent(create(WatchLiveContentResponseSchema, { event: { case: 'messageDelta', value: { text: 'Hi' } } }), 'live-item')
    expect(refreshChat).not.toHaveBeenCalled()
    applyContentEvent(create(WatchLiveContentResponseSchema, { event: { case: 'activity', value: { activity: 'Thinking' } } }), 'live-item')
    expect(refreshChat).toHaveBeenCalledWith('live-item')
    const live = queryClient.getQueryData<Live>(keys.live('live-item'))
    expect(live).toMatchObject({ parts: [{ text: 'Hi' }], activity: 'Thinking' })
    expect(live!.sequence).toBeGreaterThan(first!.sequence)
  })

  // A message cached from before a gap in the stream would otherwise be held
  // under the conversation long after history moved past it.
  it('holds no message a snapshot replaces', () => {
    queryClient.setQueryData<Live>(keys.live('gap-item'), { message_id: 'old', parts: [{ text: 'Before the gap' }], saved_as: ['entry-old'], sequence: 1 } as Live)
    applyContentEvent(snapshot({ activity: 'Thinking', messageId: 'new' }), 'gap-item')
    expect(settling.get()['gap-item'] ?? []).toEqual([])
  })
})

describe('useWatch', () => {
  it('releases the stream while the tab is hidden and resumes after the last change', async () => {
    let state: DocumentVisibilityState = 'visible'
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
    const { unmount } = renderHook(() => useWatch('item'))
    expect(streams).toHaveLength(1)
    expect(streams[0].request.itemId).toBe('item')
    await act(async () => streams[0].push(create(WatchResponseSchema, { event: { case: 'changes', value: changes({ cursor: 'cursor-1' }) } })))
    act(() => {
      state = 'hidden'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(streams[0].signal.aborted).toBe(true)
    act(() => {
      state = 'visible'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(streams).toHaveLength(2)
    expect(streams[1].request.after).toBe('cursor-1')
    expect(streams[1].signal.aborted).toBe(false)
    unmount()
  })
})

describe('streamProblem', () => {
  it('tells an older daemon apart from a dropped connection', async () => {
    const { Code, ConnectError } = await import('@connectrpc/connect')
    const { streamProblem } = await import('./watch')
    expect(streamProblem(new ConnectError('unknown procedure', Code.Unimplemented))).toMatch(/daemon is older than this page/)
    expect(streamProblem(new ConnectError('connection reset', Code.Unavailable))).toBe('Reconnecting: connection reset')
  })
})
