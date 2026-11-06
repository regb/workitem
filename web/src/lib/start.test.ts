import { describe, expect, it, vi } from 'vitest'

const calls: Array<{ action: string; body: Record<string, unknown> }> = []
const replies: Array<() => Promise<unknown>> = []

vi.mock('./api', async importOriginal => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, itemAction: (_id: string, action: string, body: Record<string, unknown>) => (calls.push({ action, body }), replies.shift()!()) }
})
vi.mock('./queries', () => ({ refreshChat: async () => {}, refreshList: async () => {}, refreshSettings: async () => {} }))

describe('starting a backlog item', () => {
  it('starts it once, under one client ID', async () => {
    const { startItem } = await import('./actions')
    calls.length = 0
    replies.push(async () => ({}))
    expect(await startItem('item')).toBe(true)
    expect(calls).toEqual([{ action: 'start', body: { client_id: expect.any(String) } }])
  })

  it('reports a refused start', async () => {
    const { ApiError } = await import('./api')
    const { startItem } = await import('./actions')
    calls.length = 0
    replies.push(() => Promise.reject(new ApiError('No capacity.', { error: 'No capacity.' })))
    expect(await startItem('item')).toBe(false)
    expect(calls).toHaveLength(1)
  })
})

describe('waiting', () => {
  it('returns at once and keeps the item out of attention until the list is re-read', async () => {
    const { waitItem } = await import('./actions')
    const { ui } = await import('./ui-state')
    calls.length = 0
    let finish!: () => void
    replies.push(() => new Promise(resolve => (finish = () => resolve({}))))
    waitItem('item', 'Item', { kind: 'time', until: '1h' })
    expect(ui.get().waiting.has('item')).toBe(true)
    expect(calls).toEqual([{ action: 'blockers', body: { kind: 'time', until: '1h' } }])
    finish()
    await vi.waitFor(() => expect(ui.get().waiting.has('item')).toBe(false))
  })
})

describe('stopping a turn', () => {
  it('asks before taking over from another controller, then stops', async () => {
    const { ApiError } = await import('./api')
    const { stopTurn } = await import('./actions')
    calls.length = 0
    const control = { you: false, status: { holder: 'terminal' }, plan: { plan_token: 'observed-plan', needs_confirmation: true, reasons: ['the terminal becomes read-only'] } }
    replies.push(() => Promise.reject(new ApiError('Another client controls this agent.', { error: 'Another client controls this agent.', control: control as never })))
    replies.push(async () => ({}))
    const confirm = vi.fn(async () => true)
    expect(await stopTurn(confirm, 'item')).toBe(true)
    expect(confirm).toHaveBeenCalledOnce()
    expect(calls[1].body.plan_token).toBe('observed-plan')
    expect(calls.map(call => [call.action, call.body.confirm])).toEqual([
      ['stop', undefined],
      ['stop', true],
    ])
  })
})
