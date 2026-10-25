import type { HistoryTool } from './api'

// What a tool call is, as far as showing it goes. Drivers name tools and
// their arguments differently (Claude's Bash and file_path, Pi's bash and
// path), so this reads both.
export type ToolView =
  | { kind: 'shell'; title: string; described: boolean; command: string }
  | { kind: 'read'; path: string; range: string }
  | { kind: 'edit'; path: string; edits: { before: string; after: string }[] }
  | { kind: 'write'; path: string; content: string }
  | { kind: 'search'; title: string; scope: string }
  | { kind: 'web'; title: string; url: string }
  | { kind: 'todo'; todos: { text: string; status: string }[] }
  | { kind: 'agent'; title: string; type: string; prompt: string }
  | { kind: 'other'; title: string; input: string }

export function toolArgs(tool: HistoryTool): Record<string, unknown> {
  try {
    const value = JSON.parse(tool.input ?? '') as unknown
    return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

const text = (value: unknown) => (typeof value === 'string' ? value : typeof value === 'number' ? String(value) : '')
const firstLine = (value: string) => value.split('\n')[0]

export function toolView(tool: HistoryTool): ToolView {
  const args = toolArgs(tool)
  const name = tool.name.toLowerCase()
  const path = text(args.file_path) || text(args.path) || text(args.notebook_path)
  if (/^(bash|shell|exec|monitor)$/.test(name) && text(args.command)) {
    const command = text(args.command)
    // Without a description, the command is the title, minus a leading
    // change of directory, which says little and takes the whole line.
    const bare = firstLine(command.replace(/^\s*cd\s+("[^"]*"|'[^']*'|\S+)\s*(&&|;)\s*/, '')) || firstLine(command)
    return { kind: 'shell', title: text(args.description) || bare, described: !!text(args.description), command }
  }
  if (name === 'read' && path) {
    const offset = Number(args.offset) || 0
    const limit = Number(args.limit) || 0
    const range = offset || limit ? `lines ${offset || 1}${limit ? `–${(offset || 1) + limit - 1}` : '+'}` : ''
    return { kind: 'read', path, range }
  }
  if ((name === 'edit' || name === 'multiedit') && path) {
    const pairs = Array.isArray(args.edits) ? (args.edits as Record<string, unknown>[]) : [args]
    const edits = pairs.map(edit => ({ before: text(edit.old_string) || text(edit.oldText), after: text(edit.new_string) || text(edit.newText) }))
    return { kind: 'edit', path, edits: edits.filter(edit => edit.before || edit.after) }
  }
  if (name === 'write' && path) return { kind: 'write', path, content: text(args.content) }
  if (/^(grep|glob|find|ls|toolsearch)$/.test(name)) {
    const pattern = text(args.pattern) || text(args.query) || text(args.glob)
    if (pattern) return { kind: 'search', title: pattern, scope: path || text(args.glob) }
  }
  if (name === 'websearch' && text(args.query)) return { kind: 'web', title: text(args.query), url: '' }
  if (name === 'webfetch' && text(args.url)) return { kind: 'web', title: text(args.url), url: text(args.url) }
  if (name === 'todowrite' && Array.isArray(args.todos)) {
    const todos = (args.todos as Record<string, unknown>[]).map(todo => ({ text: text(todo.content) || text(todo.activeForm), status: text(todo.status) }))
    return { kind: 'todo', todos }
  }
  if ((name === 'agent' || name === 'task') && (text(args.description) || text(args.prompt))) {
    return { kind: 'agent', title: text(args.description) || firstLine(text(args.prompt)), type: text(args.subagent_type), prompt: text(args.prompt) }
  }
  return { kind: 'other', title: otherSummary(args), input: prettyInput(tool.input ?? '') }
}

// otherSummary pulls the most useful argument out of an unknown tool's input.
function otherSummary(args: Record<string, unknown>) {
  for (const key of ['command', 'cmd', 'file_path', 'path', 'pattern', 'query', 'url', 'description', 'skill', 'prompt']) {
    if (text(args[key])) return firstLine(text(args[key]))
  }
  return ''
}

// prettyInput indents JSON input; anything else is shown as it is.
function prettyInput(input: string) {
  try {
    return JSON.stringify(JSON.parse(input), null, 2)
  } catch {
    return input
  }
}

// diffText shows an edit as removed and added lines, for diff highlighting.
export function diffText(edits: { before: string; after: string }[]) {
  const lines = (value: string, mark: string) => (value ? value.split('\n').map(line => mark + line) : [])
  return edits.map(edit => [...lines(edit.before, '-'), ...lines(edit.after, '+')].join('\n')).join('\n@@\n')
}

// diffStat counts the lines an edit removes and adds.
export function diffStat(edits: { before: string; after: string }[]) {
  const count = (value: string) => (value ? value.split('\n').length : 0)
  return edits.reduce((stat, edit) => ({ removed: stat.removed + count(edit.before), added: stat.added + count(edit.after) }), { removed: 0, added: 0 })
}

// The highlighter's name for a file's language, by its extension.
const extensions: Record<string, string> = {
  css: 'css',
  diff: 'diff',
  go: 'go',
  html: 'html',
  js: 'javascript',
  jsx: 'jsx',
  json: 'json',
  md: 'markdown',
  mjs: 'javascript',
  py: 'python',
  sh: 'bash',
  sql: 'sql',
  ts: 'typescript',
  tsx: 'tsx',
  yaml: 'yaml',
  yml: 'yaml',
}
export const fileLanguage = (path: string) => extensions[/\.([a-z0-9]+)$/i.exec(path)?.[1]?.toLowerCase() ?? ''] ?? 'text'

// shortPath keeps a path's last parts, which say the most in a title.
export function shortPath(path: string, parts = 3) {
  const segments = path.split('/').filter(Boolean)
  return segments.length > parts ? `…/${segments.slice(-parts).join('/')}` : path
}

// reportTitle reads the description out of Claude's "Agent "…" finished".
export function reportTitle(summary: string) {
  return /^Agent "(.*)" (?:finished|completed|failed|was stopped|stopped)$/.exec(summary)?.[1] ?? summary
}
