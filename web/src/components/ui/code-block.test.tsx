import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { CodeBlockCopyButton } from './code-block'
import { copyText } from '@/lib/clipboard'
vi.mock('@/lib/clipboard', () => ({ copyText: vi.fn() }))
afterEach(() => { cleanup(); vi.resetAllMocks() })

it('copies the raw block value and reports success only after it completes', async () => {
  let resolve!: () => void
  vi.mocked(copyText).mockReturnValue(new Promise<void>(done => { resolve = done }))
  const text = '# Heading\n\nRaw **markdown**\n'
  render(<CodeBlockCopyButton value={text} />)
  fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
  expect(copyText).toHaveBeenCalledWith(text)
  expect(screen.queryByRole('button', { name: 'Copied' })).toBeNull()
  resolve()
  expect(await screen.findByRole('button', { name: 'Copied' })).toBeTruthy()
})

it('shows a failure rather than silently swallowing clipboard errors, and allows retry', async () => {
  vi.mocked(copyText).mockRejectedValueOnce(new Error('denied')).mockResolvedValueOnce()
  render(<CodeBlockCopyButton value="content" />)
  fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
  expect(await screen.findByText('Copy failed. Select the text and copy manually.')).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Copied' })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
  expect(await screen.findByRole('button', { name: 'Copied' })).toBeTruthy()
})

it('respects a cancelled click without submitting a form', () => {
  render(<form><CodeBlockCopyButton value="content" onClick={event => event.preventDefault()} /></form>)
  const button = screen.getByRole('button', { name: 'Copy' })
  expect(button.getAttribute('type')).toBe('button')
  fireEvent.click(button)
  expect(copyText).not.toHaveBeenCalled()
})
