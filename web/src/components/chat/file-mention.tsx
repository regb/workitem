import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { repositories, workspaces } from '@/lib/rpc'

// mentionAt finds the @-mention being typed before the caret: an @ at the
// start or after a space, and the path typed after it so far.
export function mentionAt(text: string, caret: number): { start: number; query: string } | null {
  const match = /(^|\s)@([^\s@]*)$/.exec(text.slice(0, caret))
  return match ? { start: match.index + match[1].length, query: match[2] } : null
}

// useFileSearch searches files for an @-mention, as in Claude Code: fzf on
// the server, once typing pauses briefly, in an item's checkout or a
// repository's files; a null query searches nothing. searching holds until the
// result for what was typed is in.
// FileSource is where @-mentions find files: an item's checkout, or, for
// new work, its repository's integration branch.
export type FileSource = { item: string } | { repository: string } | undefined

export function useFileSearch(source: FileSource, query: string | null) {
  const open = !!source && query !== null
  const [settled, setSettled] = useState('')
  useEffect(() => {
    if (!open) return
    const timer = setTimeout(() => setSettled(query), 80)
    return () => clearTimeout(timer)
  }, [open, query])
  const { data, error, isFetching } = useQuery({
    queryKey: ['files', source, settled],
    queryFn: async () => (source && 'item' in source ? await workspaces.searchWorkspaceFiles({ itemId: source.item, query: settled }) : await repositories.searchRepositoryFiles({ repositoryId: source!.repository, query: settled })).paths,
    enabled: open,
    staleTime: 10_000,
    retry: false,
  })
  return { files: open ? (data ?? []) : [], error: open ? (error as Error | null)?.message : undefined, searching: open && (settled !== query || isFetching) }
}
