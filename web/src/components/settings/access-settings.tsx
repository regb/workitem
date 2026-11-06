import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ItemPicker } from '@/components/item-picker'
import { AccessLevel } from '@/gen/wi/core/v1/items_pb'
import { items, rpcMessage } from '@/lib/rpc'
import { useItemRows } from '@/hooks/use-item-rows'

const levels = [
  { level: AccessLevel.VIEW, name: 'view', means: 'read it: conversation, notes, review, checkout files' },
  { level: AccessLevel.MESSAGE, name: 'message', means: 'also message its agent; add notes, blockers, review comments' },
  { level: AccessLevel.CONTROL, name: 'control', means: 'also drive its agent and checkout, edit and shelve it' },
  { level: AccessLevel.INTEGRATE, name: 'integrate', means: 'also approve and merge it' },
  { level: AccessLevel.MANAGE, name: 'manage', means: 'also close and reopen it' },
]
const levelName = (level: AccessLevel) => levels.find(l => l.level === level)?.name ?? 'none'

// AccessSettings says what items' agents may do to other items. An agent
// may always manage the items it delegated, and integrate its own; entries grant
// the rest, up to a level.
export function AccessSettings() {
  const client = useQueryClient()
  const { data, error } = useQuery({ queryKey: ['access'], queryFn: ({ signal }) => items.listAccess({}, { signal }), staleTime: 0 })
  const rows = useItemRows('')
  const title = (id: string) => rows.find(entry => entry.row.id === id)?.row.title ?? id
  const [subject, setSubject] = useState('')
  const [on, setOn] = useState('')
  const [level, setLevel] = useState(AccessLevel.VIEW)
  const [picking, setPicking] = useState<'subject' | 'on' | null>(null)
  const [problem, setProblem] = useState('')
  const refresh = () => client.invalidateQueries({ queryKey: ['access'] })
  const act = async (action: () => Promise<unknown>) => {
    setProblem('')
    try {
      await action()
      await refresh()
    } catch (err) {
      setProblem(rpcMessage(err))
    }
  }
  const subjectText = (value: string) => (value === 'item:*' ? "Every item's agent" : `${title(value.slice(5))}'s agent`)
  const onText = (value: string) => (value === '*' ? 'every item' : title(value))
  return (
    <section className="flex flex-col gap-4 text-sm">
      <p className="text-muted-foreground">
        An item&apos;s agent may always manage the items it delegated, and do all but close or reopen its own item, which its owner does. What it may do to other items is what these entries grant. Deleting items, changing who owns an item, and these settings are yours only.
      </p>
      {error && <p className="text-destructive">{rpcMessage(error)}</p>}
      <ul className="divide-y rounded-lg border">
        {data?.entries.length === 0 && <li className="px-4 py-3 text-muted-foreground">No entries: agents may not act on other items.</li>}
        {data?.entries.map(entry => (
          <li key={entry.id} className="flex items-center gap-3 px-4 py-3">
            <span className="min-w-0 flex-1">
              {subjectText(entry.subject)} may <span className="font-medium">{levelName(entry.level)}</span> {onText(entry.itemId)}
            </span>
            <Button size="xs" variant="ghost" aria-label={`Revoke ${entry.id}`} onClick={() => act(() => items.revokeAccess({ entryId: entry.id }))}>
              Revoke
            </Button>
          </li>
        ))}
      </ul>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" onClick={() => setPicking('subject')}>
          {subject ? subjectText(`item:${subject}`) : "Every item's agent"}
        </Button>
        <span>may</span>
        <Select value={String(level)} onValueChange={value => setLevel(Number(value) as AccessLevel)}>
          <SelectTrigger className="w-36" aria-label="Level">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {levels.map(l => (
              <SelectItem key={l.level} value={String(l.level)}>
                {l.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="sm" variant="outline" onClick={() => setPicking('on')}>
          {on ? title(on) : 'every item'}
        </Button>
        <Button size="sm" onClick={() => act(() => items.grantAccess({ subject: subject ? `item:${subject}` : 'item:*', onItemId: on || '*', level }))}>
          Grant
        </Button>
        {(subject || on) && (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setSubject('')
              setOn('')
            }}
          >
            Every item
          </Button>
        )}
      </div>
      <p className="text-xs text-muted-foreground">{levels.find(l => l.level === level)?.means}</p>
      {problem && <p className="text-destructive">{problem}</p>}
      <ItemPicker
        open={picking !== null}
        onOpenChange={open => !open && setPicking(null)}
        title={picking === 'subject' ? 'Whose agent' : 'On which item'}
        exclude=""
        onPick={id => (picking === 'subject' ? setSubject(id) : setOn(id))}
      />
    </section>
  )
}
