import { ToolCallPhase, type LiveContent, type WatchLiveContentResponse } from '@/gen/wi/core/v1/agents_pb'
import type { HistoryTool, Live, MessagePart } from './views'
import { partsView, toolView } from './history-view'

// ContentEvent is one change to a runtime's live content, in the driver's
// terms (agentdriver/protocol/content.go): the API's live content events are
// converted to it, so the page applies them as the driver does.
export type ContentEvent = {
  sequence: number
  type: string
  message_id?: string
  text?: string
  truncated?: boolean
  tool?: HistoryTool
  native_ids?: string[]
  request_id?: string
  activity?: string
  leaf_id?: string
}

const maxTextBytes = 256 << 10
const encoder = new TextEncoder()
const decoder = new TextDecoder()

// boundText cuts text to the protocol's byte limit at a character boundary.
function boundText(text: string): [string, boolean] {
  const bytes = encoder.encode(text)
  if (bytes.length <= maxTextBytes) return [text, false]
  let end = maxTextBytes
  while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--
  return [decoder.decode(bytes.subarray(0, end)), true]
}

// liveText is the message's text as the driver bounds it: its text parts,
// joined as written.
const liveText = (parts: MessagePart[] | undefined) => (parts ?? []).map(part => ('text' in part ? part.text : '')).join('')

// maxParts bounds a live message's parts, as the driver does; the oldest go.
const maxParts = 96
const boundParts = (parts: MessagePart[]) => parts.slice(-maxParts)

// orNone leaves out an empty list, as the Go JSON does.
const orNone = (parts: MessagePart[]) => (parts.length ? parts : undefined)

// appendText mirrors protocol.AppendText: extend the last part when it is
// text, otherwise start a text part.
function appendText(parts: MessagePart[], text: string): MessagePart[] {
  if (!text) return parts
  const last = parts.at(-1)
  if (last && 'text' in last) return [...parts.slice(0, -1), { text: last.text + text }]
  return [...parts, { text }]
}

// upsertTool mirrors protocol.UpsertTool: replace, in place, the part holding
// the call with the same id (or, for calls without one, the last of the same
// name), otherwise add the call.
function upsertTool(parts: MessagePart[], tool: HistoryTool): MessagePart[] {
  for (let i = parts.length - 1; i >= 0; i--) {
    const part = parts[i]
    if ('tool' in part && ((tool.id && part.tool.id === tool.id) || (!tool.id && !part.tool.id && part.tool.name === tool.name))) return parts.with(i, { tool: { ...tool } })
  }
  return [...parts, { tool: { ...tool } }]
}

const maxThoughtBytes = 64 << 10

// boundThought keeps the newest reasoning within the byte limit.
function boundThought(text: string) {
  const bytes = encoder.encode(text)
  if (bytes.length <= maxThoughtBytes) return text
  let start = bytes.length - maxThoughtBytes
  while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++
  return decoder.decode(bytes.subarray(start))
}

// appendThought extends the reasoning the message ends with, or starts a new
// stretch of it.
function appendThought(parts: MessagePart[], text: string): MessagePart[] {
  if (!text) return parts
  const last = parts.at(-1)
  if (last && 'thought' in last) return [...parts.slice(0, -1), { thought: { ...last.thought, text: boundThought((last.thought.text ?? '') + text) } }]
  return [...parts, { thought: { text: boundThought(text) } }]
}

// applyContent mirrors driverprotocol.ApplyContent: applying a runtime's events
// in order to its snapshot yields the runtime's next snapshot.
export function applyContent(value: Live, event: ContentEvent): Live {
  const next: Live = { ...value, sequence: event.sequence }
  const parts = next.parts ?? []
  switch (event.type) {
    case 'message.started':
      Object.assign(next, { message_id: event.message_id, truncated: false, saved_as: undefined, parts: undefined })
      break
    case 'message.delta':
      if (!next.truncated) {
        const before = liveText(parts)
        const [text, cut] = boundText(before + (event.text ?? ''))
        Object.assign(next, { parts: orNone(boundParts(appendText(parts, text.slice(before.length)))), truncated: cut, saved_as: undefined })
      }
      break
    case 'message.replaced': {
      // A replacement comes whole: the message's reasoning, its text, then
      // the calls it made.
      const [text, cut] = boundText(event.text ?? '')
      const replaced = [...appendText(parts.filter(part => 'thought' in part), text), ...parts.filter(part => 'tool' in part || 'image' in part)]
      Object.assign(next, { parts: orNone(replaced), truncated: cut || !!event.truncated, saved_as: undefined })
      if (event.message_id) next.message_id = event.message_id
      break
    }
    case 'message.saved':
      next.saved_as = [...(event.native_ids ?? [])]
      break
    case 'tool.started':
    case 'tool.updated':
    case 'tool.completed':
      if (event.tool) next.parts = boundParts(upsertTool(parts, event.tool))
      break
    case 'tool.cleared':
      next.parts = orNone(parts.filter(part => !('tool' in part)))
      break
    case 'thought.delta':
      next.parts = orNone(boundParts(appendThought(parts, event.text ?? '')))
      break
    case 'prompt.saved':
      // Newest last, one link per request, as driverprotocol keeps them.
      next.prompts = [...(next.prompts ?? []).filter(link => link.request_id !== event.request_id), { request_id: event.request_id ?? '', native_ids: [...(event.native_ids ?? [])] }].slice(-8)
      break
    case 'activity':
      return { ...next, activity: event.activity ?? '', leaf_id: event.leaf_id ?? '' }
    // The question stands apart from the message: only its own events set it.
    case 'question':
      return { ...value, sequence: event.sequence, question: event.tool ? { ...event.tool } : undefined }
  }
  if (event.activity) next.activity = event.activity
  return next
}

// liveView is the page's live content from an API snapshot. Empty fields are
// left out, as the driver's JSON leaves them.
export function liveView(content: LiveContent, sequence: number): Live {
  const live: Live = { sequence, activity: content.activity }
  if (content.messageId) live.message_id = content.messageId
  if (content.truncated) live.truncated = true
  if (content.error) live.error = content.error
  if (content.savedAs.length) live.saved_as = [...content.savedAs]
  const parts = partsView(content.parts)
  if (parts.length) live.parts = parts
  if (content.prompts.length) live.prompts = content.prompts.map(link => ({ request_id: link.clientMessageId, native_ids: [...link.nativeIds] }))
  if (content.question) {
    const { output: _, ...question } = toolView(content.question)
    live.question = question
  }
  return live
}

const toolEvents: Record<ToolCallPhase, string> = {
  [ToolCallPhase.UNSPECIFIED]: 'tool.updated',
  [ToolCallPhase.STARTED]: 'tool.started',
  [ToolCallPhase.UPDATED]: 'tool.updated',
  [ToolCallPhase.COMPLETED]: 'tool.completed',
  [ToolCallPhase.CLEARED]: 'tool.cleared',
}

// contentEvent is an API live content event in the driver's terms; a
// snapshot is not one (see liveView).
export function contentEvent(response: WatchLiveContentResponse, sequence: number): ContentEvent | undefined {
  const event = response.event
  switch (event.case) {
    case 'messageStarted':
      return { sequence, type: 'message.started', message_id: event.value.messageId }
    case 'messageDelta':
      return { sequence, type: 'message.delta', message_id: event.value.messageId, text: event.value.text, truncated: event.value.truncated }
    case 'messageReplaced':
      return { sequence, type: 'message.replaced', message_id: event.value.messageId, text: event.value.text, truncated: event.value.truncated }
    case 'messageCompleted':
      return { sequence, type: 'message.completed', message_id: event.value.messageId }
    case 'messageSaved':
      return { sequence, type: 'message.saved', message_id: event.value.messageId, native_ids: [...event.value.nativeIds] }
    case 'thoughtDelta':
      return { sequence, type: 'thought.delta', message_id: event.value.messageId, text: event.value.text }
    case 'tool':
      return { sequence, type: toolEvents[event.value.phase] ?? 'tool.updated', tool: event.value.tool && toolView(event.value.tool) }
    case 'promptSaved':
      return { sequence, type: 'prompt.saved', request_id: event.value.link?.clientMessageId ?? '', native_ids: [...(event.value.link?.nativeIds ?? [])] }
    case 'activity':
      return { sequence, type: 'activity', activity: event.value.activity }
    case 'turnSettled':
      return { sequence, type: 'turn.settled' }
    case 'question': {
      const question = event.value.question && toolView(event.value.question)
      if (question) delete question.output
      return { sequence, type: 'question', tool: question }
    }
  }
  return undefined
}
