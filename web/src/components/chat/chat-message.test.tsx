import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { visible } from '@/lib/history'
import { ChatMessage, LivePreview } from './chat-message'
import { ItemContext } from './item-context'
import { TurnFold } from './turn-fold'
import type { MessagePart } from '@/lib/views'

describe('LivePreview', () => {
  it('shows a live message in the order it was written', () => {
    const live = { activity: 'Assistant responding', parts: [{ text: 'Let me look.' }, { tool: { id: 't1', name: 'read_file', status: 'completed' } }, { text: 'Found it.' }] }
    const text = render(<LivePreview live={live} />).container.textContent ?? ''
    const [before, call, after] = ['Let me look.', 'read_file', 'Found it.'].map(part => text.indexOf(part))
    expect(before).toBeGreaterThanOrEqual(0)
    expect(before < call && call < after).toBe(true)
  })

  it('shows a working indicator while the agent works and nothing is written yet', () => {
    expect(render(<LivePreview live={{ activity: 'Idle' }} working />).container.querySelector('[data-working]')).not.toBeNull()
    expect(render(<LivePreview live={null} working />).container.querySelector('[data-working]')).not.toBeNull()
    expect(render(<LivePreview live={{ activity: 'Turn failed' }} />).container.querySelector('[data-working]')).toBeNull()
  })

  it('never shows live stream errors, only whether the agent works', () => {
    const live = { error: 'no verified live runtime content endpoint' }
    expect(render(<LivePreview live={live} />).container.innerHTML).toBe('')
    const starting = render(<LivePreview live={live} working />).container
    expect(starting.querySelector('[data-working]')).not.toBeNull()
    expect(starting.textContent).not.toContain('runtime')
  })

  it('shows a stale activity as nothing, and as working once a reply is expected', () => {
    const stale = { activity: 'Tool completed' }
    expect(render(<LivePreview live={stale} />).container.innerHTML).toBe('')
    const waiting = render(<LivePreview live={stale} working />).container
    expect(waiting.querySelector('[data-working]')).not.toBeNull()
    expect(waiting.textContent).not.toContain('Tool completed')
  })

  it('lists the calls in the message, in order, each in its current state', () => {
    const live = {
      activity: 'Tool running: read',
      parts: [{ tool: { id: 'a', name: 'bash', status: 'completed' } }, { tool: { id: 'b', name: 'read', status: 'running' } }],
    }
    const { container } = render(<LivePreview live={live} />)
    expect([...container.querySelectorAll('[data-tool-status]')].map(line => line.getAttribute('data-tool-status'))).toEqual(['completed', 'running'])
  })

  it('shows reasoning as a collapsed line that runs while the agent thinks', () => {
    const thinking = render(<LivePreview live={{ activity: 'Thinking', parts: [{ thought: { text: 'Comparing options' } }] }} />).container
    expect(thinking.querySelector('[data-thinking]')?.textContent).toContain('Thinking')
    expect(thinking.querySelector('[data-working]')).toBeNull()
    // Reasoning kept private still shows as thinking, without text to open.
    const hidden = render(<LivePreview live={{ activity: 'Thinking' }} working />).container
    expect(hidden.querySelector('[data-thinking]')?.textContent).toContain('Thinking')
    expect(hidden.querySelector('[data-working]')).toBeNull()
    expect(hidden.querySelector('button')).toBeNull()
    const answered = render(<LivePreview live={{ activity: 'Assistant responding', parts: [{ thought: { text: 'Comparing options' } }, { text: 'Use B.' }] }} />).container
    expect(answered.querySelector('[data-thinking]')?.textContent).toContain('Thought')
    // Reasoning again after a call runs at the end of the message; the
    // earlier stretch is done.
    const again = render(<LivePreview live={{ activity: 'Thinking', parts: [{ thought: { text: 'First' } }, { tool: { id: 't', name: 'bash', status: 'completed' } }] }} />).container
    expect([...again.querySelectorAll('[data-thinking]')].map(line => line.textContent)).toEqual(['Thought', 'Thinking'])
  })

  it('shows saved reasoning, with how long it took when known', () => {
    const claude = render(<ChatMessage message={{ id: 'r', role: 'assistant', parts: [{ thought: { duration_ms: 75_000 } }] }} />).container
    expect(claude.textContent).toBe('Thought for 1m 15s')
    expect(claude.querySelector('button')).toBeNull()
    const pi = render(<ChatMessage message={{ id: 'p', role: 'assistant', parts: [{ thought: { text: 'Comparing options' } }, { text: 'Use B.' }] }} />).container
    expect(pi.querySelector('[data-thinking]')?.textContent).toBe('Thought')
    fireEvent.click(within(pi).getByRole('button', { name: 'Thought' }))
    expect(pi.textContent).toContain('Comparing options')
    expect(visible({ id: 'q', role: 'assistant', parts: [{ thought: {} }] })).toBe(true)
  })

  it('keeps a linked message until history shows its entry, even after the turn settled', () => {
    const live = { activity: 'Idle', parts: [{ text: 'Final answer' }], saved_as: ['entry'] }
    expect(render(<LivePreview live={live} />).container.textContent).toContain('Final answer')
    expect(render(<LivePreview live={live} savedInHistory />).container.innerHTML).toBe('')
  })

  it('hides a settled message and shows one being written', () => {
    expect(render(<LivePreview live={{ activity: 'Idle' }} />).container.innerHTML).toBe('')
    // A finished reply stays until history's last message has it.
    const done = { activity: 'Message saved', parts: [{ text: 'Done.' }] }
    expect(render(<LivePreview live={done} />).container.textContent).toContain('Done.')
    expect(render(<LivePreview live={done} savedText="Done." />).container.innerHTML).toBe('')
    const writing = render(<LivePreview live={{ activity: 'Assistant responding', parts: [{ text: 'Working on it' }] }} />).container
    expect(writing.textContent).toContain('Working on it')
    const awaiting = render(<LivePreview live={{ activity: 'Assistant message completed', parts: [{ text: 'Finished' }] }} />).container
    expect(awaiting.textContent).toContain('Finished')
  })
})

describe('a message', () => {
  const parts: MessagePart[] = [{ text: 'Let me look.' }, { tool: { id: 't1', name: 'read_file', input: '{"path":"a.go"}', status: 'completed' } }, { text: 'Found it.' }, { thought: { text: 'Is that all?' } }]
  // What a message shows, part by part: the text, call and reasoning lines.
  const shape = (container: HTMLElement) =>
    [...container.querySelectorAll('[data-slot="message-content"] > *')].map(element => (element.querySelector('[data-thinking]') || element.matches('[data-thinking]') ? 'thought' : element.querySelector('[data-tool-status]') ? 'tool' : `text:${element.textContent}`))

  it('shows its parts in the order they were written', () => {
    const { container } = render(<ChatMessage message={{ id: 'm', role: 'assistant', parts }} />)
    expect(shape(container)).toEqual(['text:Let me look.', 'tool', 'text:Found it.', 'thought'])
  })

  it('looks the same live as once saved', () => {
    const saved = render(<ChatMessage message={{ id: 'm', role: 'assistant', parts }} />).container
    const live = render(<LivePreview live={{ activity: 'Message saved', parts }} />).container
    expect(shape(live)).toEqual(shape(saved))
    // Only the generated IDs of folds differ.
    const html = (container: HTMLElement) => container.querySelector('[data-slot="message-content"]')?.innerHTML.replace(/ id="[^"]*"/g, '')
    expect(html(live)).toBe(html(saved))
  })
})

describe('TurnFold', () => {
  it('counts the folded work and opens on it', () => {
    const { container } = render(
      <TurnFold fold="fold-m1" tools={3} texts={1}>
        <p>step detail</p>
      </TurnFold>,
    )
    expect(container.textContent).toBe('3 tool calls, 1 message')
    fireEvent.click(screen.getByRole('button', { name: '3 tool calls, 1 message' }))
    expect(container.textContent).toContain('step detail')
  })

  it('opens as the reader left it in that conversation', () => {
    const fold = (item: string) => (
      <ItemContext.Provider value={item}>
        <TurnFold fold="fold-m2" tools={1} texts={0}>
          <p>step detail</p>
        </TurnFold>
      </ItemContext.Provider>
    )
    const first = render(fold('a'))
    fireEvent.click(screen.getByRole('button', { name: '1 tool call' }))
    first.unmount()
    expect(render(fold('a')).container.textContent).toContain('step detail')
    expect(render(fold('b')).container.textContent).not.toContain('step detail')
  })
})

describe('aborted replies', () => {
  it('marks a reply the operator stopped, with or without its partial text', () => {
    const partial = render(<ChatMessage message={{ id: 'a', role: 'assistant', parts: [{ text: 'partial' }], aborted: true }} />).container
    expect(partial.textContent).toContain('partial')
    expect(partial.querySelector('[data-aborted]')?.textContent).toContain('Aborted')
    expect(visible({ id: 'b', role: 'assistant', parts: [], aborted: true })).toBe(true)
    expect(render(<ChatMessage message={{ id: 'c', role: 'assistant', parts: [{ text: 'done' }] }} />).container.querySelector('[data-aborted]')).toBeNull()
  })
})

describe('operator commands', () => {
  it('shows a command run from the agent prompt with its output, on the operator side', () => {
    const tool = { name: 'bash', input: '{"command":"ls"}', output: 'README.md', status: 'completed' }
    const { container } = render(<ChatMessage message={{ id: 'a', role: 'user', parts: [{ tool }] }} />)
    const command = container.querySelector('[data-role="user"] [data-operator-command]')
    expect(command?.textContent).toContain('You ran')
    expect(command?.textContent).toContain('ls')
    expect(command?.textContent).toContain('README.md')
    expect(container.querySelector('[data-slot="bubble-content"]')).toBeNull()
  })
})

describe('summaries', () => {
  it('shows a compaction as a folded block that opens on its summary', () => {
    const message = { id: 's', role: 'summary', parts: [{ text: 'The goal was **this**.' }], summary: { kind: 'compaction' as const, tokens_before: 261479 } }
    const { container } = render(<ChatMessage message={message} />)
    const block = container.querySelector('[data-summary="compaction"]')!
    expect(block.textContent).toContain('Context compacted')
    expect(block.textContent).toContain('261k tokens before')
    expect(block.textContent).not.toContain('The goal was')
    fireEvent.click(screen.getByRole('button', { name: /Show summary/ }))
    expect(block.textContent).toContain('The goal was this.')
  })

  it('names a branch summary', () => {
    const { container } = render(<ChatMessage message={{ id: 'b', role: 'summary', parts: [{ text: 'abandoned' }], summary: { kind: 'branch' } }} />)
    expect(container.querySelector('[data-summary="branch"]')?.textContent).toContain('Summary of an earlier branch')
  })
})

describe('harness notices', () => {
  it('names whose usage limit stopped the turn', () => {
    const { container } = render(<ChatMessage message={{ id: 'n', role: 'assistant', error: true, usage_provider: 'claude', parts: [{ text: 'You hit your limit, resets 5pm' }] }} />)
    expect(container.querySelector('[data-role="notice"]')?.textContent).toBe('Claude usage limit: You hit your limit, resets 5pm')
  })
})
