import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { applyContent } from '@/lib/content'
import { answerOf, parseQuestions } from '@/lib/questions'
import { QuestionCard } from './question-card'

const answerQuestion = vi.fn(async (..._args: unknown[]) => true)
vi.mock('@/lib/actions', async original => ({ ...(await original<object>()), answerQuestion: (...args: unknown[]) => answerQuestion(...args) }))

afterEach(() => {
  cleanup()
  answerQuestion.mockClear()
})

const input = JSON.stringify({
  questions: [
    { question: 'Which color?', header: 'Color', multiSelect: false, options: [{ label: 'Red', description: 'Warm' }, { label: 'Blue' }] },
    { question: 'Which sizes?', multiSelect: true, options: [{ label: 'S' }, { label: 'M' }, { label: 'L' }] },
  ],
})

describe('questions', () => {
  it('reads the questions of a question call and turns choices into answers', () => {
    expect(parseQuestions(input).map(q => [q.question, q.options.length, q.multiSelect])).toEqual([
      ['Which color?', 2, false],
      ['Which sizes?', 3, true],
    ])
    expect(parseQuestions('not json')).toEqual([])
    expect(parseQuestions('{"questions":[{"options":[]}]}')).toEqual([])
    expect(answerOf({ labels: ['S', 'L'], other: '' })).toBe('S, L')
    expect(answerOf({ labels: [], other: ' my own ' })).toBe('my own')
    expect(answerOf(undefined)).toBe('')
  })

  it('keeps the question apart from the message in live content', () => {
    let live = applyContent({ parts: [{ text: 'hi' }] }, { sequence: 1, type: 'question', tool: { id: 'q1', name: 'AskUserQuestion', status: 'running', input } })
    expect(live.question?.id).toBe('q1')
    live = applyContent(live, { sequence: 2, type: 'message.started', message_id: 'm2' })
    expect(live.question?.id).toBe('q1')
    live = applyContent(live, { sequence: 3, type: 'question' })
    expect(live.question).toBeUndefined()
  })
})

describe('QuestionCard', () => {
  it('answers each question with options or the operator text', async () => {
    render(<QuestionCard id="item" question={{ id: 'toolu_1', name: 'AskUserQuestion', status: 'running', input }} />)
    const answer = screen.getByRole('button', { name: 'Answer' })
    expect((answer as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('radio', { name: /Red/ }))
    expect((answer as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('checkbox', { name: 'S' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'L' }))
    fireEvent.change(screen.getByLabelText('Your own answer to: Which color?'), { target: { value: 'Green' } })
    expect(screen.getByRole('radio', { name: /Red/ }).getAttribute('aria-checked')).toBe('false')
    fireEvent.click(answer)
    expect(answerQuestion).toHaveBeenCalledWith('item', 'toolu_1', { 'Which color?': 'Green', 'Which sizes?': 'S, L' })
  })

  it('shows nothing for a call without questions', () => {
    expect(render(<QuestionCard id="item" question={{ id: 'x', name: 'AskUserQuestion', status: 'running', input: '{}' }} />).container.innerHTML).toBe('')
  })
})
