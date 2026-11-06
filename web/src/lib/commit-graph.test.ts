import { expect, it } from 'vitest'
import { graphEdgePath, layoutCommitGraph } from './commit-graph'
import type { Commit } from './review'
const commit = (sha: string, parents: string[] = []): Commit => ({ sha, parent_shas: parents, subject: sha, author: 'Author', authored_at: '2026-11-05T12:00:00Z' })

it('draws forks and a merge with one shared ancestor node', () => {
  const graph = layoutCommitGraph([commit('merge', ['left', 'right']), commit('left', ['base']), commit('right', ['base']), commit('base')])
  expect(graph.columns).toBe(2)
  expect(graph.rows.map(row => row.column)).toEqual([0, 0, 1, 0])
  expect(graph.rows[0].edges).toHaveLength(2)
  expect(graph.rows[2].edges.some(edge => edge.from === 1 && edge.to === 0 && edge.start === 38)).toBe(true)
  expect(graph.rows[3].edges.every(edge => edge.end === 38)).toBe(true)
  expect(graph.rows.flatMap(row => row.edges).every(edge => edge.to >= 0)).toBe(true)
})

it('joins independent branch tips at their common ancestor', () => {
  const graph = layoutCommitGraph([commit('remote', ['base']), commit('item', ['base']), commit('base')])
  expect(graph.rows.map(row => row.column)).toEqual([0, 1, 0])
  expect(graph.rows[1].edges.some(edge => edge.from === 1 && edge.to === 0)).toBe(true)
})

it('marks connections outside the history window without inventing root commits', () => {
  const graph = layoutCommitGraph([commit('tip', ['outside'])])
  expect(graph.rows[0].edges[0].boundary).toBe(true)
  expect(graphEdgePath(graph.rows[0].edges[0])).toBe('M14 38V76')
})

it('handles octopus merges, roots and disconnected histories', () => {
  const graph = layoutCommitGraph([commit('merge', ['a', 'b', 'c']), commit('a'), commit('b'), commit('c'), commit('disconnected')])
  expect(graph.columns).toBe(3)
  expect(graph.rows[0].edges).toHaveLength(3)
  expect(graph.rows.at(-1)?.edges).toEqual([])
  expect(layoutCommitGraph([]).rows).toEqual([])
})
