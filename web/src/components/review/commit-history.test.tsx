import { BranchKind } from '@/gen/wi/core/v1/reviews_pb'
import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { CommitHistory } from './commit-history'
import type { Commits } from '@/lib/review'
afterEach(cleanup)

it('labels only the provided history branches and opens any selected context commit', () => {
  const history: Commits = {
    commits: [
      { sha: 'remote123', subject: 'Remote work', parent_shas: ['shared12'], author: 'A', authored_at: '2026-11-05' },
      { sha: 'shared12', subject: 'Shared history', author: 'A', authored_at: '2026-11-04' },
    ],
    branches: [
      { kind: BranchKind.REMOTE, name: 'upstream/main', sha: 'remote123' },
      { kind: BranchKind.INTEGRATION, name: 'main', sha: 'shared12' },
      { kind: BranchKind.ITEM, name: 'wi/task', sha: 'shared12' },
    ],
  }
  const select = vi.fn()
  const { container } = render(<CommitHistory history={history} onSelect={select} />)
  expect(screen.getAllByText('upstream/main')).toHaveLength(2)
  expect(screen.getAllByText('wi/task')).toHaveLength(2)
  expect(container.querySelectorAll('[data-commit-sha]')).toHaveLength(2)
  expect(container.querySelectorAll('circle')).toHaveLength(2)
  fireEvent.click(screen.getByRole('button', { name: 'View commit remote12: Remote work' }))
  expect(select).toHaveBeenCalledWith('remote123')
  expect(screen.getByText(/Opening history never fetches/)).toBeTruthy()
})

it('shows missing upstream and bounded-history information', () => {
  render(<CommitHistory history={{ warnings: ['No upstream is configured.'], truncated: true }} onSelect={vi.fn()} />)
  expect(screen.getByText('No upstream is configured.')).toBeTruthy()
  expect(screen.getByText(/Showing the latest 200 commits/)).toBeTruthy()
})
