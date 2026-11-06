import { expect, it } from 'vitest'
import { baseOptions, baseValue, selectBase, selectTarget, targetOptions, targetValue, timeOf, type ChangeContext } from './change-selection'
import { defaultPanel, type Commits, type PanelState } from './review'

const start = 'a'.repeat(40),
  first = 'b'.repeat(40),
  tip = 'c'.repeat(40)
const history: Commits = {
  branches: [{ kind: 'item', name: 'wi/task', sha: tip }],
  commits: [
    { sha: tip, subject: 'Latest commit', author: '', authored_at: '', parent_shas: [first] },
    { sha: first, subject: 'First commit', author: '', authored_at: '', parent_shas: [start] },
    { sha: start, subject: 'Initial commit', author: '', authored_at: '' },
  ],
}
const revision = (id: string, trigger = 'settled') => ({ id, capture_id: id, commit: 'd'.repeat(40), tree: '', observed_head: tip, base_commit: start, trigger })
const context: ChangeContext = { start, hasCheckout: true, history, revisions: [revision('r1'), revision('r2', 'manual')], merges: [] }
const panel = (patch: Partial<PanelState> = {}): PanelState => ({ ...defaultPanel, ...patch })
const values = (options: { value: string }[]) => options.map(option => option.value)

it('lists live views, checkpoints newest first and the item commits as targets', () => {
  const options = targetOptions(panel(), context)
  expect(values(options.live)).toEqual(['working', 'tip'])
  expect(values(options.checkpoints)).toEqual(['rev:r2', 'rev:r1'])
  expect(values(options.commits)).toEqual([`commit:${first}`])
  expect(targetOptions(panel(), { ...context, hasCheckout: false }).live.map(option => option.label)).toEqual(['Branch tip'])
  expect(targetOptions(panel(), { ...context, merges: [{ revision_id: 'r1', commit: '', tree: '', target_ref: '', before: '', after: '' }] }).checkpoints[1].detail).toContain('merged')
})

it('selects a target while keeping the chosen base', () => {
  const base = panel({ baseCommit: start })
  expect(selectTarget(`commit:${first}`, base, start)).toMatchObject({ comparison: 'commit', commit: first, baseCommit: start })
  expect(selectTarget('working', panel({ baseCommit: tip }), start)).toMatchObject({ comparison: 'item', baseCommit: tip, checkpoint: undefined })
  expect(selectTarget('tip', base, start)).toMatchObject({ comparison: 'committed' })
  expect(selectTarget('rev:r2', base, start)).toEqual({ checkpoint: { revision: 'r2' }, path: '' })
  expect(targetValue(panel({ comparison: 'commit', commit: first }), context)).toBe(`commit:${first}`)
  expect(targetValue(panel({ comparison: 'commit', commit: tip }), context)).toBe('tip')
  expect(targetValue(panel({ checkpoint: { revision: 'r1' } }), context)).toBe('rev:r1')
})

it('does not turn a root commit empty-tree parent into an explicit base', () => {
  const root = panel({ comparison: 'commit', commit: start })
  expect(selectTarget('working', root, 'e'.repeat(40))).toMatchObject({ comparison: 'item', baseCommit: '' })
})

it('offers item start, parent, commits, branch tip and the integration base', () => {
  const onCommit = panel({ comparison: 'commit', commit: first, baseCommit: start })
  expect(values(baseOptions(onCommit, context, start))).toEqual(['start', 'parent', `commit:${tip}`, `commit:${first}`, 'integration'])
  expect(baseValue(onCommit, context, start)).toBe('start')
  expect(selectBase('parent', onCommit, context)).toEqual({ baseCommit: '', path: '' })
  expect(baseValue(panel({ comparison: 'commit', commit: first }), context, start)).toBe('parent')
  expect(selectBase('integration', onCommit, context)).toEqual({ comparison: 'integration', baseCommit: '', path: '' })
  expect(baseValue(panel({ comparison: 'integration' }), context, start)).toBe('integration')
  expect(selectBase(`commit:${tip}`, onCommit, context)).toEqual({ baseCommit: tip, path: '' })
})

it('can use a commit picked from History as the base for working files', () => {
  const remote = 'f'.repeat(40)
  const picked: ChangeContext = { ...context, history: { ...history, commits: [{ sha: remote, subject: 'Remote commit', author: '', authored_at: '', parent_shas: [start] }, ...history.commits!] } }
  const viewing = panel({ comparison: 'commit', commit: remote })
  expect(values(baseOptions(viewing, picked, start))).toContain(`commit:${remote}`)
  const based = { ...viewing, ...selectBase(`commit:${remote}`, viewing, picked) }
  expect({ ...based, ...selectTarget('working', based, start) }).toMatchObject({ comparison: 'item', baseCommit: remote })
})

it('compares a checkpoint to its own base or to another checkpoint', () => {
  const viewing = panel({ checkpoint: { revision: 'r2' } })
  expect(values(baseOptions(viewing, context, start))).toEqual(['capture', 'rev:r1'])
  expect(baseValue(viewing, context, start)).toBe('capture')
  expect(selectBase('rev:r1', viewing, context)).toEqual({ checkpoint: { revision: 'r2', fromRevision: 'r1' }, path: '' })
  expect(selectBase('capture', panel({ checkpoint: { revision: 'r2', fromRevision: 'r1' } }), context)).toEqual({ checkpoint: { revision: 'r2', fromRevision: undefined }, path: '' })
})

it('keeps an unlisted base selectable so the control shows it', () => {
  const other = '9'.repeat(40)
  expect(values(baseOptions(panel({ baseCommit: other }), context, start))).toContain(`commit:${other}`)
})

it('drops a checkpoint base when returning to the branch, since a snapshot is not on it', () => {
  const viewing = panel({ checkpoint: { revision: 'r2', fromRevision: 'r1' } })
  expect(selectTarget('tip', viewing, 'e'.repeat(40))).toMatchObject({ checkpoint: undefined, comparison: 'committed', baseCommit: '' })
  expect(selectTarget('working', viewing, 'e'.repeat(40))).toMatchObject({ checkpoint: undefined, comparison: 'item', baseCommit: '' })
})

it('reads timestamps sent as strings or as protobuf objects', () => {
  expect(timeOf('2026-11-19T15:00:00Z')?.toISOString()).toBe('2026-11-19T15:00:00.000Z')
  expect(timeOf({ seconds: '1795100400' })?.toISOString()).toBe(new Date(1795100400 * 1000).toISOString())
  expect(timeOf(undefined)).toBeUndefined()
  const dated = { ...context, revisions: [{ ...revision('r1'), created_at: { seconds: 1795100400 } }] }
  expect(targetOptions(panel(), dated).checkpoints[0].detail).not.toContain('Invalid')
})
