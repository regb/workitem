import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ItemDescription, leadingBlocks } from './item-description'

describe('leadingBlocks', () => {
  it('keeps the first blocks and says whether it cut', () => {
    expect(leadingBlocks('one\n\ntwo\n\n\nthree\n\nfour', 3, 600)).toEqual({ text: 'one\n\ntwo\n\nthree', cut: true })
    expect(leadingBlocks('one\n\ntwo', 3, 600)).toEqual({ text: 'one\n\ntwo', cut: false })
  })

  it('counts a fenced code block with blank lines as one block', () => {
    const text = 'Intro\n\n```go\na := 1\n\nb := 2\n```\n\nAfter\n\nEnd'
    expect(leadingBlocks(text, 2, 600).text).toBe('Intro\n\n```go\na := 1\n\nb := 2\n```')
  })

  it('stops early once the preview is long, keeping at least one block', () => {
    const long = 'x'.repeat(700)
    expect(leadingBlocks(`${long}\n\nmore`, 3, 600)).toEqual({ text: long, cut: true })
  })
})

describe('ItemDescription', () => {
  it('renders Markdown and opens the full description', () => {
    const { container } = render(<ItemDescription title="Item" text={'# Plan\n\nThe **first** step\n\nSecond\n\nHidden tail'} />)
    expect(container.querySelector('[data-description] strong')?.textContent).toBe('first')
    expect(container.textContent).not.toContain('Hidden tail')
    fireEvent.click(screen.getByText('Read the full description'))
    expect(document.querySelector('[data-description-full]')?.textContent).toContain('Hidden tail')
  })

  it('shows a short description whole, without the button', () => {
    const { container } = render(<ItemDescription title="Item" text="Just this" />)
    expect(container.textContent).toBe('Just this')
    expect(container.querySelector('button')).toBeNull()
  })
})
