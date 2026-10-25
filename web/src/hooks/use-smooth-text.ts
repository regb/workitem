import { useEffect, useRef, useState } from 'react'

// Frames over which a backlog of unrevealed text is caught up. At 60 frames a
// second the reveal trails the stream by about half a second, whatever size
// the chunks arrive in, so even a paragraph delivered at once types out.
const catchUpFrames = 30

// revealStep returns the next length to show: a share of the backlog, at
// least one character, never splitting a surrogate pair.
export function revealStep(target: string, shown: number): number {
  let next = Math.min(target.length, shown + Math.max(1, Math.ceil((target.length - shown) / catchUpFrames)))
  const code = target.charCodeAt(next)
  if (next < target.length && code >= 0xdc00 && code <= 0xdfff) next++
  return next
}

// useSmoothText reveals streamed text at an even pace instead of in the
// chunks the network delivers. Text that is not streaming, or that was
// rewritten rather than extended, shows at once.
export function useSmoothText(target: string, streaming: boolean) {
  const [shown, setShown] = useState(target)
  const current = useRef(target)
  const show = (value: string) => {
    current.current = value
    setShown(value)
  }
  useEffect(() => {
    if (!streaming || !target.startsWith(current.current)) {
      show(target)
      return
    }
    let frame = 0
    const step = () => {
      if (current.current.length >= target.length) return
      show(target.slice(0, revealStep(target, current.current.length)))
      frame = requestAnimationFrame(step)
    }
    frame = requestAnimationFrame(step)
    return () => cancelAnimationFrame(frame)
  }, [target, streaming])
  return shown
}
