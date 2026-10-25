import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Markdown } from './markdown'

// A preview shows this many blocks, and stops adding them past this length.
const previewBlocks = 3
const previewChars = 600

// ItemDescription shows the start of an item's Markdown description, and
// opens the whole of it the way an attachment opens.
export function ItemDescription({ title, text }: { title: string; text: string }) {
  const [open, setOpen] = useState(false)
  if (!text.trim()) return <p className="text-sm text-muted-foreground">No description.</p>
  const preview = leadingBlocks(text, previewBlocks, previewChars)
  return (
    <>
      <div className="text-sm text-muted-foreground" data-description>
        <Markdown text={preview.text} />
      </div>
      {preview.cut && (
        <Button variant="ghost" size="xs" className="self-start text-sky-300" onClick={() => setOpen(true)}>
          Read the full description
        </Button>
      )}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="flex max-h-[90dvh] flex-col sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle className="truncate pe-8">{title}</DialogTitle>
            <DialogDescription>Description</DialogDescription>
          </DialogHeader>
          <div className="min-h-0 overflow-auto text-sm" data-description-full>
            <Markdown text={text} />
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}

// leadingBlocks keeps the first blocks of Markdown text: runs of lines
// between blank lines, a fenced code block counting as one. It keeps at
// least one block, and stops at count blocks or once chars is reached.
export function leadingBlocks(text: string, count: number, chars: number): { text: string; cut: boolean } {
  const blocks: string[] = []
  let current: string[] = []
  let fence = ''
  for (const line of text.trim().split('\n')) {
    const marker = line.match(/^\s*(`{3,}|~{3,})/)?.[1]
    if (marker && (!fence || marker.startsWith(fence))) fence = fence ? '' : marker
    if (!fence && !line.trim()) {
      if (current.length) blocks.push(current.join('\n'))
      current = []
      continue
    }
    current.push(line)
  }
  if (current.length) blocks.push(current.join('\n'))
  let kept = 1
  let length = blocks[0]?.length ?? 0
  while (kept < Math.min(count, blocks.length) && length < chars) length += blocks[kept++].length
  return { text: blocks.slice(0, kept).join('\n\n'), cut: kept < blocks.length }
}
