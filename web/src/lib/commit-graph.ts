import type { Commit } from './review'

export type GraphEdge = { from: number; to: number; start: number; end: number; boundary: boolean }
export type GraphRow = { commit: Commit; column: number; edges: GraphEdge[] }
export const graphRowHeight = 76

// Git supplies topological order and actual parent IDs. Lanes track pending
// parents, so forks and merges share nodes rather than duplicating histories.
export function layoutCommitGraph(commits: readonly Commit[]) {
  let lanes: string[] = []
  let columns = 1
  const shown = new Set(commits.map(commit => commit.sha))
  const rows: GraphRow[] = []
  for (const commit of commits) {
    const before = [...lanes]
    const existing = before.indexOf(commit.sha)
    const column = existing < 0 ? before.length : existing
    if (existing < 0) lanes.push(commit.sha)
    const after = [...lanes]
    after.splice(column, 1)
    const parents = [...new Set(commit.parent_shas ?? [])]
    parents.forEach((parent, i) => {
      if (!after.includes(parent)) {
        if (i === 0) after.splice(Math.min(column, after.length), 0, parent)
        else after.push(parent)
      }
    })
    const edges: GraphEdge[] = []
    before.forEach((sha, from) => {
      if (sha === commit.sha) edges.push({ from, to: column, start: 0, end: 38, boundary: false })
      else edges.push({ from, to: after.indexOf(sha), start: 0, end: graphRowHeight, boundary: !shown.has(sha) })
    })
    parents.forEach(parent => edges.push({ from: column, to: after.indexOf(parent), start: 38, end: graphRowHeight, boundary: !shown.has(parent) }))
    columns = Math.max(columns, column + 1, before.length, after.length)
    rows.push({ commit, column, edges })
    lanes = after
  }
  return { rows, columns }
}

export const graphX = (column: number) => 14 + column * 18
export function graphEdgePath(edge: GraphEdge) {
  const x = graphX(edge.from)
  const to = graphX(edge.to)
  if (x === to) return `M${x} ${edge.start}V${edge.end}`
  return `M${x} ${edge.start}L${x} 38C${x} 60 ${to} 60 ${to} ${edge.end}`
}
