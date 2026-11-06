import { describe, expect, it } from 'vitest'
import type { AgentSettings } from '@/lib/api'
import { levelsFor, modelLabel, shownSettings } from './model-picker'

const settings: AgentSettings = {
  agent: 'claude',
  values: { model: 'opus', reasoning: 'high' },
  overrides: {},
  runtime_online: true,
  runtime_mode: 'headless',
  live: ['model', 'reasoning'],
  catalog: {
    reasoning_levels: ['low', 'medium', 'high', 'xhigh', 'max'],
    models: [
      { value: 'opus', resolved: 'claude-opus-5-5', label: 'Opus 5.5', reasoning: true, levels: ['low', 'high'] },
      { value: 'haiku-old', label: 'Haiku 4.5', reasoning: false },
      { value: 'sonnet', label: 'Sonnet 5.5', reasoning: true },
    ],
    default: 'claude-opus-5-5',
  },
}

describe('model picker helpers', () => {
  it('labels models from the catalog and falls back to the value', () => {
    expect(modelLabel(settings, 'opus')).toBe('Opus 5.5')
    expect(modelLabel(settings, 'custom/model')).toBe('custom/model')
    expect(modelLabel(settings, 'claude-opus-5-5')).toBe('Opus 5.5')
    expect(modelLabel(settings, '')).toBe('Model')
  })

  it('shows what the agent runs with before the item settings and the default', () => {
    expect(shownSettings(settings, { model: 'sonnet' })).toEqual({ model: 'sonnet', level: 'high' })
    expect(shownSettings(settings, undefined)).toEqual({ model: 'opus', level: 'high' })
    expect(shownSettings({ ...settings, values: {} }, undefined)).toEqual({ model: 'claude-opus-5-5', level: '' })
    expect(shownSettings({ ...settings, values: {}, catalog: { ...settings.catalog!, default_reasoning: 'medium' } }, { model: 'opus' })).toEqual({ model: 'opus', level: 'medium' })
  })

  it('offers each model its own reasoning levels', () => {
    const models = settings.catalog!.models
    expect(levelsFor(settings, models[0])).toEqual(['low', 'high'])
    expect(levelsFor(settings, models[1])).toEqual([])
    expect(levelsFor(settings, models[2])).toEqual(['low', 'medium', 'high', 'xhigh', 'max'])
  })
})
