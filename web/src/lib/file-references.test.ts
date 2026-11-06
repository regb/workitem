import { describe, expect, it } from 'vitest'
import { inlineFileReference, lookupFileReference, parseFileReference, referenceExists, workspaceFilePath } from './file-references'

describe('inline file recognition', () => {
  it.each([
    ['src/auth.ts', { path: 'src/auth.ts' }],
    ['src/auth.ts:42', { path: 'src/auth.ts', line: 42 }],
    ['auth.ts:42:3', { path: 'auth.ts', line: 42, column: 3 }],
    ['./Dockerfile', { path: './Dockerfile' }],
    ['src\\auth.ts:42', { path: 'src/auth.ts', line: 42 }],
    ['C:\\repo\\src\\auth.ts:42', { path: 'C:/repo/src/auth.ts', line: 42 }],
    ['/repo/src/auth.ts', { path: '/repo/src/auth.ts' }],
    ['~/repo/auth.ts', { path: '~/repo/auth.ts' }],
    ['資料/認証.ts', { path: '資料/認証.ts' }],
    ['share/man/ls.1', { path: 'share/man/ls.1' }],
  ])('recognises %s', (input, reference) => expect(inlineFileReference(input)).toEqual(reference))

  it.each(['\\n', '\\t', '\\"', '\\r\\n', '\\u0041', '', 'auth.ts', 'src/', './src/', 'npm install', 'src/my file.ts', 'example.com/path', 'localhost/path.ts', '127.0.0.1/a.ts', 'origin/main', 'z-ai/glm-5.3', 'src/v1.2.3', 'https://example.com/a.ts', 'javascript:alert(1)', 'src/auth.ts`'])('leaves %s as code', input => expect(inlineFileReference(input)).toBeNull())
})

describe('explicit Markdown file references', () => {
  it.each([
    ['auth.ts', { path: 'auth.ts' }],
    ['src/auth.ts#L42C3', { path: 'src/auth.ts', line: 42, column: 3 }],
    ['src/auth.ts:42:3', { path: 'src/auth.ts', line: 42, column: 3 }],
    ['src/auth.ts:42:3#L5C7', { path: 'src/auth.ts', line: 42, column: 3 }],
    ['src/auth.ts#L42-L48', { path: 'src/auth.ts', line: 42, endLine: 48 }],
    ['src/auth.ts?raw=1#L42', { path: 'src/auth.ts', line: 42 }],
    ['docs/My%20Folder/', { path: 'docs/My Folder/' }],
    ['file:///repo/src/auth.ts#L42', { path: '/repo/src/auth.ts', line: 42 }],
    ['file://localhost/repo/a.ts', { path: '/repo/a.ts' }],
    ['file://server/share/a.ts', { path: '//server/share/a.ts' }],
    ['<C:%5Crepo%5Ca.ts:2>', { path: 'C:/repo/a.ts', line: 2 }],
    ['Dockerfile', { path: 'Dockerfile' }],
    ['\\\\server\\share\\a.ts', { path: '//server/share/a.ts' }],
    ['docs/', { path: 'docs/' }],
  ])('parses %s', (href, expected) => expect(parseFileReference(href)).toEqual(expected))

  it.each(['#L42', '#heading', 'https://example.com/a.ts', '//example.com/a.ts', 'mailto:hello@example.com', 'javascript:alert(1)', 'data:text/html,hello', 'wi:01M6KCHC5WTQ13SJZZ2EBM144C', 'TODO:12', 'src/auth.ts:0', 'src/auth.ts#L999999999999999', 'src/a%00.ts', '\\n', '%5Cn', '\\docs\\a.ts', 'src/a%0A.ts', 'src/%GG.ts', 'file://user:pass@server/a.ts'])('rejects %s', href => expect(parseFileReference(href)).toBeNull())
})

describe('workspace resolution', () => {
  it.each([
    ['src/auth.ts', '/repo', 'src/auth.ts'],
    ['./src/../auth.ts', '/repo', 'auth.ts'],
    ['/repo/src/auth.ts', '/repo', 'src/auth.ts'],
    ['~/repo/src/auth.ts', '/home/me/repo', 'src/auth.ts'],
    ['C:\\Repo\\src\\auth.ts', 'c:/repo', 'src/auth.ts'],
    ['//server/share/repo/a.ts', '//server/share/repo', 'a.ts'],
    ['./', '/repo', '.'],
    ['/etc/passwd', '/repo', null],
    ['/repository/a.ts', '/repo', null],
    ['../../secret', '/repo', null],
    ['../secret', '/repo/worktree', null],
    ['~/secret', '/home/me/repo', null],
    ['//other/share/repo/a.ts', '//server/share/repo', null],
    ['//server/other/a.ts', '//server/share/repo', null],
    ['src/.git/config', '/repo', null],
    ['src/.GIT/config', '/repo', null],
  ])('scopes %s against %s', (path, workspace, expected) => expect(workspaceFilePath(path, workspace)).toBe(expected))

  it('uses exact paths before abbreviated matches and does not guess ambiguous names', () => {
    const paths = ['src/auth.ts', 'tests/auth.ts', 'src/only.ts']
    expect(lookupFileReference({ path: 'src/auth.ts', line: 42 }, '/repo', paths)).toEqual([{ path: 'src/auth.ts', line: 42, directory: false }])
    expect(lookupFileReference({ path: 'only.ts', line: 2 }, '/repo', paths)).toEqual([{ path: 'src/only.ts', line: 2, directory: false }])
    expect(lookupFileReference({ path: 'auth.ts' }, '/repo', paths).map(p => p.path)).toEqual(['src/auth.ts', 'tests/auth.ts'])
    expect(lookupFileReference({ path: '/repo/only.ts' }, '/repo', paths)[0].path).toBe('only.ts')
    expect(lookupFileReference({ path: './only.ts' }, '/repo', paths)[0].path).toBe('only.ts')
  })

  it('does not treat an abbreviated match in a truncated listing as unique', () => {
    expect(() => lookupFileReference({ path: 'auth.ts' }, '/repo', ['src/auth.ts'], true)).toThrow('truncated')
    expect(lookupFileReference({ path: './auth.ts' }, '/repo', ['src/auth.ts'], true)[0].path).toBe('auth.ts')
  })

  it('identifies directories and lets the source API handle missing exact files', () => {
    expect(lookupFileReference({ path: 'src/' }, '/repo', ['src/auth.ts'])).toEqual([{ path: 'src', directory: true }])
    expect(lookupFileReference({ path: 'missing.ts' }, '/repo', [])[0]).toEqual({ path: 'missing.ts', directory: false })
    expect(() => lookupFileReference({ path: '/etc/passwd' }, '/repo', [])).toThrow('outside')
  })
})

describe('referenceExists', () => {
  const paths = ['src/auth.ts', 'src/lib/only.ts', 'docs/guide.md']
  it('finds files, directories and abbreviated names as opening would', () => {
    const exists = (path: string) => referenceExists({ path }, '/repo', paths)
    expect(['src/auth.ts', '/repo/src/auth.ts', './docs/guide.md', 'src/lib', 'lib/only.ts', 'only.ts'].map(exists)).toEqual([true, true, true, true, true, true])
    // Absent names, rooted paths elsewhere, and rooted names that only end
    // like a file are not found.
    expect(['src/missing.ts', '/v1/cities', './only.ts', '/repo/missing.md'].map(exists)).toEqual([false, false, false, false])
  })
})
