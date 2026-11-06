// Agents name items by their IDs, often in inline code: `01M8FB56…`. An ID
// in a reply, quoted or not, links to its item as a mention does.

// An item ID is a ULID: 26 Crockford base32 characters, the first 0-7.
const idPattern = /\b[0-7][0-9A-HJKMNP-TV-Z]{25}\b/g
const wholeID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/

type Node = { type: string; value?: string; url?: string; children?: Node[] }

// The wi-id: scheme tells a bare ID, shown as written, from a mention.
export const itemIDScheme = 'wi-id:'
const link = (id: string, child: Node): Node => ({ type: 'link', url: itemIDScheme + id, children: [child] })

// remarkItemIDs links item IDs: an inline code span that is one, and each
// one in text. Links, and code blocks, are left as they are.
export function remarkItemIDs() {
  const visit = (node: Node) => {
    if (!node.children || node.type === 'link' || node.type === 'linkReference') return
    node.children = node.children.flatMap(child => {
      if (child.type === 'inlineCode' && wholeID.test(child.value ?? '')) return [link(child.value!, child)]
      if (child.type === 'text') return splitText(child.value ?? '')
      visit(child)
      return [child]
    })
  }
  return (tree: Node) => visit(tree)
}

function splitText(text: string): Node[] {
  const out: Node[] = []
  let at = 0
  for (const match of text.matchAll(idPattern)) {
    if (match.index > at) out.push({ type: 'text', value: text.slice(at, match.index) })
    out.push(link(match[0], { type: 'text', value: match[0] }))
    at = match.index + match[0].length
  }
  if (at < text.length || !out.length) out.push({ type: 'text', value: text.slice(at) })
  return out
}
