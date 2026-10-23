import { useId, useImperativeHandle, useMemo, useRef, useState, type ComponentProps, type KeyboardEvent, type Ref } from 'react'
import { FileIcon, FolderIcon } from 'lucide-react'
import { useCompletionList } from '@/components/completion-list'
import { sectionTone } from '@/components/tones'
import { Textarea } from '@/components/ui/textarea'
import { matchesItem, useItemRows, type ItemRow } from '@/hooks/use-item-rows'
import { displaySegments, fromWire, itemMentionAt, mentionBeside, toWire, type ItemRef } from '@/lib/item-mentions'
import { submitsOnEnter } from '@/lib/keys'
import { cn } from '@/lib/utils'
import { mentionAt, useFileSearch } from './file-mention'

const maxItems = 8

type Props = Omit<ComponentProps<'textarea'>, 'value' | 'onChange' | 'onSubmit' | 'ref'> & {
  ref?: Ref<HTMLTextAreaElement>
  // value is the text as sent: item mentions are links (see item-mentions).
  value: string
  onValueChange: (value: string) => void
  onSubmit: () => void
  // files is the endpoint @-mentions search; empty turns them off.
  files: string
  // exclude is an item @@-mentions leave out: the conversation's own.
  exclude?: string
  // box is the field's padding and text size, which the highlight layer copies.
  box: string
}

// MentionField is a message field with mentions: @ completes a file path and
// @@ a work item. The field shows a picked item as @@Title, highlighted, and
// deletes it whole; value keeps the link that is sent. Enter submits, unless
// a list is open. Lists show above the nearest positioned ancestor.
export function MentionField({ ref, value, onValueChange, onSubmit, files, exclude = '', box, className, onKeyDown, ...props }: Props) {
  const input = useRef<HTMLTextAreaElement>(null)
  useImperativeHandle(ref, () => input.current!, [])
  const layer = useRef<HTMLDivElement>(null)
  const id = useId().replace(/:/g, '')
  // Items once mentioned stay known, so that undoing a deletion restores the
  // mention as well as its text.
  const known = useRef(new Map<string, ItemRef>())
  const { text, refs } = useMemo(() => fromWire(value), [value])
  for (const ref of refs) known.current.set(ref.title, ref)
  const [caret, setCaret] = useState(0)
  const [dismissed, setDismissed] = useState(-1)

  const change = (display: string) => onValueChange(toWire(display, [...known.current.values()]))
  // insert replaces the mention being typed and puts the caret after it.
  const insert = (start: number, inserted: string) => {
    const at = start + inserted.length
    change(text.slice(0, start) + inserted + text.slice(caret))
    setCaret(at)
    requestAnimationFrame(() => {
      input.current?.focus()
      input.current?.setSelectionRange(at, at)
    })
  }

  // @@ lists items; a mention already picked is done, and typing on after it
  // is not a search.
  const item = itemMentionAt(text, caret)
  const itemOpen = !!item && item.start !== dismissed && ![...known.current.keys()].some(title => item.query.startsWith(title))
  const rows = useItemRows(exclude)
  const itemOptions = useMemo(() => (itemOpen ? rows.filter(row => matchesItem(item.query, row)).slice(0, maxItems) : []), [itemOpen, rows, item?.query])
  const items = useCompletionList<ItemRow>({
    id: `${id}-items`,
    label: 'Items',
    open: itemOpen,
    options: itemOptions,
    query: item?.query ?? '',
    onDismiss: () => setDismissed(item?.start ?? -1),
    onPick: ({ row }) => {
      known.current.set(row.title, { id: row.id, title: row.title, slug: row.slug || undefined })
      insert(item!.start, `@@${row.title} `)
    },
    render: ({ row, status, project }) => (
      <>
        <span className="shrink-0 text-sm font-semibold text-sky-300">@@</span>
        <span className="min-w-0 flex-1 truncate py-0.5 text-sm">{row.title}</span>
        <span className="max-w-32 shrink-0 truncate text-xs text-muted-foreground">{project}</span>
        <span className={cn('hidden w-28 shrink-0 text-right text-[0.65rem] font-semibold tracking-wider sm:inline', sectionTone[status] ?? 'text-muted-foreground')}>{status}</span>
      </>
    ),
  })

  // @ lists files; a folder stays open for narrowing, a file ends the mention.
  const file = item ? null : mentionAt(text, caret)
  const fileOpen = !!files && !!file && file.start !== dismissed
  const search = useFileSearch(files, fileOpen ? file.query : null)
  const paths = useCompletionList<string>({
    id: `${id}-files`,
    label: 'Files',
    open: fileOpen,
    options: search.files,
    query: file?.query ?? '',
    hold: search.searching,
    notice: search.error,
    onDismiss: () => setDismissed(file?.start ?? -1),
    onPick: path => insert(file!.start, `@${path}${path.endsWith('/') ? '' : ' '}`),
    render: path => (
      <>
        {path.endsWith('/') ? <FolderIcon className="size-3.5 shrink-0 text-sky-400" /> : <FileIcon className="size-3.5 shrink-0 text-muted-foreground" />}
        <span className="truncate font-mono text-xs">{path}</span>
      </>
    ),
  })

  // deleteMention deletes a whole item mention beside the caret, as one edit
  // the browser can undo, and reports whether there was one.
  const deleteMention = (backwards: boolean) => {
    const field = input.current
    if (!field || field.selectionStart !== field.selectionEnd) return false
    const range = mentionBeside(text, refs, field.selectionStart, backwards)
    if (!range) return false
    field.setSelectionRange(range.start, range.end)
    if (!document.execCommand?.('delete')) change(text.slice(0, range.start) + text.slice(range.end))
    setCaret(range.start)
    return true
  }

  const keyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    onKeyDown?.(event)
    if (event.defaultPrevented) return
    const deletes = (event.key === 'Backspace' || event.key === 'Delete') && !event.metaKey
    if ((deletes && deleteMention(event.key === 'Backspace')) || items.onKeyDown(event) || paths.onKeyDown(event)) {
      event.preventDefault()
      return
    }
    if (!submitsOnEnter(event)) return
    event.preventDefault()
    if (!event.repeat) onSubmit()
  }
  const track = () => setCaret(input.current?.selectionStart ?? 0)

  // The field's own text turns transparent over a layer that draws the same
  // text with item mentions colored; scroll bars would wrap them apart.
  const segments = displaySegments(text, refs)
  const highlighting = segments.some(segment => segment.ref)
  return (
    <>
      {items.element}
      {paths.element}
      <div className="relative flex flex-col">
        {highlighting && (
          <div ref={layer} aria-hidden className={cn(box, 'pointer-events-none absolute inset-0 overflow-hidden break-words whitespace-pre-wrap text-foreground')}>
            {segments.map((segment, index) =>
              segment.ref ? (
                <mark key={index} className="rounded-sm bg-sky-500/20 text-sky-300">
                  {segment.text}
                </mark>
              ) : (
                <span key={index}>{segment.text}</span>
              ),
            )}
            {'​'}
          </div>
        )}
        <Textarea
          {...props}
          {...items.field}
          {...paths.field}
          ref={input}
          value={text}
          onChange={event => {
            change(event.target.value)
            track()
          }}
          onSelect={track}
          onScroll={() => {
            if (layer.current && input.current) layer.current.scrollTop = input.current.scrollTop
          }}
          onKeyDown={keyDown}
          className={cn(className, box, highlighting && 'text-transparent caret-foreground [scrollbar-width:none]')}
        />
      </div>
    </>
  )
}
