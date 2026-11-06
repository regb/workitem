import { fireEvent, render, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MessageRole, ToolCallStatus } from '@/gen/wi/core/v1/agents_pb'
import { ChatMessage } from './chat-message'
import { ItemContext } from './item-context'
import { ToolCall } from './tool-call'

const call = (name: string, input: unknown, extra: object = {}) => ({ id: name, name, status: 'completed', input: JSON.stringify(input), ...extra })

const getSubagentHistory = vi.fn()
vi.mock('@/lib/rpc', async original => ({ ...(await original<object>()), agents: { getSubagentHistory: (...args: unknown[]) => getSubagentHistory(...args) } }))

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
    // The subagent's conversation comes from the API, in its messages.
    const message = (id: string, role: MessageRole, text: string, tools: object[] = []) => ({ id, role, nativeIds: [], parts: [...(text ? [{ kind: { case: 'text', value: text } }] : []), ...tools.map(tool => ({ kind: { case: 'tool', value: tool } }))], clientMessageId: '', error: false, aborted: false })
    const read = vi.fn(async (_request: { itemId: string; subagentId: string }) => ({
      nextBefore: '',
      truncated: false,
      messages: [
        message('s1', MessageRole.USER, 'Go research'),
        message('s2', MessageRole.ASSISTANT, '', [{ id: 'WebSearch', name: 'WebSearch', status: ToolCallStatus.COMPLETED, input: JSON.stringify({ query: 'conductor' }), output: '', images: [] }]),
        message('s3', MessageRole.ASSISTANT, 'Found it'),
      ],
    }))
    getSubagentHistory.mockImplementation(read)
    const tool = call(
      'Agent',
      { description: 'Research Conductor', subagent_type: 'general-purpose', prompt: 'Go research' },
      { output: 'Async agent launched', subagent: { id: 'a1', type: 'general-purpose', description: 'Research Conductor', status: 'completed' } },
    )
    const { container } = render(withItem(<ToolCall tool={tool} />))
    expect(container.querySelector('[data-subagent="a1"]')?.getAttribute('data-subagent-status')).toBe('completed')
    expect(read).not.toHaveBeenCalled()
    fireEvent.click(within(container).getByText('Research Conductor'))
    await waitFor(() => expect(container.querySelector('[data-subagent-timeline]')).not.toBeNull())
    expect(read.mock.calls[0][0]).toEqual({ itemId: '01ITEM', subagentId: 'a1' })
    const timeline = container.querySelector('[data-subagent-timeline]')!
    expect(timeline.textContent).toContain('Searched the web conductor')
    expect(timeline.textContent).toContain('Found it')
    expect(timeline.textContent).not.toContain('Go research')
    expect(container.textContent).not.toContain('Async agent launched')
  })

  it('shows a finished subagent report as a line that opens on the report', () => {
    const message = { id: 'n', role: 'subagent', parts: [{ text: 'The **report**' }], subagent: { id: 'a1', description: 'Agent "Research Conductor" finished', status: 'completed' } }
    const { container } = render(withItem(<ChatMessage message={message} />))
    expect(container.textContent).toBe('Subagent finished: Research Conductor')
    fireEvent.click(within(container).getByText('Research Conductor'))
    expect(container.querySelector('strong')?.textContent).toBe('report')
  })

  it('shows a finished background command as a line', () => {
    const message = {
      id: 'c',
      role: 'subagent',
      parts: [],
      subagent: { id: 'b1', description: 'Background command "Wait for CI" was stopped after reaching its background time limit', status: 'killed' },
    }
    const { container } = render(withItem(<ChatMessage message={message} />))
    expect(container.textContent).toBe('Command stopped: Wait for CI')
    expect(container.querySelector('button')).toBeNull()
  })
})
