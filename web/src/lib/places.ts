import type { Shown } from './history'

// Where the reader left each conversation, so going back opens it there:
// the entry at the top of the view and how far its top was from the view's
// top, or the end. Places last for the tab, across reloads.
export type Place = { entry: string; offset: number } | 'end'

const key = 'wi-places'
// Enough for every conversation a session visits; the oldest go first.
const maxPlaces = 200

function storage(): Storage | undefined {
  try {
    return window.sessionStorage
  } catch {
    return undefined
  }
}

function load(): Map<string, Place> {
  try {
    const saved: unknown = JSON.parse(storage()?.getItem(key) ?? '[]')
    return new Map(Array.isArray(saved) ? (saved as [string, Place][]) : [])
  } catch {
    return new Map()
  }
}

const places = load()

export const placeOf = (id: string) => places.get(id)

export function savePlace(id: string, place: Place) {
  places.delete(id)
  places.set(id, place)
  while (places.size > maxPlaces) places.delete(places.keys().next().value!)
  try {
    storage()?.setItem(key, JSON.stringify([...places]))
  } catch {
    // A full or blocked storage only loses the place after a reload.
  }
}

// What a conversation showed when it was left, older pages included, so
// going back finds the place the reader left. Kept in memory for the few
// conversations visited last.
const shownCache = new Map<string, Shown>()
const maxCached = 8

export const cachedShown = (id: string) => shownCache.get(id)

export function cacheShown(id: string, shown: Shown) {
  shownCache.delete(id)
  shownCache.set(id, shown)
  while (shownCache.size > maxCached) shownCache.delete(shownCache.keys().next().value!)
}
