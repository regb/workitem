import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { ApiError, api, type Creation, type Outcome } from './api'
import { drafts } from './drafts'
import { useAgents, useProjects } from './queries'
import { clearError, reportError, setLastProject, setLaunching, ui } from './ui-state'

// useCreatePicks chooses the repository and agent profile for new work. The
// project is always set: the last one chosen in this browser (by a picker, a
// dashboard filter or creating work), else the first. Every create box shows
// the same one.
export function useCreatePicks() {
  const { data: projects = [], isPending: loadingProjects } = useProjects()
  const { data: agents = [], isPending: loadingAgents } = useAgents()
  const last = ui.use(state => state.lastProject)
  const project = projects.some(p => p.id === last) ? last : (projects[0]?.id ?? '')
  const setProject = setLastProject
  const [agent, setAgent] = useState(drafts.agent)
  useEffect(() => {
    drafts.agent = agent
  }, [agent])
  useEffect(() => {
    if (!agent || !agents.some(a => a.name === agent)) setAgent(agents[0]?.name ?? '')
  }, [agents, agent])
  return { projects, agents, project, setProject, agent, setAgent, loading: loadingProjects || loadingAgents }
}

// startWork creates an item from a task and starts its agent in the
// background. It resolves to the outcome; a lost response or an uncertain
// commit locks further creation until the operator checks the list.
export async function startWork(request: { project_id: string; agent: string; prompt: string }, open: (id: string) => void): Promise<{ ok: boolean; retained?: string }> {
  clearError()
  try {
    const receipt = await api<Creation>('/api/items', request)
    setLastProject(request.project_id)
    setLaunching(receipt.id, true)
    toast.success('Work item started', { description: 'The agent works in the background.', duration: 6000, action: { label: 'Open', onClick: () => open(receipt.id) } })
    warnCreation(receipt.warnings)
    return { ok: true }
  } catch (error) {
    const outcome = error instanceof ApiError ? error.outcome : undefined
    if (!outcome || outcome.creation_uncertain) ui.set(state => ({ ...state, creationUnknown: true }))
    reportError((error as Error).message)
    return { ok: false, retained: outcome?.observed?.id }
  }
}

// addToBacklog saves a titled item without starting anything.
export async function addToBacklog(request: { project_id: string; title: string; description?: string }): Promise<boolean> {
  clearError()
  try {
    const out = await api<Outcome>('/api/backlog', request)
    setLastProject(request.project_id)
    toast.success('Added to the backlog')
    warnCreation(out.warnings)
    return true
  } catch (error) {
    const outcome = error instanceof ApiError ? error.outcome : undefined
    if (!outcome || outcome.creation_uncertain) ui.set(state => ({ ...state, creationUnknown: true }))
    reportError((error as Error).message)
    return false
  }
}

// warnCreation shows what creation reported without failing, such as the
// project's main branch not following its upstream: the item starts from
// the local branch.
function warnCreation(warnings: string[] | undefined) {
  if (warnings?.length) toast.warning('Created with warnings', { description: warnings.join('\n'), duration: 15_000 })
}
