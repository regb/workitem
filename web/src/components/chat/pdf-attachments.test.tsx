import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'
import { AttachmentViewer, isPDF, viewable } from './item-notes'

const pdf = { name: 'report.pdf', type: 'application/pdf', sha256: 'digest', size: 100 }
afterEach(cleanup)

it('offers a native PDF tab and an explicit download without an embedded viewer', () => {
  const { baseElement } = render(<AttachmentViewer id="item" attachment={pdf} onClose={() => {}} />)
  const open = screen.getByRole('link', { name: 'Open PDF in a new tab' })
  expect(open.getAttribute('href')).toBe('/api/items/item/attachments/digest')
  expect(open.getAttribute('target')).toBe('_blank')
  expect(open.hasAttribute('download')).toBe(false)
  expect(screen.getByRole('link', { name: 'Download' }).getAttribute('href')).toBe('/api/items/item/attachments/digest?download=1')
  expect(baseElement.querySelector('iframe, embed, object')).toBeNull()
})

it('uses the declared media type, not a misleading PDF suffix', () => {
  expect(viewable(pdf)).toBe(true)
  expect(isPDF({ type: 'application/pdf; charset=binary' })).toBe(true)
  expect(isPDF({ type: 'text/html' })).toBe(false)
})
