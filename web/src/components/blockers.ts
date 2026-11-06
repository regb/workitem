import type { Blocker, WorkList } from '@/lib/api'

export function blockerText(blocker: Blocker, list?: WorkList) {
  if (blocker.kind === 'time' && blocker.until) return `until ${new Date(blocker.until).toLocaleString()}`
  if (blocker.kind === 'usage') {
    const limit = blocker.provider ? `${providerName(blocker.provider)} usage limit` : 'usage limit'
    return blocker.until && new Date(blocker.until) > new Date() ? `${limit}, resets ${new Date(blocker.until).toLocaleString()}` : limit
  }
  if (blocker.kind === 'item') return `on ${itemTitle(blocker.item_id ?? '', list)}`
  return blocker.event || blocker.kind
}

export const blockerShort = (blocker: Blocker, list?: WorkList) => blocker.label || (blocker.kind === 'usage' ? usageShort(blocker) : blockerText(blocker, list))

const providerNames: Record<string, string> = { claude: 'Claude', codex: 'Codex' }
const providerName = (provider: string) => providerNames[provider] ?? provider

// usageShort fits a usage blocker in a list row: when it resets, if known.
function usageShort(blocker: Blocker) {
  if (!blocker.until || new Date(blocker.until) <= new Date()) return 'usage limit'
  const until = new Date(blocker.until)
  const sameDay = until.toDateString() === new Date().toDateString()
  return `usage, resets ${until.toLocaleString([], sameDay ? { hour: '2-digit', minute: '2-digit' } : { weekday: 'short', hour: '2-digit', minute: '2-digit' })}`
}

function itemTitle(id: string, list?: WorkList) {
  for (const section of list?.display_sections ?? []) for (const row of section.items) if (row.id === id) return row.title
  return id.slice(0, 10)
}
