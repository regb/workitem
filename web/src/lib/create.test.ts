import { Code, ConnectError } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { startWork } from './create'
import { calls, resetRPC, rpcReplies } from './rpc-mock'

vi.mock('./rpc', async original => (await import('./rpc-mock')).mockRPC(await original()))
vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn(), error: vi.fn() }) }))

describe('startWork', () => {
  beforeEach(resetRPC)

  it('retries a request with its key, so the daemon cannot create a second item', async () => {
    const { ui } = await import('./ui-state')
    let lost = true
    rpcReplies.set('lifecycle.createAndStartItem', () => {
      if (lost) {
        lost = false
        throw new ConnectError('connection reset', Code.Unavailable)
      }
      return { item: { id: 'item', title: 'Fix the parser' } }
    })
    const request = { project_id: 'repo', agent: 'claude', prompt: 'Fix the parser' }
    expect((await startWork(request, () => {})).ok).toBe(false)
    // A lost response leaves the creation unknown.
    expect(ui.get().creationUnknown).toBe(true)
    ui.set(state => ({ ...state, creationUnknown: false }))
    expect((await startWork(request, () => {})).ok).toBe(true)
    // Once created, the same text is new work with a new key.
    await startWork(request, () => {})
    const keys = calls('lifecycle.createAndStartItem').map(r => (r.item as { requestId: string }).requestId)
    expect(keys[1]).toBe(keys[0])
    expect(keys[2]).not.toBe(keys[0])
  })

  it('reports a refusal as certain, and the item a partial start kept', async () => {
    const { ui } = await import('./ui-state')
    const { PartialOutcomeSchema } = await import('@/gen/wi/core/v1/common_pb')
    const { create } = await import('@bufbuild/protobuf')
    rpcReplies.set('lifecycle.createAndStartItem', () => {
      throw new ConnectError('setup failed', Code.FailedPrecondition, undefined, [{ desc: PartialOutcomeSchema, value: create(PartialOutcomeSchema, { itemId: 'kept', completed: ['created item'] }) }])
    })
    const out = await startWork({ project_id: 'repo', agent: 'claude', prompt: 'Other task' }, () => {})
    expect(out).toEqual({ ok: false, retained: 'kept' })
    expect(ui.get().creationUnknown).toBe(false)
  })
})
