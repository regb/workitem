import { useEffect, useSyncExternalStore } from 'react'
import { headers, type Chat, type Creation, type Live, type WorkList } from './api'
import { applyContent, type ContentEvent } from './content'
import { keys, noteStreamed, queryClient, refreshChat, setChat } from './queries'
import { noteLiveChange } from './settling'
import { setLiveProblem } from './ui-state'

// Frame is one server-sent event. List, creations, chat and live frames are
// full snapshots, so a reconnecting tab simply receives the current state;
// content frames carry the live events that follow the last live snapshot.
export type Frame =
  | { type: 'list'; list?: WorkList; error?: string }
  | { type: 'creations'; creations?: Creation[] }
  | { type: 'chat'; chat?: Chat; error?: string }
  | { type: 'live'; live?: Live }
  | { type: 'content'; events?: ContentEvent[] }

export function applyFrame(frame: Frame, item: string | undefined) {
  switch (frame.type) {
    case 'list':
      setLiveProblem('list', frame.error ?? '')
      if (frame.list) {
        noteStreamed(keys.list)
        queryClient.setQueryData(keys.list, frame.list)
      }
      break
    case 'creations':
      queryClient.setQueryData(keys.creations, frame.creations ?? [])
      break
    case 'chat':
      setLiveProblem('chat', frame.error ?? '')
      // A stale frame is dropped, and the chat read again: the stream
      // resends a chat only when it changes, so it may not send it again.
      if (item && frame.chat && !setChat(item, frame.chat)) void refreshChat(item)
      break
    case 'live':
    case 'content': {
      if (!item) break
      const previous = queryClient.getQueryData<Live | null>(keys.live(item))
      // Each step is checked, not just the frame: a message saved and the next
      // one started can arrive in the same frame.
      const next =
        frame.type === 'live'
          ? (noteLiveChange(item, previous, frame.live), frame.live ?? null)
          : (frame.events ?? []).reduce<Live>((value, event) => {
              const applied = applyContent(value, event)
              noteLiveChange(item, value, applied)
              return applied
            }, previous ?? {})
      queryClient.setQueryData(keys.live(item), next)
      break
    }
  }
}

// parseEvents splits an SSE buffer into complete events and the remainder.
export function parseEvents(buffer: string): { frames: Frame[]; rest: string } {
  const frames: Frame[] = []
  let rest = buffer
  let end: number
  while ((end = rest.indexOf('\n\n')) >= 0) {
    const event = rest.slice(0, end)
    rest = rest.slice(end + 2)
    const data = event
      .split('\n')
      .filter(line => line.startsWith('data:'))
      .map(line => line.slice(5).replace(/^ /, ''))
      .join('\n')
    if (data) frames.push(JSON.parse(data) as Frame)
  }
  return { frames, rest }
}

// useEventStream holds one stream for the current view while the tab is
// visible, and reconnects with backoff. Hidden tabs release it: over plain
// HTTP a browser allows only six connections per host, and every open tab
// would otherwise hold one. Each connection starts with full snapshots, so a
// tab returning to view loses nothing. It uses fetch so the tab's client
// identity can be sent.
export function useEventStream(item: string | undefined) {
  const visible = useVisible()
  useEffect(() => {
    if (!visible) return
    const controller = new AbortController()
    const run = async () => {
      let delay = 1000
      while (!controller.signal.aborted) {
        try {
          const response = await fetch(`/api/events${item ? `?item=${encodeURIComponent(item)}` : ''}`, { headers: headers(), signal: controller.signal })
          if (!response.ok || !response.body) throw new Error(`the server answered ${response.status}`)
          setLiveProblem('stream', '')
          delay = 1000
          const reader = response.body.getReader()
          const decoder = new TextDecoder()
          let buffer = ''
          for (;;) {
            const { done, value } = await reader.read()
            if (done) break
            const parsed = parseEvents(buffer + decoder.decode(value, { stream: true }))
            buffer = parsed.rest
            for (const frame of parsed.frames) applyFrame(frame, item)
          }
        } catch (error) {
          if (controller.signal.aborted) return
          // A stream the server ends is renewed quietly; only a failure shows.
          setLiveProblem('stream', `Reconnecting: ${(error as Error).message}`)
        }
        await new Promise(resolve => setTimeout(resolve, delay))
        delay = Math.min(delay * 2, 10000)
      }
    }
    void run()
    return () => {
      controller.abort()
      // A chat left behind no longer explains anything.
      setLiveProblem('chat', '')
    }
  }, [item, visible])
}

function useVisible() {
  return useSyncExternalStore(
    listener => {
      document.addEventListener('visibilitychange', listener)
      return () => document.removeEventListener('visibilitychange', listener)
    },
    () => document.visibilityState !== 'hidden',
  )
}
