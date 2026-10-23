import { useState } from 'react'
import { PlusIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { addToBacklog, useCreatePicks } from '@/lib/create'
import { ui } from '@/lib/ui-state'
import { ProjectPicker } from './pickers'

// BacklogAdd saves work for later: a title, an optional description behind
// a button, and the repository. Nothing starts.
export function BacklogAdd() {
  const { projects, project, setProject, loading } = useCreatePicks()
  const unknown = ui.use(state => state.creationUnknown)
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [describing, setDescribing] = useState(false)
  const [saving, setSaving] = useState(false)
  const locked = saving || unknown
  // A project is always picked while any is registered.
  const ready = !!project && !!title.trim() && !locked

  const submit = async () => {
    if (!ready) return
    setSaving(true)
    const added = await addToBacklog({ project_id: project, title: title.trim(), description: description.trim() || undefined })
    setSaving(false)
    if (added) {
      setTitle('')
      setDescription('')
      setDescribing(false)
    }
  }

  return (
    <form
      className="flex flex-col gap-1 rounded-lg border border-dashed border-border/70 p-1 focus-within:border-ring"
      onSubmit={event => {
        event.preventDefault()
        void submit()
      }}
    >
      <div className="flex flex-wrap items-center gap-1">
        <Input
          aria-label="Backlog title"
          placeholder={loading ? 'Loading…' : 'Add to the backlog…'}
          value={title}
          maxLength={200}
          disabled={locked}
          onChange={event => setTitle(event.target.value)}
          className="h-8 min-w-[min(100%,14rem)] flex-1 border-0 bg-transparent shadow-none focus-visible:ring-0 dark:bg-transparent"
        />
        <div className="ms-auto flex items-center gap-0.5">
          {!describing && (
            <Button type="button" variant="ghost" size="xs" className="text-muted-foreground" onClick={() => setDescribing(true)}>
              <PlusIcon /> Description
            </Button>
          )}
          <ProjectPicker projects={projects} value={project} disabled={locked || loading} onChange={setProject} />
          <Button type="submit" variant="secondary" size="xs" disabled={!ready}>
            Add
          </Button>
        </div>
      </div>
      {describing && (
        <Textarea
          aria-label="Backlog description"
          placeholder="Description (optional)"
          value={description}
          autoFocus
          rows={3}
          disabled={locked}
          onChange={event => setDescription(event.target.value)}
          onKeyDown={event => {
            if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
              event.preventDefault()
              void submit()
            }
          }}
          className="max-h-60 min-h-16 resize-none border-0 bg-transparent px-2.5 shadow-none focus-visible:ring-0 dark:bg-transparent"
        />
      )}
    </form>
  )
}
