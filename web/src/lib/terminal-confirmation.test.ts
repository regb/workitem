import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Resolution } from '@/gen/wi/core/v1/common_pb'
import { calls, resetRPC, rpcReplies } from './rpc-mock'

vi.mock('./rpc', async original => (await import('./rpc-mock')).mockRPC(await original()))
vi.mock('./queries', () => ({ refreshChat: vi.fn(), refreshList: vi.fn() }))
vi.mock('sonner', () => ({
  toast: {
    loading: vi.fn(),
    success: vi.fn(),
    dismiss: vi.fn(),
    error: vi.fn(),
  },
}))

import { closeItem, shelveItem, type ClosePlan } from './actions'

// planned answers the daemon's close plan; attached, the agent's attached
// terminal clients, which shelving reads.
const planned = (change: Partial<ClosePlan> = {}) =>
  rpcReplies.set('lifecycle.planClose', () => ({ abortsTurn: false, commits: [], removesAttachedCopies: [], attachedTerminalClients: 0, checkoutProblem: '', ...change }))
const attached = (clients: number) => rpcReplies.set('agents.getAgent', () => ({ agent: { terminal: { attachedClients: clients } } }))
const closes = () => calls('lifecycle.closeItem')
const shelves = () => calls('lifecycle.shelveItem')

beforeEach(() => {
  resetRPC()
  planned({ attachedTerminalClients: 1 })
  attached(1)
})

describe('closing from the daemon plan', () => {
  it('closes attachment copies alone without asking or committing', async () => {
    planned({ removesAttachedCopies: ['ACCESS_DESIGN.md'] })
    const confirm = vi.fn()
    expect(await closeItem(confirm, 'i', 'Item')).toBe(true)
    expect(confirm).not.toHaveBeenCalled()
    expect(closes()).toEqual([{ itemId: 'i', resolution: Resolution.COMPLETED, force: false, commitChanges: false, confirmAttachedTerminal: false }])
  })
  it('asks before committing real changes, and closes nothing when declined', async () => {
    planned({ commits: ['changed.ts'] })
    const confirm = vi.fn().mockResolvedValue(false)
    expect(await closeItem(confirm, 'i', 'Item')).toBe(false)
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ action: 'Commit and close', points: [expect.stringContaining('changed.ts')] }))
    expect(closes()).toEqual([])
  })
  it('does not act when the plan cannot be read', async () => {
    rpcReplies.set('lifecycle.planClose', () => {
      throw new Error('Cannot inspect terminal')
    })
    expect(await closeItem(vi.fn(), 'i', 'Item')).toBe(false)
    expect(closes()).toEqual([])
  })
})

describe('attached terminal confirmation', () => {
  it('does not close or shelve when cancelled', async () => {
    const confirm = vi.fn().mockResolvedValue(false)
    expect(await closeItem(confirm, 'i', 'Item')).toBe(false)
    expect(await shelveItem(confirm, 'i', 'Item')).toBe(false)
    expect([...closes(), ...shelves()]).toEqual([])
    expect(confirm).toHaveBeenLastCalledWith(expect.objectContaining({ points: [expect.stringContaining('terminal is attached')] }))
  })
  it('sends explicit consent only after confirmation', async () => {
    const confirm = vi.fn().mockResolvedValue(true)
    await closeItem(confirm, 'i', 'Item')
    await shelveItem(confirm, 'i', 'Item')
    expect(closes()).toEqual([expect.objectContaining({ confirmAttachedTerminal: true })])
    expect(shelves()).toEqual([{ itemId: 'i', confirmAttachedTerminal: true }])
  })
  it('does not shelve when terminal inspection fails', async () => {
    rpcReplies.set('agents.getAgent', () => {
      throw new Error('Cannot inspect terminal')
    })
    expect(await shelveItem(vi.fn(), 'i', 'Item')).toBe(false)
    expect(shelves()).toEqual([])
  })
  it('does not authorize a newly attached terminal based on an empty plan', async () => {
    planned()
    await closeItem(vi.fn(), 'i', 'Item')
    expect(closes()).toEqual([expect.objectContaining({ confirmAttachedTerminal: false })])
  })
})
