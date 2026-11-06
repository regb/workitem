import { createContext, useContext, useCallback, useLayoutEffect, useMemo, useRef, type ReactNode } from 'react'
import { EditProvider, File, MultiFileDiff, Virtualizer, WorkerPoolContextProvider, useVirtualizer, type EditorFactory, type FileContents, type FileOptions } from '@pierre/diffs/react'
import type { VirtualizedFile } from '@pierre/diffs'
import { Editor } from '@pierre/diffs/edit'
import DiffWorker from '@pierre/diffs/worker/worker.js?worker'
import type { FileNavigation, SourceFile } from '@/lib/review'

const poolOptions = { workerFactory: () => new DiffWorker(), poolSize: 2 }
const highlighterOptions = { preferredHighlighter: 'shiki-js' as const }
const options = {
  themeType: 'dark' as const,
  theme: 'github-dark',
  disableFileHeader: true,
  preferredHighlighter: 'shiki-js' as const,
}
const createEditor: EditorFactory<undefined, undefined> = (type, options, key) => new Editor(type, options, key)

const WrapContext = createContext(true)

export function CodeProvider({ children, wrap = true }: { children: ReactNode; wrap?: boolean }) {
  return (
    <WrapContext.Provider value={wrap}>
      <WorkerPoolContextProvider poolOptions={poolOptions} highlighterOptions={highlighterOptions}>
        <EditProvider createEditor={createEditor}>{children}</EditProvider>
      </WorkerPoolContextProvider>
    </WrapContext.Provider>
  )
}

export function unavailable(file: SourceFile) {
  return file.too_large ? 'This file exceeds the 1 MiB source limit.' : file.binary ? 'Binary files, symlinks and submodules are not shown as source.' : ''
}

export function SourceView({ file, edit = false, location, onChange }: { file: SourceFile; edit?: boolean; location?: FileNavigation; onChange?: (content: string) => void }) {
  const source = useMemo(
    () => ({
      name: file.path,
      contents: file.content ?? '',
      cacheKey: file.sha256 ? `${file.path}:${file.sha256}` : undefined,
    }),
    [file],
  )
  const reason = unavailable(file)
  if (reason || !file.exists) return <p className="p-4 text-sm text-muted-foreground">{reason || 'This file no longer exists.'}</p>
  return (
    <Virtualizer className="min-h-0 flex-1 overflow-auto">
      <NavigableSource source={source} edit={edit} location={location} onChange={onChange} />
    </Virtualizer>
  )
}

function NavigableSource({ source, edit, location, onChange }: { source: FileContents; edit: boolean; location?: FileNavigation; onChange?: (content: string) => void }) {
  const overflow = useContext(WrapContext) ? 'wrap' : 'scroll'
  const virtualizer = useVirtualizer()
  const applied = useRef<number>(undefined)
  const frame = useRef<number>(undefined)
  const editor = useRef<Editor<'file', undefined, undefined>>(undefined)
  const range = useMemo(() => {
    if (!location?.line) return undefined
    let lines = 1
    for (let i = source.contents.indexOf('\n'); i >= 0; i = source.contents.indexOf('\n', i + 1)) lines++
    const start = Math.min(location.line, lines)
    return { start, end: Math.max(start, Math.min(location.endLine ?? start, lines)) }
  }, [source.contents, location?.line, location?.endLine])
  // Cancel the old request before the child's render callback queues its next
  // reveal. A passive-effect cleanup would cancel the new request instead.
  useLayoutEffect(
    () => () => {
      if (frame.current !== undefined) cancelAnimationFrame(frame.current)
    },
    [location?.request],
  )
  const reveal = useCallback<NonNullable<FileOptions<undefined, undefined>['onPostRender']>>(
    (node, instance, phase) => {
      if (phase === 'unmount' || !location || !range || !virtualizer || applied.current === location.request) return
      if (frame.current !== undefined) cancelAnimationFrame(frame.current)
      frame.current = requestAnimationFrame(() => {
        const position = (instance as VirtualizedFile).getLinePosition(range.start)
        if (!position) return
        applied.current = location.request
        // Source loading changes the scroll height before ResizeObserver fires.
        virtualizer.markDOMDirty()
        virtualizer.scrollTo({ top: virtualizer.getOffsetInScrollContainer(node) + position.top - 32 })
        if (edit) editor.current?.focus({ lineNumber: range.start, character: (location.column ?? 1) - 1 })
      })
    },
    [location, range, virtualizer, edit],
  )
  return (
    <File
      file={source}
      options={{ ...options, overflow, onPostRender: reveal }}
      selectedLines={range}
      edit={edit}
      editorOptions={{
        onAttach: instance => {
          editor.current = instance
          instance.focus({ lineNumber: range?.start ?? 'first-visible', character: (location?.column ?? 1) - 1 })
        },
      }}
      onEditComplete={() => {
        editor.current = undefined
        return 'reject'
      }}
      onEditChange={event => onChange?.(event.file.contents)}
    />
  )
}

export function DiffView({ oldFile, newFile, split }: { oldFile: SourceFile; newFile: SourceFile; split: boolean }) {
  const overflow = useContext(WrapContext) ? 'wrap' : 'scroll'
  const oldSource = useMemo(
    () =>
      oldFile.exists
        ? {
            name: oldFile.path,
            contents: oldFile.content ?? '',
            cacheKey: `${oldFile.path}:${oldFile.sha256}`,
          }
        : null,
    [oldFile],
  )
  const newSource = useMemo(
    () =>
      newFile.exists
        ? {
            name: newFile.path,
            contents: newFile.content ?? '',
            cacheKey: `${newFile.path}:${newFile.sha256}`,
          }
        : null,
    [newFile],
  )
  const reason = unavailable(oldFile) || unavailable(newFile)
  if (reason || (!oldSource && !newSource)) return <p className="p-4 text-sm text-muted-foreground">{reason || 'Neither version exists.'}</p>
  return (
    <Virtualizer className="min-h-0 flex-1 overflow-auto">
      <MultiFileDiff
        {...(oldSource ? { oldFile: oldSource, newFile: newSource } : { oldFile: null, newFile: newSource! })}
        options={{ ...options, overflow, diffStyle: split ? 'split' : 'unified' }}
      />
    </Virtualizer>
  )
}
