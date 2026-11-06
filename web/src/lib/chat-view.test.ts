import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { describe, expect, it } from 'vitest'
import { AgentCapabilitiesSchema, AgentSchema, ControlChannel, ControlStatusSchema, GetHistoryResponseSchema, MessageRole } from '@/gen/wi/core/v1/agents_pb'
import { AgentMode, AgentStatus, ItemState } from '@/gen/wi/core/v1/common_pb'
import { ItemSchema } from '@/gen/wi/core/v1/items_pb'
import { GetItemViewResponseSchema } from '@/gen/wi/core/v1/views_pb'
import { SetupState } from '@/gen/wi/core/v1/workspaces_pb'
import { chatView } from './chat-view'

const item = create(ItemSchema, {
  id: 'item',
  slug: 'fix-it',
  title: 'Fix it',
  state: ItemState.ACTIVE,
  repository: { id: 'repo', name: 'Repo' },
  createdFromCommit: 'abc',
  checkout: { branch: 'wi/fix-it', kind: 'managed-slot', claim: { id: 'claim', generation: 3, path: '/w' } },
  agentProfile: 'pi',
  createdAt: timestampFromDate(new Date('2026-01-02T03:04:05Z')),
})

describe('chatView', () => {
  it('maps the item view as the conversation page reads it', () => {
    const chat = chatView(
      create(GetItemViewResponseSchema, {
        item,
        description: 'Why',
        observation: { unread: true },
        agent: create(AgentSchema, {
          online: true,
          mode: AgentMode.TUI,
          status: AgentStatus.BUSY,
          reason: 'Starting',
          generation: 2,
          runningSettings: { model: 'm' },
          capabilities: create(AgentCapabilitiesSchema, { control: ControlChannel.NONE, prompt: true, steering: true }),
        }),
        control: create(ControlStatusSchema, { implicit: true, plan: { needsConfirmation: true, reasons: ['a terminal is attached'], restartsIn: AgentMode.HEADLESS, planToken: 't' } }),
        history: create(GetHistoryResponseSchema, { leafId: 'leaf', nextBefore: 'older', messages: [{ id: 'm1', role: MessageRole.ASSISTANT, parts: [{ kind: { case: 'text', value: 'Hello' } }] }] }),
        setup: { state: SetupState.FAILED, error: 'exit 1', logTail: 'log' },
        warnings: ['careful'],
      }),
    )
    expect(chat).toMatchObject({
      description: 'Why',
      project: { id: 'repo', name: 'Repo' },
      item: { id: 'item', state: 'active', agent: 'pi', created_at: '2026-01-02T03:04:05.000Z', repository: { created_from_commit: 'abc' }, checkout: { specification: { branch: 'wi/fix-it', kind: 'managed-slot' }, claim: { id: 'claim', generation: 3, path: '/w' } } },
      runtime: { online: true, runtime: { id: '2', mode: 'tui', settings: { model: 'm' } }, capabilities: { prompt: true, steering: true } },
      status: 'busy',
      status_reason: 'Starting',
      unread: true,
      history: { leaf_id: 'leaf', has_more: true, before_id: 'older', messages: [{ id: 'm1', role: 'assistant', parts: [{ text: 'Hello' }] }] },
      setup: { current: true, run: { status: 'failed', error: 'exit 1' }, log_tail: 'log' },
      warnings: ['careful'],
    })
    // A TUI agent that reports no channel but takes prompts is typed into.
    expect(chat.control).toEqual({
      you: false,
      status: { holder: 'terminal', lease: undefined, channels: { control: 'emulated' }, busy: true },
      plan: { plan_token: 't', needs_confirmation: true, reasons: ['a terminal is attached'], replace: 'headless', start: undefined, abort: undefined, unavailable: undefined },
    })
  })

  it('leaves out a setup that succeeded and an absent agent', () => {
    const chat = chatView(create(GetItemViewResponseSchema, { item, setup: { state: SetupState.SUCCEEDED } }))
    expect(chat.setup).toBeUndefined()
    expect(chat.runtime).toEqual({ online: false, capabilities: {} })
    expect(chat.status).toBe('')
    expect(chat.history.messages).toEqual([])
  })
})
