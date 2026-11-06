import { useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const id = '01J0ABCDEFGHJKMNPQRSTVWXYZ'
vi.mock('@/lib/queries', async original => ({
  ...(await original<object>()),
  useWorkList: () => ({
    data: { projects: [{ id: 'p', name: 'web' }], display_sections: [{ name: 'IN PROGRESS', items: [{ id, slug: 'fix-login', title: 'Fix login', state: 'active', labels: [], project_id: 'p' }] }] },
  }),
}))

afterEach(cleanup)

describe('MentionField', () => {
  it('picks an item with @@, shows its title and sends its link', async () => {
    const { MentionField } = await import('./mention-field')
    const sent = vi.fn()
    let value = ''
    function Field() {
      const [text, setText] = useState('')
      value = text
      return <MentionField aria-label="Message" value={text} onValueChange={setText} onSubmit={sent} files={undefined} box="" />
    }
    render(
      <QueryClientProvider client={new QueryClient()}>
        <Field />
      </QueryClientProvider>,
    )
    const field = screen.getByLabelText('Message') as HTMLTextAreaElement
    fireEvent.change(field, { target: { value: 'see @@log' } })
    fireEvent.select(field)
    expect(screen.getByRole('option').textContent).toContain('Fix login')
    expect(field.getAttribute('aria-expanded')).toBe('true')
    fireEvent.keyDown(field, { key: 'Enter' })
    expect(sent).not.toHaveBeenCalled()
    expect(field.value).toBe('see @@Fix login ')
    expect(value).toBe(`see @@[Fix login](wi:${id} "fix-login") `)
    expect(screen.queryByRole('option')).toBeNull()
    // Deleting backwards into the mention removes it whole.
    fireEvent.change(field, { target: { value: 'see @@Fix login' } })
    field.setSelectionRange(15, 15)
    fireEvent.keyDown(field, { key: 'Backspace' })
    expect(value).toBe('see ')
  })

  it('shows a note in a bubble above the mention the caret is on', async () => {
    const { MentionField } = await import('./mention-field')
    const wire = `see @@[Fix login](wi:${id} "fix-login") now`
    function Field() {
      const [text, setText] = useState(wire)
      return <MentionField aria-label="Message" value={text} onValueChange={setText} onSubmit={() => {}} files={undefined} box="" note={ref => `about ${ref.title}`} />
    }
    render(
      <QueryClientProvider client={new QueryClient()}>
        <Field />
      </QueryClientProvider>,
    )
    const field = screen.getByLabelText('Message') as HTMLTextAreaElement
    // Just after the mention and its space: the bubble is about it.
    field.setSelectionRange(16, 16)
    fireEvent.select(field)
    expect(screen.getByRole('note', { name: 'About Fix login' }).textContent).toBe('about Fix login')
    // Typing on past it, the bubble goes.
    field.setSelectionRange(20, 20)
    fireEvent.select(field)
    expect(screen.queryByRole('note')).toBeNull()
  })
})
