import { createContext, useContext, useRef, useState } from 'react'
import { PlayIcon, SquareIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { JobState, type Job } from '@/gen/wi/core/v1/jobs_pb'
import { jobs, rpcMessage, workspaces } from '@/lib/rpc'
import { cn } from '@/lib/utils'

// RunContext names the item whose agent's replies are being shown: their
// shell snippets may be run in its checkout. Elsewhere, such as in your own
// messages, it is empty and nothing runs.
export const RunContext = createContext('')

// shellLanguages are the fenced-code languages a Run button is offered for.
export const shellLanguages = new Set(['bash', 'sh', 'shell', 'zsh', 'console'])

// commandOf is the command a snippet runs: a console transcript's prompts
// are dropped, and only their lines are kept.
export function commandOf(language: string, value: string) {
  const text = value.replace(/\n$/, '')
  if (language !== 'console') return text
  const prompted = text.split('\n').filter(line => /^\s*\$ /.test(line))
  return prompted.length ? prompted.map(line => line.replace(/^\s*\$ /, '')).join('\n') : text
}

type Run = { job?: Job; error?: string }

// useRunCommand runs a command in the item's checkout and follows its job:
// its output, its end and its exit status.
export function useRunCommand(itemId: string) {
  const [run, setRun] = useState<Run | null>(null)
  const following = useRef(0)
  const start = async (command: string) => {
    const attempt = ++following.current
    setRun({})
    try {
      const out = await workspaces.runCommand({ itemId, command })
      let job = out.job
      if (attempt !== following.current) return
      setRun({ job })
      if (!job) return
      for await (const update of jobs.watchJob({ jobId: job.id })) {
        if (attempt !== following.current || !update.job) continue
        job = update.job
        setRun({ job })
      }
    } catch (error) {
      if (attempt === following.current) setRun(current => ({ ...current, error: rpcMessage(error) }))
    }
  }
  const stop = () => {
    const id = run?.job?.id
    if (id) void jobs.cancelJob({ jobId: id }).catch(() => {})
  }
  return { run, start, stop, clear: () => setRun(null) }
}

// runStatus says how a run stands, for its panel.
export function runStatus(run: Run): { text: string; running: boolean; failed: boolean } {
  const job = run.job
  if (run.error && !job) return { text: run.error, running: false, failed: true }
  if (!job || job.state === JobState.QUEUED || job.state === JobState.RUNNING) return { text: 'Running…', running: true, failed: false }
  if (job.state === JobState.CANCELLED) return { text: 'Stopped', running: false, failed: true }
  if (job.state === JobState.FAILED) return { text: job.error || 'It did not run', running: false, failed: true }
  const code = job.result.case === 'command' ? job.result.value.exitCode : 0
  if (code === 0) return { text: 'Finished', running: false, failed: false }
  return { text: code < 0 ? 'Stopped' : `Exited with status ${code}`, running: false, failed: true }
}

// RunButton offers to run a snippet in the item's checkout, as you, after
// showing exactly what will run; its output shows under the snippet.
export function RunButton({ command, onRun }: { command: string; onRun: (command: string) => void }) {
  const [asking, setAsking] = useState(false)
  return (
    <>
      <Button size="icon-xs" variant="ghost" aria-label="Run" title="Run in this item's checkout, as you" onClick={() => setAsking(true)}>
        <PlayIcon />
      </Button>
      <Dialog open={asking} onOpenChange={setAsking}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Run this command?</DialogTitle>
            <DialogDescription>
              It runs in this item&apos;s checkout as you, as in a terminal you opened there: wi commands act with your access, not the agent&apos;s. The agent wrote it, so check it first.
            </DialogDescription>
          </DialogHeader>
          <pre aria-label="Command" className="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs whitespace-pre-wrap [overflow-wrap:anywhere]">
            {command}
          </pre>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAsking(false)}>
              Cancel
            </Button>
            <Button
              onClick={() => {
                setAsking(false)
                onRun(command)
              }}
            >
              <PlayIcon /> Run
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

// RunOutput shows a run's output as it comes, how it ended, and a Stop
// button while it runs.
export function RunOutput({ run, onStop, onClose }: { run: Run; onStop: () => void; onClose: () => void }) {
  const status = runStatus(run)
  const output = run.job?.logTail ?? ''
  return (
    <div aria-label="Command output" className="-mt-1 mb-2 rounded-b-lg border border-t-0 text-xs">
      <div className="flex items-center gap-2 px-3 py-1.5 text-muted-foreground">
        <span role="status" className={cn(status.failed && 'text-destructive', status.running && 'animate-pulse')}>
          {status.text}
        </span>
        <span className="flex-1" />
        {status.running ? (
          <Button size="xs" variant="ghost" onClick={onStop}>
            <SquareIcon /> Stop
          </Button>
        ) : (
          <Button size="xs" variant="ghost" onClick={onClose}>
            Close
          </Button>
        )}
      </div>
      {output && <pre className="max-h-80 overflow-auto border-t px-3 py-2 font-mono whitespace-pre-wrap [overflow-wrap:anywhere]">{output}</pre>}
    </div>
  )
}

// useRunItem is the item whose snippets may run here, if any.
export const useRunItem = () => useContext(RunContext)
