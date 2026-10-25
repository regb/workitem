import type { HistoryTool, Live } from './api'

// ContentEvent is one change to a runtime's live content; content frames carry
// them in order after a live snapshot (agentdriver/protocol/content.go).
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

const maxTools = 32
const maxThoughtBytes = 64 << 10

// upsertTool mirrors the Go helper: replace the call with the same id (or, for
// calls without one, the last of the same name), otherwise append.
function upsertTool(tools: HistoryTool[] | undefined, tool: HistoryTool) {
  const next = [...(tools ?? [])]
  for (let i = next.length - 1; i >= 0; i--) {
    if ((tool.id && next[i].id === tool.id) || (!tool.id && !next[i].id && next[i].name === tool.name)) {
      next[i] = { ...tool }
      return next
    }
  }
  next.push({ ...tool })
  return next.slice(-maxTools)
}

// boundThought keeps the newest reasoning within the byte limit.
function boundThought(text: string) {
  const bytes = encoder.encode(text)
  if (bytes.length <= maxThoughtBytes) return text
  let start = bytes.length - maxThoughtBytes
  while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++
  return decoder.decode(bytes.subarray(start))
}

// applyContent mirrors driverprotocol.ApplyContent: applying a runtime's events
// in order to its snapshot yields the runtime's next snapshot.
export function applyContent(value: Live, event: ContentEvent): Live {
  const next: Live = { ...value, sequence: event.sequence }
  switch (event.type) {
    case 'message.started':
      Object.assign(next, { message_id: event.message_id, text: '', truncated: false, tool: undefined, saved_as: undefined, thought: undefined, tools: undefined })
      break
    case 'message.delta':
      if (!next.truncated) {
        ;[next.text, next.truncated] = boundText((next.text ?? '') + (event.text ?? ''))
        next.saved_as = undefined
      }
      break
    case 'message.replaced': {
      const [text, cut] = boundText(event.text ?? '')
      Object.assign(next, { text, truncated: cut || !!event.truncated, saved_as: undefined })
      if (event.message_id) next.message_id = event.message_id
      break
    }
    case 'message.saved':
      next.saved_as = [...(event.native_ids ?? [])]
      break
    case 'tool.started':
    case 'tool.updated':
    case 'tool.completed':
      next.tool = event.tool ? { ...event.tool } : undefined
      if (event.tool) next.tools = upsertTool(next.tools, event.tool)
      break
    case 'tool.cleared':
      Object.assign(next, { tool: undefined, tools: undefined })
      break
    case 'thought.delta':
      next.thought = boundThought((next.thought ?? '') + (event.text ?? ''))
      break
    case 'prompt.saved':
      // Newest last, one link per request, as driverprotocol keeps them.
      next.prompts = [...(next.prompts ?? []).filter(link => link.request_id !== event.request_id), { request_id: event.request_id ?? '', native_ids: [...(event.native_ids ?? [])] }].slice(-8)
      break
    case 'activity':
      return { ...next, activity: event.activity ?? '', leaf_id: event.leaf_id ?? '' }
  }
  if (event.activity) next.activity = event.activity
  return next
}
