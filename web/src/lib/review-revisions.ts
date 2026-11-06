import { BranchKind } from '@/gen/wi/core/v1/reviews_pb'
import type { Commit, Commits } from './review'

// reachable is every commit of the bounded graph reachable from sha.
function reachable(bySHA: Map<string, Commit>, sha: string | undefined): Set<string> {
  const seen = new Set<string>()
  const pending = sha ? [sha] : []
  while (pending.length) {
    const next = pending.pop()!
    if (seen.has(next)) continue
    seen.add(next)
    pending.push(...(bySHA.get(next)?.parent_shas ?? []))
  }
  return seen
}

// itemRevisions are the item's own commits, oldest first: on its branch,
// since it started, and not already on the integration branch. An item that
// merged continues from the merge result, so what it merged before, and the
// integration branch's other work since it started, are not its own.
export function itemRevisions(history: Commits | undefined, start: string): Commit[] {
  const commits = history?.commits ?? []
  const tip = history?.branches?.find(branch => branch.kind === BranchKind.ITEM)?.sha
  if (!tip) return []
  const bySHA = new Map(commits.map(commit => [commit.sha, commit]))
  const integrated = reachable(bySHA, history?.branches?.find(branch => branch.kind === BranchKind.INTEGRATION)?.sha)
  const seen = new Set<string>()
  const pending = [tip]
  while (pending.length) {
    const sha = pending.pop()!
    if (sha === start || seen.has(sha) || integrated.has(sha)) continue
    seen.add(sha)
    pending.push(...(bySHA.get(sha)?.parent_shas ?? []))
  }
  return commits
    .filter(commit => seen.has(commit.sha))
    .slice(0, 30)
    .reverse()
}

// mergeBase is where the item's branch leaves the integration branch: the
// newest commit of the item's branch the integration branch also has, when
// the bounded graph shows it.
export function mergeBase(history: Commits | undefined): string | undefined {
  const commits = history?.commits ?? []
  const tip = history?.branches?.find(branch => branch.kind === BranchKind.ITEM)?.sha
  const main = history?.branches?.find(branch => branch.kind === BranchKind.INTEGRATION)?.sha
  if (!tip || !main) return undefined
  const bySHA = new Map(commits.map(commit => [commit.sha, commit]))
  const integrated = reachable(bySHA, main)
  // Breadth first from the tip finds the nearest shared commit.
  const queue = [tip]
  const seen = new Set<string>()
  while (queue.length) {
    const sha = queue.shift()!
    if (seen.has(sha)) continue
    seen.add(sha)
    if (integrated.has(sha)) return sha
    queue.push(...(bySHA.get(sha)?.parent_shas ?? []))
  }
  return undefined
}
