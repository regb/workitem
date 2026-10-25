import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from './api'

const outcomes: Array<() => Promise<unknown>> = []
vi.mock('./actions', () => ({ sendMessage: () => outcomes.shift()!() }))

describe('sent messages', () => {
  it('follow their mutation: sending, sent, failed or unknown, until forgotten', async () => {
    const { queryClient } = await import('./queries')
    const { forgetMessages, usePendingMessages, useSendMessage } = await import('./send')
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    const { result } = renderHook(() => ({ send: useSendMessage('item', async () => true, async () => null), pending: usePendingMessages('item') }), { wrapper })
    const message = (client_id: string) => ({ client_id, text: client_id, images: [], known: [], delivery: 'prompt' as const, runtime_id: '' })
    let deliver!: () => void
    outcomes.push(() => new Promise(resolve => (deliver = () => resolve({}))))
    outcomes.push(() => Promise.reject(new ApiError('refused', { error: 'refused' })))
    outcomes.push(() => Promise.reject(new ApiError('dropped')))
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
})
