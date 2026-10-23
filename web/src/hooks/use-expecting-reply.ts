import { useEffect, useRef, useState } from 'react'
import type { Live } from '@/lib/api'
import type { OutboxMessage } from '@/lib/history'

// A reply is no longer expected after this long, whatever was observed.
const expectLimit = 120_000

// Live activities of an agent that is not working on a turn.
const quiet = new Set(['', 'Idle', 'Message saved', 'Transcript available'])

type Expectation = { sequence: number; answers: number; at: number; sawWork: boolean; sawBusy: boolean; problemAtSend: boolean }

// useExpectingReply keeps the agent shown as working from the moment this tab
// sends a message until the turn visibly ends. History confirms a message
// before the runtime reports itself busy; without this the working
// indicator would drop out in between.
//
// The expectation ends once the agent was seen working since the send (busy,
// or live activity or text) and then idles or fails, or was busy and no
// longer is; when history gains a
// reply; when a problem appears that was not there at the send; when the send
// failed; or after expectLimit. An idle report before any work does not end
// it: a runtime the send started or replaced reports idle before it picks up
// the message.
export function useExpectingReply(pending: OutboxMessage[], live: Live | null | undefined, busy: boolean, problem = false, answers = 0) {
  const [expectation, setExpectation] = useState<Expectation | null>(null)
  const sent = useRef(pending.length)
  const sequence = live?.sequence ?? 0
  const activity = live?.activity ?? ''

  useEffect(() => {
    if (pending.length > sent.current) setExpectation({ sequence, answers, at: Date.now(), sawWork: false, sawBusy: false, problemAtSend: problem })
    sent.current = pending.length
  }, [pending.length, sequence, answers, problem])

  const failed = pending.some(message => message.status === 'failed' || message.status === 'unknown')
  const working = busy || (sequence > (expectation?.sequence ?? Infinity) && (!quiet.has(activity) || !!live?.text))
  useEffect(() => {
    if (!expectation) return
    const sawWork = expectation.sawWork || working
    const sawBusy = expectation.sawBusy || busy
    const settled = (sawWork && (activity === 'Idle' || activity.startsWith('Turn failed'))) || (expectation.sawBusy && !busy)
    const ended = settled || answers > expectation.answers || (problem && !expectation.problemAtSend) || failed || Date.now() - expectation.at > expectLimit
    if (ended) setExpectation(null)
    else if (sawWork !== expectation.sawWork || sawBusy !== expectation.sawBusy) setExpectation({ ...expectation, sawWork, sawBusy })
  }, [expectation, working, busy, activity, answers, problem, failed])

  // Re-check when the limit passes even if nothing else changes.
  useEffect(() => {
    if (!expectation) return
    const timer = setTimeout(() => setExpectation(current => (current === expectation ? null : current)), expectLimit)
    return () => clearTimeout(timer)
  }, [expectation])

  return expectation !== null
}
