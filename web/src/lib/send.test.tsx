import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { Code, ConnectError } from '@connectrpc/connect'

const outcomes: Array<() => Promise<unknown>> = []
vi.mock('./actions', () => ({ sendMessage: () => outcomes.shift()!() }))

describe('sent messages', () => {
  it('follow their mutation: sending, sent, failed or unknown, until forgotten', async () => {
    const { queryClient } = await import('./queries')
    const { forgetMessages, usePendingMessages, useSendMessage } = await import('./send')
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    const { result } = renderHook(() => ({ send: useSendMessage('item', async () => true), pending: usePendingMessages('item') }), { wrapper })
    const message = (client_id: string) => ({ client_id, text: client_id, images: [], known: [], delivery: 'prompt' as const, runtime_id: '' })
    let deliver!: () => void
    outcomes.push(() => new Promise(resolve => (deliver = () => resolve({}))))
    // The daemon refused it: not sent.
    outcomes.push(() => Promise.reject(new ConnectError('refused', Code.FailedPrecondition)))
    // The connection dropped: unknown.
    outcomes.push(() => Promise.reject(new ConnectError('dropped', Code.Unavailable)))
    act(() => result.current.send.mutate(message('a')))
    await waitFor(() => expect(result.current.pending.map(m => m.status)).toEqual(['sending']))
    act(() => deliver())
    await waitFor(() => expect(result.current.pending.map(m => m.status)).toEqual(['sent']))
    act(() => result.current.send.mutate(message('b')))
    await waitFor(() => expect(result.current.pending.map(m => m.status)).toEqual(['sent', 'failed']))
    act(() => result.current.send.mutate(message('c')))
    await waitFor(() => expect(result.current.pending.map(m => m.status)).toEqual(['sent', 'failed', 'unknown']))
    act(() => forgetMessages('item', new Set(['a', 'c'])))
    await waitFor(() => expect(result.current.pending.map(m => m.client_id)).toEqual(['b']))
  })

  it('keeps an acknowledged follow-up queued until native history confirms it', async () => {
    const { queryClient } = await import('./queries')
    const { forgetMessages, usePendingMessages, useSendMessage } = await import('./send')
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    const { result } = renderHook(() => ({ send: useSendMessage('follow-up-item', async () => true), pending: usePendingMessages('follow-up-item') }), { wrapper })
    outcomes.push(() => Promise.resolve({}))
    act(() => result.current.send.mutate({ client_id: 'queued', text: 'Later', images: [], known: [], delivery: 'follow_up', runtime_id: 'runtime' }))
    await waitFor(() => expect(result.current.pending).toMatchObject([{ delivery: 'follow_up', status: 'sent', text: 'Later' }]))
    // Conversation renders this combination under Queued follow-ups, without
    // the failed/unknown message controls that offer to edit and resend.
    vi.useFakeTimers()
    try {
      act(() => vi.advanceTimersByTime(31_000))
      expect(result.current.pending).toMatchObject([{ client_id: 'queued', delivery: 'follow_up', status: 'sent' }])
    } finally {
      vi.useRealTimers()
    }
    act(() => forgetMessages('follow-up-item', new Set(['queued'])))
    await waitFor(() => expect(result.current.pending).toEqual([]))
  })
})
