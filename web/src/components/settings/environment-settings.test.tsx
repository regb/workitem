import { describe, expect, it } from 'vitest'
import { parsePasted, staleAgents } from './environment-settings'

describe('parsePasted', () => {
  it('reads a pasted .env', () => {
    expect(parsePasted('# db\nexport DB_URL="postgres://x"\nPORT=8080\n\nNAME=\'a b\'\nbad line\n1X=no')).toEqual([
      { key: 'DB_URL', value: 'postgres://x' },
      { key: 'PORT', value: '8080' },
      { key: 'NAME', value: 'a b' },
    ])
  })
})

describe('staleAgents', () => {
  const agent = (id: string, stale: boolean, busy: boolean) => ({ id, title: id, started_at: '2026-11-01T00:00:00Z', stale, busy })
  it('restarts only idle agents that started before the change', () => {
    const picked = staleAgents([agent('a', true, false), agent('b', true, true), agent('c', false, false)])
    expect(picked.stale.map(a => a.id)).toEqual(['a', 'b'])
    expect(picked.idle.map(a => a.id)).toEqual(['a'])
    expect(picked.summary).toBe('2 running agents still use the previous variables.')
    expect(picked.action).toBe('Restart 1 idle')
    expect(staleAgents([agent('a', true, false)]).action).toBe('Restart it')
    expect(staleAgents([agent('c', false, false)]).stale).toEqual([])
  })
})
