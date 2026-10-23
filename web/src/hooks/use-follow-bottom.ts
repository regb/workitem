import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'

// Distance from the bottom, in pixels, that still counts as at the bottom.
const edge = 48

// useFollowBottom keeps a scroll area pinned to its end while the reader is
// there, as new output grows the content, and never moves it otherwise: any
// scroll up, a touch, or opening a detail stops following until the reader
// returns to the end or sends a message. Loading older content above keeps
// the reader's place.
export function useFollowBottom() {
  const viewport = useRef<HTMLDivElement>(null)
  const content = useRef<HTMLDivElement>(null)
  const following = useRef(true)
  const touching = useRef(false)
  const lastTop = useRef(0)
  // Distance from the end to restore once content is added above.
  const keepFromEnd = useRef<number | null>(null)
  const [atBottom, setAtBottom] = useState(true)

  const distance = (el: HTMLElement) => el.scrollHeight - el.scrollTop - el.clientHeight

  const pin = useCallback(() => {
    const el = viewport.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    lastTop.current = el.scrollTop
  }, [])

  const scrollToEnd = useCallback(() => {
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
      if (event.deltaY < 0) following.current = false
    }
    const onTouchStart = () => {
      touching.current = true
    }
    const onTouchEnd = () => {
      touching.current = false
      following.current = distance(el) <= edge
    }
    // Opening a tool call or following a link means reading here.
    const onPointerDown = (event: PointerEvent) => {
      if ((event.target as Element).closest('button, a, [role=button]')) following.current = false
    }
    const resize = new ResizeObserver(() => {
      if (keepFromEnd.current !== null) {
        el.scrollTop = el.scrollHeight - keepFromEnd.current
        keepFromEnd.current = null
        lastTop.current = el.scrollTop
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
    inner.addEventListener('pointerdown', onPointerDown)
    return () => {
      resize.disconnect()
      el.removeEventListener('scroll', onScroll)
      el.removeEventListener('wheel', onWheel)
      el.removeEventListener('touchstart', onTouchStart)
      el.removeEventListener('touchend', onTouchEnd)
      el.removeEventListener('touchcancel', onTouchEnd)
      inner.removeEventListener('pointerdown', onPointerDown)
    }
  }, [pin])

  return { viewport, content, atBottom, scrollToEnd, keepPlace }
}
