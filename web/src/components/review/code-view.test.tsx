import { useLayoutEffect, useRef } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, render } from '@testing-library/react'
import type { FileOptions, FileContents } from '@pierre/diffs/react'
import { CodeProvider, DiffView, SourceView } from './code-view'
import type { FileNavigation } from '@/lib/review'

const { virtualizer, instance } = vi.hoisted(() => ({
  virtualizer: { markDOMDirty: vi.fn(), scrollTo: vi.fn(), getOffsetInScrollContainer: () => 0 },
  instance: { getLinePosition: vi.fn((line: number) => ({ top: (line - 1) * 20, height: 20 })) },
}))
vi.mock('@pierre/diffs/react', () => ({
  WorkerPoolContextProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  EditProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  MultiFileDiff: ({ options }: { options: { overflow: string } }) => <div data-diff-overflow={options.overflow} />,
  useVirtualizer: () => virtualizer,
  Virtualizer: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  File: ({ options, selectedLines }: { options: FileOptions<undefined, undefined>; file: FileContents; selectedLines?: unknown }) => {
    const node = useRef<HTMLDivElement>(null)
    useLayoutEffect(() => {
      options.onPostRender?.(node.current!, instance as never, 'update')
    })
    return <div ref={node} data-overflow={options.overflow} data-range={JSON.stringify(selectedLines)} />
  },
}))
let queue: Map<number, FrameRequestCallback>
let nextFrame = 0
const source = { path: 'a.ts', content: Array.from({ length: 400 }, (_, i) => `line ${i + 1}`).join('\n'), exists: true }
const target: FileNavigation = { path: 'a.ts', line: 240, column: 3, directory: false, request: 1 }
function flushFrames() {
  const frames = [...queue.values()]
  queue.clear()
  frames.forEach(callback => callback(0))
}
beforeEach(() => {
  vi.clearAllMocks()
  queue = new Map()
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    queue.set(++nextFrame, callback)
    return nextFrame
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => queue.delete(id))
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

it('wraps source, editor and split or unified diffs by default, with a reversible toggle', () => {
  for (const edit of [false, true]) {
    for (const split of [false, true]) {
      const content = (
        <>
          <SourceView file={source} edit={edit} />
          <DiffView oldFile={source} newFile={source} split={split} />
        </>
      )
      const view = render(<CodeProvider>{content}</CodeProvider>)
      expect(view.container.querySelector('[data-overflow]')?.getAttribute('data-overflow')).toBe('wrap')
      expect(view.container.querySelector('[data-diff-overflow]')?.getAttribute('data-diff-overflow')).toBe('wrap')
      view.rerender(<CodeProvider wrap={false}>{content}</CodeProvider>)
      expect(view.container.querySelector('[data-overflow]')?.getAttribute('data-overflow')).toBe('scroll')
      expect(view.container.querySelector('[data-diff-overflow]')?.getAttribute('data-diff-overflow')).toBe('scroll')
      view.unmount()
    }
  }
})

it('does not scroll a source opened without a position', () => {
  render(<SourceView file={source} />)
  flushFrames()
  expect(virtualizer.scrollTo).not.toHaveBeenCalled()
})

it('reveals a virtualized line and repeats the jump for a new click on the same reference', () => {
  const view = render(<SourceView file={source} location={target} />)
  flushFrames()
  expect(virtualizer.markDOMDirty).toHaveBeenCalledOnce()
  expect(virtualizer.scrollTo).toHaveBeenLastCalledWith({ top: 4748 })
  view.rerender(<SourceView file={source} location={{ ...target, request: 2 }} />)
  flushFrames()
  expect(virtualizer.scrollTo).toHaveBeenCalledTimes(2)
})

it('does not undo manual scrolling on background source updates', () => {
  const view = render(<SourceView file={source} location={target} />)
  flushFrames()
  view.rerender(<SourceView file={{ ...source, content: source.content + '\nagent changed' }} location={target} />)
  flushFrames()
  expect(virtualizer.scrollTo).toHaveBeenCalledOnce()
})

it('cancels a pending reveal when manual selection removes the target', () => {
  const view = render(<SourceView file={source} location={target} />)
  view.rerender(<SourceView file={source} />)
  flushFrames()
  expect(virtualizer.scrollTo).not.toHaveBeenCalled()
})

it('clamps a stale line range to the actual file', () => {
  const view = render(<SourceView file={{ ...source, content: 'one\ntwo\nthree' }} location={{ ...target, line: 900, endLine: 1000 }} />)
  flushFrames()
  expect(instance.getLinePosition).toHaveBeenCalledWith(3)
  expect(view.container.querySelector('[data-range]')?.getAttribute('data-range')).toBe('{"start":3,"end":3}')
})
