import type { History, HistoryImage, HistoryMessage, HistoryTool, PromptLink } from './views'
import { partsImages, partsText, partsTools } from './parts'

// mergeHistoryMessages removes entries repeated across pages, folds a tool
// result the agent saved apart from its call (a later read, after the call's
// message left the latest page) into that call, so one tool shows once, and
// joins a compaction's boundary and its summary.
export function mergeHistoryMessages(messages: HistoryMessage[]): HistoryMessage[] {
  const merged: HistoryMessage[] = []
  const seen = new Set<string>()
  const latest = new Map(messages.map(message => [message.id, message]))
  const calls = new Map<string, { tool: HistoryTool; message: HistoryMessage }>()
  for (const entry of messages) {
    if (seen.has(entry.id)) continue
    seen.add(entry.id)
    const original = latest.get(entry.id)!
    const message: HistoryMessage = { ...original, parts: original.parts.map(part => ('tool' in part ? { tool: { ...part.tool } } : part)), native_ids: [...(original.native_ids ?? [])] }
    // Claude records a compaction as a boundary, then the summary: one block.
    const boundary = merged.at(-1)
    if (message.role === 'summary' && boundary?.role === 'summary' && !partsText(boundary.parts) && boundary.summary?.kind === message.summary?.kind) {
      boundary.parts = message.parts
      boundary.native_ids!.push(message.id, ...(message.native_ids ?? []))
      continue
    }
    const only = message.parts.length === 1 && 'tool' in message.parts[0] ? message.parts[0].tool : undefined
    const call = only && only.status !== 'running' && only.id ? calls.get(only.id) : undefined
    if (only && call) {
      Object.assign(call.tool, { output: only.output, images: only.images, status: only.status })
      call.message.native_ids!.push(message.id, ...(message.native_ids ?? []))
      continue
    }
    merged.push(message)
    for (const tool of partsTools(message.parts)) if (tool.id) calls.set(tool.id, { tool, message })
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
  // The latest page is the server's order for its window: what was shown
  // and is still in it takes its place there, as a message the agent
  // picked up later than it was first seen. What was shown before the
  // window stays above it.
  const inLatest = new Set(latest.messages.flatMap(message => [message.id, ...(message.native_ids ?? [])]))
  const older = shown.messages.filter(message => !inLatest.has(message.id) && !(message.native_ids ?? []).some(id => inLatest.has(id)))
  return {
    leaf: latest.leaf_id,
    entryIDs: [...new Set([...shown.entryIDs, ...entryIDs(latest)])],
    messages: mergeHistoryMessages([...older, ...latest.messages]),
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

// visible reports whether a message has anything to show. Reasoning shows,
// even with no text saved, as does an aborted reply's mark, and a
// compaction's place even before its summary arrives.
export const visible = (message: HistoryMessage) =>
  message.role === 'user' || message.role === 'summary' || !!(message.subagent && message.role === 'subagent') || !!message.parts.length || !!message.aborted

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
export const knownMatches = (messages: HistoryMessage[], text: string) => messages.filter(message => message.role === 'user' && normalize(partsText(message.parts)) === normalize(text)).map(message => message.id)

// The request ID drivers link a sent message by: its client ID, which the
// daemon passes on unchanged as the message's request ID.
export const requestID = (message: OutboxMessage) => message.client_id

// reconcileOutbox returns the pending messages history has not confirmed. A
// history message carrying a sent message's client ID confirms it: the
// daemon keeps the link the agent reported. Before history shows it, a link
// on the live content does. A message the driver linked to its history
// entries is confirmed once history shows one of them. Otherwise it is confirmed by a user message with the same
// text and as many images that was not already shown when it was sent; each confirms
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
  // Images match by count: agents shrink or re-encode large images before
  // saving them (Claude Code scales a wide screenshot down), so saved data
  // need not equal what was sent.
  const sameImages = (sent: OutboxMessage, message: HistoryMessage) => partsImages(message.parts).length === sent.images.length
  const byClientID = new Map(messages.filter(message => message.client_message_id).map(message => [message.client_message_id!, message]))
  const remaining = pending.filter(sent => {
    if (sent.status === 'failed') return true
    const exact = byClientID.get(requestID(sent))
    if (exact) {
      used.add(exact.id)
      confirmed.set(exact.id, sent.client_id)
      return false
    }
    const entries = linked.get(requestID(sent))
    if (entries) {
      entries.forEach(entry => used.add(entry))
      const holder = messages.find(message => [message.id, ...(message.native_ids ?? [])].some(entry => entries.includes(entry)))
      if (holder) confirmed.set(holder.id, sent.client_id)
      return !entries.some(entry => shown.has(entry))
    }
    const match = messages.find(message => message.role === 'user' && !used.has(message.id) && !sent.known.includes(message.id) && normalize(partsText(message.parts)) === normalize(sent.text) && sameImages(sent, message))
    if (!match) return true
    used.add(match.id)
    confirmed.set(match.id, sent.client_id)
    return false
  })
  return { remaining: remaining.length === pending.length ? pending : remaining, confirmed }
}

// A conversation is shown as its messages, with the work of a finished turn
// folded away behind its answer.
export type Segment = { kind: 'message'; message: HistoryMessage } | { kind: 'fold'; key: string; messages: HistoryMessage[]; tools: number; texts: number }

// foldTurns folds each turn, the agent's messages after one of the
// operator's, up to its answer: the last reply with text. The report of
// finished background work (a subagent, a command) starts a turn as well: the
// agent answers it, so that answer never folds away the one before. A turn
// without an answer stays whole, and so does the last turn while it is open
// (still streaming), so it only folds once its answer is final.
export function foldTurns(messages: HistoryMessage[], lastOpen: boolean): Segment[] {
  const segments: Segment[] = []
  let start = 0
  while (start < messages.length) {
    // What starts a turn stays in view; a summary is never folded into one.
    if (startsTurn(messages[start])) {
      segments.push({ kind: 'message', message: messages[start++] })
      continue
    }
    let end = start
    while (end < messages.length && !startsTurn(messages[end])) end++
    const turn = messages.slice(start, end)
    const answer = end === messages.length && lastOpen ? -1 : turn.findLastIndex(message => message.role === 'assistant' && !message.error && !!partsText(message.parts).trim())
    const work = turn.slice(0, Math.max(answer, 0))
    const shown = turn.slice(Math.max(answer, 0))
    // A reply saved as one message holds its work before its answer: the
    // reasoning, tool calls and text before its final stretch of text. That
    // work folds with the turn's; the answer keeps the message's identity.
    if (answer >= 0) {
      const [before, after] = splitAnswer(turn[answer])
      if (before) {
        work.push(before)
        shown[0] = after
      }
    }
    if (work.length) segments.push({ kind: 'fold', key: `fold-${work[0].id}`, messages: work, tools: work.reduce((count, message) => count + partsTools(message.parts).length, 0), texts: work.filter(message => !!partsText(message.parts).trim()).length })
    for (const message of shown) segments.push({ kind: 'message', message })
    start = end
  }
  return segments
}

// splitAnswer separates a reply's work from its answer, the text after its
// last reasoning or tool call: the work as a message of its own, folded with
// the turn, and the answer as the message itself. A reply that is all answer
// is not split.
function splitAnswer(message: HistoryMessage): [HistoryMessage | undefined, HistoryMessage] {
  const last = message.parts.findLastIndex(part => 'thought' in part || 'tool' in part)
  if (last < 0 || !partsText(message.parts.slice(last + 1)).trim()) return [undefined, message]
  return [{ ...message, id: `${message.id}:work`, native_ids: undefined, client_message_id: undefined, parts: message.parts.slice(0, last + 1) }, { ...message, parts: message.parts.slice(last + 1) }]
}

const startsTurn = (message: HistoryMessage) => message.role === 'user' || message.role === 'summary' || message.role === 'subagent'
