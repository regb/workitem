import { create } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ControlStatusSchema } from '@/gen/wi/core/v1/agents_pb'
import { AgentMode } from '@/gen/wi/core/v1/common_pb'
import { BlockerKind } from '@/gen/wi/core/v1/items_pb'
import { calls, resetRPC, rpcCalls, rpcReplies } from './rpc-mock'

vi.mock('./rpc', async original => (await import('./rpc-mock')).mockRPC(await original()))
vi.mock('./queries', () => ({ refreshChat: async () => {}, refreshList: async () => {}, refreshSettings: async () => {} }))

beforeEach(resetRPC)

describe('starting a backlog item', () => {
  it('starts it once, headless, under one message ID', async () => {
    const { startItem } = await import('./actions')
    expect(await startItem('item')).toBe(true)
    expect(calls('lifecycle.startItem')).toEqual([{ itemId: 'item', start: { mode: AgentMode.HEADLESS, clientMessageId: expect.any(String) } }])
  })

  it('reports a refused start', async () => {
    const { startItem } = await import('./actions')
    const { ui } = await import('./ui-state')
    rpcReplies.set('lifecycle.startItem', () => {
      throw new ConnectError('No capacity.', Code.ResourceExhausted)
    })
    expect(await startItem('item')).toBe(false)
    expect(rpcCalls).toHaveLength(1)
    expect(ui.get().actionError).toBe('No capacity.')
  })
})

describe('waiting', () => {
  it('returns at once and keeps the item out of attention until the list is re-read', async () => {
    const { waitItem } = await import('./actions')
    const { ui } = await import('./ui-state')
    let finish!: () => void
    rpcReplies.set('items.addBlocker', () => new Promise(resolve => (finish = () => resolve({}))))
    waitItem('item', 'Item', { kind: 'time', until: '2026-12-01T10:00:00Z', follow_up: 'check again' })
    expect(ui.get().waiting.has('item')).toBe(true)
    const [request] = calls('items.addBlocker')
    expect(request).toMatchObject({ itemId: 'item', kind: BlockerKind.TIME, followUp: 'check again', blockingItemId: '', event: '' })
    expect((request.until as { seconds: bigint }).seconds).toBe(BigInt(Date.parse('2026-12-01T10:00:00Z') / 1000))
    finish()
    await vi.waitFor(() => expect(ui.get().waiting.has('item')).toBe(false))
  })
})

describe('stopping a turn', () => {
  it('asks before taking over from another controller, then stops', async () => {
    const { stopTurn } = await import('./actions')
    // The daemon's refusal carries the control status, and the plan to confirm.
    const status = create(ControlStatusSchema, { plan: { planToken: 'observed-plan', needsConfirmation: true, reasons: ['the terminal becomes read-only'] } })
    let refusals = 1
    rpcReplies.set('agents.abortTurn', () => {
      if (refusals-- > 0) throw new ConnectError('caller does not hold the control lease', Code.PermissionDenied, undefined, [{ desc: ControlStatusSchema, value: status }])
      return {}
    })
    const confirm = vi.fn(async () => true)
    expect(await stopTurn(confirm, 'item')).toBe(true)
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ points: ['the terminal becomes read-only'] }))
    expect(rpcCalls.map(call => call.call)).toEqual(['agents.abortTurn', 'control.acquireControl', 'agents.abortTurn'])
    // Stopping takes control without starting an agent.
    expect(calls('control.acquireControl')).toEqual([expect.objectContaining({ itemId: 'item', confirm: true, planToken: 'observed-plan', startAgent: false })])
  })

  it('does not take over when the operator declines', async () => {
    const { stopTurn } = await import('./actions')
    const status = create(ControlStatusSchema, { plan: { planToken: 'p', needsConfirmation: true } })
    rpcReplies.set('agents.abortTurn', () => {
      throw new ConnectError('refused', Code.PermissionDenied, undefined, [{ desc: ControlStatusSchema, value: status }])
    })
    expect(await stopTurn(async () => false, 'item')).toBe(false)
    expect(calls('control.acquireControl')).toEqual([])
  })
})

describe('holding control', () => {
  it('renews the lease, and reads the conversation again once it is lost', async () => {
    const queries = await import('./queries')
    const refresh = vi.spyOn(queries, 'refreshChat')
    const { renewControl } = await import('./actions')
    await renewControl('item', 'lease-1')
    expect(calls('control.renewControl')).toEqual([{ leaseId: 'lease-1' }])
    expect(refresh).not.toHaveBeenCalled()
    rpcReplies.set('control.renewControl', () => {
      throw new ConnectError('control lease changed or expired', Code.Aborted)
    })
    await renewControl('item', 'lease-1')
    expect(refresh).toHaveBeenCalledWith('item')
  })
})
