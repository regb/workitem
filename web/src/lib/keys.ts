import { useEffect, useState, type KeyboardEvent } from 'react'

// touchKeyboard reports a device without a physical keyboard's Shift+Enter:
// phones and tablets, which neither hover nor point finely.
export const touchKeyboard = () => typeof window !== 'undefined' && !!window.matchMedia?.('(hover: none) and (pointer: coarse)').matches

// submitsOnEnter reports whether a key press should submit: Enter without
// Shift, and never while an input method is composing text. As in ChatGPT and
// Claude, a touch keyboard's Return adds a line; its send button sends.
export function submitsOnEnter(event: KeyboardEvent<HTMLTextAreaElement>) {
  return event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229 && !touchKeyboard()
}

// Apple keyboards label the command key ⌘; elsewhere Ctrl plays its part.
// Either one counts as the command modifier.
const apple = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)

export type HeldKeys = { mod: boolean; alt: boolean }

export const heldKeys = (event: { metaKey: boolean; ctrlKey: boolean; altKey: boolean }): HeldKeys => ({ mod: event.metaKey || event.ctrlKey, alt: event.altKey })

// shortcut names a key with modifiers the way this keyboard labels them.
export const shortcut = (key: string, held: Partial<HeldKeys> = {}) =>
  apple ? `${held.mod ? '⌘' : ''}${held.alt ? '⌥' : ''}${key}` : [held.mod && 'Ctrl', held.alt && 'Alt', key].filter(Boolean).join('+')

// useHeldKeys follows the modifiers held down, so a button can show what a
// click will do before it is clicked.
export function useHeldKeys(): HeldKeys {
  const [held, setHeld] = useState<HeldKeys>({ mod: false, alt: false })
  useEffect(() => {
    const update = (event: globalThis.KeyboardEvent) =>
      setHeld(current => {
        const next = heldKeys(event)
        return next.mod === current.mod && next.alt === current.alt ? current : next
      })
    const release = () => setHeld({ mod: false, alt: false })
    window.addEventListener('keydown', update)
    window.addEventListener('keyup', update)
    window.addEventListener('blur', release)
    return () => {
      window.removeEventListener('keydown', update)
      window.removeEventListener('keyup', update)
      window.removeEventListener('blur', release)
    }
  }, [])
  return held
}
