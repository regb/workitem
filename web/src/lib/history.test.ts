import { describe, expect, it } from 'vitest'
import type { History } from './api'
import { addOlderPage, confirmedBy, follow, keepShown, maxShown, trimShown, knownMatches, mergeHistoryMessages, reconcileOutbox, toolMessages, withLiveTools, type OutboxMessage } from './history'

const sent = (client_id: string, text: string, known: string[] = [], status: OutboxMessage['status'] = 'sent'): OutboxMessage => ({ client_id, text, images: [], status, known })

describe('mergeHistoryMessages', () => {
  it('folds a tool result into its call and keeps one disclosure', () => {
    const merged = mergeHistoryMessages([
      { id: 'call', role: 'assistant', text: '', tools: [{ id: 't', name: 'bash', status: 'running', input: 'ls' }] },
      { id: 'result', role: 'assistant', text: '', tools: [{ id: 't', name: 'bash', status: 'completed', output: 'README' }] },
    ])
    expect(merged).toHaveLength(1)
    expect(merged[0].tools![0]).toMatchObject({ status: 'completed', output: 'README', input: 'ls' })
    expect(merged[0].native_ids).toContain('result')
  })
})

describe('shown history', () => {
  const latest: History = {
    leaf_id: 'leaf',
    has_more: true,
    before_id: 'result',
    messages: [
      { id: 'result', role: 'assistant', text: '', tools: [{ id: 'tool', name: 'bash', status: 'completed', output: 'README' }] },
      { id: 'leaf', role: 'assistant', text: 'Done' },
    ],
  }
  const page: History = { leaf_id: 'leaf', has_more: false, messages: [{ id: 'user', role: 'user', text: 'Inspect' }, { id: 'call', role: 'assistant', text: '', tools: [{ id: 'tool', name: 'bash', status: 'running', input: 'ls' }] }] }

  it('joins tools across page boundaries when older pages load', () => {
    const shown = addOlderPage(follow(undefined, latest), page)
    expect(shown.messages.map(message => message.id)).toEqual(['user', 'call', 'leaf'])
    expect(shown.messages[1].tools![0]).toMatchObject({ status: 'completed', output: 'README' })
    expect(shown.cursor).toBe('')
  })

  it('keeps messages that slide out of the latest page, so the list only grows at the end', () => {
    const shown = follow(undefined, { leaf_id: 'b', messages: [{ id: 'a', role: 'user', text: 'go' }, { id: 'b', role: 'assistant', text: 'one' }] })
    const slid = follow(shown, { leaf_id: 'c', messages: [{ id: 'b', role: 'assistant', text: 'one' }, { id: 'c', role: 'assistant', text: 'two' }] })
    expect(slid.messages.map(message => message.id)).toEqual(['a', 'b', 'c'])
  })

  it('starts over when the branch changes', () => {
    const other: History = { leaf_id: 'elsewhere', messages: [{ id: 'elsewhere', role: 'user', text: 'new branch' }] }
    expect(follow(addOlderPage(follow(undefined, latest), page), other).messages.map(message => message.id)).toEqual(['elsewhere'])
  })

  it('finds the message showing each tool, and overlays live state on a running one only', () => {
    expect([...toolMessages(latest.messages)]).toEqual([['tool', 'result']])
    const running = { id: 'tool', name: 'bash', status: 'running' }
    expect(withLiveTools([running], [{ ...running, status: 'completed', output: 'done' }])[0]).toMatchObject({ status: 'completed', output: 'done' })
    const saved = { ...running, status: 'completed', output: 'final' }
    expect(withLiveTools([saved], [{ ...running, output: 'stale' }])[0]).toBe(saved)
  })
})

describe('reconcileOutbox', () => {
  it('is not confirmed by an equal message already shown when it was sent', () => {
    const messages = [{ id: 'old', role: 'user', text: 'continue' }]
    const pending = [sent('new', 'continue', knownMatches(messages, 'continue'))]
    expect(reconcileOutbox(pending, messages)).toBe(pending)
    expect(reconcileOutbox(pending, [...messages, { id: 'new', role: 'user', text: 'continue\n' }])).toEqual([])
  })

  it('confirms each pending message with a different saved message', () => {
    const pending = [sent('first', 'continue'), sent('second', 'continue')]
    expect(reconcileOutbox(pending, [{ id: 'one', role: 'user', text: 'continue' }]).map(message => message.client_id)).toEqual(['second'])
  })

  it('confirms a linked message only once history shows its entry, never by text', () => {
    const pending = [sent('c1', 'continue')]
    const links = [{ request_id: 'web:c1', native_ids: ['user-9'] }]
    // An equal message elsewhere does not confirm a linked one.
    expect(reconcileOutbox(pending, [{ id: 'other', role: 'user', text: 'continue' }], links)).toBe(pending)
    expect(reconcileOutbox(pending, [{ id: 'user-9', role: 'user', text: 'continue (edited by a hook)' }], links)).toEqual([])
  })

  it('keeps text matching away from entries a link already claims', () => {
    const pending = [sent('linked', 'go'), sent('unlinked', 'go')]
    const messages = [{ id: 'user-1', role: 'user', text: 'go' }]
    const remaining = reconcileOutbox(pending, messages, [{ request_id: 'web:linked', native_ids: ['user-1'] }])
    expect(remaining.map(message => message.client_id)).toEqual(['unlinked'])
  })

  it('keeps failed messages for editing', () => {
    const pending = [sent('failed', 'hi', [], 'failed')]
    expect(reconcileOutbox(pending, [{ id: 'x', role: 'user', text: 'hi' }])).toBe(pending)
  })
})

describe('trimShown', () => {
  it('keeps the newest messages and pages back from the oldest kept', () => {
    const messages = Array.from({ length: maxShown + 1 }, (_, index) => ({ id: `m${index}`, role: 'assistant', text: String(index) }))
    const trimmed = trimShown({ leaf: `m${maxShown}`, entryIDs: messages.map(message => message.id), messages, cursor: '' })
    expect(trimmed.messages).toHaveLength(keepShown)
    expect(trimmed.cursor).toBe(trimmed.messages[0].id)
    expect(trimmed.entryIDs[0]).toBe(trimmed.messages[0].id)
  })
})

describe('confirmedBy', () => {
  const sent = (client_id: string, text: string, known: string[] = []): OutboxMessage => ({ client_id, text, images: [], status: 'sent', known })
  it('names the history message that confirmed each sent message', () => {
    const messages = [
      { id: 'old', role: 'user', text: 'again' },
      { id: 'h1', role: 'user', text: 'again' },
      { id: 'h2', role: 'user', text: 'linked', native_ids: ['n2'] },
    ]
    const confirmed = confirmedBy([sent('a', 'again', ['old']), sent('b', 'other text')], messages)
    expect([...confirmed]).toEqual([['h1', 'a']])
    const linked = confirmedBy([{ ...sent('c', 'linked'), client_id: 'c' }], messages, [{ request_id: 'web:c', native_ids: ['n2'] }])
    expect([...linked]).toEqual([['h2', 'c']])
  })
})
