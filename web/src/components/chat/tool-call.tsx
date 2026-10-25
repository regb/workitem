import { useState, type ReactNode } from 'react'
import { BrainIcon, FilePenIcon, FilePlusIcon, FileTextIcon, GlobeIcon, ListChecksIcon, SearchIcon, TerminalIcon, WrenchIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { CodeBlock, CodeBlockContent, CodeBlockLine } from '@/components/ui/code-block'
import { Trace, TraceCollapsible, TraceCollapsibleContent, TraceCollapsibleTrigger, TraceDescription, TraceIcon, TraceLine, TraceTitle } from '@/components/ui/trace'
import type { HistoryTool } from '@/lib/api'
import { diffStat, fileLanguage, shortPath, toolView, type ToolView } from '@/lib/tools'
import { cn } from '@/lib/utils'
import { Code } from './code'
import { Markdown } from './markdown'
import { MessageImages } from './message-images'
import { SubagentCall } from './subagent'

// ToolCall is one folded line naming what a tool did; expanding it shows the
// details in the form that tool's work reads best: a terminal, a diff, a
// file, search results.
export function ToolCall({ tool }: { tool: HistoryTool }) {
  const view = toolView(tool)
  if (view.kind === 'agent' || tool.subagent) return <SubagentCall tool={tool} view={view.kind === 'agent' ? view : undefined} />
  const status = tool.status === 'running' ? 'running' : tool.status === 'error' || tool.status === 'failed' ? 'error' : undefined
  const failed = status === 'error'
  const body = toolBody(view, tool, failed)
  const line = (
    <TraceLine status={status} data-tool-status={tool.status} data-tool-kind={view.kind}>
      <TraceIcon>{toolIcon(view)}</TraceIcon>
      <ToolTitle view={view} tool={tool} running={status === 'running'} />
    </TraceLine>
  )
  if (!body && !tool.images?.length) return <Trace>{line}</Trace>
  return (
    <Trace>
      <TraceCollapsible>
        <TraceCollapsibleTrigger>{line}</TraceCollapsibleTrigger>
        <TraceCollapsibleContent className="flex flex-col gap-2 pt-2">
          {body}
          {!!tool.images?.length && <MessageImages images={tool.images} />}
        </TraceCollapsibleContent>
      </TraceCollapsible>
    </Trace>
  )
}

function toolIcon(view: ToolView) {
  switch (view.kind) {
    case 'shell':
      return <TerminalIcon />
    case 'read':
      return <FileTextIcon />
    case 'edit':
      return <FilePenIcon />
    case 'write':
      return <FilePlusIcon />
    case 'search':
      return <SearchIcon />
    case 'web':
      return <GlobeIcon />
    case 'todo':
      return <ListChecksIcon />
    default:
      return <WrenchIcon />
  }
}

const mono = 'font-mono text-xs text-foreground/80'

// ToolTitle names the work: a verb, then what it worked on.
function ToolTitle({ view, tool, running }: { view: ToolView; tool: HistoryTool; running: boolean }) {
  const verb = (done: string, doing: string) => (running ? doing : done)
  switch (view.kind) {
    case 'shell':
      return <TraceTitle className={view.described ? 'text-foreground/80' : mono}>{view.title}</TraceTitle>
    case 'read':
      return (
        <>
          <TraceTitle>
            {verb('Read', 'Reading')} <span className={mono}>{shortPath(view.path)}</span>
          </TraceTitle>
          {view.range && <TraceDescription>{view.range}</TraceDescription>}
        </>
      )
    case 'edit': {
      const stat = diffStat(view.edits)
      return (
        <>
          <TraceTitle>
            {verb('Edited', 'Editing')} <span className={mono}>{shortPath(view.path)}</span>
          </TraceTitle>
          <TraceDescription>
            <span className="text-emerald-400">+{stat.added}</span> <span className="text-red-400">−{stat.removed}</span>
          </TraceDescription>
        </>
      )
    }
    case 'write':
      return (
        <>
          <TraceTitle>
            {verb('Wrote', 'Writing')} <span className={mono}>{shortPath(view.path)}</span>
          </TraceTitle>
          <TraceDescription>{lineCount(view.content)}</TraceDescription>
        </>
      )
    case 'search':
      return (
        <>
          <TraceTitle>
            {verb('Searched', 'Searching')} <span className={mono}>{view.title}</span>
          </TraceTitle>
          {view.scope && <TraceDescription className="font-mono text-xs">{shortPath(view.scope)}</TraceDescription>}
        </>
      )
    case 'web':
      return (
        <TraceTitle>
          {view.url ? verb('Fetched', 'Fetching') : verb('Searched the web', 'Searching the web')} <span className={view.url ? mono : 'text-foreground/80'}>{view.title}</span>
        </TraceTitle>
      )
    case 'todo': {
      const done = view.todos.filter(todo => todo.status === 'completed').length
      return (
        <>
          <TraceTitle>{verb('Updated the plan', 'Updating the plan')}</TraceTitle>
          <TraceDescription>
            {done}/{view.todos.length} done
          </TraceDescription>
        </>
      )
    }
    default:
      return (
        <TraceTitle>
          {verb('Ran', 'Running')} <span className="font-medium text-foreground/80">{tool.name}</span>
          {view.kind === 'other' && view.title && <span className={cn('ms-1.5', mono)}>{view.title}</span>}
        </TraceTitle>
      )
  }
}

// OperatorCommand is a shell command the operator ran in the agent's own
// prompt (Pi's `!ls`): on the operator's side, with the command and its
// output in view rather than folded like the agent's tools.
export function OperatorCommand({ tool }: { tool: HistoryTool }) {
  const view = toolView(tool)
  const command = view.kind === 'shell' ? view.command : (tool.input ?? '')
  const failed = tool.status === 'error' || tool.status === 'failed'
  return (
    <div data-operator-command className="flex w-full max-w-[80%] min-w-0 flex-col items-end gap-1 self-end">
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <TerminalIcon className="size-3.5" aria-hidden /> You ran
        {failed && <span className="text-red-400">· failed</span>}
      </span>
      <div className="flex w-full min-w-0 flex-col overflow-hidden rounded-xl border border-sky-500/30 bg-secondary" data-terminal>
        <div className="flex min-w-0 items-start gap-2 px-3 py-2 font-mono text-sm">
          <span className="text-sky-300 select-none" aria-hidden>
            $
          </span>
          <span className="min-w-0 break-all whitespace-pre-wrap text-foreground">{command}</span>
        </div>
        {tool.output?.trim() ? <Folded value={tool.output} language="ansi" failed={failed} bare /> : null}
      </div>
    </div>
  )
}

const lineCount = (value: string) => {
  const count = value ? value.replace(/\n$/, '').split('\n').length : 0
  return `${count} ${count === 1 ? 'line' : 'lines'}`
}

// toolBody is what expanding the line shows, or null when there is nothing.
function toolBody(view: ToolView, tool: HistoryTool, failed: boolean): ReactNode {
  const output = tool.output?.trim() ? <Folded value={tool.output} language={view.kind === 'shell' ? 'ansi' : 'text'} failed={failed} /> : null
  switch (view.kind) {
    case 'shell':
      return (
        <div className="flex flex-col gap-0 overflow-hidden rounded-lg border bg-black/30" data-terminal>
          <Code language="bash" value={view.command} className="rounded-none border-0 bg-transparent" contentClassName="max-h-48" copy wrap />
          {tool.output?.trim() ? <Folded value={tool.output} language="ansi" failed={failed} bare /> : null}
        </div>
      )
    case 'read':
      return tool.output?.trim() ? <Folded value={tool.output} language={fileLanguage(view.path)} failed={failed} /> : null
    case 'edit':
      return (
        <>
          {view.edits.length > 0 && <Diff edits={view.edits} />}
          {failed && output}
        </>
      )
    case 'write':
      return (
        <>
          {view.content && <Folded value={view.content} language={fileLanguage(view.path)} />}
          {failed && output}
        </>
      )
    case 'search':
      return output
    case 'web':
      return (
        <>
          {view.url && (
            <a href={view.url} target="_blank" rel="noreferrer" className="w-fit max-w-full truncate text-xs text-sky-300 underline underline-offset-2">
              {view.url}
            </a>
          )}
          {tool.output?.trim() && (
            <div className="max-h-96 overflow-auto rounded-lg border px-3 py-2 text-sm">
              <Markdown text={tool.output} />
            </div>
          )}
        </>
      )
    case 'todo':
      return (
        <ul className="flex flex-col gap-1 text-sm">
          {view.todos.map((todo, index) => (
            <li key={index} className={cn('flex gap-2', todo.status === 'completed' && 'text-muted-foreground line-through')}>
              <span aria-hidden>{todo.status === 'completed' ? '☑' : todo.status === 'in_progress' ? '◐' : '☐'}</span>
              <span>{todo.text}</span>
            </li>
          ))}
        </ul>
      )
    case 'other':
      return (
        <>
          {view.input && view.input !== '{}' && <Code language="json" value={view.input} contentClassName="max-h-64" />}
          {output}
        </>
      )
    default:
      return output
  }
}

// Long output shows its first lines until asked for the rest.
const foldAt = 20

// Folded is output or file content, cut to its first lines with a way to see
// all of them. A failure is drawn as one.
function Folded({ value, language, failed = false, bare = false }: { value: string; language: string; failed?: boolean; bare?: boolean }) {
  const [all, setAll] = useState(false)
  const text = value.replace(/\n+$/, '')
  const lines = text.split('\n')
  const shown = all || lines.length <= foldAt + 4 ? text : lines.slice(0, foldAt).join('\n')
  return (
    <div className="flex flex-col" data-output>
      <Code
        language={language}
        value={shown}
        copy={!bare}
        className={cn(bare && 'rounded-none border-0 border-t bg-transparent', failed && 'text-red-300')}
        contentClassName={all ? 'max-h-[32rem]' : undefined}
      />
      {shown !== text && (
        <Button variant="ghost" size="xs" className="self-start text-muted-foreground" onClick={() => setAll(true)}>
          Show all {lines.length} lines
        </Button>
      )}
    </div>
  )
}

// Diff shows an edit's removed and added lines.
function Diff({ edits }: { edits: { before: string; after: string }[] }) {
  return (
    <CodeBlock size="sm" data-diff>
      <CodeBlockContent className="max-h-[32rem]">
        {edits.map((edit, index) => (
          <div key={index} className={cn(index > 0 && 'mt-2 border-t pt-2')}>
            {edit.before.split('\n').map((line, at) =>
              edit.before ? (
                <CodeBlockLine key={`-${at}`} variant="removed">
                  {line || ' '}
                </CodeBlockLine>
              ) : null,
            )}
            {edit.after.split('\n').map((line, at) =>
              edit.after ? (
                <CodeBlockLine key={`+${at}`} className="bg-emerald-500/10 after:text-emerald-400 after:content-['+']">
                  {line || ' '}
                </CodeBlockLine>
              ) : null,
            )}
          </div>
        ))}
      </CodeBlockContent>
    </CodeBlock>
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
