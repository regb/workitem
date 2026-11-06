// Recognition is deliberately separate from workspace lookup. Message text is
// untrusted: recognising a path must never read it or navigate the browser to it.
export type FileReference = { path: string; line?: number; column?: number; endLine?: number }
export type FileTarget = FileReference & { directory: boolean }

const drive = /^[A-Za-z]:\//
const names = new Set(['Makefile', 'Dockerfile', 'Containerfile', 'Justfile', 'Gemfile', 'Rakefile', 'Procfile', 'Brewfile', 'BUILD', 'WORKSPACE', 'LICENSE', 'README', 'CODEOWNERS'])
const hostTLD = /\.(?:com|net|org|io|dev|app|ai|co|edu|gov|info|biz|xyz|me|tv|gg|cloud|site|online|tech|store|uk|de|fr|nl|eu|us|ca|au|jp|cn)$/i
const extension = /\.[\p{L}\p{N}_-]+$/u
const unsafe = /[\x00-\x1f\x7f`]/

export function parseFileReference(value: string): FileReference | null {
  let raw = value.trim()
  if (raw.startsWith('<') && raw.endsWith('>')) raw = raw.slice(1, -1)
  if (!raw || raw.length > 4096 || raw.startsWith('#') || raw.startsWith('//') || unsafe.test(raw)) return null
  // A drive letter and a numeric position are not URL schemes.
  if (/^[A-Za-z][\w+.-]*:/.test(raw) && !/^[A-Za-z]:(?:[\\/]|%2f|%5c)/i.test(raw) && !/^[^:/\\]+:\d+(?::\d+)?$/.test(raw) && !/^file:/i.test(raw)) return null
  let path: string
  let hash = ''
  try {
    if (/^file:/i.test(raw)) {
      const url = new URL(raw)
      if (url.username || url.password || url.port) return null
      path = (url.hostname && url.hostname !== 'localhost' ? `//${url.hostname}` : '') + decodeURIComponent(url.pathname)
      hash = decodeURIComponent(url.hash.slice(1))
    } else {
      const hashAt = raw.indexOf('#')
      hash = hashAt < 0 ? '' : decodeURIComponent(raw.slice(hashAt + 1))
      path = decodeURIComponent((hashAt < 0 ? raw : raw.slice(0, hashAt)).split('?')[0])
    }
  } catch {
    return null
  }
  // Backslashes separate Windows paths, which start with a drive or, for a
  // network share, two backslashes. A single leading one is an escape, such
  // as \n or \", not a root.
  if (/^\\(?!\\)/.test(path)) return null
  path = path.replaceAll('\\', '/').replace(/^\/([A-Za-z]:\/)/, '$1')
  if (!path || unsafe.test(path)) return null
  const suffix = /^(.*?):([1-9]\d*)(?::([1-9]\d*))?$/.exec(path)
  const anchor = /^L?([1-9]\d*)(?:C([1-9]\d*))?(?:-L?([1-9]\d*)(?:C[1-9]\d*)?)?$/i.exec(hash)
  if (suffix) path = suffix[1]
  const line = suffix?.[2] ?? anchor?.[1]
  const column = suffix ? suffix[3] : anchor?.[2]
  const endLine = suffix ? undefined : anchor?.[3]
  if ([line, column, endLine].some(n => n && (!Number.isSafeInteger(Number(n)) || Number(n) > 10_000_000))) return null
  if (endLine && Number(endLine) < Number(line)) return null
  if (/[:]/.test(path.replace(drive, '')) || !/^[\p{L}\p{M}\p{N}._~ /+@()[\]-]+$/u.test(path.replace(drive, ''))) return null
  const explicit = /^(?:\.{1,2}\/|~\/|\/)/.test(path) || drive.test(path)
  const basename = path.split('/').at(-1) ?? ''
  if (!explicit && !path.includes('/') && !extension.test(basename) && !names.has(basename)) return null
  return { path, ...(line ? { line: Number(line) } : {}), ...(column ? { column: Number(column) } : {}), ...(endLine ? { endLine: Number(endLine) } : {}) }
}

export function inlineFileReference(value: string): FileReference | null {
  const raw = value.trim()
  if (!raw || /[\s`]/.test(raw)) return null
  const reference = parseFileReference(raw)
  if (!reference || reference.path.endsWith('/')) return null
  const path = reference.path
  if (!path.includes('/') && !reference.line) return null
  if (!/^(?:\.{1,2}\/|~\/|\/)/.test(path) && !drive.test(path)) {
    const first = path.split('/')[0]
    if (first === 'localhost' || /^\d+(?:\.\d+)+$/.test(first) || (!first.startsWith('.') && hostTLD.test(first))) return null
    const basename = path.split('/').at(-1) ?? ''
    if (/\d\.\d[^.]*$/.test(basename) || (!reference.line && !extension.test(basename))) return null
  }
  return reference
}

// Lexical containment is a client-side guard, not filesystem authorization.
// The core API still validates the claim, path and no-symlink source access.
function normalize(path: string): string | null {
  const prefix = drive.test(path) ? path.slice(0, 2) : path.startsWith('//') ? '//' : path.startsWith('/') ? '/' : ''
  const parts: string[] = []
  for (const part of path.slice(prefix.length).split('/')) {
    if (!part || part === '.') continue
    if (part === '..') {
      if (!parts.length) return null
      parts.pop()
    } else parts.push(part)
  }
  return prefix + (prefix && prefix !== '/' && prefix !== '//' ? '/' : '') + parts.join('/')
}

export function workspaceFilePath(path: string, workspace: string): string | null {
  const root = normalize(workspace.replaceAll('\\', '/'))
  if (!root) return null
  let candidate = path.replaceAll('\\', '/')
  if (candidate.split('/').some(part => part.toLowerCase() === '.git')) return null
  if (candidate.startsWith('~/')) {
    const home = /^(\/(?:Users|home)\/[^/]+|\/root|[A-Za-z]:\/Users\/[^/]+)(?:\/|$)/i.exec(root)?.[1]
    if (!home) return null
    candidate = `${home}/${candidate.slice(2)}`
  }
  const absolute = candidate.startsWith('/') || drive.test(candidate)
  const resolved = normalize(absolute ? candidate : `${root}/${candidate}`)
  if (!resolved) return null
  const key = (s: string) => (drive.test(root) || root.startsWith('//') ? s.toLowerCase() : s)
  if (key(resolved) === key(root)) return '.'
  const prefix = root.endsWith('/') ? root : `${root}/`
  return key(resolved).startsWith(key(prefix)) ? resolved.slice(prefix.length) : null
}

export function lookupFileReference(reference: FileReference, workspace: string, paths: readonly string[], truncated = false): FileTarget[] {
  const path = workspaceFilePath(reference.path, workspace)
  if (path === null) throw new Error('This reference is outside the item workspace.')
  const directory = reference.path.endsWith('/') || path === '.' || paths.some(p => p.startsWith(`${path}/`))
  if (directory) return [{ ...reference, path, directory: true }]
  if (paths.includes(path)) return [{ ...reference, path, directory: false }]
  // Only abbreviated relative names may fall back to a suffix lookup. Never
  // reinterpret an absolute path or an explicitly rooted ./path as another file.
  if (!/^(?:\.{1,2}\/|~\/|\/)/.test(reference.path) && !drive.test(reference.path)) {
    const matches = paths.filter(p => p.endsWith(`/${path}`))
    if (matches.length === 1 && truncated) throw new Error('The workspace file list is truncated. Use an explicit ./path instead of an abbreviated filename.')
    if (matches.length) return matches.map(p => ({ ...reference, path: p, directory: false }))
  }
  // Missing files are still opened by exact path, where the source API reports
  // existence and errors. Recognition does not probe the filesystem.
  return [{ ...reference, path, directory: false }]
}

// referenceExists reports whether a reference names a file or directory of
// the workspace list, as lookupFileReference would find it: exactly, or for
// an abbreviated relative name, by its ending.
export function referenceExists(reference: FileReference, workspace: string, paths: readonly string[]): boolean {
  const path = workspaceFilePath(reference.path, workspace)
  if (path === null) return false
  if (path === '.' || paths.includes(path) || paths.some(p => p.startsWith(`${path}/`))) return true
  return !/^(?:\.{1,2}\/|~\/|\/)/.test(reference.path) && !drive.test(reference.path) && paths.some(p => p.endsWith(`/${path}`))
}

// inlineURL is inline code that is one web address, as agents often write
// one: it links like a bare address, kept as code.
export function inlineURL(value: string): string | null {
  const raw = value.trim()
  if (!/^https?:\/\/[^\s`<>"]+$/i.test(raw)) return null
  try {
    const url = new URL(raw)
    return url.hostname ? raw : null
  } catch {
    return null
  }
}

type Node = { type: string; value?: string; url?: string; children?: Node[] }
export function remarkFileReferences() {
  const visit = (node: Node) => {
    if (!node.children || node.type === 'link' || node.type === 'linkReference') return
    node.children = node.children.map(child => {
      const url = child.type === 'inlineCode' ? inlineURL(child.value ?? '') : null
      if (url) return { type: 'link', url, children: [child] }
      if (child.type === 'inlineCode' && inlineFileReference(child.value ?? '')) return { type: 'link', url: child.value!, children: [child] }
      visit(child)
      return child
    })
  }
  return (tree: Node) => visit(tree)
}
