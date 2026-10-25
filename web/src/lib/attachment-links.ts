// Attachment links: a conversation naming one of its item's attachments,
// by name or by a path ending in it (`REPORT.md`, /tmp/REPORT.md), links
// the name to it, so it opens in the page.

// The links' URLs; the name follows, URI-encoded.
export const attachmentScheme = 'attachment:'
export const attachmentURL = (name: string) => attachmentScheme + encodeURIComponent(name)

// A Markdown syntax tree node: only what linking reads and writes.
type Node = { type: string; value?: string; url?: string; children?: Node[] }

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

// namePattern matches a name, or a path ending in it, standing on its own:
// not inside a longer word or file name.
function namePattern(names: string[], flags: string) {
  const alternatives = [...names]
    .sort((a, b) => b.length - a.length)
    .map(escape)
    .join('|')
  return new RegExp(`(?<![\\w.~/-])(?:[\\w.~-]*/)*(${alternatives})(?![\\w-]|\\.\\w)`, flags)
}

// remarkAttachmentLinks links the attachments named in text and inline code.
// Code blocks, and text already in a link, are left alone.
export function remarkAttachmentLinks(names: string[]) {
  if (!names.length) return () => {}
  const inText = namePattern(names, 'g')
  const whole = new RegExp(`^${namePattern(names, '').source}$`)
  const link = (name: string, child: Node): Node => ({ type: 'link', url: attachmentURL(name), children: [child] })
  const visit = (node: Node) => {
    if (!node.children || node.type === 'link' || node.type === 'linkReference') return
    node.children = node.children.flatMap(child => {
      if (child.type === 'inlineCode') {
        const name = whole.exec(child.value ?? '')?.[1]
        return name ? [link(name, child)] : [child]
      }
      if (child.type !== 'text') {
        visit(child)
        return [child]
      }
      const text = child.value ?? ''
      const parts: Node[] = []
      let last = 0
      for (const match of text.matchAll(inText)) {
        if (match.index > last) parts.push({ type: 'text', value: text.slice(last, match.index) })
        parts.push(link(match[1], { type: 'text', value: match[0] }))
        last = match.index + match[0].length
      }
      if (!parts.length) return [child]
      if (last < text.length) parts.push({ type: 'text', value: text.slice(last) })
      return parts
    })
  }
  return () => (tree: Node) => visit(tree)
}
