import { create, type MessageInitShape } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { PartialOutcomeSchema } from '@/gen/wi/core/v1/common_pb'
import { JobsChangedSchema } from '@/gen/wi/core/v1/events_pb'
import { JobSchema, JobState, type Job } from '@/gen/wi/core/v1/jobs_pb'
import { followJob, jobFeedLost, noteJobs } from './job-feed'
import { awaitJob } from './rpc'

const job = (id: string, state: JobState, value: MessageInitShape<typeof JobSchema> = {}) => create(JobSchema, { id, state, ...value })
const report = (jobs: Job[], snapshot = false) => noteJobs(create(JobsChangedSchema, { jobs, snapshot }))

describe('the job feed', () => {
  it('cannot follow a job before the stream reports jobs', async () => {
    jobFeedLost()
    expect(await followJob(job('j0', JobState.RUNNING))).toBeUndefined()
  })

  it('follows a job to its end, telling its phases', async () => {
    report([], true)
    const phases: string[] = []
    const followed = followJob(job('j1', JobState.RUNNING), phase => phases.push(phase))
    report([job('other', JobState.SUCCEEDED), job('j1', JobState.RUNNING, { phase: 'starting the agent' })])
    report([job('j1', JobState.SUCCEEDED)])
    expect((await followed)?.state).toBe(JobState.SUCCEEDED)
    expect(phases).toEqual(['starting the agent'])
  })

  it('answers at once for a job it already saw finish', async () => {
    report([], true)
    report([job('j2', JobState.FAILED)])
    expect((await followJob(job('j2', JobState.RUNNING)))?.state).toBe(JobState.FAILED)
  })

  it('gives up when the stream is lost', async () => {
    report([], true)
    const followed = followJob(job('j3', JobState.RUNNING))
    jobFeedLost()
    expect(await followed).toBeUndefined()
  })
})

describe('awaitJob', () => {
  it('rejects with the partial outcome of a job the stream reports failed', async () => {
    report([], true)
    const waited = awaitJob(job('j4', JobState.RUNNING))
    report([job('j4', JobState.FAILED, { error: 'setup failed', partial: create(PartialOutcomeSchema, { itemId: 'item' }) })])
    const error = await waited.catch((error: unknown) => error)
    expect(error).toBeInstanceOf(ConnectError)
    expect((error as ConnectError).code).toBe(Code.Aborted)
    expect((error as ConnectError).rawMessage).toBe('setup failed')
    expect((error as ConnectError).findDetails(PartialOutcomeSchema)[0]?.itemId).toBe('item')
  })

  it('resolves with the finished job', async () => {
    report([], true)
    const waited = awaitJob(job('j5', JobState.QUEUED))
    report([job('j5', JobState.SUCCEEDED)])
    expect((await waited)?.id).toBe('j5')
  })
})
