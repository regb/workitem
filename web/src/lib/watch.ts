import { Code, ConnectError } from '@connectrpc/connect'
import { useEffect, useSyncExternalStore } from 'react'
import type { WatchLiveContentResponse } from '@/gen/wi/core/v1/agents_pb'
import type { WatchChangesResponse, WatchResponse } from '@/gen/wi/core/v1/events_pb'
import type { Live } from './views'
import { applyContent, contentEvent, liveView } from './content'
import { jobFeedLost, noteJobs } from './job-feed'
import { keys, queryClient, refreshChat, refreshList } from './queries'
import { events, rpcMessage } from './rpc'
import { noteLiveChange } from './settling'
import { setLiveProblem } from './ui-state'

// The tab follows the daemon's Watch stream: which items changed, the jobs,
// and the open item's live content. The list and the conversation are read
// again when it says they changed; live content is applied as it arrives.

// readGap spaces the reads one source of changes causes: during a turn,
// changes come many times a second.
const readGap = 300

// coalesce returns a function that runs read now, or once more after the
// read under way and a gap, however often it is called meanwhile.
export function coalesce(read: () => Promise<unknown>, gap = readGap) {
  let running = false
  let again = false
  const run = async () => {
    running = true
    again = false
    try {
      await read()
    } catch {
      // The next change reads again.
    }
    await new Promise(resolve => setTimeout(resolve, gap))
    running = false
    if (again) void run()
  }
  return () => {
    if (running) again = true
    else void run()
  }
}

const listSoon = coalesce(refreshList)
const chatsSoon = new Map<string, () => void>()
function chatSoon(item: string) {
  let read = chatsSoon.get(item)
  if (!read) chatsSoon.set(item, (read = coalesce(() => refreshChat(item))))
  read()
}

// cursor is the last change the tab saw; a new stream resumes after it, so
// a tab that was hidden reads again only what changed meanwhile.
let cursor = ''
// sequence numbers live content as it arrives; the page tells new output by
// it.
let sequence = 0

// applyChanges reads again what changed. With all, anything may have;
// with opened, the open conversation is read in any case.
export function applyChanges(changes: WatchChangesResponse, item: string | undefined, all = changes.all, opened = false) {
  cursor = changes.cursor
  if (all || changes.itemIds.length || changes.repositoryIds.length) listSoon()
  if (item && (all || opened || changes.itemIds.includes(item))) chatSoon(item)
}

const savedKey = (live: Live | null | undefined) => `${live?.leaf_id ?? ''}\n${(live?.saved_as ?? []).join(' ')}`
const sameLive = (a: Live | null | undefined, b: Live) => !!a && JSON.stringify({ ...a, sequence: 0 }) === JSON.stringify({ ...b, sequence: 0 })

// applyContentEvent applies live content to the open item's. A message saved
// or the activity changing means history moved on: the conversation is read
// again.
export function applyContentEvent(response: WatchLiveContentResponse, item: string) {
  const previous = queryClient.getQueryData<Live | null>(keys.live(item))
  let next: Live
  if (response.event.case === 'snapshot') {
    next = liveView(response.event.value, sequence + 1)
    // Without an agent, the daemon repeats its empty snapshot.
    if (sameLive(previous, next)) return
  } else {
    const event = contentEvent(response, sequence + 1)
    if (!event) return
    next = applyContent(previous ?? {}, event)
  }
  sequence++
  // A snapshot replaces what the tab had, which may be from before a gap in
  // the stream (a hidden tab, a daemon restart): that message is not one that
  // just ended, and a new stream reads the conversation again anyway.
  if (response.event.case !== 'snapshot') noteLiveChange(item, previous, next)
  queryClient.setQueryData(keys.live(item), next)
  if (previous?.activity !== next.activity || savedKey(previous) !== savedKey(next)) chatSoon(item)
}

export function applyWatch(response: WatchResponse, item: string | undefined) {
  const event = response.event
  switch (event.case) {
    case 'changes':
      applyChanges(event.value, item)
      break
    case 'content':
      if (item) applyContentEvent(event.value, item)
      break
    case 'jobs':
      noteJobs(event.value)
      break
  }
}

// useWatch holds one stream for the current view while the tab is visible,
// and reconnects with backoff. Hidden tabs release it: over plain HTTP a
// browser allows only six connections per host, and every open tab would
// otherwise hold one. A stream that ran and ended (they last ten minutes) is
// renewed quietly; only a failure to connect shows.
// streamProblem says why the stream is down. A daemon that does not know the
// call is an older build than this page, which comes with the wi binary
// running wi web: restarting the daemon is the fix, and retrying is not.
export function streamProblem(error: unknown) {
  if (error instanceof ConnectError && error.code === Code.Unimplemented) return 'The daemon is older than this page. Restart it (wi daemon stop, then any wi command) to see live updates.'
  return `Reconnecting: ${rpcMessage(error)}`
}

export function useWatch(item: string | undefined) {
  const visible = useVisible()
  useEffect(() => {
    if (!visible) return
    const controller = new AbortController()
    const run = async () => {
      let delay = 1000
      while (!controller.signal.aborted) {
        // A stream from no cursor cannot say what changed before it: its
        // first answer reads everything shown. A conversation is read
        // whenever a stream opens it, as it may be shown from the cache.
        const fresh = !cursor
        const opened = Date.now()
        let first = true
        let received = false
        try {
          for await (const response of events.watch({ after: cursor, itemId: item ?? '' }, { signal: controller.signal })) {
            if (!received) {
              received = true
              setLiveProblem('stream', '')
            }
            if (first && response.event.case === 'changes') {
              first = false
              applyChanges(response.event.value, item, fresh || response.event.value.all, true)
            } else applyWatch(response, item)
          }
        } catch (error) {
          if (!controller.signal.aborted && !received) setLiveProblem('stream', streamProblem(error))
        } finally {
          jobFeedLost()
        }
        if (controller.signal.aborted) return
        // A stream that keeps ending soon after it opens backs off too.
        const lasted = received && Date.now() - opened > 5000
        if (lasted) delay = 1000
        await new Promise(resolve => setTimeout(resolve, delay))
        if (!lasted) delay = Math.min(delay * 2, 10000)
      }
    }
    void run()
    return () => {
      controller.abort()
      // A conversation left behind no longer explains anything.
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
