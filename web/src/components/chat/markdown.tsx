import { memo, useMemo } from 'react'
import ReactMarkdown, { defaultUrlTransform, type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useAttachmentLinks } from './attachment-context'
import { Code } from './code'
import { commandOf, RunButton, RunOutput, shellLanguages, useRunCommand, useRunItem } from './runnable-code'
import { FileLink } from './file-link'
import { parseFileReference, remarkFileReferences } from '@/lib/file-references'
import { ZoomableImage } from './image-lightbox'
import { attachmentScheme, remarkAttachmentLinks } from '@/lib/attachment-links'
import { wirePattern } from '@/lib/item-mentions'
import { keys, queryClient } from '@/lib/queries'
import { itemIDScheme, remarkItemIDs } from '@/lib/item-ids'
import { cn } from '@/lib/utils'

// Agent output is untrusted. react-markdown never renders raw HTML (there is
// no rehype-raw), unsafe URLs are dropped, and images load only from https:
// or inline data: URLs.
export const safeImageURL = (src: string) => /^https:\/\//.test(src) || /^data:image\/(png|jpeg|gif|webp);base64,/.test(src)

const components: Components = {
  pre: ({ children }) => <>{children}</>,
  code: ({ className, children }) => {
    const language = /language-(\w+)/.exec(className ?? '')?.[1]
    const value = String(children ?? '')
    // Fenced blocks have a language or span lines; the rest is inline code.
    if (language || value.includes('\n')) return <FencedCode language={language ?? ''} value={value} />
    return <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]">{children}</code>
  },
  a: ({ href, title, children }) => {
    if (href?.startsWith('wi:')) return <ItemMention id={href.slice(3)} slug={title}>{children}</ItemMention>
    if (href?.startsWith(itemIDScheme)) return <ItemIDLink id={href.slice(itemIDScheme.length)}>{children}</ItemIDLink>
    if (href?.startsWith(attachmentScheme)) return <AttachmentLink name={decodeURIComponent(href.slice(attachmentScheme.length))}>{children}</AttachmentLink>
    const reference = href ? parseFileReference(href) : null
    if (reference) return <FileLink reference={reference}>{children}</FileLink>
    return <a href={href} target="_blank" rel="noreferrer" className="text-sky-300 underline underline-offset-2">{children}</a>
  },
  img: ({ src, alt }) => (typeof src === 'string' && safeImageURL(src) ? <ZoomableImage src={src} alt={alt || 'Image'} className="my-2 max-h-96 rounded-md border" /> : null),
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto">
      <table className="w-full border-collapse text-sm">{children}</table>
    </div>
  ),
  th: ({ children }) => <th className="border px-2 py-1 text-left font-medium">{children}</th>,
  td: ({ children }) => <td className="border px-2 py-1 align-top">{children}</td>,
}

// ItemMention shows a mention of a work item as it was written, @@Title,
// highlighted; it opens that item's conversation.
function ItemMention({ id, slug, children }: { id: string; slug?: string | null; children: React.ReactNode }) {
  const open = (event: React.MouseEvent) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return
    event.preventDefault()
    void import('@/router').then(({ router }) => router.navigate({ to: '/items/$id', params: { id } }))
  }
  return (
    <a href={`/items/${id}`} title={slug ? `${slug} · ${id}` : id} onClick={open} className="rounded-sm bg-sky-500/20 px-0.5 text-sky-300 no-underline hover:bg-sky-500/30">
      @@{children}
    </a>
  )
}

// PDFs use ordinary new-tab links; images and Markdown keep their in-page viewer.
// itemTitle names an item from the work list already read, if it is there.
function itemTitle(id: string) {
  const list = queryClient.getQueryData<{ display_sections?: { items: { id: string; title: string }[] }[] }>(keys.list)
  return list?.display_sections?.flatMap(section => section.items).find(row => row.id === id)?.title
}

// ItemIDLink links an item's ID as it was written, quoted or not, and names
// the item when the work list knows it.
function ItemIDLink({ id, children }: { id: string; children: React.ReactNode }) {
  const title = itemTitle(id)
  const open = (event: React.MouseEvent) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return
    event.preventDefault()
    void import('@/router').then(({ router }) => router.navigate({ to: '/items/$id', params: { id } }))
  }
  return (
    <a href={`/items/${id}`} title={title ? `${title} · ${id}` : id} onClick={open} className="text-sky-300 underline decoration-sky-300/40 underline-offset-2 hover:decoration-sky-300">
      {children}
    </a>
  )
}

function AttachmentLink({ name, children }: { name: string; children: React.ReactNode }) {
  const { open, external } = useAttachmentLinks()
  const url = external?.[name]
  if (url) return <a href={url} target="_blank" rel="noopener noreferrer" title={`Open ${name} in a new tab`} data-attachment-link={name} className="text-sky-300 underline decoration-dotted underline-offset-2 hover:decoration-solid">{children}</a>
  return (
    <button
      type="button"
      onClick={() => open(name)}
      title={`Open the attachment ${name}`}
      className="text-sky-300 underline decoration-dotted underline-offset-2 hover:decoration-solid"
      data-attachment-link={name}
    >
      {children}
    </button>
  )
}

// Item mentions are links to wi: URLs, and attachment links to attachment:
// URLs, which are kept; the @@ before a mention is drawn with it. Recognised
// file URLs are consumed by FileLink, never emitted as browser file: navigation.
const urlTransform = (url: string) => (/^wi(-id)?:[0-9A-Z]{26}$/.test(url) || url.startsWith(attachmentScheme) || parseFileReference(url) ? url : defaultUrlTransform(url))
const mentionLinks = (text: string) => (text.includes('@@[') ? text.replace(wirePattern(), match => match.slice(2)) : text)

export const Markdown = memo(function Markdown({ text, className }: { text: string; className?: string }) {
  const { names } = useAttachmentLinks()
  const key = names.join('\n')
  const plugins = useMemo(() => [remarkGfm, remarkAttachmentLinks(key ? key.split('\n') : []), remarkFileReferences, remarkItemIDs], [key])
  return (
    <div className={cn('prose-wi min-w-0 text-sm leading-relaxed', className)}>
      <ReactMarkdown remarkPlugins={plugins} components={components} urlTransform={urlTransform} skipHtml>
        {mentionLinks(text)}
      </ReactMarkdown>
    </div>
  )
})

// FencedCode is a fenced block. In an agent's reply in an item's
// conversation, a shell snippet can be run in the item's checkout, its
// output shown under it.
function FencedCode({ language, value }: { language: string; value: string }) {
  const item = useRunItem()
  const runnable = !!item && shellLanguages.has(language.toLowerCase())
  if (!runnable) return <Code language={language} value={value} className="my-2" />
  return <RunnableCode item={item} language={language.toLowerCase()} value={value} />
}

function RunnableCode({ item, language, value }: { item: string; language: string; value: string }) {
  const { run, start, stop, clear } = useRunCommand(item)
  return (
    <>
      <Code language={language} value={value} className={run ? 'mt-2 rounded-b-none' : 'my-2'} actions={<RunButton command={commandOf(language, value)} onRun={command => void start(command)} />} />
      {run && <RunOutput run={run} onStop={stop} onClose={clear} />}
    </>
  )
}
