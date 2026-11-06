// The browser Clipboard API is restricted to secure contexts. Wi can also be
// served over HTTP on a private network, so copy gestures need a DOM fallback.
export async function copyText(text: string): Promise<void> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return
    }
  } catch {
    // Permissions or browser policy can also reject a supported API.
  }
  if (!document.execCommand || !text) throw new Error('Clipboard access is unavailable.')
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const selection = window.getSelection()
  const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, i) => selection.getRangeAt(i).cloneRange()) : []
  const input = active instanceof HTMLTextAreaElement || active instanceof HTMLInputElement ? active : null
  const start = input?.selectionStart
  const end = input?.selectionEnd
  const direction = input?.selectionDirection
  const field = document.createElement('textarea')
  field.value = text
  field.readOnly = true
  field.tabIndex = -1
  field.setAttribute('aria-hidden', 'true')
  Object.assign(field.style, { position: 'fixed', top: '0', left: '0', width: '1px', height: '1px', opacity: '0', fontSize: '16px' })
  document.body.append(field)
  try {
    field.focus({ preventScroll: true })
    field.select()
    field.setSelectionRange(0, text.length)
    if (!document.execCommand('copy')) throw new Error('The browser refused to copy.')
  } finally {
    field.remove()
    active?.focus({ preventScroll: true })
    if (input && start != null && end != null) input.setSelectionRange(start, end, direction ?? undefined)
    if (selection) {
      selection.removeAllRanges()
      ranges.forEach(range => selection.addRange(range))
    }
  }
}
