// Item mentions: @@ followed by an item's title in the message box. What is
// sent is a compact Markdown link an agent can act on, with the stable ID
// (for wi show), the slug and the title:
//
//   @@[Fix the login page](wi:01J0ABCDEFGHJKMNPQRSTVWXYZ "fix-login-page")
//
// The box shows only @@Title; the link turns back into that when a sent
// message is edited again.

export type ItemRef = { id: string; title: string; slug?: string }

const escapeTitle = (title: string) => title.replace(/[\\\]\[]/g, '\\$&').replace(/\s+/g, ' ')

// wireMention is the compact form of a mention that is sent.
export const wireMention = (ref: ItemRef) => `@@[${escapeTitle(ref.title)}](wi:${ref.id}${ref.slug ? ` "${ref.slug.replace(/"/g, '')}"` : ''})`

// wirePattern matches sent mentions: escaped title, ID, optional slug.
export const wirePattern = () => /@@\[((?:[^\]\\]|\\.)*)\]\(wi:([0-9A-HJKMNP-TV-Z]{26})(?: "([^"]*)")?\)/g

// Segment is a run of display text, a mention when ref is set.
export type Segment = { text: string; ref?: ItemRef }

// displaySegments splits the box text at its @@Title mentions. A title must
// end at a space, punctuation or the end of the text; when several known
// titles match, the longest wins. A picked item comes before an unpicked
// one of the same title.
export function displaySegments(text: string, refs: ItemRef[]): Segment[] {
  const segments: Segment[] = []
  const known = [...refs].sort((a, b) => b.title.length - a.title.length)
  let plain = 0
  for (let at = text.indexOf('@@'); at >= 0; at = text.indexOf('@@', at + 2)) {
    if ((at > 0 && !/\s/.test(text[at - 1])) || at < plain) continue
    const rest = text.slice(at + 2)
    const ref = known.find(ref => ref.title && rest.startsWith(ref.title) && !/[\p{L}\p{N}_]/u.test(rest[ref.title.length] ?? ''))
    if (!ref) continue
    if (at > plain) segments.push({ text: text.slice(plain, at) })
    segments.push({ text: `@@${ref.title}`, ref })
    plain = at + 2 + ref.title.length
  }
  if (plain < text.length) segments.push({ text: text.slice(plain) })
  return segments
}

// toWire turns each @@Title mention of the box into its sent form.
export const toWire = (text: string, refs: ItemRef[]) =>
  displaySegments(text, refs)
    .map(segment => (segment.ref ? wireMention(segment.ref) : segment.text))
    .join('')

// fromWire turns sent mentions back into @@Title, with the items they name.
export function fromWire(text: string): { text: string; refs: ItemRef[] } {
  const refs: ItemRef[] = []
  const display = text.replace(wirePattern(), (_, title: string, id: string, slug?: string) => {
    const ref = {
      id,
      title: title.replace(/\\(.)/g, '$1'),
      slug: slug || undefined,
    }
    refs.push(ref)
    return `@@${ref.title}`
  })
  return { text: display, refs }
}

// mentionBeside finds the mention a deletion at the caret would cut into: the
// one ending at it when deleting backwards, the one starting at it when
// deleting forwards. A mention is deleted whole, as it names an item.
export function mentionBeside(text: string, refs: ItemRef[], caret: number, backwards: boolean): { start: number; end: number } | null {
  let start = 0
  for (const segment of displaySegments(text, refs)) {
    const end = start + segment.text.length
    if (segment.ref && (backwards ? end === caret : start === caret)) return { start, end }
    start = end
  }
  return null
}

// itemMentionAt finds the @@-mention being typed before the caret: @@ at the
// start or after a space, and the words typed after it on the same line.
export function itemMentionAt(text: string, caret: number): { start: number; query: string } | null {
  const match = /(^|\s)@@([^\n@]{0,80})$/.exec(text.slice(0, caret))
  return match ? { start: match.index + match[1].length, query: match[2] } : null
}
