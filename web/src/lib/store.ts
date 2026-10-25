import { useSyncExternalStore } from 'react'

// A minimal external store for UI state shared across routes. Selectors must
// return stable values (primitives or stored references).
export function createStore<T>(initial: T) {
  let state = initial
  const listeners = new Set<() => void>()
  const subscribe = (listener: () => void) => {
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  }
  return {
    get: () => state,
    set(next: T | ((current: T) => T)) {
      state = typeof next === 'function' ? (next as (current: T) => T)(state) : next
      listeners.forEach(listener => listener())
    },
    subscribe,
    use<S>(select: (current: T) => S): S {
      return useSyncExternalStore(subscribe, () => select(state))
    },
  }
}
