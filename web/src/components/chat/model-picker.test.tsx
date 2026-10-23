import { describe, expect, it } from 'vitest'
import type { AgentSettings } from '@/lib/api'
import { levelsFor, modelLabel } from './model-picker'

const settings: AgentSettings = {
  agent: 'claude',
  values: { model: 'opus', effort: 'high' },
  overrides: {},
  runtime_online: true,
  runtime_mode: 'headless',
  live: ['model', 'effort'],
  catalog: {
    model_setting: 'model',
    reasoning_setting: 'effort',
    reasoning_levels: ['low', 'medium', 'high', 'xhigh', 'max'],
    models: [
      { value: 'opus', label: 'Opus 5.5', reasoning: true, levels: ['low', 'high'] },
      { value: 'haiku-old', label: 'Haiku 4.5', reasoning: false },
      { value: 'sonnet', label: 'Sonnet 5.5', reasoning: true },
    ],
  },
}

describe('model picker helpers', () => {
  it('labels models from the catalog and falls back to the value', () => {
    expect(modelLabel(settings, 'opus')).toBe('Opus 5.5')
    expect(modelLabel(settings, 'custom/model')).toBe('custom/model')
    expect(modelLabel(settings, '')).toBe('Default model')
  })

  it('offers each model its own reasoning levels', () => {
    const models = settings.catalog!.models
    expect(levelsFor(settings, models[0])).toEqual(['low', 'high'])
    expect(levelsFor(settings, models[1])).toEqual([])
    expect(levelsFor(settings, models[2])).toEqual(['low', 'medium', 'high', 'xhigh', 'max'])
  })
})
