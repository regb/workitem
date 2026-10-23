import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Markdown } from './markdown'
import { toolSummary } from './tool-call'

describe('Markdown', () => {
  it('renders agent HTML as nothing, not markup', () => {
    const { container } = render(<Markdown text={'<img src=x onerror="alert(1)"> **bold** [link](javascript:alert(1))'} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('strong')?.textContent).toBe('bold')
    expect(container.querySelector('a')?.getAttribute('href') ?? '').not.toContain('javascript')
  })

  it('renders tables and only safe images', () => {
    const { container } = render(<Markdown text={'| a | b |\n|---|---|\n| 1 | 2 |\n\n![ok](https://example.test/x.png) ![no](http://example.test/y.png)'} />)
    expect(container.querySelectorAll('td')).toHaveLength(2)
    expect([...container.querySelectorAll('img')].map(image => image.getAttribute('src'))).toEqual(['https://example.test/x.png'])
  })
})

describe('toolSummary', () => {
  it('uses the command or path from JSON input', () => {
    expect(toolSummary({ name: 'bash', status: 'completed', input: '{"command":"git status\\n--short"}' })).toBe('git status')
    expect(toolSummary({ name: 'read', status: 'completed', input: '{"path":"README.md"}' })).toBe('README.md')
    expect(toolSummary({ name: 'x', status: 'completed', input: 'not json' })).toBe('')
  })
})

describe('item mentions', () => {
  it('shows a sent mention as a highlighted @@Title link to the item', () => {
    const { container } = render(<Markdown text={'look at @@[Fix \\[the\\] login](wi:01J0ABCDEFGHJKMNPQRSTVWXYZ "fix-login") first'} />)
    const link = container.querySelector('a')
    expect(link?.textContent).toBe('@@Fix [the] login')
    expect(link?.getAttribute('href')).toBe('/items/01J0ABCDEFGHJKMNPQRSTVWXYZ')
    expect(container.textContent).toBe('look at @@Fix [the] login first')
  })
})
