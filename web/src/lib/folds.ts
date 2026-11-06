// Which folds the reader opened in each conversation (turns, tool calls,
// subagents, summaries), by a key stable across reloads, so going back shows
// them as they were left. Folds last for the tab, like places.
const key = 'wi-folds'
// Enough for every conversation a session visits; the oldest go first.
const maxItems = 200
const maxOpen = 500

function storage(): Storage | undefined {
  try {
    return window.sessionStorage
  } catch {
    return undefined
  }
}

function load(): Map<string, Set<string>> {
  try {
    const saved: unknown = JSON.parse(storage()?.getItem(key) ?? '[]')
    return new Map(Array.isArray(saved) ? (saved as [string, string[]][]).map(([item, open]) => [item, new Set(open)]) : [])
  } catch {
    return new Map()
  }
}

const folds = load()

export const foldOpen = (item: string, fold: string) => !!folds.get(item)?.has(fold)

export function saveFold(item: string, fold: string, open: boolean) {
  const opened = folds.get(item) ?? new Set<string>()
  folds.delete(item)
  opened.delete(fold)
  if (open) opened.add(fold)
  while (opened.size > maxOpen) opened.delete(opened.values().next().value!)
  if (opened.size) folds.set(item, opened)
  while (folds.size > maxItems) folds.delete(folds.keys().next().value!)
  try {
    storage()?.setItem(key, JSON.stringify([...folds].map(([item, open]) => [item, [...open]])))
  } catch {
    // A full or blocked storage only loses the folds after a reload.
  }
}
