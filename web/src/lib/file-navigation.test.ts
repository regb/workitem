import { beforeEach, expect, it } from 'vitest'
import { beginFileOpen, isCurrentFileOpen, openWorkspaceFile, reviewUI, setPanel } from './review'
const getPanel = (id: string) => reviewUI.get().panels.get(id)!

beforeEach(() => reviewUI.set(state => ({ ...state, open: new Set(), panels: new Map(), drafts: new Map() })))

it('opens Files at the requested position while preserving sidebar preferences and drafts', () => {
  setPanel('item', { chatWidth: 440, split: false })
  const drafts = reviewUI.get().drafts
  openWorkspaceFile('item', { path: 'src/auth.ts', line: 42, column: 3, directory: false })
  expect(reviewUI.get().open.has('item')).toBe(true)
  expect(getPanel('item')).toMatchObject({ tab: 'files', path: 'src/auth.ts', chatWidth: 440, split: false, fileTarget: { line: 42, column: 3 } })
  expect(reviewUI.get().drafts).toBe(drafts)
})

it('gives repeated clicks a new navigation request, even for the same file and line', () => {
  const target = { path: 'src/auth.ts', line: 42, directory: false }
  openWorkspaceFile('item', target)
  const first = getPanel('item').fileTarget!.request
  openWorkspaceFile('item', target)
  expect(getPanel('item').fileTarget!.request).toBeGreaterThan(first)
})

it('lets the latest asynchronous file click win', () => {
  const older = beginFileOpen('item')
  const newer = beginFileOpen('item')
  expect(isCurrentFileOpen('item', older)).toBe(false)
  openWorkspaceFile('item', { path: 'new.ts', directory: false }, newer)
  openWorkspaceFile('item', { path: 'old.ts', directory: false }, older)
  expect(getPanel('item').path).toBe('new.ts')
})

it('does not let a pending lookup override manual navigation', () => {
  const request = beginFileOpen('item')
  setPanel('item', { tab: 'history', path: '' })
  openWorkspaceFile('item', { path: 'late.ts', directory: false }, request)
  expect(getPanel('item').tab).toBe('history')
  expect(reviewUI.get().open.has('item')).toBe(false)
})

it('clears line targets on manual selection, but not when resizing the sidebar', () => {
  openWorkspaceFile('item', { path: 'src/auth.ts', line: 42, directory: false })
  setPanel('item', { chatWidth: 450 })
  expect(getPanel('item').fileTarget?.line).toBe(42)
  setPanel('item', { path: 'other.ts' })
  expect(getPanel('item').fileTarget).toBeUndefined()
})
