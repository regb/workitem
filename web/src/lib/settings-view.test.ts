import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentMode } from '@/gen/wi/core/v1/common_pb'
import { resetRPC, rpcReplies } from './rpc-mock'

vi.mock('./rpc', async original => (await import('./rpc-mock')).mockRPC(await original()))

const catalog = { models: [{ value: 'opus', resolved: '', label: 'Opus', description: '', reasoning: true, levels: ['low', 'high'], imageInput: true }], reasoningLevels: ['low', 'high'], defaultModel: 'opus', defaultReasoning: '' }

beforeEach(resetRPC)

describe('agent settings', () => {
  it("reads an item's settings with what its running agent changes live", async () => {
    rpcReplies.set('agents.getAgentSettings', () => ({ agentProfile: 'claude', driver: 'claude', values: { model: 'opus' }, overrides: { model: 'opus' }, running: {}, catalog, catalogError: '' }))
    rpcReplies.set('agents.getAgent', () => ({ agent: { online: true, mode: AgentMode.HEADLESS, capabilities: { liveSettings: ['model'] } } }))
    const { readAgentSettings } = await import('./settings-view')
    expect(await readAgentSettings('item')).toEqual({
      agent: 'claude',
      driver: 'claude',
      values: { model: 'opus' },
      overrides: { model: 'opus' },
      running: undefined,
      catalog: { default: 'opus', default_reasoning: undefined, reasoning_levels: ['low', 'high'], models: [{ value: 'opus', resolved: undefined, label: 'Opus', description: undefined, reasoning: true, levels: ['low', 'high'], image_input: true }] },
      catalog_error: undefined,
      runtime_online: true,
      runtime_mode: 'headless',
      live: ['model'],
    })
  })

  it("reads a profile's models with nothing running", async () => {
    rpcReplies.set('profiles.getProfileModels', () => ({ driver: 'pi', values: { model: 'test/model' }, catalogError: 'offline' }))
    const { readProfileModels } = await import('./settings-view')
    expect(await readProfileModels('pi')).toMatchObject({ agent: 'pi', values: { model: 'test/model' }, overrides: {}, catalog_error: 'offline', runtime_online: false, live: [] })
  })
})
