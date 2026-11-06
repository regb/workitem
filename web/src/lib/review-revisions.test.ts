import { expect, it } from 'vitest'
import { itemRevisions } from './review-revisions'
import type { Commits } from './review'

it('lists actual item revisions oldest-first without unrelated upstream commits', () => {
  const history: Commits = {
    branches: [{ kind: 'item', name: 'wi/task', sha: 'tip' }],
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
