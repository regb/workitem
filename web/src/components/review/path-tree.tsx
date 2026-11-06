import { useEffect, useMemo, useRef } from 'react'
import { FileTree, useFileTree } from '@pierre/trees/react'
import type { FileNavigation, Review } from '@/lib/review'

export function PathTree({
  paths,
  target,
  selectedPath,
  changes,
  expanded = false,
  onSelect,
}: {
  paths: string[]
  target?: FileNavigation
  selectedPath?: string
  changes?: Review['files']
  expanded?: boolean
  onSelect: (path: string) => void
}) {
  const selecting = useRef(false)
  const revealed = useRef<number>(undefined)
  const callback = useRef(onSelect)
  callback.current = onSelect
  const statuses = useRef(new Map<string, string>())
  statuses.current = useMemo(() => new Map(changes?.map(file => [file.path, file.change]) ?? []), [changes])
  const { model } = useFileTree({
    paths,
    initialExpansion: expanded ? 'open' : 1,
    onSelectionChange: selected => {
      const path = selected[0]
      if (!selecting.current && path && !model.getItem(path)?.isDirectory()) callback.current(path)
    },
    renderRowDecoration: ({ row }) => {
      const change = statuses.current.get(row.path)
      if (!change) return null
      const color = change === 'added' ? '#86efac' : change === 'deleted' ? '#fca5a5' : change === 'conflicted' ? '#fbbf24' : '#93c5fd'
      const text = change === 'conflicted' ? '!' : change[0].toUpperCase()
      return { text, title: change, parts: [{ text, color }] }
    },
    unsafeCSS: ':host { color-scheme: dark; --trees-font-size: 12px; }',
  })
  useEffect(() => {
    model.resetPaths(paths)
  }, [model, paths])
  useEffect(() => {
    if (!selectedPath) return
    selecting.current = true
    try {
      model.getItem(selectedPath)?.select()
    } finally {
      selecting.current = false
    }
  }, [model, paths, selectedPath])
  useEffect(() => {
    if (!target || revealed.current === target.request) return
    if (target.path === '.') {
      const first = model.getVisibleRows(0, 1)[0]
      if (first) model.scrollToPath(first.path, { focus: false, offset: 'top' })
      revealed.current = target.request
      return
    }
    const item = model.getItem(target.path)
    if (!item) return
    selecting.current = true
    try {
      const parts = target.path.split('/')
      for (let i = 1; i < parts.length; i++) {
        const parent = model.getItem(parts.slice(0, i).join('/'))
        if (parent && 'expand' in parent) parent.expand()
      }
      if ('expand' in item) item.expand()
      item.select()
      model.scrollToPath(item.getPath(), { focus: false, offset: 'nearest' })
      revealed.current = target.request
    } finally {
      selecting.current = false
    }
  }, [model, paths, target])
  return <FileTree model={model} className="h-full min-h-16" />
}
