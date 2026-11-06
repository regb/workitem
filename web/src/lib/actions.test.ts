import { describe, expect, it } from 'vitest'
import { closeCheck, type ClosePlan } from './actions'

const plan = (change: Partial<ClosePlan> = {}): ClosePlan => ({ abortsTurn: false, commits: [], removesAttachedCopies: [], attachedTerminalClients: 0, checkoutProblem: '', ...change })

describe('closeCheck', () => {
  it('closes a safe item without asking', () => {
    expect(closeCheck(plan())).toEqual({ warn: [], force: false, commit: false, terminal: false })
  })

  it('asks before stopping a busy agent and then forces the stop', () => {
    const check = closeCheck(plan({ abortsTurn: true }))
    expect(check.force).toBe(true)
    expect(check.warn).toHaveLength(1)
  })

  it('commits only real changes and names them', () => {
    expect(closeCheck(plan({ removesAttachedCopies: ['DESIGN.md'] }))).toEqual({ warn: [], force: false, commit: false, terminal: false })
    const check = closeCheck(plan({ commits: ['a.go', 'b.go', 'c.go', 'd.go'], removesAttachedCopies: ['DESIGN.md'] }))
    expect(check.commit).toBe(true)
    expect(check.warn[0]).toMatch(/\(a\.go, b\.go, c\.go and 1 more\): they are committed to the item's branch/)
    expect(check.warn[1]).toBe('DESIGN.md is removed from the checkout, unchanged since attached: the item keeps it.')
  })

  it('warns about a checkout problem and an attached terminal', () => {
    const check = closeCheck(plan({ checkoutProblem: 'the checkout is on another branch', attachedTerminalClients: 1 }))
    expect(check.terminal).toBe(true)
    expect(check.warn).toEqual([expect.stringContaining('terminal is attached'), expect.stringContaining('on another branch')])
  })
})
