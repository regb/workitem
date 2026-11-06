import type { ModelCatalog as CatalogMessage } from '@/gen/wi/core/v1/agents_pb'
import { AgentMode } from '@/gen/wi/core/v1/common_pb'
import type { AgentSettings, ModelCatalog } from './views'
import { agents, profiles } from './rpc'

// An item's agent settings, and a profile's before any item uses it, in the
// shape the model picker reads.

function catalogView(catalog: CatalogMessage | undefined): ModelCatalog | undefined {
  if (!catalog) return undefined
  return {
    default: catalog.defaultModel || undefined,
    default_reasoning: catalog.defaultReasoning || undefined,
    reasoning_levels: catalog.reasoningLevels,
    models: catalog.models.map(m => ({ value: m.value, resolved: m.resolved || undefined, label: m.label || undefined, description: m.description || undefined, reasoning: m.reasoning, levels: m.levels, image_input: m.imageInput || undefined })),
  }
}

const modes: Partial<Record<AgentMode, string>> = { [AgentMode.TUI]: 'tui', [AgentMode.HEADLESS]: 'headless' }

// readAgentSettings reads an item's settings with what its running agent can
// change in place.
export async function readAgentSettings(itemId: string, signal?: AbortSignal): Promise<AgentSettings> {
  const [settings, agent] = await Promise.all([agents.getAgentSettings({ itemId }, { signal }), agents.getAgent({ itemId }, { signal }).catch(() => undefined)])
  const running = agent?.agent
  return {
    agent: settings.agentProfile,
    driver: settings.driver || undefined,
    values: settings.values,
    overrides: settings.overrides,
    running: Object.keys(settings.running).length ? settings.running : undefined,
    catalog: catalogView(settings.catalog),
    catalog_error: settings.catalogError || undefined,
    runtime_online: !!running?.online,
    runtime_mode: running ? modes[running.mode] : undefined,
    live: running?.capabilities?.liveSettings ?? [],
  }
}

// readProfileModels reads a profile's settings and models, for choosing a
// new item's model: no overrides, nothing running.
export async function readProfileModels(name: string, signal?: AbortSignal): Promise<AgentSettings> {
  const value = await profiles.getProfileModels({ name }, { signal })
  return { agent: name, driver: value.driver || undefined, values: value.values, overrides: {}, catalog: catalogView(value.catalog), catalog_error: value.catalogError || undefined, runtime_online: false, live: [] }
}
