import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { AgentService, ControlService, ControlStatusSchema, type ControlStatus } from '@/gen/wi/core/v1/agents_pb'
import { EventService } from '@/gen/wi/core/v1/events_pb'
import { ItemService } from '@/gen/wi/core/v1/items_pb'
import { PartialOutcomeSchema } from '@/gen/wi/core/v1/common_pb'
import { JobService, JobState, type Job } from '@/gen/wi/core/v1/jobs_pb'
import { LifecycleService } from '@/gen/wi/core/v1/lifecycle_pb'
import { AgentProfileService } from '@/gen/wi/core/v1/profiles_pb'
import { RepositoryService } from '@/gen/wi/core/v1/repositories_pb'
import { ReviewService } from '@/gen/wi/core/v1/reviews_pb'
import { ScheduleService } from '@/gen/wi/core/v1/schedules_pb'
import { SystemService } from '@/gen/wi/core/v1/system_pb'
import { ViewService } from '@/gen/wi/core/v1/views_pb'
import { WorkspaceService } from '@/gen/wi/core/v1/workspaces_pb'
import { finished, followJob } from './job-feed'
import { clientID } from './session'

// The page calls the daemon's API, wi.core.v1, with the generated clients:
// wi web forwards the calls to the daemon under this tab's identity, which
// it reads from X-Wi-Client, as it does for the page's other requests.
const identify: Interceptor = next => request => {
  request.header.set('X-Wi-Client', clientID)
  return next(request)
}

const transport = createConnectTransport({ baseUrl: typeof window === 'undefined' ? 'http://localhost' : window.location.origin, interceptors: [identify] })

export const profiles = createClient(AgentProfileService, transport)
export const schedules = createClient(ScheduleService, transport)
export const system = createClient(SystemService, transport)
export const repositories = createClient(RepositoryService, transport)
export const jobs = createClient(JobService, transport)
export const workspaces = createClient(WorkspaceService, transport)
export const items = createClient(ItemService, transport)
export const agents = createClient(AgentService, transport)
export const reviews = createClient(ReviewService, transport)
export const control = createClient(ControlService, transport)
export const lifecycle = createClient(LifecycleService, transport)
export const views = createClient(ViewService, transport)
export const events = createClient(EventService, transport)

// rpcMessage is what to tell the operator about a failed call: the daemon's
// own message, without the code Connect prefixes it with.
export function rpcMessage(error: unknown) {
  if (error instanceof ConnectError) return error.rawMessage
  return error instanceof Error ? error.message : String(error)
}

// refused reports whether the daemon answered a call with a refusal, so it
// did nothing (or what a partial outcome says). Any other failure, such as a
// dropped connection, leaves the result unknown.
export function refused(error: unknown) {
  if (!(error instanceof ConnectError)) return false
  return ![Code.Unknown, Code.Unavailable, Code.DeadlineExceeded, Code.Canceled, Code.Internal].includes(error.code)
}

// refusedControl is the control status a refusal for lack of control
// carries: who holds it and what taking it over would do.
export function refusedControl(error: unknown): ControlStatus | undefined {
  return error instanceof ConnectError ? error.findDetails(ControlStatusSchema)[0] : undefined
}

// partialItem is the item a failed creation or start kept, if any.
export function partialItem(error: unknown) {
  return error instanceof ConnectError ? error.findDetails(PartialOutcomeSchema)[0]?.itemId || undefined : undefined
}

// awaitJob resolves with a job once it finished, following its progress, or
// rejects with its error. A job that kept part of its work reports it in the
// error's message, as its summary says. The tab's watch stream reports jobs;
// without it, or once it is lost, the job is watched on a stream of its own.
export async function awaitJob(job: Job | undefined, onPhase?: (phase: string) => void): Promise<Job | undefined> {
  if (!job) return undefined
  let latest = job
  if (!finished(latest)) latest = (await followJob(latest, onPhase)) ?? latest
  if (!finished(latest)) {
    for await (const update of jobs.watchJob({ jobId: job.id })) {
      if (!update.job) continue
      latest = update.job
      if (latest.phase) onPhase?.(latest.phase)
      if (finished(latest)) break
    }
  }
  if (latest.state !== JobState.SUCCEEDED) {
    const partial = latest.partial ? [{ desc: PartialOutcomeSchema, value: latest.partial }] : undefined
    throw new ConnectError(latest.error || 'the job did not finish', Code.Aborted, undefined, partial)
  }
  return latest
}

// allPages reads a paged list to its end: read takes a page token and
// returns that page, and the pages' entries are joined in order. The first
// page's other fields stand for the whole.
export async function allPages<T extends { nextPageToken: string }>(read: (pageToken: string) => Promise<T>, entries: (page: T) => unknown[]): Promise<T> {
  const first = await read('')
  let token = first.nextPageToken
  for (let pages = 1; token && pages < 1000; pages++) {
    const next = await read(token)
    entries(first).push(...entries(next))
    token = next.nextPageToken
  }
  first.nextPageToken = ''
  return first
}
