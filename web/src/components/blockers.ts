import type { Blocker, WorkList } from '@/lib/api'

export function blockerText(blocker: Blocker, list?: WorkList) {
  if (blocker.kind === 'time' && blocker.until) return `until ${new Date(blocker.until).toLocaleString()}`
  if (blocker.kind === 'item') return `on ${itemTitle(blocker.item_id ?? '', list)}`
  return blocker.event || blocker.kind
}

export const blockerShort = (blocker: Blocker, list?: WorkList) => blocker.label || blockerText(blocker, list)

function itemTitle(id: string, list?: WorkList) {
  for (const section of list?.display_sections ?? []) for (const row of section.items) if (row.id === id) return row.title
  return id.slice(0, 10)
}
