import { memo, useContext, useRef } from 'react'
import { MessageCircleQuestionIcon, OctagonXIcon, TriangleAlertIcon } from 'lucide-react'
import { useSmoothText } from '@/hooks/use-smooth-text'
import { Bubble, BubbleContent } from '@/components/ui/bubble'
import { Button } from '@/components/ui/button'
import { Message, MessageContent } from '@/components/ui/message'
import type { HistoryImage, HistoryMessage, HistoryTool, Live, MessagePart } from '@/lib/views'
import type { OutboxMessage } from '@/lib/history'
import { partsImages, partsText, partsTools } from '@/lib/parts'
import { providerName } from '@/components/blockers'
import { cn, sentence } from '@/lib/utils'
import { Markdown } from './markdown'
import { ItemContext } from './item-context'
import { RunContext } from './runnable-code'
import { MessageImages } from './message-images'
import { SubagentReport } from './subagent'
import { SummaryBlock } from './summary-block'
import { OperatorCommand, Thinking, ToolCall } from './tool-call'

export const ChatMessage = memo(function ChatMessage({ message }: { message: HistoryMessage }) {
  if (message.role === 'user') return <SentMessage text={partsText(message.parts)} images={partsImages(message.parts)} tools={partsTools(message.parts)} />
  if (message.error) return <HarnessNotice text={partsText(message.parts)} provider={message.usage_provider} />
  if (message.role === 'subagent') return <SubagentReport message={message} />
  if (message.role === 'summary') return <SummaryBlock message={message} />
  return (
    <Message data-role={message.role}>
      <MessageContent>
        <MessageParts id={message.id} parts={message.parts} />
        {message.aborted && <Aborted />}
      </MessageContent>
    </Message>
  )
})

// MessageParts shows a message as it was written, part by part: text as
// Markdown, reasoning as a folded line, tool calls, images. A saved message
// and the live one it becomes are drawn by it alike, so history taking over
// from the live message moves nothing. Live (streaming set), the last text
// is revealed at an even pace, while streaming, and while the model thinks
// (since when), the last reasoning runs.
// An agent's reply in an item's conversation may offer to run its shell
// snippets in the item's checkout.
export function MessageParts(props: { id?: string; parts: MessagePart[]; streaming?: boolean; thinking?: number }) {
  const item = useContext(ItemContext)
  return (
    <RunContext.Provider value={item}>
      <AgentParts {...props} />
    </RunContext.Provider>
  )
}

function AgentParts({ id, parts, streaming, thinking }: { id?: string; parts: MessagePart[]; streaming?: boolean; thinking?: number }) {
  const lastText = parts.findLastIndex(part => 'text' in part)
  const last = parts.at(-1)
  const runs = thinking !== undefined && !!last && 'thought' in last
  // Calls and images in a row sit together, as one list.
  const groups: { at: number; parts: MessagePart[] }[] = []
  parts.forEach((part, at) => {
    const previous = groups.at(-1)?.parts[0]
    if (previous && (('tool' in part && 'tool' in previous) || ('image' in part && 'image' in previous))) groups.at(-1)!.parts.push(part)
    else groups.push({ at, parts: [part] })
  })
  return (
    <>
      {groups.map(({ at, parts: group }) => {
        const part = group[0]
        if ('text' in part) return streaming !== undefined && at === lastText ? <LiveText key={at} text={part.text} streaming={streaming} /> : <Markdown key={at} text={part.text} />
        if ('thought' in part) {
          const active = runs && at === parts.length - 1
          return <Thinking key={at} text={part.thought.text ?? ''} active={active} since={active ? thinking : undefined} durationMs={part.thought.duration_ms} fold={id ? `thought:${id}:${at}` : undefined} />
        }
        if ('image' in part) return <MessageImages key={at} images={partsImages(group)} />
        return (
          <div key={at} className="flex flex-col gap-1.5">
            {partsTools(group).map((tool, index) => (
              <ToolCall key={tool.id || index} tool={tool} />
            ))}
          </div>
        )
      })}
      {thinking !== undefined && !runs && <Thinking text="" active since={thinking} />}
    </>
  )
}

// Aborted marks where the operator stopped a reply. The turn ended there;
// the next message continues as usual.
function Aborted() {
  return (
    <span data-aborted className="flex items-center gap-1.5 text-xs text-red-400">
      <OctagonXIcon className="size-3.5" aria-hidden /> Aborted
    </span>
  )
}

// HarnessNotice shows what the agent's harness said in place of a reply, such
// as a usage limit and when it resets: the turn stopped there. A usage limit
// names whose quota ran out, as the item's usage blocker does.
function HarnessNotice({ text, provider }: { text: string; provider?: string }) {
  return (
    <div role="note" data-role="notice" data-usage-provider={provider} className="flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-sm">
      <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-400" aria-hidden />
      <span>
        {provider && <span className="font-medium">{providerName(provider)} usage limit: </span>}
        {text || 'The agent stopped before replying.'}
      </span>
    </div>
  )
}

const problems: Partial<Record<OutboxMessage['status'], string>> = {
  unknown: 'Delivery unknown',
  failed: 'Not sent',
}

// SentMessage is a user message, from history or still pending. A pending
// message looks like any other; the reply side shows the agent working. Only
// a problem adds a note and a way to retry. One component draws both, so a
// confirmed message updates the bubble already shown instead of replacing it.
// Tools are commands the operator ran in the agent's own prompt (`!ls`).
// A message another item's agent sent names it: it reached this agent in an
// envelope wi wrote.
export const SentMessage = memo(function SentMessage({ text, images, tools, status, onRetry, sender }: { text: string; images?: HistoryImage[]; tools?: HistoryTool[]; status?: OutboxMessage['status']; onRetry?: () => void; sender?: string }) {
  const problem = status && problems[status]
  const fromItem = sender?.startsWith('item:') ? sender.slice(5) : ''
  return (
    <Message align="end" data-role="user" data-pending={status} data-sender={sender}>
      <MessageContent>
        {fromItem && <Markdown text={`From [another item's agent](wi:${fromItem})`} className="self-end text-xs text-muted-foreground" />}
        {(text || !!images?.length || !tools?.length) && (
          <Bubble align="end" variant="muted">
            <BubbleContent>
              {text && <Markdown text={text} />}
              {!!images?.length && <MessageImages images={images} />}
            </BubbleContent>
          </Bubble>
        )}
        {!!tools?.length && (
          <div className="flex w-full flex-col items-end gap-1.5">
            {tools.map((tool, index) => (
              <OperatorCommand key={tool.id || index} tool={tool} />
            ))}
          </div>
        )}
        {problem && (
          <div className="flex items-center justify-end gap-2 text-xs text-amber-300">
            <span>{problem}</span>
            <Button variant="outline" size="xs" onClick={onRetry}>
              Edit and retry
            </Button>
          </div>
        )}
      </MessageContent>
    </Message>
  )
})

// Activities that need no line of their own: the agent is between turns.
const quietActivities = new Set(['Idle', 'Message saved', 'Transcript available'])
// Activities after which unlinked live text is history's, not news.
const finishedActivities = new Set(['Idle', 'Transcript available'])

// LivePreview shows the message the agent is writing, drawn as history will
// show it, and that the agent works. A message the driver linked to history
// entries (saved_as) stays until history shows one of them; an unlinked one
// until history's last message has the same text or the turn is over. Either
// way a finished message never blinks out before history has it.
// A live stream error only says that no runtime streams right now, which is
// plumbing, not news: it shows as no live content.
export function LivePreview({
  live: stream,
  savedInHistory = false,
  savedText,
  working = false,
  waitingOn,
}: {
  live: Live | null | undefined
  savedInHistory?: boolean
  savedText?: string
  working?: boolean
  // waitingOn names what the page waits on before the agent shows work.
  waitingOn?: string
}) {
  // No live content yet (a runtime starting) renders like empty content, so
  // the working animation keeps its place, and its element, once text comes.
  const live: Live = (stream?.error ? null : stream) ?? {}
  const text = partsText(live.parts)
  const saved = savedInHistory || (!live.saved_as?.length && (finishedActivities.has(live.activity ?? '') || (!!text && sameText(text, savedText ?? ''))))
  const parts = saved ? [] : (live.parts ?? [])
  // Activity is shown only as the working animation: its wording ("Tool
  // completed", left over from an earlier turn) is not news, and problems have
  // their own notice.
  const activity = quietActivities.has(live.activity ?? '') ? '' : (live.activity ?? '')
  const busy = working || activeActivity(activity)
  // The model is reasoning, shared or not, unless it writes again: timed
  // from when this stretch of reasoning began, and itself the sign of work,
  // so no second one.
  const last = parts.at(-1)
  const thinking = live.activity === 'Thinking' && !(last && 'text' in last)
  const stretch = last && 'thought' in last ? parts.length - 1 : parts.length
  const since = useStartedAt(thinking ? `${live.message_id ?? 'live'}:${stretch}` : undefined)
  const streaming = streamingActivities.has(live.activity ?? '') || (live.activity ?? '').startsWith('Streaming')
  const written = parts.some(part => 'text' in part || 'tool' in part)
  if (!parts.length && !busy) return null
  return (
    <Message data-live>
      <MessageContent>
        <MessageParts parts={parts} streaming={streaming} thinking={since} />
        {!written && !thinking && busy && <Working label={waitingOn ?? (activity === 'Thinking' ? 'Thinking' : 'Working')} waiting={!!waitingOn} />}
      </MessageContent>
    </Message>
  )
}

// sameText compares text as written live and as history saved it, which may
// break lines differently.
const sameText = (a: string, b: string) => a.replace(/\s+/g, ' ').trim() === b.replace(/\s+/g, ' ').trim()

// useStartedAt is when key was first seen, so a timer survives re-renders
// and restarts for the next message.
function useStartedAt(key: string | undefined) {
  const started = useRef<{ key: string; at: number } | null>(null)
  if (key === undefined) return undefined
  if (started.current?.key !== key) started.current = { key, at: Date.now() }
  return started.current.at
}

// Activities during which the live text is still being written.
const streamingActivities = new Set(['Assistant responding', 'Streaming assistant lines'])

// LiveText shows the reply being written, revealed at an even pace.
function LiveText({ text, streaming }: { text: string; streaming: boolean }) {
  return <Markdown text={useSmoothText(text, streaming)} />
}

// activeActivity reports driver activity that means work is under way;
// anything else (a failure, waiting for a transcript) is shown as text.
const activeActivity = (activity: string) => activity === 'Assistant responding' || activity === 'Working' || activity === 'Thinking' || activity.startsWith('Tool running')

// AgentProblem says the agent stopped on a problem instead of answering, so a
// silent failure never looks like work in progress.
export function AgentProblem({ reason }: { reason?: string }) {
  return (
    <div role="alert" className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm">
      <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-destructive" aria-hidden />
      <span>
        The agent needs attention.{reason && <span className="text-muted-foreground"> {sentence(reason)}.</span>}
      </span>
    </div>
  )
}

// AgentQuestion shows a turn paused until the operator answers in the
// agent's terminal, which the chat cannot do.
export function AgentQuestion({ reason }: { reason?: string }) {
  return (
    <div role="status" className="flex items-start gap-2 rounded-lg border bg-muted/40 px-3 py-2 text-sm">
      <MessageCircleQuestionIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
      <span>
        {reason || 'The agent is waiting for your answer'}.<span className="text-muted-foreground"> Answer it in the agent's terminal.</span>
      </span>
    </div>
  )
}

// Working marks where the agent's next text will appear while it works.
// While the page still waits for the agent to show work, the dots rest and
// the label pulses; the element stays, so nothing moves once work shows.
export function Working({ label = 'Working', waiting = false }: { label?: string; waiting?: boolean }) {
  return (
    <div data-working={waiting ? 'waiting' : ''} className="flex items-center gap-1.5 text-sm text-muted-foreground" role="status" aria-label={waiting ? label : 'The agent is working'}>
      <span className={cn(waiting && 'animate-pulse')}>{label}</span>
      <span className="flex gap-0.5" aria-hidden>
        {[0, 1, 2].map(dot => (
          <span key={dot} className={cn('size-1 rounded-full bg-current', waiting ? 'opacity-40' : 'animate-bounce')} style={{ animationDelay: `${dot * 150}ms` }} />
        ))}
      </span>
    </div>
  )
}
