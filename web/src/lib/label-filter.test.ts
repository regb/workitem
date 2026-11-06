import { afterEach, describe, expect, it, vi } from 'vitest'
import { calls, rpcReplies } from './rpc-mock'
import { labelRules, normalizeLabel, parseLabelFilter, readLabelFilter, saveLabelFilter } from './label-filter'

vi.mock('./rpc', async original => (await import('./rpc-mock')).mockRPC(await original()))
vi.mock('./repositories', () => ({ listProjects: async () => [] }))

afterEach(() => localStorage.clear())

const rules = (text: string) => parseLabelFilter(text).map(rule => rule.rule)

describe('the dashboard label filter', () => {
  it('reads labels as wi list reads WI_LIST_LABELS', () => {
    expect(rules(' team, -Personal ,+ops/oncall, ')).toEqual([
      { case: 'require', value: 'team' },
      { case: 'exclude', value: 'personal' },
      { case: 'require', value: 'ops/oncall' },
    ])
    // A later rule for a label wins, and ! drops what came before.
    expect(rules('team, -team')).toEqual([{ case: 'exclude', value: 'team' }])
    expect(rules('team, !, deep work')).toEqual([{ case: 'require', value: 'deep-work' }])
  })

  it('refuses what is not a label, keeping the saved filter', () => {
    expect(normalizeLabel('  Deep   Work ')).toBe('deep-work')
    expect(() => normalizeLabel('a*b')).toThrow(/unsafe character/)
    saveLabelFilter('team')
    expect(() => saveLabelFilter('team, -')).toThrow(/empty/)
    expect(readLabelFilter()).toBe('team')
    expect(labelRules().map(rule => rule.rule)).toEqual([{ case: 'require', value: 'team' }])
    saveLabelFilter('  ')
    expect(readLabelFilter()).toBe('')
    expect(labelRules()).toEqual([])
  })
})

describe('the dashboard', () => {
  it('reads items with the saved filter', async () => {
    saveLabelFilter('-personal')
    rpcReplies.set('items.listItems', () => ({ items: [], warnings: [] }))
    const { readWorkList } = await import('./items')
    await readWorkList()
    expect((calls('items.listItems')[0].labelRules as { rule: unknown }[]).map(rule => rule.rule)).toEqual([{ case: 'exclude', value: 'personal' }])
  })
})
