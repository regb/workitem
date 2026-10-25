import type { History, HistoryImage, HistoryMessage, HistoryTool, PromptLink } from './api'

// mergeHistoryMessages removes entries repeated across pages, folds a tool
// result entry into the earlier call it completes, so one tool shows once,
// and joins a compaction's boundary and its summary.
export function mergeHistoryMessages(messages: HistoryMessage[]): HistoryMessage[] {
  const merged: HistoryMessage[] = []
  const seen = new Set<string>()
  const latest = new Map(messages.map(message => [message.id, message]))
  const calls = new Map<string, { tool: HistoryTool; message: HistoryMessage }>()
  for (const entry of messages) {
    if (seen.has(entry.id)) continue
    seen.add(entry.id)
    const original = latest.get(entry.id)!
    const message: HistoryMessage = { ...original, tools: (original.tools ?? []).map(tool => ({ ...tool })), native_ids: [...(original.native_ids ?? [])] }
    const tools = message.tools!
    // Claude records a compaction as a boundary, then the summary: one block.
    const boundary = merged.at(-1)
    if (message.role === 'summary' && boundary?.role === 'summary' && !boundary.text && boundary.summary?.kind === message.summary?.kind) {
      boundary.text = message.text
      boundary.native_ids!.push(message.id, ...(message.native_ids ?? []))
      continue
    }
    const call = !message.text && tools.length === 1 && tools[0].status !== 'running' && tools[0].id ? calls.get(tools[0].id) : undefined
    if (call) {
      Object.assign(call.tool, { output: tools[0].output, images: tools[0].images, status: tools[0].status })
      call.message.native_ids!.push(message.id, ...(message.native_ids ?? []))
      continue
    }
    merged.push(message)
    for (const tool of tools) if (tool.id) calls.set(tool.id, { tool, message })
  }
  return merged
}

export const entryIDs = (history: History) => history.native_entry_ids ?? history.messages.map(message => message.id)

// Shown is what a conversation displays for one branch: every message seen
// since it opened plus older pages loaded on request. The server sends only
// the latest page, which slides as the agent works; keeping what was seen
// means the list only grows at the end, so the reader's place never moves.
export type Shown = { leaf: string; entryIDs: string[]; messages: HistoryMessage[]; cursor: string }

// follow folds a newer latest page into what is shown. A page that no longer
// reaches the shown leaf belongs to another branch and starts over.
export function follow(shown: Shown | undefined, latest: History): Shown {
  if (!shown || !reachesLeaf(latest, shown.leaf)) {
    return { leaf: latest.leaf_id, entryIDs: entryIDs(latest), messages: mergeHistoryMessages(latest.messages), cursor: latest.has_more ? (latest.before_id ?? '') : '' }
  }
  return {
    leaf: latest.leaf_id,
    entryIDs: [...new Set([...shown.entryIDs, ...entryIDs(latest)])],
    messages: mergeHistoryMessages([...shown.messages, ...latest.messages]),
    cursor: shown.cursor,
  }
}

// reachesLeaf reports whether the latest page still contains the leaf shown
// before, i.e. the branch did not change.
function reachesLeaf(latest: History, leaf: string) {
  return latest.leaf_id === leaf || latest.messages.some(message => message.id === leaf || message.native_ids?.includes(leaf))
}

// addOlderPage prepends one page loaded above what is shown.
export function addOlderPage(shown: Shown, page: History): Shown {
  return {
    ...shown,
    entryIDs: [...new Set([...entryIDs(page), ...shown.entryIDs])],
    messages: mergeHistoryMessages([...page.messages, ...shown.messages]),
    cursor: page.has_more ? (page.before_id ?? '') : '',
  }
}

// Bounds on what one open conversation keeps in memory: past maxShown
// messages it keeps the newest keepShown, and older ones load again on request.
export const maxShown = 600
export const keepShown = 400

// trimShown drops the oldest messages beyond the bound; the cursor then pages
// back from the oldest one kept.
export function trimShown(shown: Shown): Shown {
  if (shown.messages.length <= maxShown) return shown
  const messages = shown.messages.slice(-keepShown)
  const first = messages[0]
  const from = shown.entryIDs.indexOf(first.id)
  return { ...shown, messages, entryIDs: from >= 0 ? shown.entryIDs.slice(from) : shown.entryIDs, cursor: first.id }
}

// visible reports whether a message has anything to show; thinking-only
// entries carry no text, tools or images. An aborted reply shows its mark,
// and a compaction its place even before its summary arrives.
export const visible = (message: HistoryMessage) => message.role === 'user' || message.role === 'summary' || !!(message.text || message.tools?.length || message.images?.length || message.aborted)

export type OutboxMessage = {
  client_id: string
  text: string
  images: HistoryImage[]
  status: 'sending' | 'sent' | 'failed' | 'unknown'
  // How it was sent: a follow-up waits for the turn in progress to end.
  delivery?: 'prompt' | 'steer' | 'follow_up'
  // User messages with the same text already shown when this one was sent;
  // they can never confirm it.
  known: string[]
}

const normalize = (text: string) => text.replace(/\r\n/g, '\n').trim()

// knownMatches lists the shown user messages a new message could be confused
// with, recorded when it is sent.
export const knownMatches = (messages: HistoryMessage[], text: string) => messages.filter(message => message.role === 'user' && normalize(message.text) === normalize(text)).map(message => message.id)

// The request ID the server gives a message sent from this page.
export const requestID = (message: OutboxMessage) => `web:${message.client_id}`

// reconcileOutbox returns the pending messages history has not confirmed. A
// message the driver linked to its history entries is confirmed once history
// shows one of them. Otherwise it is confirmed by a user message with the same
// text and images that was not already shown when it was sent; each confirms
// one message.
export function reconcileOutbox(pending: OutboxMessage[], messages: HistoryMessage[], links: PromptLink[] = []): OutboxMessage[] {
  return matchOutbox(pending, messages, links).remaining
}

// confirmedBy maps each history message that confirms a pending message to
// that message's client ID, so the conversation keeps drawing it as the same
// bubble.
export function confirmedBy(pending: OutboxMessage[], messages: HistoryMessage[], links: PromptLink[] = []): Map<string, string> {
  return matchOutbox(pending, messages, links).confirmed
}

// matchOutbox does both at once: the pending messages left, and which
// history message confirms each other one.
export function matchOutbox(pending: OutboxMessage[], messages: HistoryMessage[], links: PromptLink[] = []) {
  const confirmed = new Map<string, string>()
  if (!pending.length) return { remaining: pending, confirmed }
  const shown = new Set(messages.flatMap(message => [message.id, ...(message.native_ids ?? [])]))
  const linked = new Map(links.map(link => [link.request_id, link.native_ids]))
  const used = new Set<string>()
  const sameImages = (sent: OutboxMessage, message: HistoryMessage) =>
    (message.images?.length ?? 0) === sent.images.length &&
    sent.images.every((image, index) => !image.data || (image.data === message.images![index].data && image.mime_type === message.images![index].mime_type))
  const remaining = pending.filter(sent => {
    if (sent.status === 'failed') return true
    const entries = linked.get(requestID(sent))
    if (entries) {
      entries.forEach(entry => used.add(entry))
      const holder = messages.find(message => [message.id, ...(message.native_ids ?? [])].some(entry => entries.includes(entry)))
      if (holder) confirmed.set(holder.id, sent.client_id)
      return !entries.some(entry => shown.has(entry))
    }
    const match = messages.find(message => message.role === 'user' && !used.has(message.id) && !sent.known.includes(message.id) && normalize(message.text) === normalize(sent.text) && sameImages(sent, message))
    if (!match) return true
    used.add(match.id)
    confirmed.set(match.id, sent.client_id)
    return false
  })
  return { remaining: remaining.length === pending.length ? pending : remaining, confirmed }
}

// toolMessages maps each tool call in history to the message showing it, so a
// tool the live preview reports can be shown once, in place.
export function toolMessages(messages: HistoryMessage[]) {
  const owners = new Map<string, string>()
  for (const message of messages) for (const tool of message.tools ?? []) if (tool.id) owners.set(tool.id, message.id)
  return owners
}

// withLiveTools lays live tool state over saved calls while history still
// shows them running; a finished saved call is final.
export function withLiveTools(tools: HistoryTool[], live: HistoryTool[] | undefined) {
  if (!live?.length) return tools
  return tools.map(tool => {
    const current = tool.status === 'running' && tool.id ? live.find(other => other.id === tool.id) : undefined
    return current ? { ...tool, status: current.status, output: current.output || tool.output } : tool
  })
}

// A conversation is shown as its messages, with the work of a finished turn
// folded away behind its answer.
export type Segment = { kind: 'message'; message: HistoryMessage } | { kind: 'fold'; key: string; messages: HistoryMessage[]; tools: number; texts: number }

// foldTurns folds each turn, the agent's messages after one of the
// operator's, up to its answer: the last reply with text. A turn without an
// answer stays whole, and so does the last turn while it is open (still
// streaming), so it only folds once its answer is final.
export function foldTurns(messages: HistoryMessage[], lastOpen: boolean): Segment[] {
  const segments: Segment[] = []
  let start = 0
  while (start < messages.length) {
    // A summary stays in view between turns, never folded into one.
    if (messages[start].role === 'user' || messages[start].role === 'summary') {
      segments.push({ kind: 'message', message: messages[start++] })
      continue
    }
    let end = start
    while (end < messages.length && messages[end].role !== 'user' && messages[end].role !== 'summary') end++
    const turn = messages.slice(start, end)
    const answer = end === messages.length && lastOpen ? -1 : turn.findLastIndex(message => message.role === 'assistant' && !message.error && !!message.text.trim())
    if (answer > 0) {
      const work = turn.slice(0, answer)
      segments.push({ kind: 'fold', key: `fold-${work[0].id}`, messages: work, tools: work.reduce((count, message) => count + (message.tools?.length ?? 0), 0), texts: work.filter(message => !!message.text.trim()).length })
    }
    for (const message of turn.slice(Math.max(answer, 0))) segments.push({ kind: 'message', message })
    start = end
  }
  return segments
}
