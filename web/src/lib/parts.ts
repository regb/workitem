import type { HistoryImage, HistoryTool, MessagePart } from './views'

// What a message's parts hold, for what reads one kind only: its text to
// match a sent message or tell an answer, its calls to count them.

// partsText joins a message's text parts with a blank line where other
// parts came between, as the daemon's history does.
export const partsText = (parts: MessagePart[] | undefined) => (parts ?? []).flatMap(part => ('text' in part && part.text ? [part.text] : [])).join('\n\n')

export const partsTools = (parts: MessagePart[] | undefined): HistoryTool[] => (parts ?? []).flatMap(part => ('tool' in part ? [part.tool] : []))

export const partsImages = (parts: MessagePart[] | undefined): HistoryImage[] => (parts ?? []).flatMap(part => ('image' in part ? [part.image] : []))
