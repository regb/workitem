import { afterEach, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
const execDescriptor = Object.getOwnPropertyDescriptor(document, 'execCommand')
function clipboard(value: unknown) { Object.defineProperty(navigator, 'clipboard', { configurable: true, value }) }
function fallback(callback = () => true) {
  const command = vi.fn(callback)
  Object.defineProperty(document, 'execCommand', { configurable: true, value: command })
  return command
}
afterEach(() => {
  if (clipboardDescriptor) Object.defineProperty(navigator, 'clipboard', clipboardDescriptor)
  else Reflect.deleteProperty(navigator, 'clipboard')
  if (execDescriptor) Object.defineProperty(document, 'execCommand', execDescriptor)
  else Reflect.deleteProperty(document, 'execCommand')
  document.body.replaceChildren()
  window.getSelection()?.removeAllRanges()
})

it('uses the secure clipboard API when available', async () => {
  const writeText = vi.fn(async () => {})
  clipboard({ writeText })
  const legacy = fallback()
  await copyText('raw markdown\n')
  expect(writeText).toHaveBeenCalledWith('raw markdown\n')
  expect(legacy).not.toHaveBeenCalled()
})

it('copies exact text over HTTP and restores the focused input and selection', async () => {
  clipboard(undefined)
  const input = document.createElement('textarea')
  input.value = 'keep this draft'
  document.body.append(input)
  input.focus()
  input.setSelectionRange(2, 7)
  const text = '# Heading\n\n```ts\nconst a = "<tag>"\n```\n'
  const legacy = fallback(() => {
    const field = document.activeElement as HTMLTextAreaElement
    expect(field.value).toBe(text)
    expect(field.selectionStart).toBe(0)
    expect(field.selectionEnd).toBe(text.length)
    return true
  })
  await copyText(text)
  expect(legacy).toHaveBeenCalledWith('copy')
  expect(document.activeElement).toBe(input)
  expect(input.selectionStart).toBe(2)
  expect(input.selectionEnd).toBe(7)
  expect(document.querySelectorAll('textarea')).toHaveLength(1)
})

it('falls back when the supported Clipboard API rejects access', async () => {
  clipboard({ writeText: vi.fn(async () => { throw new Error('denied') }) })
  const legacy = fallback()
  await copyText('copy me')
  expect(legacy).toHaveBeenCalledWith('copy')
})

it('restores document selection and removes the temporary field on failure', async () => {
  clipboard(undefined)
  document.body.append(document.createTextNode('selected text'))
  const selection = window.getSelection()!
  const range = document.createRange()
  range.selectNodeContents(document.body.firstChild!)
  selection.addRange(range)
  fallback(() => false)
  await expect(copyText('copy me')).rejects.toThrow('refused')
  expect(document.querySelector('textarea')).toBeNull()
  expect(selection.toString()).toBe('selected text')
})

it('does not report success when no copying mechanism exists', async () => {
  clipboard(undefined)
  Reflect.deleteProperty(document, 'execCommand')
  await expect(copyText('copy me')).rejects.toThrow('unavailable')
})
