import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import type { Schedule } from '@/gen/wi/core/v1/schedules_pb'

const calls: Array<{ method: string; request: unknown }> = []
const schedule = {
  id: '01M7SCHED0000000000000000A',
  revision: 3,
  repositoryId: 'repo',
  cron: '0 9 * * 1-5',
  title: '',
  prompt: 'Triage CI failures',
  agentProfile: 'claude',
  agentSettings: { model: 'opus' },
  labels: ['ci'],
  enabled: true,
  nextRunAt: timestampFromDate(new Date('2026-11-20T08:00:00Z')),
  lastItemId: '',
  lastError: '',
} as unknown as Schedule
vi.mock('@/lib/rpc', async original => ({
  ...(await original<object>()),
  schedules: new Proxy(
    {},
    {
      get: (_, method: string) => async (request: unknown) => {
        calls.push({ method, request })
        return method === 'runSchedule' ? { itemId: 'new-item' } : {}
      },
    },
  ),
}))
vi.mock('@/lib/queries', async original => ({
  ...(await original<object>()),
  useSchedules: () => ({ data: [schedule] }),
  useRepositories: () => ({ data: [{ id: 'repo', name: 'workitem' }] }),
  useAgentProfiles: () => ({ data: { revision: 1, defaultProfile: 'claude', profiles: [{ name: 'claude', driver: 'claude', label: 'Claude', users: 0 }] } }),
  useProfileModels: () => ({
    data: {
      agent: 'claude',
      values: { model: 'opus' },
      overrides: {},
      live: [],
      runtime_online: false,
      catalog: { models: [{ value: 'opus', label: 'Opus', reasoning: true }, { value: 'sonnet', label: 'Sonnet', reasoning: true }], reasoning_levels: ['low', 'high'], default_reasoning: 'low' },
    },
  }),
}))
vi.mock('@tanstack/react-router', () => ({ Link: ({ children }: { children: unknown }) => children }))
vi.mock('@/components/confirm', () => ({ useConfirm: () => async () => true }))

beforeEach(() => (calls.length = 0))
afterEach(cleanup)

describe('ScheduleSettings', () => {
  it('lists schedules with a toggle that saves the change', async () => {
    const { ScheduleSettings } = await import('./schedule-settings')
    render(<ScheduleSettings />)
    expect(screen.getByText('Title generated from the prompt')).toBeTruthy()
    expect(screen.getByText('workitem · Claude · opus')).toBeTruthy()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable schedule' }))
    await waitFor(() => expect(calls).toEqual([{ method: 'putSchedule', request: { schedule: { id: schedule.id, repositoryId: 'repo', cron: schedule.cron, agentProfile: 'claude', agentSettings: { model: 'opus' }, title: '', prompt: schedule.prompt, labels: ['ci'], enabled: false }, expectedRevision: 3 } }]))
  })

  it('runs and removes a schedule', async () => {
    const { ScheduleSettings } = await import('./schedule-settings')
    render(<ScheduleSettings />)
    fireEvent.click(screen.getByRole('button', { name: 'Run now' }))
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Started a new item'))
    fireEvent.click(screen.getByRole('button', { name: 'Remove schedule' }))
    await waitFor(() => expect(calls).toEqual([{ method: 'runSchedule', request: { id: schedule.id } }, { method: 'deleteSchedule', request: { id: schedule.id } }]))
  })

  it('adds a schedule with the default agent, an optional title and parsed labels', async () => {
    const { ScheduleSettings } = await import('./schedule-settings')
    render(<ScheduleSettings />)
    fireEvent.click(screen.getByRole('button', { name: /Add schedule/ }))
    const save = screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement
    expect(save.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('Cron expression'), { target: { value: ' @daily ' } })
    fireEvent.change(screen.getByLabelText('Prompt'), { target: { value: 'Summarize yesterday for {{repo}}' } })
    fireEvent.change(screen.getByLabelText('Labels'), { target: { value: 'daily, report' } })
    fireEvent.click(save)
    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: 'putSchedule',
          request: { schedule: { id: '', repositoryId: 'repo', cron: '@daily', timeZone: '', agentProfile: 'claude', agentSettings: {}, title: '', prompt: 'Summarize yesterday for {{repo}}', labels: ['daily', 'report'], enabled: true }, expectedRevision: 0 },
        },
      ]),
    )
  })
})

describe('ScheduleForm', () => {
  it('chooses the model and thinking level as new work does, saving only what differs from the profile', async () => {
    const { ScheduleSettings } = await import('./schedule-settings')
    render(<ScheduleSettings />)
    fireEvent.click(screen.getByRole('button', { name: 'Edit schedule' }))
    fireEvent.click(screen.getByRole('button', { name: /^Agent: Claude/ }))
    fireEvent.click(screen.getByRole('option', { name: 'Sonnet' }))
    fireEvent.click(screen.getByRole('radio', { name: 'high' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(calls).toEqual([{ method: 'putSchedule', request: expect.objectContaining({ schedule: expect.objectContaining({ agentProfile: 'claude', agentSettings: { model: 'sonnet', reasoning: 'high' } }) }) }]))
  })
})

describe('parseLabels', () => {
  it('splits on commas and spaces', async () => {
    const { parseLabels } = await import('./schedule-settings')
    expect(parseLabels(' a, b  c,,')).toEqual(['a', 'b', 'c'])
  })
})
