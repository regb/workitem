import { toast } from 'sonner'
import type { Choice, ConfirmOptions } from '@/components/confirm'
import { api, ApiError, headers, itemAction, type AgentSettings, type ControlView, type ListItem, type Outcome, type SettingsResult } from './api'
import { refreshChat, refreshList, refreshSettings } from './queries'
import { newClientID } from './session'
import { clearError, reportError, setClosing, setLaunching, setWaiting } from './ui-state'

type Confirm = (options: ConfirmOptions) => Promise<boolean>
type Choose = (options: ConfirmOptions & { secondary: string }) => Promise<Choice>

const warnings = (out: Outcome) => (out.warnings?.length ? { description: out.warnings.join('\n') } : undefined)

// run performs one item action: it clears the previous error, reports
// success as a toast and failure in the error bar, then re-reads the item.
async function run(id: string, action: string, body: unknown, progress: string, done: string) {
  clearError()
  const pending = toast.loading(progress)
  try {
    const out = await itemAction(id, action, body)
    toast.success(done, { id: pending, ...warnings(out) })
    return true
  } catch (error) {
    toast.dismiss(pending)
    reportError((error as Error).message)
    return false
  } finally {
    void refreshChat(id)
    void refreshList()
  }
}

export type Approval = 'allow' | 'deny'

// withApproval runs an action that may need the operator's approval first,
// such as trusting a worktree's .envrc. The server asks instead of deciding;
// this shows the question and repeats the action with the answer. Dismissing
// it abandons the action.
async function withApproval<T>(choose: Choose, act: (approval?: Approval) => Promise<T>): Promise<T> {
  try {
    return await act()
  } catch (error) {
    const approval = error instanceof ApiError ? error.outcome?.approval : undefined
    if (!approval) throw error
    const choice = await choose({ title: approval.question, description: approval.path ? `The agent's environment would load ${approval.path}, running it as a shell script.` : undefined, action: 'Allow', secondary: 'Start without it' })
    if (!choice) throw new ApiError('Not started.', { error: 'Not started.' })
    return act(choice === 'primary' ? 'allow' : 'deny')
  }
}

// startItem starts a backlog item like wi new --start: its agent receives
// the configured startup message and reads the item's description itself.
export async function startItem(choose: Choose, id: string) {
  clearError()
  setLaunching(id, true)
  const client_id = newClientID()
  try {
    const out = await withApproval(choose, approval => itemAction(id, 'start', { client_id, approval }))
    if (out.warnings?.length) toast.info('Started.', warnings(out))
    return true
  } catch (error) {
    reportError((error as Error).message)
    return false
  } finally {
    setLaunching(id, false)
    void refreshChat(id)
    void refreshList()
  }
}

export type Resolution = 'completed' | 'abandoned'

// closeCheck reads, from the item's list row, whether closing needs the
// operator first. Uncommitted changes refuse to close; a busy agent is
// aborted; a checkout problem may refuse. A safe item closes at once, and the
// server checks again either way.
export function closeCheck(row: ListItem | undefined): { refuse?: string; warn: string[]; force: boolean } {
  if (row?.worktree?.dirty) return { refuse: 'Its checkout has uncommitted changes. Ask the agent to commit or discard them, then close it.', warn: [], force: false }
  const force = row?.agent?.status === 'busy'
  const warn: string[] = []
  if (force) warn.push('The agent is working: its turn is aborted and the agent is stopped.')
  if (row?.worktree?.status === 'problem' || row?.worktree?.branch_mismatch) warn.push(`The checkout needs attention${row.worktree.reason ? ` (${row.worktree.reason})` : ''}; closing may be refused.`)
  return { warn, force }
}

// closeItem closes an item in the background and returns at once: false if
// the operator cancelled, true once closing has started. The item leaves the
// lists meanwhile and comes back if closing fails; onOpen reopens it from the
// failure notice.
export async function closeItem(confirm: Confirm, id: string, title: string, row: ListItem | undefined, resolution: Resolution = 'completed', onOpen?: () => void) {
  const check = closeCheck(row)
  if (check.refuse) {
    await confirm({ title: `${title} cannot close yet`, description: check.refuse, action: '', cancel: 'OK' })
    return false
  }
  if (check.warn.length && !(await confirm({ title: `Close ${title}?`, points: check.warn, action: check.force ? 'Stop and close' : 'Close', destructive: check.force }))) return false
  clearError()
  setClosing(id, true)
  itemAction(id, 'close', { resolution, force: check.force })
    // Its warnings only narrate the agent stopping; failures carry what matters.
    .then(() => toast.success(resolution === 'completed' ? `Done: ${title}` : `Closed as ${resolution}: ${title}`))
    .catch((error: Error) => {
      setClosing(id, false)
      toast.error(`Could not close ${title}`, { description: error.message, duration: 15_000, ...(onOpen ? { action: { label: 'Open', onClick: onOpen } } : {}) })
    })
    .finally(() => {
      void refreshList()
      void refreshChat(id)
    })
  return true
}

// nextItem asks for the next item needing attention in the selected projects,
// other than the one being left; '' when there is none.
export async function nextItem(leaving: string, selected: Set<string> | null) {
  const query = new URLSearchParams()
  if (selected !== null) {
    for (const project of selected) query.append('project', project)
    if (!selected.size) query.append('project', '')
  }
  query.set('current', leaving)
  try {
    const next = await api<{ id: string }>(`/api/next?${query}`)
    return next.id !== leaving ? next.id : ''
  } catch {
    return ''
  }
}

export async function shelveItem(confirm: Confirm, id: string, title: string) {
  const ok = await confirm({
    title: `Shelve ${title}?`,
    description: 'Wi will stop an idle agent and release its checkout. The item returns to the backlog.',
    action: 'Shelve',
  })
  return ok && run(id, 'shelve', {}, 'Shelving…', 'Shelved.')
}

// readMarks remembers, per item, the history leaf last marked read, so a
// conversation marked unread is not marked read again until it changes.
export const readMarks = new Map<string, string>()

export function markUnread(id: string, leaf: string) {
  readMarks.set(id, leaf)
  return run(id, 'read', { unread: true }, 'Marking unread…', 'Marked unread.')
}
export const markRead = (id: string) => itemAction(id, 'read', { unread: false }).catch(() => {})
export const releaseControl = (id: string) => itemAction(id, 'release').catch(() => {})
// releaseOnExit gives control back as the tab closes, so another tab need not
// wait for the lease to expire; keepalive lets the request outlive the page.
export const releaseOnExit = (id: string) => void fetch(`/api/items/${id}/release`, { method: 'POST', headers: headers(true), body: '{}', keepalive: true }).catch(() => {})

// waitItem makes an item wait in the background and returns at once, like
// closeItem: the caller moves on to the next item while the item leaves the
// attention count, and a failure reports itself with a way back.
export function waitItem(id: string, title: string, request: Record<string, string>, onOpen?: () => void) {
  clearError()
  setWaiting(id, true)
  itemAction(id, 'blockers', request)
    .then(() => toast.success(`Waiting: ${title}`))
    .catch((error: Error) => toast.error(`Could not set ${title} waiting`, { description: error.message, duration: 15_000, ...(onOpen ? { action: { label: 'Open', onClick: onOpen } } : {}) }))
    .finally(async () => {
      await refreshList()
      setWaiting(id, false)
      void refreshChat(id)
    })
}
export const clearBlocker = (id: string, blockerID: string) => run(id, 'clear-blocker', { blocker_id: blockerID }, 'Clearing…', 'Stopped waiting on that.')

// stopTurn aborts the agent's current turn. When another client controls the
// agent it asks to take over first, as sending does.
export async function stopTurn(confirm: Confirm, id: string) {
  clearError()
  try {
    // A confirmed stop takes control without starting a runtime.
    await withTakeover(confirm, 'Take control of this agent and stop it?', confirmed => itemAction(id, 'stop', confirmed ? { confirm: true } : {}))
    return true
  } catch (error) {
    reportError((error as Error).message)
    return false
  } finally {
    void refreshChat(id)
  }
}

// withTakeover runs an action that needs control of the agent. When another
// client holds it, this asks to take over, then runs the action again as
// confirmed; each action knows how a confirmed attempt takes control.
async function withTakeover<T>(confirm: Confirm, title: string, act: (confirmed: boolean) => Promise<T>): Promise<T> {
  try {
    return await act(false)
  } catch (error) {
    const control = error instanceof ApiError ? error.outcome?.control : undefined
    if (!control?.plan.needs_confirmation || !(await confirmTakeover(confirm, control, title))) throw error
    return act(true)
  }
}

// confirmTakeover asks before an interrupting takeover and lists what it does.
export function confirmTakeover(confirm: Confirm, control: ControlView | undefined, title: string) {
  const plan = control?.plan
  if (!plan?.needs_confirmation) return Promise.resolve(true)
  return confirm({ title, points: plan.reasons ?? [], action: 'Take control', destructive: true })
}

// takeoverEffect says, in one sentence, what the other client loses.
export function takeoverEffect(control: ControlView | undefined) {
  const plan = control?.plan
  if (control?.status.holder === 'terminal') {
    if (plan?.replace) return `The agent running in the terminal is stopped and restarted here in ${plan.replace} mode on the same conversation. The terminal no longer shows it until someone runs \`wi switch --take\`, which restarts it there.`
    return 'The tmux pane becomes read-only: typing there stops reaching the agent until someone runs `wi switch --take`. Anyone attached keeps seeing the output.'
  }
  return 'The other tab becomes read-only until it takes control back.'
}

// takeControl always asks first, explaining what the other client loses and
// what the takeover does to a running turn or runtime.
export async function takeControl(confirm: Confirm, id: string, control: ControlView | undefined) {
  const ok = await confirm({
    title: control?.status.holder === 'terminal' ? 'Take control from the terminal?' : 'Take control from the other browser tab?',
    description: takeoverEffect(control),
    points: control?.plan.reasons ?? [],
    action: 'Take control',
    destructive: true,
  })
  return ok && run(id, 'control', { confirm: true }, 'Taking control…', 'You control this agent.')
}

// steer adds a message to the turn in progress; follow_up waits for it.
export type Delivery = 'prompt' | 'steer' | 'follow_up'
export type MessageRequest = { text: string; images: { mime_type: string; data: string }[]; delivery: Delivery; runtime_id: string; client_id: string; approval?: Approval }

// sendMessage delivers once. When another client controls the agent it asks
// to take over, then delivers to the runtime the takeover produced.
export async function sendMessage(confirm: Confirm, choose: Choose, id: string, request: MessageRequest) {
  await withApproval(choose, approval =>
    withTakeover(confirm, 'Take control of this agent and send?', async confirmed => {
      let runtime_id = request.runtime_id
      if (confirmed) {
        const taken = await itemAction<Outcome<{ runtime?: { id?: string } }>>(id, 'control', { confirm: true })
        runtime_id = taken.result?.runtime?.id ?? ''
      }
      return itemAction(id, 'messages', { ...request, approval, runtime_id })
    }),
  )
}

// changeSettings saves an item's model and reasoning settings. A running
// agent switches live where it can; otherwise this asks whether to restart
// it now on the same conversation or apply on its next start. Changing a
// running agent needs control, so another controller prompts a takeover.
export async function changeSettings(confirm: Confirm, choose: Choose, id: string, settings: Record<string, string>, view: AgentSettings) {
  clearError()
  let restart = false
  const needsRestart = Object.keys(settings).some(name => !view.live.includes(name))
  if (view.runtime_online && needsRestart) {
    const choice = await choose({
      title: 'Restart the agent to switch now?',
      description: `This ${view.runtime_mode ?? ''} runtime cannot change these settings while it runs. Restarting resumes the same conversation with them; the agent must be idle.`,
      action: 'Restart now',
      secondary: 'On next start',
    })
    if (!choice) return false
    restart = choice === 'primary'
  }
  const pending = toast.loading('Saving…')
  const request = { settings, restart }
  try {
    const result = await withTakeover(confirm, 'Take control of this agent to change its settings?', async confirmed => {
      if (confirmed) await itemAction(id, 'control', { confirm: true })
      return itemAction<Outcome<SettingsResult>>(id, 'settings', request)
    })
    const outcome = result.result
    const message = !outcome?.changed.length ? 'Settings unchanged.' : outcome.restarted ? 'Restarted with the new settings.' : outcome.pending.length ? 'Saved; applies when the agent next starts.' : view.runtime_online ? 'Switched.' : 'Saved.'
    toast.success(message, { id: pending, ...(result.warnings?.length ? { description: result.warnings.join('\n') } : {}) })
    return true
  } catch (error) {
    toast.dismiss(pending)
    reportError((error as Error).message)
    return false
  } finally {
    void refreshSettings(id)
    void refreshChat(id)
  }
}
