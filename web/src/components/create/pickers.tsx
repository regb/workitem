import { useRef, useState } from 'react'
import { ChevronDownIcon } from 'lucide-react'
import { DriverLogo, driverNames } from '@/components/agent-logo'
import { findModel, levelsFor, modelLabel, ModelList, shownSettings, ThinkingLevels } from '@/components/chat/model-picker'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { AgentProfile } from '@/gen/wi/core/v1/profiles_pb'
import type { AgentSettings, Project } from '@/lib/views'
import { useProfileModels } from '@/lib/queries'
import { cn } from '@/lib/utils'

const chip = 'h-7 border-0 bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:bg-foreground/10 hover:text-foreground dark:bg-transparent dark:hover:bg-foreground/10'

// ProjectPicker is the compact repository chip of the create boxes.
export function ProjectPicker({ projects, value, onChange, disabled }: { projects: Project[]; value: string; onChange: (id: string) => void; disabled?: boolean }) {
  return (
    <Select value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger aria-label="Repository" size="sm" className={cn(chip, 'max-w-48')}>
        <SelectValue placeholder="Repository" />
      </SelectTrigger>
      <SelectContent>
        {projects.map(project => (
          <SelectItem key={project.id} value={project.id}>
            {project.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

// AgentChoice is what new work runs with: a profile and, when chosen, a model
// and thinking level that override the profile's.
export type AgentChoice = { agent: string; model: string; level: string }

// AgentPicker is the create box's agent chip: the driver's logo, the profile,
// and its model and thinking level, all chosen in one popover. Choices apply
// as they are picked; nothing runs until the task starts.
export function AgentPicker({ agents, value, onChange, disabled }: { agents: AgentProfile[]; value: AgentChoice; onChange: (value: AgentChoice) => void; disabled?: boolean }) {
  const [open, setOpen] = useState(false)
  // Closing by a click elsewhere leaves focus where the click went, rather
  // than on this button, which would refocus the create box.
  const outside = useRef(false)
  const { data: settings } = useProfileModels(value.agent)
  const profile = agents.find(agent => agent.name === value.agent)
  const shown = settings ? shownSettings(settings, undefined) : { model: '', level: '' }
  const model = value.model || shown.model
  const level = value.level || shown.level
  const selected = settings ? findModel(settings, model) : undefined
  const levels = settings ? levelsFor(settings, selected) : []
  const label = [profile?.label || value.agent, settings && model ? modelLabel(settings, model) : '', levels.length ? level : ''].filter(Boolean).join(' · ')
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button type="button" variant="ghost" size="sm" disabled={disabled || !agents.length} aria-label={`Agent: ${label || 'none'}`} className={cn(chip, 'h-7 max-w-64 gap-1.5')}>
          <DriverLogo driver={profile?.driver} />
          <span className="truncate">{label || 'Agent'}</span>
          <ChevronDownIcon className="opacity-60" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        className="w-80 p-0"
        onInteractOutside={() => {
          outside.current = true
        }}
        onCloseAutoFocus={event => {
          if (outside.current) event.preventDefault()
          outside.current = false
        }}
      >
        <div className="p-1" role="radiogroup" aria-label="Agent profile">
          {agents.map(agent => (
            <button
              key={agent.name}
              type="button"
              role="radio"
              aria-checked={agent.name === value.agent}
              onClick={() => onChange({ agent: agent.name, model: '', level: '' })}
              className={cn('flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-muted', agent.name === value.agent && 'bg-muted')}
            >
              <DriverLogo driver={agent.driver} />
              <span className="min-w-0 flex-1 truncate">{agent.label || agent.name}</span>
              <span className="text-xs text-muted-foreground">{driverNames[agent.driver ?? ''] ?? agent.driver}</span>
            </button>
          ))}
        </div>
        <div className="border-t">
          <div className="px-3 pt-2 text-xs text-muted-foreground">Model</div>
          {settings ? (
            <ModelList catalog={settings.catalog} error={settings.catalog_error} selected={selected} onSelect={next => onChange({ ...value, model: next, level: value.level })} />
          ) : (
            <p className="p-3 text-sm text-muted-foreground">Loading models…</p>
          )}
        </div>
        <ThinkingLevels levels={levels} value={level} onSelect={next => onChange({ ...value, level: next })} />
      </PopoverContent>
    </Popover>
  )
}

// choiceOverrides is what a new item sends: only a model or level that
// differs from the profile's own.
export function choiceOverrides(value: AgentChoice, settings: AgentSettings | undefined): { model?: string; reasoning?: string } {
  const own = settings ? shownSettings(settings, undefined) : { model: '', level: '' }
  return { ...(value.model && value.model !== own.model ? { model: value.model } : {}), ...(value.level && value.level !== own.level ? { reasoning: value.level } : {}) }
}
