import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { visible } from '@/lib/history'
import { ChatMessage, LivePreview } from './chat-message'
import { TurnFold } from './turn-fold'

describe('LivePreview', () => {
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

  it('lists only the live tool calls it is given, in order', () => {
    const live = { activity: 'Tool running: bash', tool: { id: 'b', name: 'read', status: 'running' } }
    expect(render(<LivePreview live={live} />).container.querySelector('[data-tool-status]')).toBeNull()
    const tools = [
      { id: 'a', name: 'bash', status: 'completed' },
      { id: 'b', name: 'read', status: 'running' },
    ]
    const { container } = render(<LivePreview live={live} tools={tools} />)
    expect([...container.querySelectorAll('[data-tool-status]')].map(line => line.getAttribute('data-tool-status'))).toEqual(['completed', 'running'])
  })

  it('shows reasoning as a collapsed line that runs while the agent thinks', () => {
    const thinking = render(<LivePreview live={{ activity: 'Thinking', thought: 'Comparing options' }} />).container
    expect(thinking.querySelector('[data-thinking]')?.textContent).toContain('Thinking')
    const answered = render(<LivePreview live={{ activity: 'Assistant responding', thought: 'Comparing options', text: 'Use B.' }} />).container
    expect(answered.querySelector('[data-thinking]')?.textContent).toContain('Thought')
  })

  it('keeps linked text until history shows its entry, even after the turn settled', () => {
    const live = { activity: 'Idle', text: 'Final answer', saved_as: ['entry'] }
    expect(render(<LivePreview live={live} />).container.textContent).toContain('Final answer')
    expect(render(<LivePreview live={live} savedInHistory />).container.innerHTML).toBe('')
  })

  it('hides settled text and shows text being written', () => {
    expect(render(<LivePreview live={{ activity: 'Idle' }} />).container.innerHTML).toBe('')
    // A finished reply stays until history's last message has it.
    expect(render(<LivePreview live={{ activity: 'Message saved', text: 'Done.' }} />).container.textContent).toContain('Done.')
    expect(render(<LivePreview live={{ activity: 'Message saved', text: 'Done.' }} savedText="Done." />).container.innerHTML).toBe('')
    const writing = render(<LivePreview live={{ activity: 'Assistant responding', text: 'Working on it' }} />).container
    expect(writing.textContent).toContain('Working on it')
    const awaiting = render(<LivePreview live={{ activity: 'Assistant message completed', text: 'Finished' }} />).container
    expect(awaiting.textContent).toContain('Finished')
  })
})

describe('TurnFold', () => {
  it('counts the folded work and opens on it', () => {
    const { container } = render(
      <TurnFold tools={3} texts={1}>
        <p>step detail</p>
      </TurnFold>,
    )
    expect(container.textContent).toBe('3 tool calls, 1 message')
    fireEvent.click(screen.getByRole('button', { name: '3 tool calls, 1 message' }))
    expect(container.textContent).toContain('step detail')
  })
})

describe('aborted replies', () => {
  it('marks a reply the operator stopped, with or without its partial text', () => {
    const partial = render(<ChatMessage message={{ id: 'a', role: 'assistant', text: 'partial', aborted: true }} />).container
    expect(partial.textContent).toContain('partial')
    expect(partial.querySelector('[data-aborted]')?.textContent).toContain('Aborted')
    expect(visible({ id: 'b', role: 'assistant', text: '', aborted: true })).toBe(true)
    expect(render(<ChatMessage message={{ id: 'c', role: 'assistant', text: 'done' }} />).container.querySelector('[data-aborted]')).toBeNull()
  })
})

describe('operator commands', () => {
  it('shows a command run from the agent prompt with its output, on the operator side', () => {
    const tools = [{ name: 'bash', input: '{"command":"ls"}', output: 'README.md', status: 'completed' }]
    const { container } = render(<ChatMessage message={{ id: 'a', role: 'user', text: '', tools }} />)
    const command = container.querySelector('[data-role="user"] [data-operator-command]')
    expect(command?.textContent).toContain('You ran')
    expect(command?.textContent).toContain('ls')
    expect(command?.textContent).toContain('README.md')
    expect(container.querySelector('[data-slot="bubble-content"]')).toBeNull()
  })
})

describe('summaries', () => {
  it('shows a compaction as a folded block that opens on its summary', () => {
    const message = { id: 's', role: 'summary', text: 'The goal was **this**.', summary: { kind: 'compaction' as const, tokens_before: 261479 } }
    const { container } = render(<ChatMessage message={message} />)
    const block = container.querySelector('[data-summary="compaction"]')!
    expect(block.textContent).toContain('Context compacted')
    expect(block.textContent).toContain('261k tokens before')
    expect(block.textContent).not.toContain('The goal was')
    fireEvent.click(screen.getByRole('button', { name: /Show summary/ }))
    expect(block.textContent).toContain('The goal was this.')
  })

  it('names a branch summary', () => {
    const { container } = render(<ChatMessage message={{ id: 'b', role: 'summary', text: 'abandoned', summary: { kind: 'branch' } }} />)
    expect(container.querySelector('[data-summary="branch"]')?.textContent).toContain('Summary of an earlier branch')
  })
})
