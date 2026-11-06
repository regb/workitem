import { useRef, useState, type ClipboardEvent } from 'react'
import { PaperclipIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { HistoryMessage } from '@/lib/api'
import { reportError } from '@/lib/ui-state'

type Attachment = { mime_type: string; data: string; name: string; bytes: number }

const imageTypes = new Set(['image/jpeg', 'image/png', 'image/gif', 'image/webp'])
const maxImages = 4
const maxImageBytes = 8 << 20

function readImage(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result).split(',')[1] ?? '')
    reader.onerror = () => reject(new Error(`Could not read ${file.name}`))
    reader.readAsDataURL(file)
  })
}

// useImageAttachments holds the images attached to a message being written:
// at most four, 8 MiB in all, of the types agents read. supported is whether
// the agent's driver takes images at all.
export function useImageAttachments(supported: boolean) {
  const [images, setImages] = useState<Attachment[]>([])
  const add = async (files: File[]) => {
    try {
      if (!supported) throw new Error('This agent driver does not support image input.')
      const next = [...images]
      for (const file of files) {
        if (next.length >= maxImages) throw new Error('Attach at most four images.')
        if (!imageTypes.has(file.type)) throw new Error(`${file.name} is not a supported image.`)
        if (next.reduce((sum, image) => sum + image.bytes, 0) + file.size > maxImageBytes) throw new Error('Images exceed 8 MiB total.')
        next.push({ mime_type: file.type, data: await readImage(file), name: file.name, bytes: file.size })
      }
      setImages(next)
    } catch (error) {
      reportError((error as Error).message)
    }
  }
  return {
    images,
    add,
    // paste attaches pasted images instead of pasting them as text.
    paste: (event: ClipboardEvent<HTMLTextAreaElement>) => {
      const pasted = [...event.clipboardData.items]
        .filter(entry => entry.kind === 'file' && entry.type.startsWith('image/'))
        .map(entry => entry.getAsFile())
        .filter((file): file is File => !!file)
      if (!pasted.length) return
      event.preventDefault()
      void add(pasted)
    },
    remove: (index: number) => setImages(images.filter((_, other) => other !== index)),
    clear: () => setImages([]),
    // restore attaches again the images of a message being edited to retry.
    restore: (restored: NonNullable<HistoryMessage['images']>) =>
      setImages(restored.filter(image => image.data).map(image => ({ mime_type: image.mime_type, data: image.data, name: image.alt || 'Image', bytes: Math.floor(image.data.length * 0.75) }))),
    // sent is the images as a message carries them.
    sent: () => images.map(image => ({ mime_type: image.mime_type, data: image.data, alt: image.name })),
  }
}

// AttachedImages shows the attached images, each with a way to remove it.
export function AttachedImages({ images, onRemove }: { images: Attachment[]; onRemove: (index: number) => void }) {
  if (!images.length) return null
  return (
    <div className="flex flex-wrap gap-2 p-2 pb-0" aria-label="Image attachments">
      {images.map((image, index) => (
        <div key={index} className="relative">
          <img src={`data:${image.mime_type};base64,${image.data}`} alt={image.name} className="size-16 rounded-md border object-cover" />
          <Button type="button" variant="secondary" size="icon-xs" className="absolute -end-1.5 -top-1.5 rounded-full" aria-label={`Remove ${image.name}`} onClick={() => onRemove(index)}>
            <XIcon />
          </Button>
        </div>
      ))}
    </div>
  )
}

// AttachButton opens the file chooser for images.
export function AttachButton({ onFiles, disabled }: { onFiles: (files: File[]) => void; disabled?: boolean }) {
  const files = useRef<HTMLInputElement>(null)
  return (
    <>
      <input
        ref={files}
        type="file"
        accept="image/jpeg,image/png,image/gif,image/webp"
        multiple
        className="sr-only"
        onChange={event => {
          onFiles([...(event.target.files ?? [])])
          event.target.value = ''
        }}
      />
      <Button type="button" variant="ghost" size="icon-sm" aria-label="Attach images" disabled={disabled} onClick={() => files.current?.click()}>
        <PaperclipIcon />
      </Button>
    </>
  )
}
