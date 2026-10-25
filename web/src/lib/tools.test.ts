import { describe, expect, it } from 'vitest'
import { diffStat, diffText, fileLanguage, reportTitle, shortPath, toolView } from './tools'

const tool = (name: string, input: unknown) => ({ name, status: 'completed', input: JSON.stringify(input) })

describe('toolView', () => {
  it('reads shell calls from Claude and Pi, titled by their description', () => {
    expect(toolView(tool('Bash', { command: 'git status\n--short', description: 'Show status' }))).toEqual({ kind: 'shell', title: 'Show status', described: true, command: 'git status\n--short' })
    expect(toolView(tool('bash', { command: 'ls -la' }))).toEqual({ kind: 'shell', title: 'ls -la', described: false, command: 'ls -la' })
    expect(toolView(tool('Bash', { command: 'cd "/tmp/a b" && git log -1' }))).toMatchObject({ title: 'git log -1' })
    expect(toolView(tool('Bash', { command: 'cd /tmp/x' }))).toMatchObject({ title: 'cd /tmp/x' })
  })

  it('reads file reads, edits and writes under either argument name', () => {
    expect(toolView(tool('Read', { file_path: '/a/b.ts', offset: 10, limit: 5 }))).toEqual({ kind: 'read', path: '/a/b.ts', range: 'lines 10–14' })
    expect(toolView(tool('read', { path: 'README.md' }))).toEqual({ kind: 'read', path: 'README.md', range: '' })
    expect(toolView(tool('Edit', { file_path: 'x.go', old_string: 'a', new_string: 'b\nc' }))).toEqual({ kind: 'edit', path: 'x.go', edits: [{ before: 'a', after: 'b\nc' }] })
    expect(toolView(tool('edit', { path: 'y.ts', edits: [{ oldText: 'p', newText: 'q' }] }))).toEqual({ kind: 'edit', path: 'y.ts', edits: [{ before: 'p', after: 'q' }] })
    expect(toolView(tool('write', { path: 'n.md', content: 'hi' }))).toEqual({ kind: 'write', path: 'n.md', content: 'hi' })
  })

  it('reads searches, web calls, plans and subagents', () => {
    expect(toolView(tool('Grep', { pattern: 'TODO', path: 'src' }))).toEqual({ kind: 'search', title: 'TODO', scope: 'src' })
    expect(toolView(tool('WebSearch', { query: 'conductor docs' }))).toEqual({ kind: 'web', title: 'conductor docs', url: '' })
    expect(toolView(tool('WebFetch', { url: 'https://x.test', prompt: 'p' }))).toEqual({ kind: 'web', title: 'https://x.test', url: 'https://x.test' })
    expect(toolView(tool('TodoWrite', { todos: [{ content: 'a', status: 'completed' }] }))).toEqual({ kind: 'todo', todos: [{ text: 'a', status: 'completed' }] })
    expect(toolView(tool('Agent', { description: 'Look', subagent_type: 'general-purpose', prompt: 'Go' }))).toEqual({ kind: 'agent', title: 'Look', type: 'general-purpose', prompt: 'Go' })
  })

  it('shows anything else with its main argument and indented input', () => {
    expect(toolView(tool('Skill', { skill: 'review', args: 'x' }))).toEqual({ kind: 'other', title: 'review', input: '{\n  "skill": "review",\n  "args": "x"\n}' })
    expect(toolView({ name: 'x', status: 'completed', input: 'not json' })).toEqual({ kind: 'other', title: '', input: 'not json' })
    expect(toolView(tool('Bash', {}))).toMatchObject({ kind: 'other' })
  })
})

describe('tool helpers', () => {
  it('counts and marks edited lines', () => {
    const edits = [{ before: 'a\nb', after: 'c' }]
    expect(diffStat(edits)).toEqual({ removed: 2, added: 1 })
    expect(diffText(edits)).toBe('-a\n-b\n+c')
  })

  it('names languages, shortens paths and reads report titles', () => {
    expect(fileLanguage('internal/web/server.go')).toBe('go')
    expect(fileLanguage('Makefile')).toBe('text')
    expect(shortPath('/Users/me/src/wi/internal/web/server.go')).toBe('…/internal/web/server.go')
    expect(shortPath('web/a.ts')).toBe('web/a.ts')
    expect(reportTitle('Agent "Research Conductor" finished')).toBe('Research Conductor')
    expect(reportTitle('Something else')).toBe('Something else')
  })
})
