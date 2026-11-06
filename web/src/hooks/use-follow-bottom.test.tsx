import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render } from '@testing-library/react'
import { useFollowBottom } from './use-follow-bottom'

let resize: () => void
let visible: boolean
let linkTop: number
function Reader() {
  const { viewport, content } = useFollowBottom()
  return <div ref={viewport} data-testid="viewport"><div ref={content}><button data-file-link="a.ts">a.ts</button></div></div>
}
function mount() {
  const view = render(<Reader />)
  const viewport = view.getByTestId('viewport')
  const link = view.getByRole('button')
  Object.defineProperties(viewport, {
    clientHeight: { get: () => visible ? 500 : 0 },
    scrollHeight: { get: () => 4000 },
  })
  vi.spyOn(viewport, 'getBoundingClientRect').mockImplementation(() => ({ top: 0 }) as DOMRect)
  vi.spyOn(link, 'getBoundingClientRect').mockImplementation(() => ({ top: visible ? linkTop - viewport.scrollTop : 0 }) as DOMRect)
  viewport.scrollTop = 1000
  fireEvent.scroll(viewport)
  return { viewport, link }
}
beforeEach(() => {
  visible = true
  linkTop = 1200
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback: () => void) { resize = callback }
    observe() {}
    disconnect() {}
  })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('keeps a clicked file chip at the same visual offset when chat rewraps', () => {
  const { viewport, link } = mount()
  fireEvent.pointerDown(link)
  fireEvent.click(link)
  linkTop = 1600
  act(() => resize())
  expect(viewport.scrollTop).toBe(1400)
  expect(link.getBoundingClientRect().top).toBe(200)
})

it('also preserves the position for keyboard activation', () => {
  const { viewport, link } = mount()
  fireEvent.keyDown(link, { key: 'Enter' })
  fireEvent.click(link)
  linkTop = 1500
  act(() => resize())
  expect(viewport.scrollTop).toBe(1300)
})

it('does not corrupt the anchor while mobile hides chat', () => {
  const { viewport, link } = mount()
  fireEvent.click(link)
  visible = false
  act(() => resize())
  expect(viewport.scrollTop).toBe(1000)
  visible = true
  linkTop = 1500
  act(() => resize())
  expect(link.getBoundingClientRect().top).toBe(200)
})

it('releases the anchor when the reader scrolls away', () => {
  const { viewport, link } = mount()
  fireEvent.click(link)
  fireEvent.wheel(viewport, { deltaY: -100 })
  linkTop = 1500
  act(() => resize())
  expect(viewport.scrollTop).toBe(1000)
})
