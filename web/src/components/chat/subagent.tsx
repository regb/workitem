import { createContext, useContext, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { BotIcon, CheckIcon, ChevronRightIcon, LoaderIcon, XIcon } from 'lucide-react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { api, type History, type HistoryMessage, type HistorySubagent, type HistoryTool } from '@/lib/api'
import { reportTitle, type ToolView } from '@/lib/tools'
import { cn } from '@/lib/utils'
import { Markdown } from './markdown'
import { ToolCall } from './tool-call'

// ItemContext names the work item whose conversation is shown, so a subagent
// call can read that item's subagent conversations.
export const ItemContext = createContext('')

type AgentView = Extract<ToolView, { kind: 'agent' }>

// SubagentCall is a call that delegated work to a subagent: a card naming
// the task, which opens on the prompt and the subagent's own conversation.
export function SubagentCall({ tool, view }: { tool: HistoryTool; view?: AgentView }) {
  const [open, setOpen] = useState(false)
  const subagent = tool.subagent
  const title = subagent?.description || view?.title || tool.name
  const type = subagent?.type || view?.type
  const status = subagentStatus(tool)
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-lg border bg-muted/20" data-subagent={subagent?.id ?? ''} data-subagent-status={status}>
      <CollapsibleTrigger className="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left text-sm outline-none focus-visible:ring-3 focus-visible:ring-ring/50">
        <BotIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className={cn('min-w-0 truncate font-medium', status === 'running' && 'shimmer')}>{title}</span>
        {type && <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">{type}</span>}
        <StatusMark status={status} />
        <ChevronRightIcon className={cn('ms-auto size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')} aria-hidden />
      </CollapsibleTrigger>
      <CollapsibleContent className="flex flex-col gap-3 border-t px-3 py-3">
        {view?.prompt && <Prompt text={view.prompt} />}
        {subagent ? (
          <Timeline subagent={subagent} running={status === 'running'} />
        ) : (
          tool.output?.trim() && (
            <div className="text-sm">
              <Markdown text={tool.output} />
            </div>
          )
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}

// subagentStatus prefers what Claude records of the subagent's run; the call
// itself completes as soon as a background subagent is launched.
function subagentStatus(tool: HistoryTool) {
  if (tool.status === 'failed' || tool.status === 'error') return 'failed'
  return tool.subagent?.status || (tool.status === 'running' ? 'running' : 'completed')
}

function StatusMark({ status }: { status: string }) {
  if (status === 'running')
    return (
      <span className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
        <LoaderIcon className="size-3 animate-spin" aria-hidden /> Running
      </span>
    )
  if (status === 'failed' || status === 'killed')
    return (
      <span className="flex shrink-0 items-center gap-1 text-xs text-destructive">
        <XIcon className="size-3" aria-hidden /> {status === 'killed' ? 'Stopped' : 'Failed'}
      </span>
    )
  return <CheckIcon className="size-3.5 shrink-0 text-emerald-400" aria-label="Finished" />
}

// Prompt is the task the subagent was given, folded: it is often long.
function Prompt({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground">
        <ChevronRightIcon className={cn('size-3.5 transition-transform', open && 'rotate-90')} aria-hidden /> Prompt
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-2 max-h-96 overflow-auto rounded-md border bg-background/40 px-3 py-2 text-sm text-muted-foreground">
        <Markdown text={text} />
      </CollapsibleContent>
    </Collapsible>
  )
}

// Timeline is the subagent's own conversation, read when the card opens and
// re-read while the subagent runs.
function Timeline({ subagent, running }: { subagent: HistorySubagent; running: boolean }) {
  const item = useContext(ItemContext)
  const { data, error, isPending } = useQuery({
    queryKey: ['subagent', item, subagent.id],
    queryFn: ({ signal }) => api<History>(`/api/items/${item}/subagents/${encodeURIComponent(subagent.id)}`, undefined, signal),
    enabled: !!item,
    refetchInterval: running ? 3000 : false,
    retry: false,
  })
  if (isPending) return <p className="text-sm text-muted-foreground">Loading the subagent's work…</p>
  if (error) return <p className="text-sm text-muted-foreground">{(error as Error).message}</p>
  // Its first message is the prompt, shown above.
  const messages = data.messages.filter((message, index) => !(index === 0 && message.role === 'user'))
  if (!messages.length) return <p className="text-sm text-muted-foreground">{running ? 'The subagent has not reported anything yet.' : 'The subagent recorded no work.'}</p>
  return (
    <div className="flex flex-col gap-1.5" data-subagent-timeline>
      {data.has_more && <p className="text-xs text-muted-foreground">Earlier steps are not shown.</p>}
      {messages.map(message => (
        <TimelineEntry key={message.id} message={message} />
      ))}
    </div>
  )
}

function TimelineEntry({ message }: { message: HistoryMessage }) {
  if (message.role === 'user' && message.text)
    return (
      <div className="self-end rounded-lg bg-muted px-3 py-1.5 text-sm">
        <Markdown text={message.text} />
      </div>
    )
  if (message.role === 'subagent') return <SubagentReport message={message} />
  return (
    <div className="flex flex-col gap-1.5">
      {message.text && (
        <div className="text-sm">
          <Markdown text={message.text} />
        </div>
      )}
      {message.tools?.map((tool, index) => (
        <ToolCall key={tool.id || index} tool={tool} />
      ))}
    </div>
  )
}

// SubagentReport is the result a background subagent sent back when it
// finished: a line that opens on the full report.
export function SubagentReport({ message }: { message: HistoryMessage }) {
  const [open, setOpen] = useState(false)
  const subagent = message.subagent
  const failed = subagent?.status === 'failed' || subagent?.status === 'killed'
  const title = reportTitle(subagent?.description ?? '') || 'Subagent'
  return (
    <Collapsible open={open} onOpenChange={setOpen} data-role="subagent" data-subagent-report={subagent?.id ?? ''}>
      <CollapsibleTrigger className="flex max-w-full min-w-0 items-center gap-2 text-sm text-muted-foreground hover:text-foreground">
        {failed ? <XIcon className="size-4 shrink-0 text-destructive" aria-hidden /> : <CheckIcon className="size-4 shrink-0 text-emerald-400" aria-hidden />}
        <span className="min-w-0 truncate">
          {failed ? 'Subagent stopped' : 'Subagent finished'}: <span className="text-foreground/80">{title}</span>
        </span>
        <ChevronRightIcon className={cn('size-4 shrink-0 transition-transform', open && 'rotate-90')} aria-hidden />
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-2 rounded-lg border bg-muted/20 px-3 py-2 text-sm">
        {message.text ? <Markdown text={message.text} /> : <span className="text-muted-foreground">No result was returned.</span>}
      </CollapsibleContent>
    </Collapsible>
  )
}
