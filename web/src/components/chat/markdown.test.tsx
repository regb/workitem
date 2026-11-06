import { fireEvent, render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { AttachmentLinksContext } from './attachment-context'
import { Markdown } from './markdown'

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

describe('item mentions', () => {
  it('shows a sent mention as a highlighted @@Title link to the item', () => {
    const { container } = render(<Markdown text={'look at @@[Fix \\[the\\] login](wi:01J0ABCDEFGHJKMNPQRSTVWXYZ "fix-login") first'} />)
    const link = container.querySelector('a')
    expect(link?.textContent).toBe('@@Fix [the] login')
    expect(link?.getAttribute('href')).toBe('/items/01J0ABCDEFGHJKMNPQRSTVWXYZ')
    expect(container.textContent).toBe('look at @@Fix [the] login first')
  })
})

describe('attachment links', () => {
  const linked = (text: string, names = ['REPORT.md', 'shot.png']) => {
    const open = vi.fn()
    const { container } = render(
      <AttachmentLinksContext value={{ names, open }}>
        <Markdown text={text} />
      </AttachmentLinksContext>,
    )
    return { links: [...container.querySelectorAll('[data-attachment-link]')].map(link => `${link.getAttribute('data-attachment-link')}=${link.textContent}`), container, open }
  }

  it('links attachment names and paths ending in one, in text and inline code', () => {
    expect(linked('Attached `REPORT.md`. See /tmp/REPORT.md and shot.png, or `out/shot.png`.').links).toEqual([
      'REPORT.md=REPORT.md',
      'REPORT.md=/tmp/REPORT.md',
      'shot.png=shot.png',
      'shot.png=out/shot.png',
    ])
  })

  it('leaves longer names, code blocks, links and other code alone', () => {
    const text = 'OLD_REPORT.md, REPORT.mdx, REPORT.md.bak, [REPORT.md](https://example.com), `cat REPORT.md`\n\n```\nREPORT.md\n```'
    expect(linked(text).links).toEqual([])
  })

  it('opens the attachment it names, and links nothing without attachments', () => {
    const { container, open } = linked('Attached `REPORT.md`.')
    fireEvent.click(container.querySelector('[data-attachment-link]')!)
    expect(open).toHaveBeenCalledWith('REPORT.md')
    expect(linked('Attached `REPORT.md`.', []).links).toEqual([])
  })
})
