import { useState } from 'react'
import { CheckIcon, ChevronDownIcon, SparklesIcon } from 'lucide-react'
import { useChoose, useConfirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { changeSettings } from '@/lib/actions'
import type { AgentSettings, ModelOption } from '@/lib/api'
import { useAgentSettings } from '@/lib/queries'
import { cn } from '@/lib/utils'

// modelLabel names a model value by its catalog label when known.
export function modelLabel(settings: AgentSettings | undefined, value: string | undefined) {
  if (!value) return 'Default model'
  return settings?.catalog?.models.find(model => model.value === value)?.label || value
}

// levelsFor lists the reasoning levels a model accepts.
export function levelsFor(settings: AgentSettings, model: ModelOption | undefined) {
  if (!settings.catalog?.reasoning_setting || (model && !model.reasoning)) return []
  return model?.levels?.length ? model.levels : (settings.catalog.reasoning_levels ?? [])
}

// ModelPicker shows the conversation's model and reasoning level, and
// changes them for this item.
export function ModelPicker({ id, disabled }: { id: string; disabled?: boolean }) {
  const confirm = useConfirm()
  const choose = useChoose()
  const { data: settings } = useAgentSettings(id)
  const [open, setOpen] = useState(false)
  const [model, setModel] = useState('')
  const [level, setLevel] = useState('')
  const catalog = settings?.catalog
  const modelKey = catalog?.model_setting ?? 'model'
  const levelKey = catalog?.reasoning_setting ?? ''
  const current = { model: settings?.values[modelKey] ?? '', level: levelKey ? (settings?.values[levelKey] ?? '') : '' }
  if (!settings) return null
  const selected = catalog?.models.find(option => option.value === (model || current.model))
  const levels = levelsFor(settings, selected)
  const chosenLevel = level || current.level
  const label = [modelLabel(settings, current.model), current.level].filter(Boolean).join(' · ')

  const apply = async () => {
    const changes: Record<string, string> = {}
    if (model && model !== current.model) changes[modelKey] = model
    if (levelKey && level && level !== current.level && levels.includes(level)) changes[levelKey] = level
    setOpen(false)
    if (Object.keys(changes).length) await changeSettings(confirm, choose, id, changes, settings)
  }
  const reset = async () => {
    setOpen(false)
    await changeSettings(confirm, choose, id, Object.fromEntries(Object.keys(settings.overrides).map(name => [name, ''])), settings)
  }
  return (
    <Popover
      open={open}
      onOpenChange={next => {
        setOpen(next)
        if (next) {
          setModel('')
          setLevel('')
        }
      }}
    >
      <PopoverTrigger asChild>
        <Button type="button" variant="ghost" size="sm" disabled={disabled} className="max-w-[55%] gap-1 text-muted-foreground hover:bg-foreground/10 hover:text-foreground sm:max-w-none" aria-label={`Model: ${label}`}>
          <SparklesIcon />
          <span className="truncate">{label}</span>
          <ChevronDownIcon className="opacity-60" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <div className="border-b px-3 py-2 text-xs text-muted-foreground">
          Model for this conversation · {settings.agent}
          {settings.runtime_online && !settings.live.includes(modelKey) && <span className="block text-amber-300/90">Switching restarts the {settings.runtime_mode} runtime.</span>}
        </div>
        {catalog ? (
          <div className="quiet-scroll max-h-64 overflow-y-auto p-1" role="listbox" aria-label="Models">
            {catalog.models.map(option => {
              const active = option.value === (model || current.model)
              return (
                <button
                  key={option.value}
                  // The current model is scrolled into view when the list opens.
                  ref={active && !model ? element => element?.scrollIntoView?.({ block: 'nearest' }) : undefined}
                  type="button"
                  role="option"
                  aria-selected={active}
                  onClick={() => setModel(option.value)}
                  className={cn('flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-muted', active && 'bg-muted')}
                >
                  <CheckIcon className={cn('mt-0.5 size-4 shrink-0', active ? 'opacity-100' : 'opacity-0')} />
                  <span className="min-w-0">
                    <span className="block truncate">{option.label || option.value}</span>
                    {option.description && <span className="block truncate text-xs text-muted-foreground">{option.description}</span>}
                  </span>
                </button>
              )
            })}
          </div>
        ) : (
          <p className="p-3 text-sm text-muted-foreground">Models are unavailable{settings.catalog_error ? `: ${settings.catalog_error}` : '.'}</p>
        )}
        {levels.length > 0 && (
          <div className="border-t p-2">
            <div className="mb-1.5 px-1 text-xs text-muted-foreground capitalize">{levelKey}</div>
            <div className="flex flex-wrap gap-1" role="radiogroup" aria-label={levelKey}>
              {levels.map(value => (
                <Button key={value} type="button" size="xs" variant={value === chosenLevel ? 'secondary' : 'ghost'} role="radio" aria-checked={value === chosenLevel} onClick={() => setLevel(value)}>
                  {value}
                </Button>
              ))}
            </div>
          </div>
        )}
        <div className="flex items-center justify-between gap-2 border-t p-2">
          {Object.keys(settings.overrides).length ? (
            <Button type="button" variant="ghost" size="xs" className="text-muted-foreground" onClick={reset}>
              Reset to profile
            </Button>
          ) : (
            <span />
          )}
          <Button type="button" size="sm" onClick={apply} disabled={(!model || model === current.model) && (!level || level === current.level)}>
            Apply
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}
