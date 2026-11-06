import type { Live, MessagePart } from './views'
import { partsText } from './parts'
import { createStore } from './store'

// A saved message the live content moved on from (the next message started)
// before history showed it. The conversation keeps showing it until history
// has one of its entries, so a finished message never blinks out while
// history catches up.
export type Settling = { ids: string[]; parts: MessagePart[]; at: number }

// Held messages are dropped after this long even if history never shows them.
const settlingLimit = 60_000

export const settling = createStore<Record<string, Settling[]>>({})

const none: Settling[] = []
export const useSettling = (item: string) => settling.use(state => state[item] ?? none)

// noteLiveChange holds the previous live message when it was saved but is no
// longer the live message.
export function noteLiveChange(item: string, previous: Live | null | undefined, next: Live | null | undefined) {
  const ids = previous?.saved_as
  if (!previous?.parts?.length || !ids?.length) return
  if (next?.message_id === previous.message_id && partsText(next?.parts) === partsText(previous.parts)) return
  const held: Settling = { ids, parts: previous.parts, at: Date.now() }
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
