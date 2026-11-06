import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { create } from '@bufbuild/protobuf'
import { JobSchema, JobState } from '@/gen/wi/core/v1/jobs_pb'
import { Markdown } from './markdown'
import { RunContext, commandOf, runStatus } from './runnable-code'

// Radix's dialog and jsdom.
Element.prototype.scrollIntoView ??= () => {}

const calls: { method: string; request: unknown }[] = []
const running = create(JobSchema, { id: 'job-1', kind: 'run_command', state: JobState.RUNNING, logTail: 'building\n' })
const done = create(JobSchema, { id: 'job-1', kind: 'run_command', state: JobState.SUCCEEDED, logTail: 'building\nok\n', result: { case: 'command', value: { exitCode: 2 } } })
vi.mock('@/lib/rpc', async original => ({
  ...(await original<object>()),
  workspaces: {
    runCommand: async (request: unknown) => {
      calls.push({ method: 'runCommand', request })
      return { job: running }
    },
  },
  jobs: {
    watchJob: async function* () {
      yield { job: done }
    },
    cancelJob: async () => ({}),
  },
}))

afterEach(() => {
  cleanup()
  calls.length = 0
})

const reply = 'Run this:\n\n```bash\nmake test\n```\n\nand this:\n\n```go\nfmt.Println()\n```\n'

it('reads the command a snippet runs', () => {
  expect(commandOf('bash', 'make test\n')).toBe('make test')
  expect(commandOf('console', '$ make test\nok\n$ make lint\n')).toBe('make test\nmake lint')
})

it('says how a run ended', () => {
  expect(runStatus({ job: running })).toMatchObject({ text: 'Running…', running: true })
  expect(runStatus({ job: done })).toMatchObject({ text: 'Exited with status 2', failed: true })
  expect(runStatus({ job: create(JobSchema, { state: JobState.CANCELLED }) }).text).toBe('Stopped')
})

it('offers to run shell snippets only in an agent reply in an item', () => {
  render(<Markdown text={reply} />)
  expect(screen.queryByRole('button', { name: 'Run' })).toBeNull()
  cleanup()
  render(
    <RunContext.Provider value="item-1">
      <Markdown text={reply} />
    </RunContext.Provider>,
  )
  expect(screen.getAllByRole('button', { name: 'Run' })).toHaveLength(1)
})

it('shows the command before running it, then its output and how it ended', async () => {
  render(
    <RunContext.Provider value="item-1">
      <Markdown text={reply} />
    </RunContext.Provider>,
  )
  fireEvent.click(screen.getByRole('button', { name: 'Run' }))
  expect(screen.getByLabelText('Command').textContent).toBe('make test')
  expect(calls).toHaveLength(0)
  fireEvent.click(screen.getAllByRole('button', { name: /Run/ }).at(-1)!)
  await waitFor(() => expect(screen.getByText('Exited with status 2')).toBeTruthy())
  expect(screen.getByLabelText('Command output').textContent).toContain('ok')
  expect(calls).toEqual([{ method: 'runCommand', request: { itemId: 'item-1', command: 'make test' } }])
})
