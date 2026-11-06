import { describe, expect, it } from 'vitest'
import { parseEvents } from './events'

describe('parseEvents', () => {
  it('returns complete frames, skips comments and keeps the remainder', () => {
    const { frames, rest } = parseEvents(': keep-alive\n\ndata: {"type":"live","live":{"text":"hi"}}\n\ndata: {"type":"li')
    expect(frames).toEqual([{ type: 'live', live: { text: 'hi' } }])
    expect(rest).toBe('data: {"type":"li')
  })
})

describe('useEventStream', () => {
  it('releases the stream while the tab is hidden and reconnects when visible', async () => {
    const { renderHook } = await import('@testing-library/react')
    const { useEventStream } = await import('./events')
    const signals: AbortSignal[] = []
    globalThis.fetch = ((_: unknown, init?: RequestInit) => {
      signals.push(init!.signal!)
      return new Promise(() => {})
    }) as typeof fetch
    let state: DocumentVisibilityState = 'visible'
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
    const { unmount } = renderHook(() => useEventStream('item'))
    expect(signals).toHaveLength(1)
    const { act } = await import('@testing-library/react')
    act(() => {
      state = 'hidden'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(signals[0].aborted).toBe(true)
    act(() => {
      state = 'visible'
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(signals).toHaveLength(2)
    expect(signals[1].aborted).toBe(false)
    unmount()
  })
})

describe('live problems', () => {
  it('shows a server read failure until the next good frame', async () => {
    const { applyFrame } = await import('./events')
    const { ui } = await import('./ui-state')
    applyFrame({ type: 'list', error: 'daemon unavailable' }, undefined)
    expect(ui.get().liveProblems.list).toBe('daemon unavailable')
    applyFrame({ type: 'list', list: { projects: [], display_sections: [] } as never }, undefined)
    expect(ui.get().liveProblems.list).toBeUndefined()
  })
})

describe('chat frames', () => {
  it('keep the snapshot whose read began last', async () => {
    const { applyFrame } = await import('./events')
    const { keys, queryClient, setChat } = await import('./queries')
    const chat = (status: string, read_at: number) => ({ status, read_at }) as unknown as import('./api').Chat
    expect(setChat('x', chat('busy', 20))).toBe(true)
    // A stream read begun before the agent came online lands afterwards.
    globalThis.fetch = (() => new Promise(() => {})) as typeof fetch
    applyFrame({ type: 'chat', chat: chat('idle', 10) }, 'x')
    expect(queryClient.getQueryData<{ status: string }>(keys.chat('x'))?.status).toBe('busy')
    applyFrame({ type: 'chat', chat: chat('idle', 30) }, 'x')
    expect(queryClient.getQueryData<{ status: string }>(keys.chat('x'))?.status).toBe('idle')
  })
})
