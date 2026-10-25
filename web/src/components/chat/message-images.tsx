import type { HistoryImage } from '@/lib/api'
import { ZoomableImage } from './image-lightbox'
import { safeImageURL } from './markdown'

export function MessageImages({ images }: { images: (HistoryImage & { url?: string })[] }) {
  const sources = images
    .map(image => ({ src: image.data ? `data:${image.mime_type};base64,${image.data}` : (image.url ?? ''), alt: image.alt || 'Image' }))
    .filter(image => safeImageURL(image.src))
  if (!sources.length) return null
  return (
    <div className="flex flex-wrap gap-2">
      {sources.map((image, index) => (
        <ZoomableImage key={index} src={image.src} alt={image.alt} className="max-h-64 max-w-full rounded-md border object-contain" />
      ))}
    </div>
  )
}
