import { fireEvent, render, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChatMessage } from './chat-message'
import { ItemContext } from './subagent'
import { ToolCall } from './tool-call'

const call = (name: string, input: unknown, extra: object = {}) => ({ id: name, name, status: 'completed', input: JSON.stringify(input), ...extra })

afterEach(() => vi.unstubAllGlobals())

describe('ToolCall', () => {
  it('shows a shell call by its description, and the command and output as a terminal', () => {
    const { container } = render(<ToolCall tool={call('Bash', { command: 'git status', description: 'Show status' }, { output: 'clean' })} />)
    expect(container.querySelector('[data-tool-kind="shell"]')?.textContent).toBe('Show status')
    fireEvent.click(within(container).getByText('Show status'))
    const terminal = container.querySelector('[data-terminal]')
    expect(terminal?.textContent).toContain('git status')
    expect(terminal?.textContent).toContain('clean')
    expect(terminal?.textContent).not.toContain('{"command"')
  })

  it('folds long output until asked for all of it', () => {
    const output = Array.from({ length: 60 }, (_, i) => `line ${i}`).join('\n')
    const { container } = render(<ToolCall tool={call('bash', { command: 'seq 60' }, { output })} />)
    fireEvent.click(within(container).getByText('seq 60'))
    expect(container.textContent).not.toContain('line 59')
    fireEvent.click(within(container).getByText('Show all 60 lines'))
    expect(container.textContent).toContain('line 59')
  })

  it('shows an edit as a diff with its line counts', () => {
    const { container } = render(<ToolCall tool={call('Edit', { file_path: 'a/b.go', old_string: 'old', new_string: 'new\nmore' })} />)
    expect(container.textContent).toContain('Edited a/b.go')
    expect(container.textContent).toContain('+2 −1')
    fireEvent.click(within(container).getByText('a/b.go'))
    expect(container.querySelector('[data-diff]')?.textContent).toBe('oldnewmore')
  })
})

describe('subagents', () => {
  const withItem = (node: React.ReactNode) => (
    <QueryClientProvider client={new QueryClient()}>
      <ItemContext.Provider value="01ITEM">{node}</ItemContext.Provider>
    </QueryClientProvider>
  )

  it('opens a subagent card on its prompt and its own work, read on demand', async () => {
    const fetch = vi.fn(
      async (_url: string) =>
        new Response(
          JSON.stringify({
            leaf_id: 's3',
            messages: [
              { id: 's1', role: 'user', text: 'Go research' },
              { id: 's2', role: 'assistant', text: '', tools: [call('WebSearch', { query: 'conductor' })] },
              { id: 's3', role: 'assistant', text: 'Found it' },
            ],
          }),
        ),
    )
    vi.stubGlobal('fetch', fetch)
    const tool = call(
      'Agent',
      { description: 'Research Conductor', subagent_type: 'general-purpose', prompt: 'Go research' },
      { output: 'Async agent launched', subagent: { id: 'a1', type: 'general-purpose', description: 'Research Conductor', status: 'completed' } },
    )
    const { container } = render(withItem(<ToolCall tool={tool} />))
    expect(container.querySelector('[data-subagent="a1"]')?.getAttribute('data-subagent-status')).toBe('completed')
    expect(fetch).not.toHaveBeenCalled()
    fireEvent.click(within(container).getByText('Research Conductor'))
    await waitFor(() => expect(container.querySelector('[data-subagent-timeline]')).not.toBeNull())
    expect(fetch.mock.calls[0][0]).toBe('/api/items/01ITEM/subagents/a1')
    const timeline = container.querySelector('[data-subagent-timeline]')!
    expect(timeline.textContent).toContain('Searched the web conductor')
    expect(timeline.textContent).toContain('Found it')
    expect(timeline.textContent).not.toContain('Go research')
    expect(container.textContent).not.toContain('Async agent launched')
  })

  it('shows a finished subagent report as a line that opens on the report', () => {
    const message = { id: 'n', role: 'subagent', text: 'The **report**', subagent: { id: 'a1', description: 'Agent "Research Conductor" finished', status: 'completed' } }
    const { container } = render(withItem(<ChatMessage message={message} />))
    expect(container.textContent).toBe('Subagent finished: Research Conductor')
    fireEvent.click(within(container).getByText('Research Conductor'))
    expect(container.querySelector('strong')?.textContent).toBe('report')
  })
})
