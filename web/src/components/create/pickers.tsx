import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { AgentProfile, Project } from '@/lib/api'
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

// AgentPicker is the compact agent profile chip of the create box.
export function AgentPicker({ agents, value, onChange, disabled }: { agents: AgentProfile[]; value: string; onChange: (name: string) => void; disabled?: boolean }) {
  return (
    <Select value={value} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger aria-label="Agent profile" size="sm" className={cn(chip, 'max-w-40')}>
        <SelectValue placeholder="Agent" />
      </SelectTrigger>
      <SelectContent>
        {agents.map(agent => (
          <SelectItem key={agent.name} value={agent.name}>
            {agent.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
