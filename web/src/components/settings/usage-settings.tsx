import { useState } from 'react'
import { RefreshCwIcon, TriangleAlertIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { api, type QuotaResult, type QuotaWindow, type Usage } from '@/lib/api'
import { keys, queryClient, useUsage } from '@/lib/queries'
import { cn } from '@/lib/utils'

const providers: Record<string, string> = { claude: 'Claude', codex: 'OpenAI (Codex)', 'opencode-go': 'OpenCode Go' }

// UsageSettings shows how much of each subscription's quota is left, as
// wi usage does: asked of each native client, never from its credentials.
export function UsageSettings() {
  const { data, error, isFetching } = useUsage()
  const [refreshing, setRefreshing] = useState(false)
  const [failure, setFailure] = useState('')
  const refresh = async () => {
    setRefreshing(true)
    setFailure('')
    try {
      queryClient.setQueryData(keys.usage, await api<Usage>('/api/usage?refresh=1'))
    } catch (problem) {
      setFailure((problem as Error).message)
    } finally {
      setRefreshing(false)
    }
  }
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>
  if (!data) return <p className="text-sm text-muted-foreground">Asking each subscription&apos;s client…</p>
  if (!data.enabled)
    return (
      <p className="text-sm text-muted-foreground">
        The usage plugin is not enabled. Add <code>[plugins.usage] enabled = true</code> to Wi&apos;s configuration file, then restart <code>wi web</code>.
      </p>
    )
  const counts = new Map<string, number>()
  for (const result of data.results) counts.set(result.provider, (counts.get(result.provider) ?? 0) + 1)
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-start gap-3">
        <p className="flex-1 text-sm text-muted-foreground">
          How much of each subscription&apos;s quota is left, asked of its own client (Claude Code, Codex, OpenCode), which keeps its login. Results are reused for five minutes.
        </p>
        <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={refreshing || isFetching}>
          <RefreshCwIcon className={cn(refreshing && 'animate-spin')} /> Refresh
        </Button>
      </div>
      {failure && (
        <p role="alert" className="text-sm text-destructive">
          {failure}
        </p>
      )}
      {data.results.length ? (
        data.results.map(result => <Subscription key={`${result.provider}/${result.account ?? ''}`} result={result} several={(counts.get(result.provider) ?? 0) > 1} />)
      ) : (
        <p className="text-sm text-muted-foreground">No subscription clients found. Install Claude Code, Codex or OpenCode and sign in with it.</p>
      )}
    </div>
  )
}

function Subscription({ result, several }: { result: QuotaResult; several: boolean }) {
  const name = providers[result.provider] ?? result.provider
  return (
    <section className="flex flex-col gap-3 rounded-lg border p-4" data-subscription={result.provider}>
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="font-medium">
          {name}
          {several && result.account && <span className="ms-2 font-mono text-xs text-muted-foreground">{result.account}</span>}
        </h3>
        {result.observed_at && <span className="text-xs text-muted-foreground">checked {ago(result.observed_at)}</span>}
      </header>
      {result.error && (
        <p className={cn('flex items-start gap-2 text-sm', result.stale ? 'text-amber-300' : 'text-muted-foreground')}>
          <TriangleAlertIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
          {result.error.message}
        </p>
      )}
      {result.windows.length > 0 && (
        <ul className="flex flex-col gap-2">
          {result.windows.map(window => (
            <QuotaLine key={window.id + (window.scope?.model ?? '') + (window.scope?.surface ?? '')} window={window} />
          ))}
        </ul>
      )}
      {result.credits && (result.credits.unlimited || result.credits.remaining !== null) && (
        <p className="text-sm text-muted-foreground">Credits: {result.credits.unlimited ? 'unlimited' : `${result.credits.remaining} ${result.credits.unit}`}</p>
      )}
      {result.included_usage_allowed === false && <p className="text-sm text-amber-300">The provider currently blocks included usage on this subscription.</p>}
    </section>
  )
}

// QuotaLine is one quota window: how much is left, as a bar, and when it
// resets. Unknown is shown as unknown, never as empty or full.
function QuotaLine({ window }: { window: QuotaWindow }) {
  const left = window.remaining_percent
  const tone = left === null ? '' : left <= 10 ? 'bg-red-400' : left <= 30 ? 'bg-amber-400' : 'bg-emerald-400'
  return (
    <li className="grid grid-cols-[5rem_1fr_7.5rem] items-center gap-3 text-sm" data-window={window.id}>
      <span className="text-muted-foreground">{windowLabel(window)}</span>
      <span className="h-2 overflow-hidden rounded-full bg-muted" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={left ?? undefined} aria-label={`${windowLabel(window)} left`}>
        {left !== null && <span className={cn('block h-full rounded-full', tone)} style={{ width: `${Math.min(100, Math.max(0, left))}%` }} />}
      </span>
      <span className="text-right tabular-nums">
        {left === null ? 'unknown' : `${Math.round(left * 100) / 100}% left`}
        <span className="block text-xs text-muted-foreground">{window.resets_at ? `resets ${until(window.resets_at)}` : 'reset unknown'}</span>
      </span>
    </li>
  )
}

// windowLabel names a window as wi usage does.
export function windowLabel(window: QuotaWindow) {
  let label = window.id
  if (window.id === 'five_hour') label = '5-hour'
  else if (window.id === 'seven_day' || window.id === 'weekly') label = 'Weekly'
  else if (window.id === 'monthly') label = 'Monthly'
  else if (window.id.startsWith('seven_day_')) label = `Weekly ${window.id.slice('seven_day_'.length).replaceAll('_', ' ')}`
  else if (window.duration_seconds === 18000 || window.duration_seconds === 604800) {
    label = window.duration_seconds === 18000 ? '5-hour' : 'Weekly'
    if (window.id !== 'codex/primary' && window.id !== 'codex/secondary') label += ` · ${window.id}`
  }
  if (window.scope?.model) label += ` · ${window.scope.model}`
  if (window.scope?.surface) label += ` · ${window.scope.surface}`
  return label
}

const span = (ms: number) => {
  const minutes = Math.max(0, Math.round(ms / 60_000))
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const [big, small] = hours < 24 ? [`${hours}h`, minutes % 60 ? ` ${minutes % 60}m` : ''] : [`${Math.floor(hours / 24)}d`, hours % 24 ? ` ${hours % 24}h` : '']
  return big + small
}
const until = (at: string) => {
  const ms = Date.parse(at) - Date.now()
  return ms <= 0 ? 'now' : `in ${span(ms)}`
}
const ago = (at: string) => {
  const ms = Date.now() - Date.parse(at)
  return ms < 60_000 ? 'just now' : `${span(ms)} ago`
}
