import type { Commit, Commits } from './review'

// Actual item commits, not Gerrit patch-set numbers. The bounded graph may
// also contain integration/upstream commits that do not belong in this strip.
export function itemRevisions(history: Commits | undefined, start: string): Commit[] {
  const commits = history?.commits ?? []
  const tip = history?.branches?.find(branch => branch.kind === 'item')?.sha
  if (!tip) return []
  const bySHA = new Map(commits.map(commit => [commit.sha, commit]))
  const seen = new Set<string>()
  const pending = [tip]
  while (pending.length) {
    const sha = pending.pop()!
    if (sha === start || seen.has(sha)) continue
    seen.add(sha)
    pending.push(...(bySHA.get(sha)?.parent_shas ?? []))
  }
  return commits
    .filter(commit => seen.has(commit.sha))
    .slice(0, 30)
    .reverse()
}
