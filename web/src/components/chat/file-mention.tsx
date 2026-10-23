import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'

// mentionAt finds the @-mention being typed before the caret: an @ at the
// start or after a space, and the path typed after it so far.
export function mentionAt(text: string, caret: number): { start: number; query: string } | null {
  const match = /(^|\s)@([^\s@]*)$/.exec(text.slice(0, caret))
  return match ? { start: match.index + match[1].length, query: match[2] } : null
}

// useFileSearch searches files for an @-mention, as in Claude Code: fzf on
// the server, once typing pauses briefly. search is an item's or a project's
// files endpoint; a null query searches nothing. searching holds until the
// result for what was typed is in.
export function useFileSearch(search: string, query: string | null) {
  const open = !!search && query !== null
  const [settled, setSettled] = useState('')
  useEffect(() => {
    if (!open) return
    const timer = setTimeout(() => setSettled(query), 80)
    return () => clearTimeout(timer)
  }, [open, query])
  const { data, error, isFetching } = useQuery({
    queryKey: ['files', search, settled],
    queryFn: () => api<{ files: string[] }>(`${search}?${new URLSearchParams({ q: settled })}`),
    enabled: open,
    staleTime: 10_000,
    retry: false,
  })
  return { files: open ? (data?.files ?? []) : [], error: open ? (error as Error | null)?.message : undefined, searching: open && (settled !== query || isFetching) }
}
