import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { ArrowUpIcon, LoaderIcon } from 'lucide-react'
import { toast } from 'sonner'
import { MentionField } from '@/components/chat/mention-field'
import { useConfirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { startWork, useCreatePicks } from '@/lib/create'
import { drafts } from '@/lib/drafts'
import { useCreations, useWorkList } from '@/lib/queries'
import { startupProblems, ui } from '@/lib/ui-state'
import { cn } from '@/lib/utils'
import { AgentPicker, ProjectPicker } from './pickers'

// QuickCreate starts new work from a task: a four-line field that grows as
// it is typed, then a row with the repository and agent profile and Start.
// The agent works in the background, and the box clears for the next task.
export function QuickCreate({ onCreated, className }: { onCreated?: () => void; className?: string }) {
  const navigate = useNavigate()
  const confirm = useConfirm()
  const { projects, agents, project, setProject, agent, setAgent, loading } = useCreatePicks()
  const unknown = ui.use(state => state.creationUnknown)
  const [prompt, setPrompt] = useState(drafts.task)
  const [creating, setCreating] = useState(false)
  const [retained, setRetained] = useState<string>()
  const input = useRef<HTMLTextAreaElement>(null)
  const change = (value: string) => {
    drafts.task = value
    setPrompt(value)
  }
  // Grow with the text where the browser does not size fields to content.
  useLayoutEffect(() => {
    const field = input.current
    if (!field || CSS.supports('field-sizing', 'content')) return
    field.style.height = 'auto'
    field.style.height = `${field.scrollHeight}px`
  }, [prompt])

  const locked = creating || unknown
  const ready = !!project && !!agent && !!prompt.trim() && !locked
  const submit = async () => {
    if (!ready) return
    const request = { project_id: project, agent, prompt: prompt.trim() }
    setCreating(true)
    setRetained(undefined)
    const outcome = await startWork(request, id => navigate({ to: '/items/$id', params: { id } }))
    setCreating(false)
    if (outcome.ok || outcome.retained) change(drafts.task.trim() === request.prompt ? '' : drafts.task)
    setRetained(outcome.retained)
    if (outcome.ok) onCreated?.()
  }
  const allowRetry = async () => {
    const ok = await confirm({
      title: 'Allow another attempt?',
      description: 'An item may already exist. Check the work list first; retrying could create a duplicate.',
      action: 'I checked the list',
    })
    if (ok) ui.set(state => ({ ...state, creationUnknown: false }))
  }

  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <form
        className="relative flex flex-col rounded-xl border bg-card shadow-lg shadow-black/30 focus-within:border-ring"
        onSubmit={event => {
          event.preventDefault()
          void submit()
        }}
      >
        {/* @ mentions search the chosen project's files: new work starts
            from its integration branch. */}
        <MentionField
          ref={input}
          aria-label="New task"
          placeholder={loading ? 'Loading…' : 'Ask an agent to do something. It starts right away in the chosen project.'}
          value={prompt}
          onValueChange={change}
          onSubmit={() => void submit()}
          files={project ? `/api/projects/${project}/files` : ''}
          box="px-3.5 pt-3 pb-1 text-base md:text-sm"
          rows={4}
          disabled={locked}
          className="max-h-60 min-h-[7rem] resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 md:min-h-[6rem] dark:bg-transparent"
        />
        <div className="flex items-center justify-end gap-1 px-2 pb-2">
          <ProjectPicker projects={projects} value={project} disabled={locked || loading} onChange={setProject} />
          <AgentPicker agents={agents} value={agent} onChange={setAgent} disabled={locked || loading} />
          <Button type="submit" size="sm" disabled={!ready} className="ms-1">
            {creating ? <LoaderIcon className="animate-spin" /> : <ArrowUpIcon />} Start
          </Button>
        </div>
      </form>
      {!loading && !projects.length && <p className="text-xs text-amber-300">No registered repositories yet. Register one with wi repo add NAME --from SOURCE.</p>}
      {!loading && !agents.length && <p className="text-xs text-amber-300">No configured agent profiles are available.</p>}
      {retained && (
        <p className="text-xs">
          Saved, but startup needs attention.{' '}
          <Link to="/items/$id" params={{ id: retained }} className="underline">
            View conversation
          </Link>
        </p>
      )}
      {unknown && (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-amber-500/40 px-3 py-2 text-xs">
          <span>The creation result is unknown. An item may already exist; check the list before trying again.</span>
          <Button type="button" variant="outline" size="xs" onClick={allowRetry}>
            I checked; allow retry
          </Button>
        </div>
      )}
    </div>
  )
}

// LaunchProblems tells, once, about a background start that failed: a toast
// that opens the item. The dashboard shows it, as the create box is hidden on
// phones until it is opened. The item itself stays listed with its problem.
const toldProblems = new Set<string>()
const problemNewFor = 10 * 60_000

export function LaunchProblems() {
  const navigate = useNavigate()
  const { data: list } = useWorkList()
  const { data: jobs = [] } = useCreations()
  const selected = ui.use(state => state.selectedProjects)
  useEffect(() => {
    for (const job of startupProblems(list, jobs, selected)) {
      if (toldProblems.has(job.id)) continue
      toldProblems.add(job.id)
      // A page loaded later does not bring back old problems.
      if (Date.now() - Date.parse(job.created_at ?? '') > problemNewFor) continue
      toast.error(job.title, { description: job.error, duration: 15_000, action: { label: 'View', onClick: () => void navigate({ to: '/items/$id', params: { id: job.id } }) } })
    }
  }, [list, jobs, selected, navigate])
  return null
}
