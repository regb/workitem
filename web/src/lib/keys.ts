import type { KeyboardEvent } from 'react'

// touchKeyboard reports a device without a physical keyboard's Shift+Enter:
// phones and tablets, which neither hover nor point finely.
export const touchKeyboard = () => typeof window !== 'undefined' && !!window.matchMedia?.('(hover: none) and (pointer: coarse)').matches

// submitsOnEnter reports whether a key press should submit: Enter without
// Shift, and never while an input method is composing text. As in ChatGPT and
// Claude, a touch keyboard's Return adds a line; its send button sends.
export function submitsOnEnter(event: KeyboardEvent<HTMLTextAreaElement>) {
  return event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229 && !touchKeyboard()
}
