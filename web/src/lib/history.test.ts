import { describe, expect, it } from 'vitest'
import type { History, HistoryMessage } from './views'
import { addOlderPage, confirmedBy, foldTurns, follow, keepShown, maxShown, trimShown, knownMatches, mergeHistoryMessages, reconcileOutbox,  type OutboxMessage } from './history'

const sent = (client_id: string, text: string, known: string[] = [], status: OutboxMessage['status'] = 'sent'): OutboxMessage => ({ client_id, text, images: [], status, known })

describe('mergeHistoryMessages', () => {
  it('folds a tool result into its call and keeps one disclosure', () => {
    const merged = mergeHistoryMessages([
      { id: 'call', role: 'assistant', parts: [{ tool: { id: 't', name: 'bash', status: 'running', input: 'ls' } }] },
      { id: 'result', role: 'assistant', parts: [{ tool: { id: 't', name: 'bash', status: 'completed', output: 'README' } }] },
    ])
    expect(merged).toHaveLength(1)
    expect(merged[0].parts[0]).toMatchObject({ tool: { status: 'completed', output: 'README', input: 'ls' } })
    expect(merged[0].native_ids).toContain('result')
  })

  it('joins a compaction boundary and the summary after it', () => {
    const compaction = { kind: 'compaction' as const }
    const page = [
      { id: 'boundary', role: 'summary', parts: [], summary: { ...compaction, tokens_before: 288117 } },
      { id: 'kept', role: 'summary', parts: [{ text: 'what was done' }], summary: compaction },
    ]
    const merged = mergeHistoryMessages(page)
    expect(merged).toEqual([expect.objectContaining({ id: 'boundary', parts: [{ text: 'what was done' }], summary: { kind: 'compaction', tokens_before: 288117 }, native_ids: ['kept'] })])
    // Merging again, as each new page does, keeps one block.
    expect(mergeHistoryMessages([...merged, ...page])).toHaveLength(1)
  })
})

describe('shown history', () => {
  const latest: History = {
    leaf_id: 'leaf',
    has_more: true,
    before_id: 'result',
    messages: [
      { id: 'result', role: 'assistant', parts: [{ tool: { id: 'tool', name: 'bash', status: 'completed', output: 'README' } }] },
      { id: 'leaf', role: 'assistant', parts: [{ text: 'Done' }] },
    ],
  }
  const page: History = { leaf_id: 'leaf', has_more: false, messages: [{ id: 'user', role: 'user', parts: [{ text: 'Inspect' }] }, { id: 'call', role: 'assistant', parts: [{ tool: { id: 'tool', name: 'bash', status: 'running', input: 'ls' } }] }] }

  it('joins tools across page boundaries when older pages load', () => {
    const shown = addOlderPage(follow(undefined, latest), page)
    expect(shown.messages.map(message => message.id)).toEqual(['user', 'call', 'leaf'])
    expect(shown.messages[1].parts[0]).toMatchObject({ tool: { status: 'completed', output: 'README' } })
    expect(shown.cursor).toBe('')
  })

  it('keeps messages that slide out of the latest page, so the list only grows at the end', () => {
    const shown = follow(undefined, { leaf_id: 'b', messages: [{ id: 'a', role: 'user', parts: [{ text: 'go' }] }, { id: 'b', role: 'assistant', parts: [{ text: 'one' }] }] })
    const slid = follow(shown, { leaf_id: 'c', messages: [{ id: 'b', role: 'assistant', parts: [{ text: 'one' }] }, { id: 'c', role: 'assistant', parts: [{ text: 'two' }] }] })
    expect(slid.messages.map(message => message.id)).toEqual(['a', 'b', 'c'])
  })

  it("follows the server's order in its window, so a message the agent took up later moves there", () => {
    const m = (id: string, role: 'user' | 'assistant', text: string) => ({ id, role, parts: [{ text }] })
    // A message sent while the agent worked was first seen right after the
    // prompt; the agent took it up after its first reply.
    const shown = follow(undefined, { leaf_id: 'steer', messages: [m('go', 'user', 'go'), m('steer', 'user', 'also this')] })
    const later = follow(shown, { leaf_id: 'two', messages: [m('go', 'user', 'go'), m('one', 'assistant', 'one'), m('steer', 'user', 'also this'), m('two', 'assistant', 'two')] })
    expect(later.messages.map(message => message.id)).toEqual(['go', 'one', 'steer', 'two'])
    // What slid out of the window stays above it.
    const slid = follow(later, { leaf_id: 'three', messages: [m('steer', 'user', 'also this'), m('two', 'assistant', 'two'), m('three', 'assistant', 'three')] })
    expect(slid.messages.map(message => message.id)).toEqual(['go', 'one', 'steer', 'two', 'three'])
  })

  it('starts over when the branch changes', () => {
    const other: History = { leaf_id: 'elsewhere', messages: [{ id: 'elsewhere', role: 'user', parts: [{ text: 'new branch' }] }] }
    expect(follow(addOlderPage(follow(undefined, latest), page), other).messages.map(message => message.id)).toEqual(['elsewhere'])
  })
})

describe('reconcileOutbox', () => {
  it('is not confirmed by an equal message already shown when it was sent', () => {
    const messages = [{ id: 'old', role: 'user', parts: [{ text: 'continue' }] }]
    const pending = [sent('new', 'continue', knownMatches(messages, 'continue'))]
    expect(reconcileOutbox(pending, messages)).toBe(pending)
    expect(reconcileOutbox(pending, [...messages, { id: 'new', role: 'user', parts: [{ text: 'continue\n' }] }])).toEqual([])
  })

  it('confirms each pending message with a different saved message', () => {
    const pending = [sent('first', 'continue'), sent('second', 'continue')]
    expect(reconcileOutbox(pending, [{ id: 'one', role: 'user', parts: [{ text: 'continue' }] }]).map(message => message.client_id)).toEqual(['second'])
  })

  it('confirms a linked message only once history shows its entry, never by text', () => {
    const pending = [sent('c1', 'continue')]
    const links = [{ request_id: 'c1', native_ids: ['user-9'] }]
    // An equal message elsewhere does not confirm a linked one.
    expect(reconcileOutbox(pending, [{ id: 'other', role: 'user', parts: [{ text: 'continue' }] }], links)).toBe(pending)
    expect(reconcileOutbox(pending, [{ id: 'user-9', role: 'user', parts: [{ text: 'continue (edited by a hook)' }] }], links)).toEqual([])
  })

  it('keeps text matching away from entries a link already claims', () => {
    const pending = [sent('linked', 'go'), sent('unlinked', 'go')]
    const messages = [{ id: 'user-1', role: 'user', parts: [{ text: 'go' }] }]
    const remaining = reconcileOutbox(pending, messages, [{ request_id: 'linked', native_ids: ['user-1'] }])
    expect(remaining.map(message => message.client_id)).toEqual(['unlinked'])
  })

  it('keeps failed messages for editing', () => {
    const pending = [sent('failed', 'hi', [], 'failed')]
    expect(reconcileOutbox(pending, [{ id: 'x', role: 'user', parts: [{ text: 'hi' }] }])).toBe(pending)
  })
})

describe('trimShown', () => {
  it('keeps the newest messages and pages back from the oldest kept', () => {
    const messages = Array.from({ length: maxShown + 1 }, (_, index) => ({ id: `m${index}`, role: 'assistant', parts: [{ text: String(index) }] }))
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
      { id: 'old', role: 'user', parts: [{ text: 'again' }] },
      { id: 'h1', role: 'user', parts: [{ text: 'again' }] },
      { id: 'h2', role: 'user', parts: [{ text: 'linked' }], native_ids: ['n2'] },
    ]
    const confirmed = confirmedBy([sent('a', 'again', ['old']), sent('b', 'other text')], messages)
    expect([...confirmed]).toEqual([['h1', 'a']])
    const linked = confirmedBy([{ ...sent('c', 'linked'), client_id: 'c' }], messages, [{ request_id: 'c', native_ids: ['n2'] }])
    expect([...linked]).toEqual([['h2', 'c']])
  })

  it('confirms a message by the client ID history carries, whatever its text', () => {
    const saved = [{ id: 'h4', role: 'user', parts: [{ text: 'rewritten by the agent [Image: note]' }], client_message_id: 'e' }]
    expect([...confirmedBy([sent('e', 'what I typed')], saved)]).toEqual([['h4', 'e']])
    // An ID on another message never confirms by text instead.
    expect(reconcileOutbox([sent('f', 'what I typed')], saved)).toHaveLength(1)
  })

  it('confirms a message whose image the agent saved scaled down', () => {
    const screenshot = { ...sent('d', 'look at this'), images: [{ data: 'original', mime_type: 'image/png' }] }
    const saved = [{ id: 'h3', role: 'user', parts: [{ text: 'look at this' }, { image: { data: 'scaled', mime_type: 'image/png' } }] }]
    expect([...confirmedBy([screenshot], saved)]).toEqual([['h3', 'd']])
    expect(reconcileOutbox([screenshot], [{ ...saved[0], parts: [{ text: 'look at this' }] }])).toHaveLength(1)
  })

  it('matches a message by its text parts, wherever its images sit', () => {
    const note = { ...sent('g', 'before\n\nafter'), images: [{ data: 'x', mime_type: 'image/png' }] }
    const saved = [{ id: 'h5', role: 'user', parts: [{ text: 'before' }, { image: { data: 'x', mime_type: 'image/png' } }, { text: 'after' }] }]
    expect([...confirmedBy([note], saved)]).toEqual([['h5', 'g']])
  })
})

describe('foldTurns', () => {
  const user = (id: string): HistoryMessage => ({ id, role: 'user', parts: [{ text: id }] })
  const calls = (id: string, tools: number) => Array.from({ length: tools }, (_, n) => ({ tool: { id: `${id}-${n}`, name: 'bash', status: 'done' } }))
  const say = (id: string, tools = 0): HistoryMessage => ({ id, role: 'assistant', parts: [{ text: id }, ...calls(id, tools)] })
  const work = (id: string, tools = 1): HistoryMessage => ({ id, role: 'assistant', parts: calls(id, tools) })
  const shape = (segments: ReturnType<typeof foldTurns>) => segments.map(segment => (segment.kind === 'fold' ? `fold(${segment.messages.map(m => m.id)}:${segment.tools}t${segment.texts}m)` : segment.message.id))

  it('folds each finished turn up to its answer', () => {
    const messages = [user('q1'), say('a', 1), work('b', 2), say('answer1'), user('q2'), say('only'), user('q3'), work('c'), say('answer3')]
    expect(shape(foldTurns(messages, false))).toEqual(['q1', 'fold(a,b:3t1m)', 'answer1', 'q2', 'only', 'q3', 'fold(c:1t0m)', 'answer3'])
  })

  it('keeps the last turn whole while it is open', () => {
    const messages = [user('q1'), work('a'), say('answer1'), user('q2'), work('b'), say('partial')]
    expect(shape(foldTurns(messages, true))).toEqual(['q1', 'fold(a:1t0m)', 'answer1', 'q2', 'b', 'partial'])
  })

  it('keeps a turn without an answer whole, and notices after the answer shown', () => {
    const notice: HistoryMessage = { id: 'limit', role: 'assistant', parts: [{ text: 'Usage limit' }], error: true }
    expect(shape(foldTurns([user('q'), work('a'), work('b')], false))).toEqual(['q', 'a', 'b'])
    expect(shape(foldTurns([user('q'), work('a'), say('answer'), notice], false))).toEqual(['q', 'fold(a:1t0m)', 'answer', 'limit'])
  })

  it('never folds a compaction away inside a turn', () => {
    const compacted: HistoryMessage = { id: 'compacted', role: 'summary', parts: [{ text: 'summary' }], summary: { kind: 'compaction' } }
    expect(shape(foldTurns([user('q'), work('a'), compacted, work('b'), say('answer')], false))).toEqual(['q', 'a', 'compacted', 'fold(b:1t0m)', 'answer'])
  })

  it('folds the work a reply saved as one message did before its answer', () => {
    // Antigravity, and Claude once its records fold, save a whole reply as
    // one message: reasoning, tool calls and text, then the answer.
    const reply: HistoryMessage = { id: 'r', role: 'assistant', native_ids: ['n1'], client_message_id: 'c', parts: [{ thought: { text: 'plan' } }, ...calls('r', 2), { text: 'Checking more.' }, { thought: {} }, ...calls('s', 1), { text: 'The answer.' }] }
    const segments = foldTurns([user('q'), work('a'), reply], false)
    expect(shape(segments)).toEqual(['q', 'fold(a,r:work:4t1m)', 'r'])
    const fold = segments[1] as Extract<(typeof segments)[number], { kind: 'fold' }>
    expect(fold.messages[1].parts).toHaveLength(6)
    // The work is no saved entry of its own; the answer stays the message.
    expect([fold.messages[1].native_ids, fold.messages[1].client_message_id]).toEqual([undefined, undefined])
    const answer = segments[2] as Extract<(typeof segments)[number], { kind: 'message' }>
    expect(answer.message.parts).toEqual([{ text: 'The answer.' }])
    expect([answer.message.native_ids, answer.message.client_message_id]).toEqual([['n1'], 'c'])
    // A reply that is all answer, or ends in a tool call, stays whole; so
    // does one still being written.
    expect(shape(foldTurns([user('q'), say('only')], false))).toEqual(['q', 'only'])
    expect(shape(foldTurns([user('q'), say('ends', 1)], false))).toEqual(['q', 'ends'])
    expect(shape(foldTurns([user('q'), reply], true))).toEqual(['q', 'r'])
  })

  it('answers a background work report in a turn of its own', () => {
    const report: HistoryMessage = { id: 'report', role: 'subagent', parts: [{ text: 'CI passed' }] }
    expect(shape(foldTurns([user('q'), work('a'), say('answer'), report, work('b'), say('ci')], false))).toEqual(['q', 'fold(a:1t0m)', 'answer', 'report', 'fold(b:1t0m)', 'ci'])
  })
})
