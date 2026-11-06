import { create } from '@bufbuild/protobuf'
import { LabelRuleSchema, type LabelRule } from '@/gen/wi/core/v1/items_pb'

// The dashboard's label filter is this browser's preference, written as
// `wi list` reads WI_LIST_LABELS: comma-separated labels, each required, or
// excluded with a leading minus (a plus is allowed); `!` drops the rules
// before it. The CLI's configured filters do not apply here: they belong to
// the terminal they are set in.

const storageKey = 'wi.listLabels'

// normalizeLabel normalizes a label as the daemon does, or reports why it
// is not one.
export function normalizeLabel(raw: string): string {
  let out = ''
  let separated = false
  for (const c of raw.trim().toLowerCase()) {
    if (/[a-z0-9_\-./]/.test(c)) {
      out += c
      separated = false
    } else if (/\s/.test(c)) {
      if (out && !separated) out += '-'
      separated = true
    } else throw new Error(`label "${raw.trim()}" contains unsafe character "${c}"`)
  }
  out = out.replace(/^-+|-+$/g, '')
  if (!out) throw new Error('label must not be empty')
  return out
}

// parseLabelFilter reads a filter into label rules; a bad label throws.
export function parseLabelFilter(text: string): LabelRule[] {
  const rules = new Map<string, boolean>()
  for (let token of text.split(',')) {
    token = token.trim()
    if (!token) continue
    if (token === '!') {
      rules.clear()
      continue
    }
    const include = token[0] !== '-'
    if (token[0] === '+' || token[0] === '-') token = token.slice(1)
    rules.set(normalizeLabel(token), include)
  }
  return [...rules].map(([label, include]) => create(LabelRuleSchema, { rule: include ? { case: 'require', value: label } : { case: 'exclude', value: label } }))
}

export function readLabelFilter() {
  try {
    return localStorage.getItem(storageKey) ?? ''
  } catch {
    return ''
  }
}

// saveLabelFilter keeps a valid filter for this browser; an invalid one
// throws and changes nothing.
export function saveLabelFilter(text: string) {
  parseLabelFilter(text)
  try {
    if (text.trim()) localStorage.setItem(storageKey, text.trim())
    else localStorage.removeItem(storageKey)
  } catch {
    throw new Error('this browser does not keep preferences')
  }
}

// labelRules are the saved filter's rules; a filter saved by another
// version that no longer parses filters nothing.
export function labelRules(): LabelRule[] {
  try {
    return parseLabelFilter(readLabelFilter())
  } catch {
    return []
  }
}
