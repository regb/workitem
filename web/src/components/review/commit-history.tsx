import { useMemo } from 'react'
import { graphEdgePath, graphRowHeight, graphX, layoutCommitGraph } from '@/lib/commit-graph'
import type { Commits, ReviewBranch } from '@/lib/review'
import { BranchKind } from '@/gen/wi/core/v1/reviews_pb'

const colors: Record<BranchKind, string> = { [BranchKind.UNSPECIFIED]: '', [BranchKind.ITEM]: 'text-sky-300', [BranchKind.INTEGRATION]: 'text-violet-300', [BranchKind.REMOTE]: 'text-amber-300' }
const names: Record<BranchKind, string> = { [BranchKind.UNSPECIFIED]: 'Branch', [BranchKind.ITEM]: 'Item', [BranchKind.INTEGRATION]: 'Integration', [BranchKind.REMOTE]: 'Remote' }

function BranchLabel({ branch }: { branch: ReviewBranch }) {
  return (
    <span
      className={`inline-block max-w-full truncate rounded border border-current/25 px-1.5 py-0.5 font-mono text-[11px] ${colors[branch.kind]}`}
      title={`${names[branch.kind]}: ${branch.name}${branch.sha ? ` · ${branch.sha}` : ' · not available locally'}`}
    >
      {branch.name}
    </span>
  )
}

export function CommitHistory({ history, onSelect }: { history: Commits; onSelect: (sha: string) => void }) {
  const commits = history.commits ?? []
  const branches = history.branches ?? []
  const { rows, columns } = useMemo(() => layoutCommitGraph(commits), [commits])
  const width = graphX(columns) + 4
  return (
    <div className="min-h-0 flex-1 overflow-auto" aria-label="Commit graph">
      <div className="space-y-1 border-b px-3 py-2 text-xs text-muted-foreground">
        <p>{branches.length ? 'Item, integration, and cached upstream history. Other branch tips are hidden.' : 'Branch context is unavailable from this server.'}</p>
        <p>Remote refs reflect the last repository sync. Opening history never fetches.</p>
        <div className="flex flex-wrap gap-x-4 gap-y-1 py-1">
          {branches.map(branch => (
            <span key={branch.kind} className="inline-flex min-w-0 max-w-full items-center gap-1.5">
              <span>{names[branch.kind]}</span>
              <BranchLabel branch={branch} />
            </span>
          ))}
        </div>
        {history.warnings?.map(warning => (
          <p key={warning} className="text-amber-300">
            {warning}
          </p>
        ))}
      </div>
      {!rows.length && <p className="p-3 text-sm text-muted-foreground">No commits available.</p>}
      {rows.map(({ commit, column, edges }) => {
        const labels = branches.filter(branch => branch.sha === commit.sha)
        return (
          <button
            type="button"
            key={commit.sha}
            data-commit-sha={commit.sha}
            aria-label={`View commit ${commit.sha.slice(0, 8)}: ${commit.subject}`}
            onClick={() => onSelect(commit.sha)}
            className="flex w-full items-stretch border-b text-left hover:bg-muted"
            style={{ height: graphRowHeight }}
          >
            <svg width={width} height={graphRowHeight} className="shrink-0 text-muted-foreground" aria-hidden>
              {edges.map((edge, i) => (
                <path key={i} d={graphEdgePath(edge)} stroke="currentColor" strokeWidth="1.5" fill="none" strokeDasharray={edge.boundary ? '3 3' : undefined} />
              ))}
              <circle cx={graphX(column)} cy={38} r={4} className={labels.length ? colors[labels[0].kind] : ''} fill="currentColor" />
            </svg>
            <span className="flex min-w-0 flex-1 flex-col justify-center gap-0.5 py-1 pe-3">
              {!!labels.length && (
                <span className="flex min-w-0 gap-1 overflow-hidden">
                  {labels.map(branch => (
                    <BranchLabel key={branch.kind} branch={branch} />
                  ))}
                </span>
              )}
              <span className="truncate text-sm" title={commit.subject}>
                {commit.subject}
              </span>
              <span className="truncate text-xs text-muted-foreground">
                <span className="font-mono">{commit.sha.slice(0, 8)}</span> · {commit.author} · {new Date(commit.authored_at).toLocaleDateString()}
              </span>
            </span>
          </button>
        )
      })}
      {history.truncated && <p className="p-3 text-xs text-muted-foreground">Showing the latest 200 commits. Dashed lines and branch tips outside this window continue into older history.</p>}
    </div>
  )
}
