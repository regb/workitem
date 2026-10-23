import { BrainIcon, FileIcon, SearchIcon, TerminalIcon, WrenchIcon } from 'lucide-react'
import { CodeBlock, CodeBlockContent } from '@/components/ui/code-block'
import { Trace, TraceCollapsible, TraceCollapsibleContent, TraceCollapsibleTrigger, TraceIcon, TraceLine, TraceTitle } from '@/components/ui/trace'
import type { HistoryTool } from '@/lib/api'
import { MessageImages } from './message-images'

// toolSummary pulls the most useful argument (a command, path or pattern)
// out of a tool's JSON input for its one-line title.
export function toolSummary(tool: HistoryTool) {
  try {
    const input = JSON.parse(tool.input ?? '') as Record<string, unknown>
    for (const key of ['command', 'cmd', 'file_path', 'path', 'pattern', 'query', 'url', 'description']) {
      if (typeof input[key] === 'string' && input[key]) return (input[key] as string).split('\n')[0]
    }
  } catch {
    // Not JSON: show the name only.
  }
  return ''
}

function icon(name: string) {
  const lower = name.toLowerCase()
  if (/bash|shell|exec|command/.test(lower)) return <TerminalIcon />
  if (/grep|glob|search|find/.test(lower)) return <SearchIcon />
  if (/read|write|edit|file/.test(lower)) return <FileIcon />
  return <WrenchIcon />
}

export function ToolCall({ tool }: { tool: HistoryTool }) {
  const status = tool.status === 'running' ? 'running' : tool.status === 'error' || tool.status === 'failed' ? 'error' : undefined
  const verb = status === 'running' ? 'Running' : status === 'error' ? 'Failed' : 'Ran'
  const summary = toolSummary(tool)
  const hasDetail = !!(tool.input || tool.output || tool.images?.length)
  const line = (
    <TraceLine status={status} data-tool-status={tool.status}>
      <TraceIcon>{icon(tool.name)}</TraceIcon>
      <TraceTitle>
        {verb} <span className="font-medium text-foreground/80">{tool.name}</span>
        {summary && <span className="ms-1.5 font-mono text-xs">{summary}</span>}
      </TraceTitle>
    </TraceLine>
  )
  if (!hasDetail) return <Trace>{line}</Trace>
  return (
    <Trace>
      <TraceCollapsible>
        <TraceCollapsibleTrigger>{line}</TraceCollapsibleTrigger>
        <TraceCollapsibleContent className="flex flex-col gap-2 pt-2">
          {tool.input && <Output label="Input" value={tool.input} />}
          {tool.output && <Output label="Output" value={tool.output} />}
          {!!tool.images?.length && <MessageImages images={tool.images} />}
        </TraceCollapsibleContent>
      </TraceCollapsible>
    </Trace>
  )
}

function Output({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs text-muted-foreground">{label}</span>
      <CodeBlock size="sm">
        <CodeBlockContent wrap className="max-h-64">
          {value}
        </CodeBlockContent>
      </CodeBlock>
    </div>
  )
}

// Thinking shows an agent's shared reasoning as a collapsed line; it is
// running while the agent is still thinking.
export function Thinking({ text, active }: { text: string; active: boolean }) {
  return (
    <Trace>
      <TraceCollapsible>
        <TraceCollapsibleTrigger>
          <TraceLine status={active ? 'running' : undefined} data-thinking>
            <TraceIcon>
              <BrainIcon />
            </TraceIcon>
            <TraceTitle>{active ? 'Thinking' : 'Thought'}</TraceTitle>
          </TraceLine>
        </TraceCollapsibleTrigger>
        <TraceCollapsibleContent className="pt-2">
          <p className="text-sm whitespace-pre-wrap text-muted-foreground">{text}</p>
        </TraceCollapsibleContent>
      </TraceCollapsible>
    </Trace>
  )
}
