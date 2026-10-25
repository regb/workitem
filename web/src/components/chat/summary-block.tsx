import { useState } from 'react'
import { ChevronRightIcon, FoldVerticalIcon, GitBranchIcon } from 'lucide-react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import type { HistoryMessage } from '@/lib/api'
import { Markdown } from './markdown'

// SummaryBlock marks where the agent's context was compacted, or a summary
// of a branch the conversation left: one folded line across the
// conversation, opening on the summary the agent kept.
export function SummaryBlock({ message }: { message: HistoryMessage }) {
  const [open, setOpen] = useState(false)
  const branch = message.summary?.kind === 'branch'
  const tokens = message.summary?.tokens_before
  const title = branch ? 'Summary of an earlier branch' : 'Context compacted'
  const Icon = branch ? GitBranchIcon : FoldVerticalIcon
  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      data-role="summary"
      data-summary={message.summary?.kind ?? 'compaction'}
      className="w-full rounded-lg border border-dashed border-violet-400/40 bg-violet-500/5"
    >
      <CollapsibleTrigger
        disabled={!message.text.trim()}
        className="group flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm text-violet-200 transition-colors outline-none hover:bg-violet-500/10 focus-visible:ring-3 focus-visible:ring-ring/50 disabled:hover:bg-transparent"
      >
        <Icon className="size-4 shrink-0 text-violet-300" aria-hidden />
        <span className="shrink-0 font-medium whitespace-nowrap">{title}</span>
        {!!tokens && <span className="min-w-0 truncate text-xs text-muted-foreground">{shortTokens(tokens)} tokens before</span>}
        {!!message.text.trim() && (
          <span className="ms-auto flex shrink-0 items-center gap-1 text-xs text-muted-foreground group-hover:text-foreground">
            <span className="max-sm:sr-only">{open ? 'Hide summary' : 'Show summary'}</span>
            <ChevronRightIcon className="size-4 transition-transform group-aria-expanded:rotate-90" aria-hidden />
          </span>
        )}
      </CollapsibleTrigger>
      <CollapsibleContent className="border-t border-dashed border-violet-400/30 px-3 py-3 text-sm">
        <Markdown text={message.text} />
      </CollapsibleContent>
    </Collapsible>
  )
}

const shortTokens = (n: number) => (n >= 1000 ? `${Math.round(n / 1000)}k` : String(n))
