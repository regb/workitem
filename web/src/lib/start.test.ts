import { describe, expect, it, vi } from 'vitest'

const calls: Array<{ action: string; body: Record<string, unknown> }> = []
const replies: Array<() => Promise<unknown>> = []

vi.mock('./api', async importOriginal => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, itemAction: (_id: string, action: string, body: Record<string, unknown>) => (calls.push({ action, body }), replies.shift()!()) }
})
vi.mock('./queries', () => ({ refreshChat: async () => {}, refreshList: async () => {}, refreshSettings: async () => {} }))

describe('starting a backlog item', () => {
  it('asks for an approval the server needs, then starts with the answer', async () => {
    const { ApiError } = await import('./api')
    const { startItem } = await import('./actions')
    replies.push(() => Promise.reject(new ApiError('Allow worktree .envrc?', { error: 'Allow worktree .envrc?', approval: { question: 'Allow worktree .envrc?', path: '/w/.envrc' } })))
    replies.push(async () => ({}))
    const choose = vi.fn(async () => 'primary' as const)
    expect(await startItem(choose, 'item')).toBe(true)
    expect(choose).toHaveBeenCalledWith(expect.objectContaining({ title: 'Allow worktree .envrc?', description: expect.stringContaining('/w/.envrc') }))
    expect(calls.map(call => [call.action, call.body.approval])).toEqual([
      ['start', undefined],
      ['start', 'allow'],
    ])
    // Both attempts are the same start.
    expect(calls[0].body.client_id).toBe(calls[1].body.client_id)
  })

  it('does not start when the question is dismissed', async () => {
    const { ApiError } = await import('./api')
    const { startItem } = await import('./actions')
    calls.length = 0
    replies.push(() => Promise.reject(new ApiError('Allow?', { error: 'Allow?', approval: { question: 'Allow?' } })))
    expect(await startItem(async () => null, 'item')).toBe(false)
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
    const control = { you: false, status: { holder: 'terminal' }, plan: { needs_confirmation: true, reasons: ['the terminal becomes read-only'] } }
    replies.push(() => Promise.reject(new ApiError('Another client controls this agent.', { error: 'Another client controls this agent.', control: control as never })))
    replies.push(async () => ({}))
    const confirm = vi.fn(async () => true)
    expect(await stopTurn(confirm, 'item')).toBe(true)
    expect(confirm).toHaveBeenCalledOnce()
    expect(calls.map(call => [call.action, call.body.confirm])).toEqual([
      ['stop', undefined],
      ['stop', true],
    ])
  })
})
