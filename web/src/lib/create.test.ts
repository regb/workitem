import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from './api'
import { startWork } from './create'

vi.mock('./api', async original => ({ ...(await original<typeof import('./api')>()), api: vi.fn() }))
vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn() }) }))

describe('startWork', () => {
  afterEach(() => vi.mocked(api).mockReset())

  it('retries a request with its key, so the daemon cannot create a second item', async () => {
    const sent: string[] = []
    vi.mocked(api).mockImplementation(async (_path, body) => {
      sent.push((body as { request_id: string }).request_id)
      if (sent.length === 1) throw new ApiError('lost', { creation_uncertain: true })
      return { id: 'item', warnings: [] }
    })
    const request = { project_id: 'repo', agent: 'claude', prompt: 'Fix the parser' }
    expect((await startWork(request, () => {})).ok).toBe(false)
    expect((await startWork(request, () => {})).ok).toBe(true)
    // Once created, the same text is new work with a new key.
    await startWork(request, () => {})
    expect(sent[1]).toBe(sent[0])
    expect(sent[2]).not.toBe(sent[0])
  })
})
