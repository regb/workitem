import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Live } from '@/lib/api'
import type { OutboxMessage } from '@/lib/history'
import { useExpectingReply } from './use-expecting-reply'
import { revealStep, useSmoothText } from './use-smooth-text'

afterEach(() => vi.useRealTimers())

describe('revealStep', () => {
  it('reveals a share of the backlog, at least one character, never half a pair', () => {
    expect(revealStep('abc', 0)).toBe(1)
    expect(revealStep('x'.repeat(300), 0)).toBe(10)
    // The step would land inside the emoji's surrogate pair.
    expect(revealStep('a🌍', 1)).toBe(3)
  })
})

describe('useSmoothText', () => {
  it('shows finished or rewritten text at once, and streams the rest', () => {
    const { result, rerender } = renderHook(({ text, streaming }) => useSmoothText(text, streaming), { initialProps: { text: 'Hello', streaming: true } })
    expect(result.current).toBe('Hello')
    rerender({ text: 'Hello there, a longer reply', streaming: true })
    expect(result.current).toBe('Hello')
    rerender({ text: 'Rewritten', streaming: true })
    expect(result.current).toBe('Rewritten')
    rerender({ text: 'Rewritten and done', streaming: false })
    expect(result.current).toBe('Rewritten and done')
  })
})

describe('useExpectingReply', () => {
  const message = (status: OutboxMessage['status']): OutboxMessage => ({ client_id: 'c', text: 'hi', images: [], status, known: [] })

  it('stays on from sending through confirmation until the turn ends', () => {
    type Props = { pending: OutboxMessage[]; live: Live; busy: boolean }
    const { result, rerender } = renderHook(({ pending, live, busy }: Props) => useExpectingReply(pending, live, busy), {
      initialProps: { pending: [] as OutboxMessage[], live: { sequence: 5, activity: 'Idle' }, busy: false },
    })
    expect(result.current.expecting).toBe(false)
    rerender({ pending: [message('sending')], live: { sequence: 5, activity: 'Idle' }, busy: false })
    expect(result.current.expecting).toBe(true)
    // History confirmed the message; the runtime has not reported busy yet.
    rerender({ pending: [], live: { sequence: 5, activity: 'Idle' }, busy: false })
    expect(result.current.expecting).toBe(true)
    rerender({ pending: [], live: { sequence: 6, activity: 'Assistant responding' }, busy: true })
    expect(result.current.expecting).toBe(true)
    rerender({ pending: [], live: { sequence: 9, activity: 'Idle' }, busy: true })
    expect(result.current.expecting).toBe(false)
  })

  it("has not started until the agent shows work, and the last turn's Idle does not end it", () => {
    type Props = { pending: OutboxMessage[]; live: Live; busy: boolean }
    const { result, rerender } = renderHook(({ pending, live, busy }: Props) => useExpectingReply(pending, live, busy), {
      initialProps: { pending: [] as OutboxMessage[], live: { sequence: 5, activity: 'Idle' }, busy: false },
    })
    rerender({ pending: [message('sent')], live: { sequence: 5, activity: 'Idle' }, busy: false })
    expect(result.current).toEqual({ expecting: true, started: false })
    // Busy is the agent's own report; the shown Idle is the last turn's.
    rerender({ pending: [message('sent')], live: { sequence: 5, activity: 'Idle' }, busy: true })
    expect(result.current).toEqual({ expecting: true, started: true })
  })

  it('ends on a problem that appears after sending, not on one already there', () => {
    type Props = { pending: OutboxMessage[]; problem: boolean }
    const { result, rerender } = renderHook(({ pending, problem }: Props) => useExpectingReply(pending, null, false, problem), { initialProps: { pending: [] as OutboxMessage[], problem: true } })
    // The item already needed attention: sending to fix it is still awaited.
    rerender({ pending: [message('sending')], problem: true })
    expect(result.current.expecting).toBe(true)
    const fresh = renderHook(({ pending, problem }: Props) => useExpectingReply(pending, null, false, problem), { initialProps: { pending: [] as OutboxMessage[], problem: false } })
    fresh.rerender({ pending: [message('sending')], problem: false })
    fresh.rerender({ pending: [message('sent')], problem: true })
    expect(fresh.result.current.expecting).toBe(false)
  })

  it('ends when the send fails or after the limit', () => {
    vi.useFakeTimers()
    const { result, rerender } = renderHook(({ pending }: { pending: OutboxMessage[] }) => useExpectingReply(pending, null, false), { initialProps: { pending: [] as OutboxMessage[] } })
    rerender({ pending: [message('sending')] })
    rerender({ pending: [message('failed')] })
    expect(result.current.expecting).toBe(false)
    // Nothing is ever observed: the expectation still ends.
    const quiet = renderHook(({ pending }: { pending: OutboxMessage[] }) => useExpectingReply(pending, null, false), { initialProps: { pending: [] as OutboxMessage[] } })
    quiet.rerender({ pending: [message('sending')] })
    quiet.rerender({ pending: [] })
    expect(quiet.result.current.expecting).toBe(true)
    act(() => vi.advanceTimersByTime(121_000))
    expect(quiet.result.current.expecting).toBe(false)
  })
})

describe('useExpectingReply with a runtime the send starts', () => {
  const message = (status: OutboxMessage['status']): OutboxMessage => ({ client_id: 'c', text: 'hi', images: [], status, known: [] })
  type Props = { pending: OutboxMessage[]; live: Live; busy: boolean; answers: number }

  it('ignores the new runtime reporting idle before it picks up the message', () => {
    const { result, rerender } = renderHook(({ pending, live, busy, answers }: Props) => useExpectingReply(pending, live, busy, false, answers), {
      initialProps: { pending: [] as OutboxMessage[], live: { sequence: 0, activity: '' } as Live, busy: false, answers: 3 },
    })
    rerender({ pending: [message('sending')], live: { sequence: 0, activity: '' }, busy: false, answers: 3 })
    rerender({ pending: [message('sent')], live: { sequence: 0, activity: '' }, busy: false, answers: 3 })
    // The started runtime's first snapshot: ready, nothing done yet.
    rerender({ pending: [message('sent')], live: { sequence: 2, activity: 'Idle' }, busy: false, answers: 3 })
    expect(result.current.expecting).toBe(true)
    rerender({ pending: [], live: { sequence: 3, activity: 'Assistant responding', text: 'Hel' } as Live, busy: false, answers: 3 })
    expect(result.current.expecting).toBe(true)
    rerender({ pending: [], live: { sequence: 7, activity: 'Idle' }, busy: false, answers: 3 })
    expect(result.current.expecting).toBe(false)
  })

  it('ends when history gains the reply even if no work was seen', () => {
    const { result, rerender } = renderHook(({ pending, live, busy, answers }: Props) => useExpectingReply(pending, live, busy, false, answers), {
      initialProps: { pending: [] as OutboxMessage[], live: { sequence: 4, activity: 'Idle' } as Live, busy: false, answers: 1 },
    })
    rerender({ pending: [message('sending')], live: { sequence: 4, activity: 'Idle' }, busy: false, answers: 1 })
    rerender({ pending: [], live: { sequence: 4, activity: 'Idle' }, busy: false, answers: 1 })
    expect(result.current.expecting).toBe(true)
    rerender({ pending: [], live: { sequence: 4, activity: 'Idle' }, busy: false, answers: 2 })
    expect(result.current.expecting).toBe(false)
  })
})
