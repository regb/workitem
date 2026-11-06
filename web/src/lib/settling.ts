import type { Live } from './api'
import { createStore } from './store'

// A message whose live text moved on (the next message started) before history
// showed it. The conversation keeps showing it until history has one of its
// entries, so finished text never blinks out while history catches up.
export type Settling = { ids: string[]; text: string; at: number }

// Held messages are dropped after this long even if history never shows them.
const settlingLimit = 60_000

export const settling = createStore<Record<string, Settling[]>>({})

const none: Settling[] = []
export const useSettling = (item: string) => settling.use(state => state[item] ?? none)

// noteLiveChange holds the previous live text when it was saved but is no
// longer the live text.
export function noteLiveChange(item: string, previous: Live | null | undefined, next: Live | null | undefined) {
  const ids = previous?.saved_as
  if (!previous?.text || !ids?.length || next?.text === previous.text) return
  const held: Settling = { ids, text: previous.text, at: Date.now() }
  settling.set(state => ({ ...state, [item]: [...(state[item] ?? []).filter(entry => entry.ids[0] !== ids[0]), held] }))
}

// pruneSettling forgets held messages history now shows, or that are too old.
export function pruneSettling(item: string, shown: (ids: string[]) => boolean) {
  settling.set(state => {
    const current = state[item]
    if (!current?.length) return state
    const kept = current.filter(entry => !shown(entry.ids) && Date.now() - entry.at < settlingLimit)
    return kept.length === current.length ? state : { ...state, [item]: kept }
  })
}

// forgetSettling drops what was held for a conversation no longer open.
export function forgetSettling(item: string) {
  settling.set(state => {
    if (!(item in state)) return state
    const { [item]: _, ...rest } = state
    return rest
  })
}
