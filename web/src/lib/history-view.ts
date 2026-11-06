import { MessageRole, ToolCallStatus, type ConversationMessage, type Part, type SubagentRef, type ToolCall } from '@/gen/wi/core/v1/agents_pb'
import type { Image } from '@/gen/wi/core/v1/common_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import type { History, HistoryImage, HistoryMessage, HistorySubagent, HistoryTool, MessagePart } from './views'
import { agents } from './rpc'

// A conversation's history as the chat shows it, built from the API's
// messages.

const lower = (names: Record<number, string>, value: number) => (names[value] ?? '').toLowerCase()

// base64 encodes an image's bytes for a data: URL.
function base64(bytes: Uint8Array) {
  let binary = ''
  for (let at = 0; at < bytes.length; at += 0x8000) binary += String.fromCharCode(...bytes.subarray(at, at + 0x8000))
  return btoa(binary)
}

const imageView = (image: Image): HistoryImage => ({ mime_type: image.mimeType, data: base64(image.data), alt: image.alt || undefined })

const subagentView = (subagent?: SubagentRef): HistorySubagent | undefined =>
  subagent ? { id: subagent.id, type: subagent.type || undefined, description: subagent.description || undefined, status: subagent.status || undefined } : undefined

export const toolView = (tool: ToolCall): HistoryTool => ({
  id: tool.id || undefined,
  name: tool.name,
  input: tool.input || undefined,
  output: tool.output || undefined,
  status: lower(ToolCallStatus, tool.status),
  images: tool.images.length ? tool.images.map(imageView) : undefined,
  subagent: subagentView(tool.subagent),
})

// partView is one part of a message, saved or live; empty text is none.
export function partView(part: Part): MessagePart | undefined {
  const kind = part.kind
  switch (kind.case) {
    case 'text':
      return kind.value ? { text: kind.value } : undefined
    case 'thought':
      return { thought: { text: kind.value.text || undefined, duration_ms: kind.value.durationMs || undefined } }
    case 'tool':
      return { tool: toolView(kind.value) }
    case 'image':
      return { image: imageView(kind.value) }
  }
  return undefined
}

export const partsView = (parts: Part[]) => parts.map(partView).filter(part => !!part)

export function messageView(m: ConversationMessage): HistoryMessage {
  return {
    id: m.id,
    time: m.time ? timestampDate(m.time).toISOString() : undefined,
    sender: m.sender || undefined,
    native_ids: m.nativeIds.length ? m.nativeIds : undefined,
    role: lower(MessageRole, m.role),
    parts: partsView(m.parts),
    client_message_id: m.clientMessageId || undefined,
    error: m.error || undefined,
    usage_provider: m.usageProvider || undefined,
    aborted: m.aborted || undefined,
    subagent: subagentView(m.subagent),
    summary: m.summary ? { kind: m.summary.kind as 'compaction' | 'branch', tokens_before: m.summary.tokensBefore || undefined } : undefined,
  }
}

export const historyView = (value: { messages: ConversationMessage[]; nextBefore: string; truncated: boolean; leafId?: string }): History => ({
  messages: value.messages.map(messageView),
  leaf_id: value.leafId ?? '',
  before_id: value.nextBefore || undefined,
  has_more: !!value.nextBefore,
  truncated: value.truncated || undefined,
})

// readHistory reads a page of an item's conversation: on the branch leaf,
// older than before.
export async function readHistory(itemId: string, leafId: string, before: string, signal?: AbortSignal) {
  return historyView(await agents.getHistory({ itemId, leafId, before }, { signal }))
}

// readSubagentHistory reads a subagent's own conversation.
export async function readSubagentHistory(itemId: string, subagentId: string, signal?: AbortSignal) {
  return historyView(await agents.getSubagentHistory({ itemId, subagentId }, { signal }))
}
