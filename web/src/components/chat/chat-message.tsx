import { memo } from 'react'
import { MessageCircleQuestionIcon, TriangleAlertIcon } from 'lucide-react'
import { useSmoothText } from '@/hooks/use-smooth-text'
import { Bubble, BubbleContent } from '@/components/ui/bubble'
import { Button } from '@/components/ui/button'
import { Message, MessageContent } from '@/components/ui/message'
import type { HistoryMessage, HistoryTool, Live } from '@/lib/api'
import { withLiveTools, type OutboxMessage } from '@/lib/history'
import { sentence } from '@/lib/utils'
import { Markdown } from './markdown'
import { MessageImages } from './message-images'
import { Thinking, ToolCall } from './tool-call'

// liveTools are this message's tool calls the agent is still running.
export const ChatMessage = memo(function ChatMessage({ message, liveTools }: { message: HistoryMessage; liveTools?: HistoryTool[] }) {
  if (message.role === 'user') return <SentMessage text={message.text} images={message.images} />
  if (message.error) return <HarnessNotice text={message.text} />
  return (
    <Message data-role={message.role}>
      <MessageContent>
        {message.role === 'summary' && <span className="text-xs font-medium text-muted-foreground">Summary</span>}
        {message.text && <Markdown text={message.text} className={message.role === 'summary' ? 'text-muted-foreground' : undefined} />}
        {!!message.images?.length && <MessageImages images={message.images} />}
        {!!message.tools?.length && (
          <div className="flex flex-col gap-1.5">
            {withLiveTools(message.tools, liveTools).map((tool, index) => (
              <ToolCall key={tool.id || index} tool={tool} />
            ))}
          </div>
        )}
      </MessageContent>
    </Message>
  )
})

// HarnessNotice shows what the agent's harness said in place of a reply, such
// as a usage limit and when it resets: the turn stopped there.
function HarnessNotice({ text }: { text: string }) {
  return (
    <div role="note" data-role="notice" className="flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-sm">
      <TriangleAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-400" aria-hidden />
      <span>{text || 'The agent stopped before replying.'}</span>
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
export const SentMessage = memo(function SentMessage({ text, images, status, onRetry }: { text: string; images?: HistoryMessage['images']; status?: OutboxMessage['status']; onRetry?: () => void }) {
  const problem = status && problems[status]
  return (
    <Message align="end" data-role="user" data-pending={status}>
      <MessageContent>
        <Bubble align="end" variant="muted">
          <BubbleContent>
            {text && <Markdown text={text} />}
            {!!images?.length && <MessageImages images={images} />}
          </BubbleContent>
        </Bubble>
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

// LivePreview shows what the agent is doing now: reasoning, tool calls
// history does not show yet (tools), and the reply being written. Text the driver linked to history entries
// (saved_as) stays until history shows one of them; unlinked text stays until
// history's last message has the same text or the turn is over. Either way
// finished text never blinks out before history has it.
// A live stream error only says that no runtime streams right now, which is
// plumbing, not news: it shows as no live content.
export function LivePreview({ live: stream, tools = [], savedInHistory = false, savedText, working = false }: { live: Live | null | undefined; tools?: HistoryTool[]; savedInHistory?: boolean; savedText?: string; working?: boolean }) {
  // No live content yet (a runtime starting) renders like empty content, so
  // the working animation keeps its place, and its element, once text comes.
  const live: Live = (stream?.error ? null : stream) ?? {}
  const saved = live.saved_as?.length ? savedInHistory : finishedActivities.has(live.activity ?? '') || (!!live.text && live.text.trim() === savedText?.trim())
  const text = saved ? '' : (live.text ?? '')
  // Reasoning belongs to the message being written; it goes with its text.
  const thought = saved ? '' : (live.thought ?? '')
  // Activity is shown only as the working animation: its wording ("Tool
  // completed", left over from an earlier turn) is not news, and problems have
  // their own notice.
  const activity = quietActivities.has(live.activity ?? '') ? '' : (live.activity ?? '')
  const busy = working || activeActivity(activity)
  if (!text && !thought && !tools.length && !busy) return null
  return (
    <Message data-live>
      <MessageContent>
        {thought && <Thinking text={thought} active={!text && live.activity === 'Thinking'} />}
        {!!tools.length && (
          <div className="flex flex-col gap-1.5">
            {tools.map((tool, index) => (
              <ToolCall key={tool.id || index} tool={tool} />
            ))}
          </div>
        )}
        {text && <LiveText text={text} streaming={streamingActivities.has(live.activity ?? '') || (live.activity ?? '').startsWith('Streaming')} />}
        {!text && !tools.length && busy && <Working label={activity === 'Thinking' ? 'Thinking' : 'Working'} />}
      </MessageContent>
    </Message>
  )
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
export function Working({ label = 'Working' }: { label?: string }) {
  return (
    <div data-working className="flex items-center gap-1.5 text-sm text-muted-foreground" role="status" aria-label="The agent is working">
      <span>{label}</span>
      <span className="flex gap-0.5" aria-hidden>
        {[0, 1, 2].map(dot => (
          <span key={dot} className="size-1 animate-bounce rounded-full bg-current" style={{ animationDelay: `${dot * 150}ms` }} />
        ))}
      </span>
    </div>
  )
}
