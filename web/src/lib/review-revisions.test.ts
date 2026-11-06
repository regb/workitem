import { BranchKind } from '@/gen/wi/core/v1/reviews_pb'
import { expect, it } from 'vitest'
import { itemRevisions, mergeBase } from './review-revisions'
import type { Commits } from './review'

it('lists actual item revisions oldest-first without unrelated upstream commits', () => {
  const history: Commits = {
    branches: [{ kind: BranchKind.ITEM, name: 'wi/task', sha: 'tip' }],
    commits: [
      { sha: 'remote', parent_shas: ['start'], subject: '', author: '', authored_at: '' },
      { sha: 'tip', parent_shas: ['first'], subject: '', author: '', authored_at: '' },
      { sha: 'first', parent_shas: ['start'], subject: '', author: '', authored_at: '' },
      { sha: 'start', parent_shas: ['older'], subject: '', author: '', authored_at: '' },
      { sha: 'older', subject: '', author: '', authored_at: '' },
    ],
  }
  expect(itemRevisions(history, 'start').map(commit => commit.sha)).toEqual(['first', 'tip'])
  expect(itemRevisions(undefined, '')).toEqual([])
})

it('leaves out commits the integration branch already has, and finds where the item left it', () => {
  const history: Commits = {
    branches: [
      { kind: BranchKind.ITEM, name: 'wi/task', sha: 'tip' },
      { kind: BranchKind.INTEGRATION, name: 'main', sha: 'main' },
    ],
    commits: [
      { sha: 'tip', parent_shas: ['merged'], subject: '', author: '', authored_at: '' },
      { sha: 'main', parent_shas: ['merged'], subject: '', author: '', authored_at: '' },
      { sha: 'merged', parent_shas: ['start'], subject: '', author: '', authored_at: '' },
      { sha: 'start', subject: '', author: '', authored_at: '' },
    ],
  }
  expect(itemRevisions(history, 'start').map(commit => commit.sha)).toEqual(['tip'])
  expect(mergeBase(history)).toBe('merged')
  expect(mergeBase({ ...history, branches: history.branches!.slice(0, 1) })).toBeUndefined()
})
