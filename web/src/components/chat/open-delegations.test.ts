import { create } from '@bufbuild/protobuf'
import { expect, it } from 'vitest'
import { AgentStatus, ItemState } from '@/gen/wi/core/v1/common_pb'
import { ItemSummarySchema } from '@/gen/wi/core/v1/items_pb'
import { GetItemViewResponseSchema } from '@/gen/wi/core/v1/views_pb'
import { chatView } from '@/lib/chat-view'
import { delegationState } from './open-delegations'

it('reads a delegation at a glance', () => {
  const row = (state: ItemState, observation: object = {}) => create(ItemSummarySchema, { state, observation })
  expect(delegationState(row(ItemState.BACKLOG)).label).toBe('not started')
  expect(delegationState(row(ItemState.ACTIVE, { agentStatus: AgentStatus.BUSY })).label).toBe('working')
  expect(delegationState(row(ItemState.ACTIVE, { agentStatus: AgentStatus.PROBLEM })).label).toBe('problem')
  expect(delegationState(row(ItemState.ACTIVE, { agentStatus: AgentStatus.BUSY, asking: true })).label).toBe('asking')
  expect(delegationState(row(ItemState.ACTIVE, { agentStatus: AgentStatus.IDLE, unread: true })).label).toBe('replied')
  expect(delegationState(row(ItemState.ACTIVE, { agentStatus: AgentStatus.IDLE })).label).toBe('idle')
  expect(delegationState(row(ItemState.ACTIVE, { agentStatus: AgentStatus.IDLE, unread: true, backgroundTasks: 1 })).label).toBe('working')
  expect(delegationState(row(ItemState.ACTIVE, { agentStatus: AgentStatus.IDLE, workingDelegations: 2 })).label).toBe('working')
})

it('reads background work from the item view', () => {
  const idle = chatView(create(GetItemViewResponseSchema, { item: { id: 'i' }, observation: {} }))
  expect(idle.background).toBeUndefined()
  const waiting = chatView(create(GetItemViewResponseSchema, { item: { id: 'i' }, observation: { background: [{ id: 'b1', label: 'Wait for CI' }] } }))
  expect(waiting.background).toEqual([{ id: 'b1', label: 'Wait for CI', started_at: undefined }])
})
