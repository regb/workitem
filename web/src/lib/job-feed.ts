import { JobState, type Job } from '@/gen/wi/core/v1/jobs_pb'
import type { JobsChanged } from '@/gen/wi/core/v1/events_pb'

// The jobs the tab's watch stream reports, so waiting on a job needs no
// stream of its own. The store only knows what it saw while connected: a
// waiter is told when the stream is lost and asks the daemon instead.

export const finished = (job: Job) => job.state === JobState.SUCCEEDED || job.state === JobState.FAILED || job.state === JobState.CANCELLED

type Waiter = (job: Job | undefined) => void

let connected = false
const known = new Map<string, Job>()
const waiters = new Set<Waiter>()

// noteJobs records what the stream reports. A snapshot starts a connection:
// it lists every unfinished job.
export function noteJobs(changed: JobsChanged) {
  if (changed.snapshot) {
    known.clear()
    connected = true
  }
  for (const job of changed.jobs) {
    known.set(job.id, job)
    waiters.forEach(waiter => waiter(job))
  }
}

// jobFeedLost forgets the jobs once the stream ends: what happens until the
// next snapshot is unseen.
export function jobFeedLost() {
  if (!connected) return
  connected = false
  known.clear()
  waiters.forEach(waiter => waiter(undefined))
}

// followJob resolves with a job once the stream reports it finished,
// telling onPhase of its progress meanwhile. It resolves with undefined when
// the stream is not connected or is lost before then.
export function followJob(job: Job, onPhase?: (phase: string) => void): Promise<Job | undefined> {
  if (!connected) return Promise.resolve(undefined)
  const seen = known.get(job.id)
  if (seen && finished(seen)) return Promise.resolve(seen)
  return new Promise(resolve => {
    const waiter: Waiter = update => {
      if (update && update.id !== job.id) return
      if (update?.phase) onPhase?.(update.phase)
      if (update && !finished(update)) return
      waiters.delete(waiter)
      resolve(update)
    }
    waiters.add(waiter)
  })
}
