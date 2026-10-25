import type { WorkList } from './api'

// placeStarting lists items whose agent is still starting under IN PROGRESS:
// until the agent exists the daemon reports a problem, which is expected.
export function placeStarting(sections: WorkList['display_sections'], launches: Map<string, number>) {
  if (!launches.size) return sections
  const starting = sections.flatMap(section => (section.name === 'BACKLOG' ? [] : section.items.filter(row => launches.has(row.id))))
  if (!starting.length) return sections
  const rest = sections.map(section => ({ ...section, items: section.items.filter(row => !starting.includes(row)) }))
  const progress = rest.find(section => section.name === 'IN PROGRESS')
  if (progress) progress.items = [...starting, ...progress.items]
  else rest.splice(Math.max(0, rest.findIndex(section => section.name === 'WAITING' || section.name === 'BACKLOG')), 0, { name: 'IN PROGRESS', items: starting })
  return rest
}
