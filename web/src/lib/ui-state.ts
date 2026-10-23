import type { Creation, WorkList } from './api'
import { createStore } from './store'

const lastProjectKey = 'wi.lastProject'

// readLastProject reads the project last chosen in this browser; storage can
// be unavailable, which reads as none.
function readLastProject() {
  try {
    return localStorage.getItem(lastProjectKey) ?? ''
  } catch {
    return ''
  }
}

// UI state shared across routes; server data lives in the query cache.
export const ui = createStore({
  // The error from the last action; it stays until the next action or
  // navigation so failures never flash by.
  actionError: '',
  // The projects the dashboard is filtered to; null when it is not filtered.
  // Clicking an item's project adds it; removing the last one clears it.
  selectedProjects: null as Set<string> | null,
  // The project new work goes to: the last one chosen or filtered to, kept
  // in this browser.
  lastProject: readLastProject(),
  // Items created in this tab whose background start is still pending.
  pendingLaunches: new Map<string, number>(),
  // Creation may have reached the daemon; retry needs an explicit check.
  creationUnknown: false,
  // Items being closed in the background; lists hide them meanwhile.
  closing: new Set<string>(),
  // Items being set waiting in the background: their conversation fades out
  // and they leave the attention count until the list shows them waiting.
  waiting: new Set<string>(),
  // Why live updates are not current, by source: the event stream itself,
  // or the server failing to read the list or the open chat. Empty when all
  // is well.
  liveProblems: {} as Partial<Record<'stream' | 'list' | 'chat', string>>,
})

// setLiveProblem records or clears why one source of live updates fails.
export function setLiveProblem(source: 'stream' | 'list' | 'chat', problem: string) {
  if ((ui.get().liveProblems[source] ?? '') === problem) return
  ui.set(state => ({ ...state, liveProblems: { ...state.liveProblems, [source]: problem || undefined } }))
}

// markItem adds an item to one of the sets of items in the background, or
// removes it.
function markItem(key: 'waiting' | 'closing', id: string, on: boolean) {
  ui.set(state => {
    const next = new Set(state[key])
    if (on) next.add(id)
    else next.delete(id)
    return { ...state, [key]: next }
  })
}

export const setWaiting = (id: string, waiting: boolean) => markItem('waiting', id, waiting)
export const setClosing = (id: string, closing: boolean) => markItem('closing', id, closing)

// setLaunching shows an item as starting until its start returns, or until
// the list shows it settled.
export const setLaunching = (id: string, launching: boolean) =>
  ui.set(state => {
    const pendingLaunches = new Map(state.pendingLaunches)
    if (launching) pendingLaunches.set(id, Date.now())
    else pendingLaunches.delete(id)
    return { ...state, pendingLaunches }
  })

// setLastProject remembers the project new work defaults to.
export function setLastProject(id: string) {
  if (!id) return
  try {
    localStorage.setItem(lastProjectKey, id)
  } catch {
    // Without storage, the choice lasts for this page.
  }
  ui.set(state => (state.lastProject === id ? state : { ...state, lastProject: id }))
}

// addProjectFilter filters the dashboard to one more project, which also
// becomes the default for new work.
export function addProjectFilter(id: string) {
  ui.set(state => ({ ...state, selectedProjects: new Set([...(state.selectedProjects ?? []), id]) }))
  setLastProject(id)
}

// removeProjectFilter drops one project filter; without any left, the
// dashboard is not filtered.
export function removeProjectFilter(id: string) {
  ui.set(state => {
    const next = new Set(state.selectedProjects)
    next.delete(id)
    return { ...state, selectedProjects: next.size ? next : null }
  })
}

export const reportError = (message: string) => ui.set(state => ({ ...state, actionError: message }))
export const clearError = () => ui.set(state => (state.actionError ? { ...state, actionError: '' } : state))

export const projectSelected = (selected: Set<string> | null, projectID: string) => selected === null || selected.has(projectID)

const starting = (job: Creation) => job.state === 'queued' || job.state === 'starting'

// startupProblems are failed background starts for items in the selected projects.
export function startupProblems(list: WorkList | undefined, jobs: Creation[], selected: Set<string> | null) {
  const visible = new Set<string>()
  for (const section of list?.display_sections ?? []) for (const row of section.items) if (projectSelected(selected, row.project_id)) visible.add(row.id)
  return jobs.filter(job => job.state === 'problem' && (!list || visible.has(job.id)))
}

// attentionCount counts items needing the operator, excluding ones still starting.
export function attentionCount(list: WorkList | undefined, jobs: Creation[], selected: Set<string> | null, pending: Map<string, number>, closing: Set<string> = new Set()) {
  const startingIDs = new Set([...pending.keys(), ...jobs.filter(starting).map(job => job.id), ...closing])
  const ids = new Set(startupProblems(list, jobs, selected).map(job => job.id))
  for (const section of list?.display_sections ?? []) {
    if (section.name !== 'NEEDS ATTENTION' && section.name !== 'NEEDS FIXING') continue
    for (const row of section.items) if (!startingIDs.has(row.id) && projectSelected(selected, row.project_id)) ids.add(row.id)
  }
  return ids.size
}
