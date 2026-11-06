import { toast } from 'sonner'
import type { Choice, ConfirmOptions } from '@/components/confirm'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { Delivery as DeliveryKind } from '@/gen/wi/core/v1/agents_pb'
import { AgentMode, ItemState, Resolution as ResolutionKind } from '@/gen/wi/core/v1/common_pb'
import { BlockerKind, ListOrder } from '@/gen/wi/core/v1/items_pb'
import type { PlanCloseResponse } from '@/gen/wi/core/v1/lifecycle_pb'
import type { AgentSettings, ControlView, WorkList } from './views'
import { keys, queryClient, refreshChat, refreshList, refreshSettings } from './queries'
import { agents, awaitJob, control, items, lifecycle, refusedControl, rpcMessage } from './rpc'
import { clientID, newClientID } from './session'
import { clearError, reportError, setClosing, setLaunching, setWaiting } from './ui-state'

type Confirm = (options: ConfirmOptions) => Promise<boolean>
type Choose = (options: ConfirmOptions & { secondary: string }) => Promise<Choice>

// warnings is what an action reported without failing, as a toast's
// description.
type Warned = { warnings?: string[] } | undefined
const warnings = (out: unknown) => {
  const told = (out as Warned)?.warnings
  return told?.length ? { description: told.join('\n') } : undefined
}

// run performs one item action: it clears the previous error, reports
// success as a toast and failure in the error bar, then re-reads the item.
// An action that continues in a job resolves once the job finished.
async function run(id: string, act: () => Promise<unknown>, progress: string, done: string) {
  clearError()
  const pending = toast.loading(progress)
  try {
    const out = await act()
    toast.success(done, { id: pending, ...warnings(out) })
    return true
  } catch (error) {
    toast.dismiss(pending)
    reportError(rpcMessage(error))
    return false
  } finally {
    void refreshChat(id)
    void refreshList()
  }
}

// answerQuestion answers the question the agent waits on, keyed by
// question text.
export function answerQuestion(id: string, toolCallID: string, answers: Record<string, string>) {
  return run(id, () => agents.answerQuestion({ itemId: id, toolCallId: toolCallID, answers }), 'Answering…', 'Answered; the agent continues.')
}

// retrySetup sets the item's checkout up again after a failed setup; its
// agent starts once setup succeeds.
export function retrySetup(id: string) {
  return run(id, () => start(id, ''), 'Setting up the workspace…', 'Workspace set up; the agent is starting.')
}

// start starts an item headless, the way a tab drives it, and waits for the
// job doing it.
async function start(id: string, clientMessageId: string) {
  const out = await lifecycle.startItem({ itemId: id, start: { mode: AgentMode.HEADLESS, clientMessageId } })
  return awaitJob(out.job)
}

// startItem starts a backlog item like wi new --start: its agent receives
// the configured startup message and reads the item's description itself.
export async function startItem(id: string) {
  clearError()
  setLaunching(id, true)
  try {
    const out = await start(id, newClientID())
    if (out?.warnings.length) toast.info('Started.', warnings(out))
    return true
  } catch (error) {
    reportError(rpcMessage(error))
    return false
  } finally {
    setLaunching(id, false)
    void refreshChat(id)
    void refreshList()
  }
}

export type Resolution = 'completed' | 'abandoned'

// ClosePlan is what the daemon says closing an item would do now.
export type ClosePlan = Pick<PlanCloseResponse, 'abortsTurn' | 'commits' | 'removesAttachedCopies' | 'attachedTerminalClients' | 'checkoutProblem'>

// closeCheck reads from the daemon's plan whether closing needs the operator
// first, and the flags that consent: a busy agent is aborted; uncommitted
// changes are committed to the item's branch; an attached terminal is
// closed; a checkout problem may refuse. Copies of attachments alone need
// nothing: the item keeps them. A safe item closes at once, and the daemon
// checks again either way.
export function closeCheck(plan: ClosePlan): { warn: string[]; force: boolean; commit: boolean; terminal: boolean } {
  const force = plan.abortsTurn
  const commit = plan.commits.length > 0
  const terminal = plan.attachedTerminalClients > 0
  const attached = plan.removesAttachedCopies
  const warn: string[] = []
  if (terminal) warn.push(attachedTerminalWarning)
  if (force) warn.push('The agent is working: its turn is aborted and the agent is stopped.')
  if (commit) warn.push(`Its checkout has uncommitted changes (${fileList(plan.commits)}): they are committed to the item's branch, where reopening the item finds them.`)
  if (plan.checkoutProblem) warn.push(`The checkout needs attention (${plan.checkoutProblem}); closing may be refused.`)
  // Said only when the dialog shows anyway: removing them loses nothing.
  if (warn.length && attached.length) warn.push(`${fileList(attached)} ${attached.length === 1 ? 'is' : 'are'} removed from the checkout, unchanged since attached: the item keeps ${attached.length === 1 ? 'it' : 'them'}.`)
  return { warn, force, commit, terminal }
}

// fileList names up to three files and counts the rest.
const fileList = (files: string[]) => (files.length <= 3 ? files.join(', ') : `${files.slice(0, 3).join(', ')} and ${files.length - 3} more`)

// closeItem closes an item in the background and returns at once: false if
// the operator cancelled, true once closing has started. The item leaves the
// lists meanwhile and comes back if closing fails; onOpen reopens it from the
// failure notice.
export async function closeItem(confirm: Confirm, id: string, title: string, resolution: Resolution = 'completed', onOpen?: () => void) {
  let plan: ClosePlan
  try {
    plan = await lifecycle.planClose({ itemId: id })
  } catch (error) {
    reportError(rpcMessage(error))
    return false
  }
  const check = closeCheck(plan)
  const action = check.commit ? 'Commit and close' : check.force ? 'Stop and close' : 'Close'
  if (check.warn.length && !(await confirm({ title: `Close ${title}?`, points: check.warn, action, destructive: check.force }))) return false
  clearError()
  setClosing(id, true)
  lifecycle
    .closeItem({ itemId: id, resolution: resolution === 'completed' ? ResolutionKind.COMPLETED : ResolutionKind.ABANDONED, force: check.force, commitChanges: check.commit, confirmAttachedTerminal: check.terminal })
    .then(out => awaitJob(out.job))
    // Its warnings only narrate the agent stopping; failures carry what matters.
    .then(() => toast.success(resolution === 'completed' ? `Done: ${title}` : `Closed as ${resolution}: ${title}`))
    .catch((error: unknown) => {
      setClosing(id, false)
      toast.error(`Could not close ${title}`, { description: rpcMessage(error), duration: 15_000, ...(onOpen ? { action: { label: 'Open', onClick: onOpen } } : {}) })
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
  if (selected !== null && !selected.size) return ''
  try {
    const out = await items.listItems({ order: ListOrder.ATTENTION, repositoryIds: selected ? [...selected] : [], states: [ItemState.ACTIVE] })
    return out.items.find(row => row.observation?.needsAttention && row.id !== leaving)?.id ?? ''
  } catch {
    return ''
  }
}

// restartAgent restarts an idle agent on the same conversation and
// checkout, so it picks up the item's settings and the repository's
// variables as they are now. Setup does not run again.
export async function restartAgent(confirm: Confirm, title: string, id: string) {
  const ok = await confirm({
    title: `Restart the agent of ${title}?`,
    description: "The agent stops and resumes the same conversation in its checkout, with the item's current settings and the repository's current environment variables. The checkout is not set up again.",
    action: 'Restart',
  })
  return ok && run(id, () => restart(id), 'Restarting the agent…', 'Restarted.')
}

// restart restarts an item's agent and waits for it.
export async function restart(id: string) {
  return awaitJob((await agents.restartAgent({ itemId: id })).job)
}

async function readTerminalClients(id: string) {
  try {
    const out = await agents.getAgent({ itemId: id })
    return out.agent?.terminal?.attachedClients ?? 0
  } catch (error) {
    reportError(rpcMessage(error))
    return undefined
  }
}

export const attachedTerminalWarning = 'A terminal is attached. Its tmux session will be closed, disconnecting anyone watching it.'

export async function shelveItem(confirm: Confirm, id: string, title: string) {
  const terminalClients = await readTerminalClients(id)
  if (terminalClients === undefined) return false
  const ok = await confirm({
    title: `Shelve ${title}?`,
    description: 'Wi will stop an idle agent and release its checkout. The item returns to the backlog.',
    points: terminalClients > 0 ? [attachedTerminalWarning] : [],
    action: 'Shelve',
  })
  const shelve = async () => {
    const out = await lifecycle.shelveItem({ itemId: id, confirmAttachedTerminal: terminalClients > 0 })
    await awaitJob(out.job)
    return out
  }
  return ok && run(id, shelve, 'Shelving…', 'Shelved.')
}

// reopenItem returns a closed item to work, like wi reopen: reopening is
// for going back to it, so its agent starts again on the same conversation
// and branch, sent nothing, waiting for the operator's message. onReopened
// runs once it has. A failed start leaves it reopened in the backlog, where
// sending a message starts it.
export async function reopenItem(id: string, onReopened?: () => void) {
  clearError()
  setLaunching(id, true)
  const pending = toast.loading('Reopening…')
  try {
    const out = await lifecycle.reopenItem({ itemId: id, start: true, mode: AgentMode.HEADLESS })
    const job = await awaitJob(out.job)
    toast.success('Reopened.', { id: pending, ...warnings(job) })
    onReopened?.()
    return true
  } catch (error) {
    toast.dismiss(pending)
    reportError(rpcMessage(error))
    return false
  } finally {
    setLaunching(id, false)
    void refreshChat(id)
    void refreshList()
  }
}

// readMarks remembers, per item, the history leaf last marked read, so a
// conversation marked unread is not marked read again until it changes.
export const readMarks = new Map<string, string>()

export function markUnread(id: string, leaf: string) {
  readMarks.set(id, leaf)
  return run(id, () => items.markRead({ itemId: id, unread: true }), 'Marking unread…', 'Marked unread.')
}
// markRead shows the item read at once, then reads the list back rather
// than wait for the stream to report it: a waiting item read leaves NEEDS
// ATTENTION.
export function markRead(id: string) {
  queryClient.setQueryData<WorkList>(
    keys.list,
    list => list && { ...list, display_sections: list.display_sections.map(section => ({ ...section, items: section.items.map(row => (row.id === id ? { ...row, unread: false } : row)) })) },
  )
  return items
    .markRead({ itemId: id, unread: false })
    .then(() => refreshList())
    .catch(() => {})
}
// leaseRenewal is how often a tab renews the control lease it holds; the
// daemon lets a lease expire 45 seconds after its last renewal.
export const leaseRenewal = 15_000

// renewControl keeps this tab's lease while the conversation stays open. A
// lease that changed or expired is not renewed: the conversation is read
// again, and shows who controls the agent now.
export const renewControl = (id: string, leaseId: string) =>
  control.renewControl({ leaseId }).catch(() => void refreshChat(id))

// releaseControl gives back this tab's control of the item, if it has it.
export const releaseControl = (id: string) => control.releaseControl({ itemId: id }).catch(() => {})
// releaseOnExit gives control back as the tab closes, so another tab need not
// wait for the lease to expire; keepalive lets the request outlive the page,
// which a client call cannot ask for.
export const releaseOnExit = (id: string) =>
  void fetch('/wi.core.v1.ControlService/ReleaseControl', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', 'X-Wi-Client': clientID },
    body: JSON.stringify({ item_id: id }),
    keepalive: true,
  }).catch(() => {})

// waitItem makes an item wait in the background and returns at once, like
// closeItem: the caller moves on to the next item while the item leaves the
// attention count, and a failure reports itself with a way back.
export type WaitRequest = { kind: 'time' | 'item' | 'external'; until?: string; item_id?: string; event?: string; follow_up?: string }
export function waitItem(id: string, title: string, request: WaitRequest, onOpen?: () => void) {
  clearError()
  setWaiting(id, true)
  const kind = { time: BlockerKind.TIME, item: BlockerKind.ITEM, external: BlockerKind.EXTERNAL }[request.kind]
  const until = request.until ? timestampFromDate(new Date(request.until)) : undefined
  items
    .addBlocker({ itemId: id, kind, until, blockingItemId: request.item_id ?? '', event: request.event ?? '', followUp: request.follow_up ?? '' })
    .then(() => toast.success(`Waiting: ${title}`))
    .catch((error: unknown) => toast.error(`Could not set ${title} waiting`, { description: rpcMessage(error), duration: 15_000, ...(onOpen ? { action: { label: 'Open', onClick: onOpen } } : {}) }))
    .finally(async () => {
      await refreshList()
      setWaiting(id, false)
      void refreshChat(id)
    })
}
// clearBlocker stops waiting on one blocker; drop also discards its
// follow-up instead of sending it.
export const clearBlocker = (id: string, blockerID: string, drop = false) =>
  run(id, () => items.clearBlocker({ itemId: id, blockerId: blockerID, clearAll: !blockerID, dropFollowUp: drop }), 'Clearing…', drop ? 'Stopped waiting; its message will not be sent.' : 'Stopped waiting on that.')

// stopTurn aborts the agent's current turn. When another client controls the
// agent it asks to take over first, as sending does.
export async function stopTurn(confirm: Confirm, id: string) {
  clearError()
  try {
    // A confirmed stop takes control without starting a runtime.
    await withTakeover(confirm, 'Take control of this agent and stop it?', async (confirmed, planToken) => {
      if (confirmed) await take(id, planToken, false)
      return agents.abortTurn({ itemId: id })
    })
    return true
  } catch (error) {
    reportError(rpcMessage(error))
    return false
  } finally {
    void refreshChat(id)
  }
}

// withTakeover runs an action that needs control of the agent. When another
// client holds it, the daemon's refusal says what taking over does: this
// asks, then runs the action again as confirmed; each action knows how a
// confirmed attempt takes control.
async function withTakeover<T>(confirm: Confirm, title: string, act: (confirmed: boolean, planToken?: string) => Promise<T>): Promise<T> {
  try {
    return await act(false)
  } catch (error) {
    const plan = refusedControl(error)?.plan
    if (!plan?.needsConfirmation || !(await confirm({ title, points: plan.reasons, action: 'Take control', destructive: true }))) throw error
    return act(true, plan.planToken)
  }
}

// take takes control of an item's agent under a confirmed plan; start also
// starts or replaces the agent this tab can drive, waiting for it.
async function take(id: string, planToken: string | undefined, start: boolean) {
  const out = await control.acquireControl({ itemId: id, clientId: clientID, confirm: true, planToken: planToken ?? '', startAgent: start })
  await awaitJob(out.job)
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
  const user = otherUser(control)
  return `${user ? `${user}'s tab` : 'The other tab'} becomes read-only until it takes control back.`
}

// otherUser is who holds control in another tab, when the proxy names users.
function otherUser(control: ControlView | undefined) {
  const label = control?.status.holder === 'web' ? control.status.lease?.label : undefined
  return label && label !== 'web' ? label : ''
}

// takeControl always asks first, explaining what the other client loses and
// what the takeover does to a running turn or runtime.
export async function takeControl(confirm: Confirm, id: string, control: ControlView | undefined) {
  const user = otherUser(control)
  const ok = await confirm({
    title: control?.status.holder === 'terminal' ? 'Take control from the terminal?' : control?.status.holder === 'cli' ? 'Take control from the command line?' : `Take control from ${user ? `${user}'s` : 'the other'} browser tab?`,
    description: takeoverEffect(control),
    points: control?.plan.reasons ?? [],
    action: 'Take control',
    destructive: true,
  })
  return ok && run(id, () => take(id, control?.plan.plan_token, true), 'Taking control…', 'You control this agent.')
}

// steer adds a message to the turn in progress; follow_up waits for it.
export type Delivery = 'prompt' | 'steer' | 'follow_up'
export type MessageRequest = { text: string; images: { mime_type: string; data: string }[]; delivery: Delivery; runtime_id: string; client_id: string }

const deliveries = { prompt: DeliveryKind.PROMPT, steer: DeliveryKind.STEER, follow_up: DeliveryKind.FOLLOW_UP }

// sendMessage delivers once. When another client controls the agent it asks
// to take over, then delivers to whichever agent the takeover left running.
// A message to an item without a running place starts it; the call resolves
// once the job doing so delivered it.
export async function sendMessage(confirm: Confirm, id: string, request: MessageRequest) {
  const images = request.images.map(image => ({ mimeType: image.mime_type, data: Uint8Array.from(atob(image.data), c => c.charCodeAt(0)) }))
  await withTakeover(confirm, 'Take control of this agent and send?', async (confirmed, planToken) => {
    if (confirmed) await take(id, planToken, true)
    // The page's runtime ID is the agent's generation it saw.
    const generation = confirmed ? 0 : Number(request.runtime_id) || 0
    const out = await agents.sendMessage({ itemId: id, text: request.text, images, delivery: deliveries[request.delivery], clientMessageId: request.client_id, expectedGeneration: generation, startMode: AgentMode.HEADLESS })
    return awaitJob(out.job)
  })
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
    const outcome = await withTakeover(confirm, 'Take control of this agent to change its settings?', async (confirmed, planToken) => {
      if (confirmed) await take(id, planToken, false)
      return agents.updateAgentSettings({ itemId: id, ...request })
    })
    const message = !outcome.changed.length ? 'Settings unchanged.' : outcome.restarted ? 'Restarted with the new settings.' : outcome.pending.length ? 'Saved; applies when the agent next starts.' : view.runtime_online ? 'Switched.' : 'Saved.'
    toast.success(message, { id: pending, ...warnings(outcome) })
    return true
  } catch (error) {
    toast.dismiss(pending)
    reportError(rpcMessage(error))
    return false
  } finally {
    void refreshSettings(id)
    void refreshChat(id)
  }
}


// takeOwnership makes an item an agent delegated the user's own.
export const takeOwnership = (id: string) => run(id, () => items.updateItem({ itemId: id, owner: 'user:operator' }), 'Taking it…', 'It is yours now, in your list.')
