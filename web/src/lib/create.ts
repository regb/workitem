import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { AgentMode } from '@/gen/wi/core/v1/common_pb'
import type { AccessLevel } from '@/gen/wi/core/v1/items_pb'
import { drafts } from './drafts'
import { refreshList, useAgents, useProjects } from './queries'
import { awaitJob, items, lifecycle, partialItem, refused, rpcMessage } from './rpc'
import { newClientID } from './session'
import { clearError, reportError, setLastProject, ui } from './ui-state'

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

// requestKeys are idempotency keys by request: a retry of the same request
// reuses its key, so the daemon answers with the item it already created
// instead of creating another. A key is dropped once its item is created.
const requestKeys = new Map<string, string>()
const requestKey = (body: object) => {
  const id = JSON.stringify(body)
  let key = requestKeys.get(id)
  if (!key) requestKeys.set(id, (key = newClientID()))
  return { id, key }
}

// startWork creates an item from a task and starts its agent in the
// background: the daemon runs the start as a job, and the item shows it
// starting meanwhile. A failed start is told once, here, and the item shows
// it as a problem. A lost response locks further creation until the
// operator checks the list.
// grants are levels the writer chose for items the prompt mentions, beyond
// the read access the prompt gives: they are granted once the item exists.
export async function startWork(request: { project_id: string; agent: string; prompt: string; model?: string; reasoning?: string }, open: (id: string) => void, grants: Record<string, AccessLevel> = {}): Promise<{ ok: boolean; retained?: string }> {
  clearError()
  const { id, key } = requestKey({ kind: 'start', ...request })
  const agentSettings: Record<string, string> = {}
  if (request.model) agentSettings.model = request.model
  if (request.reasoning) agentSettings.reasoning = request.reasoning
  try {
    const out = await lifecycle.createAndStartItem({
      item: { repositoryId: request.project_id, description: request.prompt, generateTitle: true, agentProfile: request.agent, agentSettings, requestId: `web-${key}` },
      start: { prompt: request.prompt, mode: AgentMode.HEADLESS, clientMessageId: `web-${key}-prompt` },
    })
    requestKeys.delete(id)
    setLastProject(request.project_id)
    const item = out.item!
    for (const [onItemId, level] of Object.entries(grants)) {
      await items.grantAccess({ subject: `item:${item.id}`, onItemId, level }).catch(error => reportError(rpcMessage(error)))
    }
    toast.success('Work item started', { description: 'The agent works in the background.', duration: 6000, action: { label: 'Open', onClick: () => open(item.id) } })
    void refreshList()
    awaitJob(out.job)
      .then(job => warnCreation(job?.warnings))
      .catch(error => toast.error(item.title || 'The agent did not start', { description: rpcMessage(error), duration: 15_000, action: { label: 'View', onClick: () => open(item.id) } }))
      .finally(() => void refreshList())
    return { ok: true }
  } catch (error) {
    if (!refused(error)) ui.set(state => ({ ...state, creationUnknown: true }))
    reportError(rpcMessage(error))
    return { ok: false, retained: partialItem(error) }
  }
}

// addToBacklog saves a titled item without starting anything.
export async function addToBacklog(request: { project_id: string; title: string; description?: string }): Promise<boolean> {
  clearError()
  const { id, key } = requestKey({ kind: 'backlog', ...request })
  try {
    const out = await items.createItem({ repositoryId: request.project_id, title: request.title, description: request.description ?? '', requestId: `web-${key}` })
    requestKeys.delete(id)
    setLastProject(request.project_id)
    toast.success('Added to the backlog')
    warnCreation(out.warnings)
    void refreshList()
    return true
  } catch (error) {
    if (!refused(error)) ui.set(state => ({ ...state, creationUnknown: true }))
    reportError(rpcMessage(error))
    return false
  }
}

// warnCreation shows what creation reported without failing, such as the
// project's main branch not following its upstream: the item starts from
// the local branch.
function warnCreation(warnings: string[] | undefined) {
  if (warnings?.length) toast.warning('Created with warnings', { description: warnings.join('\n'), duration: 15_000 })
}
