import { useRef, useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { DownloadIcon, FileTextIcon, ImageIcon, LoaderIcon, PaperclipIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { api, itemAction, type ItemNotes, type NoteAttachment } from '@/lib/api'
import { fileLanguage } from '@/lib/tools'
import { Code } from './code'
import { Markdown } from './markdown'

// Notes and attachments are quiet: read while shown, never marked unread.
const refreshEvery = 5000

// ItemNotesSection shows an item's log of notes, newest first, and its
// attachments.
export function ItemNotesSection({ id }: { id: string }) {
  const { data, error } = useItemNotes(id, refreshEvery)
  const [open, setOpen] = useState<NoteAttachment | null>(null)
  return (
    <>
      <section className="flex flex-col gap-1.5" data-notes>
        <h3 className="text-sm font-medium">Notes</h3>
        <NoteInput id={id} />
        {error && <p className="text-sm text-destructive">{(error as Error).message}</p>}
        {data?.notes.length ? (
          <ol className="flex flex-col gap-3 border-s ps-3">
            {[...data.notes].reverse().map((note, index) => (
              <li key={index} className="flex flex-col gap-0.5">
                <span className="flex items-center gap-2 text-xs text-muted-foreground">
                  <time dateTime={note.time}>{when(note.time)}</time>
                  {note.head && (
                    <span className="font-mono" title={note.head}>
                      {note.head.slice(0, 9)}
                    </span>
                  )}
                </span>
                <p className="text-sm whitespace-pre-wrap">{note.text}</p>
                {note.attachment && <AttachmentButton attachment={note.attachment} onOpen={setOpen} />}
              </li>
            ))}
          </ol>
        ) : (
          !error && <p className="text-sm text-muted-foreground">No notes</p>
        )}
      </section>
      <section className="flex flex-col gap-1.5" data-attachments>
        <h3 className="text-sm font-medium">Attachments</h3>
        {data?.attachments.length ? (
          <ul className="flex flex-col gap-1">
            {data.attachments.map(attachment => (
              <li key={attachment.name}>
                <AttachmentButton attachment={attachment} onOpen={setOpen} />
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-muted-foreground">No attachments</p>
        )}
        <AttachFile id={id} />
      </section>
      <AttachmentViewer id={id} attachment={open} onClose={() => setOpen(null)} />
    </>
  )
}

// useItemNotes reads an item's notes and attachments, again every refresh
// milliseconds when given.
export const useItemNotes = (id: string, refresh?: number) =>
  useQuery({
    queryKey: ['notes', id],
    queryFn: ({ signal }) => api<ItemNotes>(`/api/items/${id}/notes`, undefined, signal),
    refetchInterval: refresh,
    staleTime: 0,
  })

// Notes and attachments match wi note and wi attach.
const maxNoteBytes = 2048
const maxAttachmentBytes = 16 << 20

// NoteInput adds a note to the log. It never reaches the agent.
function NoteInput({ id }: { id: string }) {
  const queryClient = useQueryClient()
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!text.trim() || sending) return
    setSending(true)
    setError('')
    try {
      await itemAction(id, 'notes', { text })
      setText('')
      await queryClient.invalidateQueries({ queryKey: ['notes', id] })
    } catch (failure) {
      setError((failure as Error).message)
    } finally {
      setSending(false)
    }
  }
  const tooLong = new TextEncoder().encode(text.trim()).length > maxNoteBytes
  return (
    <form onSubmit={submit} className="flex flex-col gap-1" data-note-input>
      <Input
        value={text}
        onChange={event => setText(event.target.value)}
        placeholder="Add a note"
        aria-label="Add a note"
        disabled={sending}
        aria-invalid={tooLong || undefined}
        className="h-7 text-sm"
      />
      {tooLong && <p className="text-xs text-destructive">Notes are at most 2 KB; attach longer content.</p>}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </form>
  )
}

// AttachFile attaches a file to the item, which also adds a note.
function AttachFile({ id }: { id: string }) {
  const queryClient = useQueryClient()
  const input = useRef<HTMLInputElement>(null)
  const [sending, setSending] = useState('')
  const [error, setError] = useState('')
  const attach = async (file: File) => {
    setError('')
    if (file.size > maxAttachmentBytes) {
      setError(`${file.name} is ${size(file.size)}; attachments are at most 16 MB.`)
      return
    }
    setSending(file.name)
    try {
      await itemAction(id, 'attachments', { name: file.name, content: await readBase64(file) })
      await queryClient.invalidateQueries({ queryKey: ['notes', id] })
    } catch (failure) {
      setError((failure as Error).message)
    } finally {
      setSending('')
    }
  }
  return (
    <div className="flex flex-col gap-1">
      <input
        ref={input}
        type="file"
        className="hidden"
        data-attach-input
        onChange={event => {
          const file = event.target.files?.[0]
          event.target.value = ''
          if (file) void attach(file)
        }}
      />
      <Button type="button" variant="ghost" size="xs" className="self-start text-muted-foreground" disabled={!!sending} onClick={() => input.current?.click()}>
        {sending ? <LoaderIcon className="animate-spin" /> : <PaperclipIcon />} {sending ? `Attaching ${sending}…` : 'Attach file'}
      </Button>
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  )
}

function readBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result).split(',')[1] ?? '')
    reader.onerror = () => reject(new Error(`Could not read ${file.name}`))
    reader.readAsDataURL(file)
  })
}

const when = (time: string) => new Date(time).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })

// viewable reports an attachment a conversation links to: an image or
// Markdown, which the viewer shows.
export const viewable = (attachment: NoteAttachment) => isImage(attachment.type) || isMarkdown(attachment)

const isMarkdown = (attachment: NoteAttachment) => /^text\/markdown\b/.test(attachment.type) || /\.md$/i.test(attachment.name)
const isImage = (type: string) => /^image\/(png|jpeg|gif|webp)\b/.test(type)
const isText = (type: string) => (/^text\//.test(type) && !/^text\/html\b/.test(type)) || /^application\/json\b/.test(type)

function AttachmentButton({ attachment, onOpen }: { attachment: NoteAttachment; onOpen: (attachment: NoteAttachment) => void }) {
  const Icon = isImage(attachment.type) ? ImageIcon : isText(attachment.type) ? FileTextIcon : PaperclipIcon
  return (
    <button type="button" onClick={() => onOpen(attachment)} className="flex max-w-full min-w-0 items-center gap-1.5 text-start text-sm text-sky-300 hover:underline" data-attachment={attachment.name}>
      <Icon className="size-3.5 shrink-0" aria-hidden />
      <span className="min-w-0 truncate">{attachment.name}</span>
      <span className="shrink-0 text-xs text-muted-foreground">{size(attachment.size)}</span>
    </button>
  )
}

const size = (bytes: number) => (bytes >= 1 << 20 ? `${(bytes / (1 << 20)).toFixed(1)} MB` : bytes >= 1 << 10 ? `${(bytes / (1 << 10)).toFixed(1)} KB` : `${bytes} B`)

// AttachmentViewer shows an image or text attachment in the page, and offers
// anything else as a download.
export function AttachmentViewer({ id, attachment, onClose }: { id: string; attachment: NoteAttachment | null; onClose: () => void }) {
  const url = attachment ? `/api/items/${id}/attachments/${attachment.sha256}` : ''
  return (
    <Dialog open={!!attachment} onOpenChange={open => !open && onClose()}>
      <DialogContent className="flex max-h-[90dvh] flex-col sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="truncate pe-8">{attachment?.name}</DialogTitle>
          <DialogDescription className="flex items-center gap-2">
            {attachment && size(attachment.size)}
            <a href={url} download={attachment?.name} className="inline-flex items-center gap-1 text-sky-300 hover:underline">
              <DownloadIcon className="size-3.5" aria-hidden /> Download
            </a>
          </DialogDescription>
        </DialogHeader>
        {attachment && (
          <div className="min-h-0 overflow-auto">
            {isImage(attachment.type) ? (
              <img src={url} alt={attachment.name} className="mx-auto max-w-full rounded-md border" />
            ) : isText(attachment.type) ? (
              <AttachmentText url={url} attachment={attachment} />
            ) : (
              <p className="text-sm text-muted-foreground">This file cannot be shown here.</p>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function AttachmentText({ url, attachment }: { url: string; attachment: NoteAttachment }) {
  const { data, error, isPending } = useQuery({
    queryKey: ['attachment', url],
    queryFn: async ({ signal }) => {
      const response = await fetch(url, { signal })
      if (!response.ok) throw new Error(`${response.status} ${response.statusText}`)
      return response.text()
    },
  })
  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (error) return <p className="text-sm text-destructive">{(error as Error).message}</p>
  if (isMarkdown(attachment)) {
    return (
      <div className="text-sm" data-attachment-markdown>
        <Markdown text={data} />
      </div>
    )
  }
  return <Code language={fileLanguage(attachment.name)} value={data} contentClassName="max-h-none" />
}
