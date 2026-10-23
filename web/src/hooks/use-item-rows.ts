import { useMemo } from 'react'
import type { ListItem } from '@/lib/api'
import { useWorkList } from '@/lib/queries'

// ItemRow is a work list item with what lists of items show beside it: its
// dashboard section, which is also its status, and its project's name.
export type ItemRow = { row: ListItem; status: string; project: string }

// useItemRows lists the work items in dashboard order, leaving one out.
export function useItemRows(exclude = ''): ItemRow[] {
  const { data: list } = useWorkList()
  return useMemo(() => {
    const names = new Map(list?.projects.map(project => [project.id, project.name]))
    return (list?.display_sections ?? []).flatMap(section => section.items.filter(row => row.id !== exclude).map(row => ({ row, status: section.name, project: names.get(row.project_id) ?? '' })))
  }, [list, exclude])
}

// matchesItem reports whether every word of the query appears in the item's
// title, slug, project or labels.
export function matchesItem(query: string, { row, project }: ItemRow) {
  const text = [row.title, row.slug, project, ...(row.labels ?? [])].join(' ').toLowerCase()
  return query.trim().toLowerCase().split(/\s+/).every(word => text.includes(word))
}
