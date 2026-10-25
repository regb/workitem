import { useState, type ReactNode } from 'react'
import { ChevronRightIcon, ListCollapseIcon } from 'lucide-react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

// TurnFold folds the work a finished turn did before its answer: one line
// counting its tool calls and messages, opening on everything as it was.
export function TurnFold({ tools, texts, children }: { tools: number; texts: number; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <Collapsible open={open} onOpenChange={setOpen} data-fold className="flex flex-col">
      <CollapsibleTrigger className="group flex w-fit items-center gap-1.5 rounded-md text-sm text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50">
        <ListCollapseIcon className="size-4" aria-hidden />
        <span>{foldSummary(tools, texts)}</span>
        <ChevronRightIcon className="size-4 transition-transform group-aria-expanded:rotate-90" aria-hidden />
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-3 ml-[7px] flex flex-col gap-5 border-l pl-4">{children}</CollapsibleContent>
    </Collapsible>
  )
}

const count = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`

// foldSummary names what a fold holds, leaving out what it has none of.
export const foldSummary = (tools: number, texts: number) => [tools && count(tools, 'tool call'), texts && count(texts, 'message')].filter(Boolean).join(', ') || 'Earlier steps'
