import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt'
import type { Attachment } from '@/gen/wi/core/v1/items_pb'
import type { ItemNotes, NoteAttachment } from './views'
import { allPages, items } from './rpc'

// An item's notes and attachments as the notes panel shows them, built from
// the API's messages.

const iso = (at?: Timestamp) => (at ? timestampDate(at).toISOString() : '')
const attachmentView = (a: Attachment): NoteAttachment => ({ name: a.name, sha256: a.sha256, size: a.size, type: a.contentType })

export async function readNotes(id: string, signal?: AbortSignal): Promise<ItemNotes> {
  const value = await allPages(pageToken => items.listNotes({ itemId: id, pageToken }, { signal }), page => page.notes)
  return {
    notes: value.notes.map(note => ({ time: iso(note.time), text: note.text, head: note.head || undefined, attachment: note.attachment ? attachmentView(note.attachment) : undefined })),
    attachments: value.attachments.filter(current => current.attachment).map(current => ({ ...attachmentView(current.attachment!), time: iso(current.time) })),
  }
}
