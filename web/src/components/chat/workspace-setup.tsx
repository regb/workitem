import { LoaderIcon, RotateCcwIcon, WrenchIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { retrySetup } from '@/lib/actions'
import type { WorkspaceSetup } from '@/lib/api'

// WorkspaceSetupState shows the checkout being set up before its agent
// starts (repository files copied, the setup command run), with the end of
// its log, and after a failure, why, with a way to retry.
export function WorkspaceSetupState({ id, setup }: { id: string; setup: WorkspaceSetup }) {
  const failed = setup.run?.status === 'failed'
  const lines = (setup.log_tail ?? '').trimEnd().split('\n').slice(-12).join('\n')
  return (
    <div
      role={failed ? 'alert' : 'status'}
      data-setup={setup.run?.status}
      className={failed ? 'flex flex-col gap-2 rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm' : 'flex flex-col gap-2 rounded-lg border bg-muted/30 px-3 py-2 text-sm'}
    >
      <div className="flex items-start gap-2">
        {failed ? <WrenchIcon className="mt-0.5 size-4 shrink-0 text-destructive" aria-hidden /> : <LoaderIcon className="mt-0.5 size-4 shrink-0 animate-spin text-muted-foreground" aria-hidden />}
        <span className="min-w-0 flex-1">
          {failed ? 'Workspace setup failed, so the agent did not start.' : 'Setting up the workspace. The agent starts when it is done.'}
          {failed && setup.run?.error && <span className="block break-words text-muted-foreground">{setup.run.error}</span>}
        </span>
        {failed && (
          <Button variant="outline" size="xs" onClick={() => void retrySetup(id)}>
            <RotateCcwIcon /> Retry setup
          </Button>
        )}
      </div>
      {lines && <pre className="max-h-60 overflow-auto rounded-md bg-background/60 px-2 py-1.5 font-mono text-xs whitespace-pre-wrap text-muted-foreground">{lines}</pre>}
    </div>
  )
}
