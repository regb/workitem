import { useId, useState } from 'react'
import { XIcon } from 'lucide-react'
import { useCompletionList } from '@/components/completion-list'
import { Badge } from '@/components/ui/badge'
import type { Project, WorkList } from '@/lib/api'
import { addProjectFilter, removeProjectFilter, ui } from '@/lib/ui-state'

// ProjectFilters shows the projects the dashboard is filtered to, as badges
// with a way to remove each, and a field to add more. Clicking an item's
// project adds one; without any, the bar is gone.
export function ProjectFilters({ list }: { list: WorkList }) {
  const selected = ui.use(state => state.selectedProjects)
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)
  const id = useId().replace(/:/g, '')
  const needle = query.trim().toLowerCase()
  const options = selected ? list.projects.filter(project => !selected.has(project.id) && project.name.toLowerCase().includes(needle)) : []
  const add = (project: Project) => {
    addProjectFilter(project.id)
    setQuery('')
  }
  const projects = useCompletionList<Project>({
    id: `${id}-projects`,
    label: 'Projects',
    open,
    below: true,
    options,
    query,
    onPick: add,
    onDismiss: () => {
      setQuery('')
      setOpen(false)
    },
    render: project => <span className="truncate py-0.5 text-sm">{project.name}</span>,
  })
  if (!selected?.size) return null
  const names = new Map(list.projects.map(project => [project.id, project.name]))
  return (
    <div className="flex flex-wrap items-center gap-1.5 rounded-lg border px-2 py-1.5 focus-within:border-ring" aria-label="Project filters">
      {[...selected].map(project => (
        <Badge key={project} variant="secondary" className="gap-1 pe-1">
          {names.get(project) ?? project}
          <button type="button" aria-label={`Stop filtering to ${names.get(project) ?? project}`} className="rounded-sm p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground" onClick={() => removeProjectFilter(project)}>
            <XIcon className="size-3" />
          </button>
        </Badge>
      ))}
      <div className="relative min-w-32 flex-1">
        <input
          {...projects.field}
          aria-label="Add a project filter"
          role="combobox"
          placeholder={options.length || needle ? 'Add project…' : ''}
          value={query}
          className="w-full bg-transparent px-1 text-sm outline-none placeholder:text-muted-foreground"
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onChange={event => {
            setQuery(event.target.value)
            setOpen(true)
          }}
          onKeyDown={event => {
            if (projects.onKeyDown(event)) event.preventDefault()
            // Backspace in the empty field removes the last filter.
            else if (event.key === 'Backspace' && !query) removeProjectFilter([...selected].at(-1)!)
          }}
        />
        {projects.element}
      </div>
    </div>
  )
}
