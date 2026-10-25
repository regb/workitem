import { useCallback, useLayoutEffect, useRef, useState, type PointerEvent } from 'react'
import { Dialog as DialogPrimitive } from 'radix-ui'
import { MinusIcon, PlusIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'

// ZoomableImage is an image in the conversation that opens full screen when
// clicked.
export function ZoomableImage({ src, alt, className }: { src: string; alt: string; className?: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button
        type="button"
        className="w-fit max-w-full cursor-zoom-in rounded-md outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
        onClick={() => setOpen(true)}
        aria-label={`View ${alt}`}
      >
        <img src={src} alt={alt} loading="lazy" className={className} />
      </button>
      {open && <Lightbox src={src} alt={alt} onClose={() => setOpen(false)} />}
    </>
  )
}

const maxZoom = 8

// Lightbox shows an image over the page, fitted to the screen. It zooms with
// the buttons, a pinch, Ctrl and the wheel, or a double tap, and pans by
// dragging or scrolling.
function Lightbox({ src, alt, onClose }: { src: string; alt: string; onClose: () => void }) {
  const scroller = useRef<HTMLDivElement>(null)
  const [natural, setNatural] = useState<{ width: number; height: number }>()
  const [zoom, setZoom] = useState(1)
  // The point to keep in place while zooming, as a fraction of the image and
  // a position on screen.
  const anchor = useRef<{ fx: number; fy: number; x: number; y: number }>(undefined)
  const pointers = useRef(new Map<number, { x: number; y: number }>())
  const pinch = useRef<{ distance: number; zoom: number }>(undefined)
  const drag = useRef<{ x: number; y: number; left: number; top: number }>(undefined)
  const moved = useRef(false)
  const onImage = useRef(false)
  const lastTap = useRef<{ time: number; x: number; y: number }>(undefined)

  const fit = natural && scroller.current ? Math.min(1, scroller.current.clientWidth / natural.width, scroller.current.clientHeight / natural.height) : 1
  const width = natural ? natural.width * fit * zoom : undefined
  const height = natural ? natural.height * fit * zoom : undefined

  const zoomTo = (next: number, x?: number, y?: number) => {
    const element = scroller.current
    const image = element?.querySelector('img')
    next = Math.min(maxZoom, Math.max(1, next))
    if (!element || !image || next === zoom) return
    const box = image.getBoundingClientRect()
    const area = element.getBoundingClientRect()
    const atX = x ?? area.left + area.width / 2
    const atY = y ?? area.top + area.height / 2
    anchor.current = { fx: (atX - box.left) / box.width, fy: (atY - box.top) / box.height, x: atX - area.left, y: atY - area.top }
    setZoom(next)
  }

  useLayoutEffect(() => {
    const element = scroller.current
    const image = element?.querySelector('img')
    const at = anchor.current
    if (!element || !image || !at) return
    anchor.current = undefined
    const box = image.getBoundingClientRect()
    const area = element.getBoundingClientRect()
    element.scrollLeft += box.left - area.left + at.fx * box.width - at.x
    element.scrollTop += box.top - area.top + at.fy * box.height - at.y
  }, [zoom])

  // Ctrl with the wheel, which is also how a trackpad pinch arrives, zooms;
  // the page must not. React wheel listeners are passive, so this one is not.
  const zoomRef = useRef(zoomTo)
  zoomRef.current = zoomTo
  const attach = useCallback((element: HTMLDivElement | null) => {
    scroller.current = element
    if (!element) return
    element.addEventListener(
      'wheel',
      event => {
        if (!event.ctrlKey) return
        event.preventDefault()
        zoomRef.current(zoomed.current * Math.exp(-event.deltaY / 100), event.clientX, event.clientY)
      },
      { passive: false },
    )
  }, [])
  const zoomed = useRef(zoom)
  zoomed.current = zoom

  // Pointers do it all, so touch and mouse behave alike: one drags, two
  // pinch, and a double tap or click toggles between fitted and close up.
  const onPointerDown = (event: PointerEvent) => {
    event.currentTarget.setPointerCapture?.(event.pointerId)
    pointers.current.set(event.pointerId, { x: event.clientX, y: event.clientY })
    moved.current = false
    onImage.current = event.target instanceof HTMLImageElement
    if (pointers.current.size === 2) {
      pinch.current = { distance: spread(pointers.current), zoom }
      drag.current = undefined
    } else if (pointers.current.size === 1 && scroller.current) {
      drag.current = { x: event.clientX, y: event.clientY, left: scroller.current.scrollLeft, top: scroller.current.scrollTop }
    }
  }
  const onPointerMove = (event: PointerEvent) => {
    if (!pointers.current.has(event.pointerId)) return
    pointers.current.set(event.pointerId, { x: event.clientX, y: event.clientY })
    const element = scroller.current
    if (pinch.current && pointers.current.size === 2) {
      moved.current = true
      const [a, b] = [...pointers.current.values()]
      zoomTo((pinch.current.zoom * spread(pointers.current)) / pinch.current.distance, (a.x + b.x) / 2, (a.y + b.y) / 2)
    } else if (drag.current && element) {
      const dx = event.clientX - drag.current.x
      const dy = event.clientY - drag.current.y
      if (Math.hypot(dx, dy) > 4) moved.current = true
      element.scrollLeft = drag.current.left - dx
      element.scrollTop = drag.current.top - dy
    }
  }
  const onPointerUp = (event: PointerEvent) => {
    const tapped = pointers.current.size === 1 && !moved.current && event.type === 'pointerup'
    pointers.current.delete(event.pointerId)
    if (pointers.current.size < 2) pinch.current = undefined
    drag.current = undefined
    if (!tapped) return
    const previous = lastTap.current
    const now = Date.now()
    if (previous && now - previous.time < 300 && Math.hypot(event.clientX - previous.x, event.clientY - previous.y) < 30) {
      lastTap.current = undefined
      zoomTo(zoom > 1 ? 1 : 2.5, event.clientX, event.clientY)
    } else {
      lastTap.current = { time: now, x: event.clientX, y: event.clientY }
      // A tap beside the image closes it, unless it starts a double tap.
      if (!onImage.current) setTimeout(() => lastTap.current?.time === now && onClose(), 300)
    }
  }

  return (
    <DialogPrimitive.Root open onOpenChange={open => !open && onClose()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/90 data-open:animate-in data-open:fade-in-0" />
        <DialogPrimitive.Content className="fixed inset-0 z-50 outline-none" data-lightbox aria-describedby={undefined}>
          <DialogPrimitive.Title className="sr-only">{alt}</DialogPrimitive.Title>
          <div
            ref={attach}
            className="size-full touch-none overflow-auto overscroll-contain select-none"
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={onPointerUp}
            onPointerCancel={onPointerUp}
          >
            <div className="grid min-h-full min-w-full place-items-center">
              <img
                src={src}
                alt={alt}
                draggable={false}
                className={zoom > 1 ? 'max-w-none cursor-grab active:cursor-grabbing' : 'max-w-none'}
                style={{ width, height }}
                onLoad={event => setNatural({ width: event.currentTarget.naturalWidth, height: event.currentTarget.naturalHeight })}
              />
            </div>
          </div>
          <div className="fixed top-3 right-3 flex items-center gap-1 rounded-lg bg-black/60 p-1 text-white">
            <Button variant="ghost" size="icon-sm" onClick={() => zoomTo(zoom / 1.5)} disabled={zoom <= 1} aria-label="Zoom out">
              <MinusIcon />
            </Button>
            <button type="button" className="w-12 text-center text-xs tabular-nums" onClick={() => zoomTo(1)} title="Fit to screen">
              {Math.round(fit * zoom * 100)}%
            </button>
            <Button variant="ghost" size="icon-sm" onClick={() => zoomTo(zoom * 1.5)} disabled={zoom >= maxZoom} aria-label="Zoom in">
              <PlusIcon />
            </Button>
            <DialogPrimitive.Close asChild>
              <Button variant="ghost" size="icon-sm" aria-label="Close">
                <XIcon />
              </Button>
            </DialogPrimitive.Close>
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}

const spread = (points: Map<number, { x: number; y: number }>) => {
  const [a, b] = [...points.values()]
  return Math.hypot(a.x - b.x, a.y - b.y) || 1
}
