import { BacklogAdd } from '@/components/create/backlog-add'
import { LaunchProblems } from '@/components/create/quick-create'
import { CreateDock } from '@/components/dashboard/create-dock'
import { ItemRow } from '@/components/dashboard/item-row'
import { ProjectFilters } from '@/components/dashboard/project-filters'
import { sectionTone } from '@/components/tones'
import { Skeleton } from '@/components/ui/skeleton'
import { useWorkList } from '@/lib/queries'
import { projectSelected, ui } from '@/lib/ui-state'
import { cn } from '@/lib/utils'
import { placeStarting } from '@/lib/work-list'

// Waiting and backlog items do not need the operator now, so they recede;
// hover or focus brings a row back to full contrast.
const quietSections = new Set(['WAITING', 'BACKLOG'])

export function WorkListPage() {
  const { data: list, isPending, error } = useWorkList()
  const selected = ui.use(state => state.selectedProjects)
  const closing = ui.use(state => state.closing)
  const launches = ui.use(state => state.pendingLaunches)
  if (isPending) return <ListSkeleton />
  if (error || !list) return <p className="p-6 text-sm text-destructive">{error?.message}</p>
  const names = new Map(list.projects.map(project => [project.id, project.name]))
  // The backlog always shows, empty or not: it is where work is added for later.
  const all = placeStarting(list.display_sections.some(section => section.name === 'BACKLOG') ? list.display_sections : [...list.display_sections, { name: 'BACKLOG', items: [] }], launches)
  const sections = [...all]
    .sort((a, b) => Number(b.name === 'NEEDS ATTENTION') - Number(a.name === 'NEEDS ATTENTION'))
    .map(section => ({ ...section, items: section.items.filter(row => projectSelected(selected, row.project_id) && !closing.has(row.id)) }))
    .filter(section => section.items.length || section.name === 'BACKLOG')
  return (
    <div className="quiet-scroll min-h-0 flex-1 overflow-y-auto">
      {/* Room below the last section for the floating create box. */}
      <div className="mx-auto flex max-w-6xl flex-col gap-5 p-4 pb-24 sm:p-6 sm:pb-52">
        <ProjectFilters list={list} />
        <LaunchProblems />
        {!!list.warnings?.length && <p className="text-xs whitespace-pre-wrap text-amber-300">{list.warnings.join('\n')}</p>}
        {sections.length === 1 && (
          <p className="text-sm text-muted-foreground">
            {selected === null ? 'No active work items.' : 'No work items in the filtered projects. Remove a filter to see more.'}
          </p>
        )}
        {sections.map(section => (
          <section key={section.name} className="flex flex-col gap-1">
            <h2 className={cn('flex items-center gap-2 pb-1 text-xs font-semibold tracking-widest', sectionTone[section.name] ?? 'text-muted-foreground')}>
              {section.name}
              <span className="font-normal tracking-normal text-muted-foreground">{section.items.length}</span>
            </h2>
            {section.name === 'BACKLOG' && <BacklogAdd />}
            {!!section.items.length && (
              <div className={cn('flex flex-col divide-y rounded-lg border', quietSections.has(section.name) && 'border-border/50 divide-border/50')}>
                {section.items.map(row => (
                  <ItemRow key={row.id} row={row} list={list} project={names.get(row.project_id) ?? 'Unknown project'} quiet={quietSections.has(section.name)} />
                ))}
              </div>
            )}
          </section>
        ))}
      </div>
      <CreateDock />
    </div>
  )
}

function ListSkeleton() {
  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-3 p-6">
      {Array.from({ length: 5 }, (_, index) => (
        <Skeleton key={index} className="h-12 w-full" />
      ))}
    </div>
  )
}
