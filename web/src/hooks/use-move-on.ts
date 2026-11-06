import { useNavigate } from '@tanstack/react-router'
import { nextItem, waitItem } from '@/lib/actions'
import { ui } from '@/lib/ui-state'

// useMoveOn leaves an item for the next one needing attention, as Done and
// Wait do: the conversation fades out and the next item in the selected
// projects opens, or the dashboard when there is none. reopen comes back to
// the item, for a failure notice.
export function useMoveOn(id: string) {
  const navigate = useNavigate()
  const selected = ui.use(state => state.selectedProjects)
  const reopen = () => void navigate({ to: '/items/$id', params: { id } })
  const goNext = async () => {
    const [next] = await Promise.all([nextItem(id, selected), new Promise(resolve => setTimeout(resolve, 180))])
    await (next ? navigate({ to: '/items/$id', params: { id: next } }) : navigate({ to: '/' }))
  }
  return { reopen, goNext }
}

// useWait is every wait action: the item waits in the background and the
// page moves on to the next item.
export function useWait(id: string, title: string) {
  const { reopen, goNext } = useMoveOn(id)
  return (request: Record<string, string>) => {
    waitItem(id, title, request, reopen)
    void goNext()
  }
}
