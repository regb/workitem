import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Schedule } from '@/lib/api'

const calls: Array<{ path: string; body: unknown }> = []
const schedule: Schedule = {
  id: '01M7SCHED0000000000000000A',
  revision: 3,
  repository_id: 'repo',
  cron: '0 9 * * 1-5',
  prompt: 'Triage CI failures',
  agent_profile: 'claude',
  labels: ['ci'],
  enabled: true,
  next_run_at: '2026-11-20T08:00:00Z',
}
vi.mock('@/lib/api', async original => ({
  ...(await original<object>()),
  api: async (path: string, body: unknown) => {
    calls.push({ path, body })
    return path.endsWith('/run') ? { result: { item_id: 'new-item' } } : { result: body }
  },
}))
vi.mock('@/lib/queries', async original => ({
  ...(await original<object>()),
  useSchedules: () => ({ data: [schedule] }),
  useRepositories: () => ({ data: [{ id: 'repo', name: 'workitem' }] }),
  useAgentProfiles: () => ({ data: { revision: 1, default: 'claude', profiles: [{ name: 'claude', driver: 'claude', label: 'Claude', users: 0 }] } }),
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
    expect(screen.getByText('workitem · Claude')).toBeTruthy()
    fireEvent.click(screen.getByRole('switch', { name: 'Disable schedule' }))
    await waitFor(() => expect(calls).toEqual([{ path: '/api/schedules', body: { ...schedule, enabled: false } }]))
  })

  it('runs and removes a schedule', async () => {
    const { ScheduleSettings } = await import('./schedule-settings')
    render(<ScheduleSettings />)
    fireEvent.click(screen.getByRole('button', { name: 'Run now' }))
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Started a new item'))
    fireEvent.click(screen.getByRole('button', { name: 'Remove schedule' }))
    await waitFor(() => expect(calls.map(call => call.path)).toEqual([`/api/schedules/${schedule.id}/run`, `/api/schedules/${schedule.id}/delete`]))
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
          path: '/api/schedules',
          body: { repository_id: 'repo', cron: '@daily', agent_profile: 'claude', title: '', prompt: 'Summarize yesterday for {{repo}}', labels: ['daily', 'report'], enabled: true },
        },
      ]),
    )
  })
})

describe('parseLabels', () => {
  it('splits on commas and spaces', async () => {
    const { parseLabels } = await import('./schedule-settings')
    expect(parseLabels(' a, b  c,,')).toEqual(['a', 'b', 'c'])
  })
})
