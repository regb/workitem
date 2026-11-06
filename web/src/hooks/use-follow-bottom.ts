import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'

// Distance from the bottom, in pixels, that still counts as at the bottom.
const edge = 48
// Room left above an element the area opens on.
const anchorGap = 16

// useFollowBottom keeps a scroll area pinned to its end while the reader is
// there, as new output grows the content, and never moves it otherwise: any
// scroll up, a touch, or opening a detail stops following until the reader
// returns to the end or sends a message. Loading older content above keeps
// the reader's place. startAt opens the area on an element instead of the
// end, at a given distance from the top, and keeps it there while content
// loads, until the reader moves.
export function useFollowBottom() {
  const viewport = useRef<HTMLDivElement>(null)
  const content = useRef<HTMLDivElement>(null)
  const following = useRef(true)
  const touching = useRef(false)
  const lastTop = useRef(0)
  // Distance from the end to restore once content is added above.
  const keepFromEnd = useRef<number | null>(null)
  // The element the reader was placed on, until they scroll themselves.
  const anchor = useRef<HTMLElement | null>(null)
  const anchorTop = useRef(anchorGap)
  const [atBottom, setAtBottom] = useState(true)

  const distance = (el: HTMLElement) => el.scrollHeight - el.scrollTop - el.clientHeight

  const pin = useCallback(() => {
    const el = viewport.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    lastTop.current = el.scrollTop
  }, [])

  const showAnchor = useCallback(() => {
    const el = viewport.current
    if (!el || !anchor.current) return
    el.scrollTop += anchor.current.getBoundingClientRect().top - el.getBoundingClientRect().top - anchorTop.current
    lastTop.current = el.scrollTop
  }, [])

  const startAt = useCallback(
    (element: HTMLElement, top = anchorGap) => {
      following.current = false
      anchor.current = element
      anchorTop.current = top
      showAnchor()
      const el = viewport.current
      setAtBottom(!!el && distance(el) <= edge)
    },
    [showAnchor],
  )

  const scrollToEnd = useCallback(() => {
    anchor.current = null
    following.current = true
    pin()
    setAtBottom(true)
  }, [pin])

  // keepPlace is called before content is added above the current view.
  const keepPlace = useCallback(() => {
    const el = viewport.current
    if (el) keepFromEnd.current = el.scrollHeight - el.scrollTop
  }, [])

  useLayoutEffect(() => {
    pin()
  }, [pin])

  useEffect(() => {
    const el = viewport.current
    const inner = content.current
    if (!el || !inner || typeof ResizeObserver === 'undefined') return
    const onScroll = () => {
      const bottom = distance(el) <= edge
      if (bottom) following.current = true
      else if (el.scrollTop < lastTop.current - 1) following.current = false
      lastTop.current = el.scrollTop
      setAtBottom(bottom)
    }
    const onWheel = (event: WheelEvent) => {
      anchor.current = null
      if (event.deltaY < 0) following.current = false
    }
    const onTouchStart = () => {
      anchor.current = null
      touching.current = true
    }
    const onKeyDown = () => {
      anchor.current = null
    }
    const onTouchEnd = () => {
      touching.current = false
      following.current = distance(el) <= edge
    }
    // Opening a tool call or following a link means reading here.
    const onPointerDown = (event: PointerEvent) => {
      anchor.current = null
      if ((event.target as Element).closest('button, a, [role=button]')) following.current = false
    }
    const resize = new ResizeObserver(() => {
      if (keepFromEnd.current !== null) {
        el.scrollTop = el.scrollHeight - keepFromEnd.current
        keepFromEnd.current = null
        lastTop.current = el.scrollTop
      } else if (anchor.current) {
        showAnchor()
      } else if (following.current && !touching.current) {
        pin()
      }
      setAtBottom(distance(el) <= edge)
    })
    resize.observe(inner)
    el.addEventListener('scroll', onScroll, { passive: true })
    el.addEventListener('wheel', onWheel, { passive: true })
    el.addEventListener('touchstart', onTouchStart, { passive: true })
    el.addEventListener('touchend', onTouchEnd, { passive: true })
    el.addEventListener('touchcancel', onTouchEnd, { passive: true })
    el.addEventListener('keydown', onKeyDown)
    inner.addEventListener('pointerdown', onPointerDown)
    return () => {
      resize.disconnect()
      el.removeEventListener('scroll', onScroll)
      el.removeEventListener('wheel', onWheel)
      el.removeEventListener('touchstart', onTouchStart)
      el.removeEventListener('touchend', onTouchEnd)
      el.removeEventListener('touchcancel', onTouchEnd)
      el.removeEventListener('keydown', onKeyDown)
      inner.removeEventListener('pointerdown', onPointerDown)
    }
  }, [pin, showAnchor])

  // place says where the reader is: at the end, or the element of those
  // given at the top of the view and how far its top is from the view's.
  const place = useCallback((elements: Iterable<HTMLElement>) => {
    const el = viewport.current
    if (!el || distance(el) <= edge) return 'end' as const
    const top = el.getBoundingClientRect().top
    for (const element of elements) {
      const box = element.getBoundingClientRect()
      if (box.bottom > top) return { element, offset: box.top - top }
    }
    return 'end' as const
  }, [])

  return { viewport, content, atBottom, scrollToEnd, keepPlace, startAt, place }
}
