import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { MessageImages } from './message-images'

describe('image lightbox', () => {
  it('opens a message image full screen and closes it again', () => {
    render(<MessageImages images={[{ mime_type: 'image/png', data: 'AAAA', alt: 'screenshot' }]} />)
    expect(document.querySelector('[data-lightbox]')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'View screenshot' }))
    const lightbox = document.querySelector('[data-lightbox]')
    expect(lightbox?.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,AAAA')
    expect(screen.getByRole('button', { name: 'Zoom in' })).not.toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(document.querySelector('[data-lightbox]')).toBeNull()
  })
})
