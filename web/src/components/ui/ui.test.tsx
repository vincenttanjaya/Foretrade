import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { AssetIcon } from './AssetIcon'
import { Badge } from './Badge'
import { Button, buttonClass } from './Button'
import { Card, CardHeader } from './Card'
import { Disclaimer } from './Disclaimer'
import { EmptyState } from './EmptyState'
import * as icons from './icons'
import { Input, Label, Select, Textarea } from './Input'
import { LogoMark } from './Logo'
import { PriceChange } from './PriceChange'
import { ProbabilityBars } from './ProbabilityBars'
import { Sparkline } from './Sparkline'
import { FullPageLoader, Skeleton, Spinner } from './Spinner'
import { Stat } from './Stat'
import { Tabs } from './Tabs'

describe('ProbabilityBars', () => {
  const items = [
    { label: 'Below $80k', p: 0.15 },
    { label: '$80k – $88k', p: 0.62 },
    { label: 'Above $88k', p: 0.23 },
  ]

  it('renders label, bar and percentage per outcome', () => {
    render(<ProbabilityBars items={items} />)
    const rows = screen.getAllByRole('listitem')
    expect(rows.map((r) => r.textContent)).toEqual(['Below $80k15%', '$80k – $88k62%', 'Above $88k23%'])
    expect(screen.getByTitle('$80k – $88k')).toBeInTheDocument() // long labels are truncated with a tooltip
  })

  it('highlights only the most likely outcome in gold', () => {
    render(<ProbabilityBars items={items} />)
    expect(screen.getByText('62%')).toHaveClass('text-accent-ink', 'font-semibold')
    expect(screen.getByText('15%')).toHaveClass('text-muted')
    expect(screen.getByText('15%')).not.toHaveClass('text-accent-ink')
  })

  it('clamps bar widths to 0–100%', () => {
    const { container } = render(
      <ProbabilityBars
        items={[
          { label: 'a', p: 1.4 },
          { label: 'b', p: -0.2 },
        ]}
      />,
    )
    const bars = [...container.querySelectorAll<HTMLElement>('.animate-bar')]
    expect(bars.map((b) => b.style.width)).toEqual(['100%', '0%'])
  })

  it('has a compact variant', () => {
    render(<ProbabilityBars compact items={items} className="extra" />)
    expect(screen.getByRole('list')).toHaveClass('gap-1.5', 'extra')
  })

  it('adds a hint line and a baseline tick, named for screen readers, only to items that carry them', () => {
    const { container } = render(
      <ProbabilityBars
        baselineLabel="volatility-only baseline"
        items={[
          { label: 'Below $80k', p: 0.15, hint: 'below -5%', baseline: 0.25 },
          { label: 'Above $80k', p: 0.85, baseline: 0.75 },
          { label: 'Other', p: 0 },
        ]}
      />,
    )
    expect(screen.getAllByRole('listitem').map((r) => r.textContent)).toEqual([
      'Below $80kbelow -5%15%, volatility-only baseline 25%',
      'Above $80k85%, volatility-only baseline 75%',
      'Other0%',
    ])
    expect([...container.querySelectorAll<HTMLElement>('[data-baseline]')].map((t) => t.style.left)).toEqual(['25%', '75%'])
  })

  it('draws relative widths against the largest share, bar or baseline', () => {
    const { container } = render(
      <ProbabilityBars
        relative
        items={[
          { label: 'a', p: 0.2, baseline: 0.4 },
          { label: 'b', p: 0.3, baseline: 0.1 },
        ]}
      />,
    )
    expect([...container.querySelectorAll<HTMLElement>('.animate-bar')].map((b) => b.style.width)).toEqual(['50%', '75%'])
    expect([...container.querySelectorAll<HTMLElement>('[data-baseline]')].map((t) => t.style.left)).toEqual(['99.5%', '25%'])
  })
})

describe('PriceChange', () => {
  it('shows gains with ▲ in green and a screen-reader word', () => {
    render(<PriceChange value={2.314} />)
    const el = screen.getByText('2.31%', { exact: false }).closest('span.font-mono')!
    expect(el).toHaveClass('text-up')
    expect(el).toHaveTextContent('▲up 2.31%')
  })

  it('shows losses with ▼ in red', () => {
    render(<PriceChange value={-1.02} />)
    const el = screen.getByText(/1\.02%/).closest('span.font-mono')!
    expect(el).toHaveClass('text-down')
    expect(el).toHaveTextContent('▼down 1.02%')
  })

  it('treats a move that rounds to zero as flat', () => {
    render(<PriceChange value={-0.004} />)
    const el = screen.getByText('0.00%').closest('span.font-mono')!
    expect(el).toHaveClass('text-muted')
    expect(el.textContent).toBe('0.00%')
  })

  it('formats USD changes and shows a dash for missing values', () => {
    const { rerender } = render(<PriceChange value={-250.5} kind="usd" />)
    expect(screen.getByText(/\$250\.50/)).toBeInTheDocument()
    rerender(<PriceChange value={null} />)
    expect(screen.getByText('—')).toBeInTheDocument()
    rerender(<PriceChange value={1.26} decimals={1} />)
    expect(screen.getByText(/1\.3%/)).toBeInTheDocument()
  })
})

describe('Tabs', () => {
  it('marks the active tab and reports clicks with the value', async () => {
    const onChange = vi.fn()
    render(<Tabs<string> value="1h" onChange={onChange} items={['15m', '1h', { value: '4h', label: 'Four hours' }]} aria-label="Interval" />)
    expect(screen.getByRole('group', { name: 'Interval' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '1h' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: '15m' })).toHaveAttribute('aria-pressed', 'false')
    await userEvent.click(screen.getByRole('button', { name: 'Four hours' }))
    expect(onChange).toHaveBeenCalledWith('4h')
  })

  it('has a small size', () => {
    render(<Tabs size="sm" value="a" onChange={() => {}} items={['a']} />)
    expect(screen.getByRole('button', { name: 'a' })).toHaveClass('h-7')
  })
})

describe('Button', () => {
  it('is a type=button by default and forwards clicks', async () => {
    const onClick = vi.fn()
    render(<Button onClick={onClick}>Save</Button>)
    const b = screen.getByRole('button', { name: 'Save' })
    expect(b).toHaveAttribute('type', 'button')
    expect(b).toHaveClass('bg-ink', 'h-10') // primary, md
    await userEvent.click(b)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('is disabled and busy while loading, with a spinner', async () => {
    const onClick = vi.fn()
    const { container } = render(
      <Button loading onClick={onClick}>
        Saving
      </Button>,
    )
    const b = screen.getByRole('button', { name: 'Saving' })
    expect(b).toBeDisabled()
    expect(b).toHaveAttribute('aria-busy', 'true')
    expect(container.querySelector('.animate-spin')).not.toBeNull()
    await userEvent.click(b)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('supports disabled, variants, sizes and submit', () => {
    render(
      <>
        <Button disabled>Off</Button>
        <Button variant="danger" size="sm" type="submit">
          Delete
        </Button>
      </>,
    )
    expect(screen.getByRole('button', { name: 'Off' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Off' })).not.toHaveAttribute('aria-busy')
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveAttribute('type', 'submit')
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveClass('text-down', 'h-8')
  })

  it('buttonClass styles links as buttons', () => {
    expect(buttonClass('accent', 'lg', 'w-full')).toMatch(/bg-accent.*h-12.*w-full/)
    expect(buttonClass()).toContain('bg-ink')
  })
})

describe('Input, Textarea, Select, Label', () => {
  it('Input forwards props and labels', async () => {
    const onChange = vi.fn()
    render(
      <>
        <Label htmlFor="amt">Amount</Label>
        <Input id="amt" placeholder="0.00" onChange={onChange} aria-invalid className="font-mono" />
      </>,
    )
    const input = screen.getByLabelText('Amount')
    expect(input).toHaveClass('h-10', 'font-mono')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    await userEvent.type(input, '12')
    expect(onChange).toHaveBeenCalledTimes(2)
  })

  it('Textarea grows to its content and forwards refs and onInput', async () => {
    const objRef = createRef<HTMLTextAreaElement>()
    const fnRef = vi.fn()
    const onInput = vi.fn()
    const { rerender } = render(<Textarea aria-label="Idea" ref={objRef} value="" onChange={() => {}} onInput={onInput} />)
    const box = screen.getByLabelText('Idea') as HTMLTextAreaElement
    expect(objRef.current).toBe(box)
    expect(box).toHaveAttribute('rows', '3')

    Object.defineProperty(box, 'scrollHeight', { configurable: true, value: 120 })
    rerender(<Textarea aria-label="Idea" ref={objRef} value={'a\nb\nc'} onChange={() => {}} onInput={onInput} />)
    expect(box.style.height).toBe('120px')

    Object.defineProperty(box, 'scrollHeight', { configurable: true, value: 180 })
    await userEvent.type(box, 'x')
    expect(onInput).toHaveBeenCalled()
    expect(box.style.height).toBe('180px')

    rerender(<Textarea aria-label="Idea" ref={fnRef} value="" onChange={() => {}} />)
    expect(fnRef).toHaveBeenCalledWith(box)
  })

  it('Select renders its options with the chevron', () => {
    render(
      <Select aria-label="Market" defaultValue="ETH" style={{ color: 'red' }}>
        <option value="BTC">BTC</option>
        <option value="ETH">ETH</option>
      </Select>,
    )
    const select = screen.getByRole('combobox', { name: 'Market' }) as HTMLSelectElement
    expect(select.value).toBe('ETH')
    expect(select.style.backgroundImage).toContain('data:image/svg+xml')
    expect(select.style.color).toBe('red')
  })
})

describe('Stat', () => {
  it('shows label, value and sub line, coloured by tone', () => {
    const { rerender } = render(<Stat label="Cash" value="$94,000.00" sub="available" />)
    expect(screen.getByText('Cash')).toHaveClass('label')
    expect(screen.getByText('$94,000.00')).toHaveClass('text-text')
    expect(screen.getByText('available')).toBeInTheDocument()
    for (const [tone, cls] of [
      ['up', 'text-up'],
      ['down', 'text-down'],
      ['accent', 'text-accent-ink'],
    ] as const) {
      rerender(<Stat label="P&L" value="x" tone={tone} />)
      expect(screen.getByText('x')).toHaveClass(cls)
    }
    expect(screen.queryByText('available')).toBeNull()
  })
})

describe('Sparkline', () => {
  it('draws nothing under two points', () => {
    const { container } = render(<Sparkline values={[1]} />)
    expect(container.querySelector('polyline')).toBeNull()
  })

  it('is green when the series ends higher and red when lower', () => {
    const { container, rerender } = render(<Sparkline values={[100, 90, 120]} width={100} height={20} />)
    const line = () => container.querySelector('polyline')!
    expect(line()).toHaveAttribute('stroke', 'var(--up)')
    expect(line().getAttribute('points')).toBe('0.0,12.7 50.0,18.0 100.0,2.0')
    rerender(<Sparkline values={[120, 100]} />)
    expect(line()).toHaveAttribute('stroke', 'var(--down)')
  })

  it('handles a flat series without NaN', () => {
    const { container } = render(<Sparkline values={[5, 5, 5]} />)
    expect(container.querySelector('polyline')!.getAttribute('points')).not.toContain('NaN')
  })
})

describe('small pieces', () => {
  it('Badge, Card, CardHeader, EmptyState and Disclaimer render their content', () => {
    render(
      <Card className="p-5" data-testid="card">
        <CardHeader label="Open positions" title="2 positions" actions={<button>Refresh</button>} />
        <Badge tone="up">Supports</Badge>
        <EmptyState icon={<span>i</span>} title="No trades yet" description="Fills land here." action={<button>Trade</button>} />
        <Disclaimer />
      </Card>,
    )
    expect(screen.getByTestId('card')).toHaveClass('card', 'p-5')
    expect(screen.getByRole('heading', { name: '2 positions' })).toBeInTheDocument()
    expect(screen.getByText('Open positions')).toHaveClass('label')
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeInTheDocument()
    expect(screen.getByText('Supports')).toHaveClass('text-up')
    expect(screen.getByRole('heading', { name: 'No trades yet' })).toBeInTheDocument()
    expect(screen.getByText('Fills land here.')).toBeInTheDocument()
    expect(screen.getByText('Simulated · not financial advice')).toBeInTheDocument()
  })

  it('EmptyState works with just a title', () => {
    render(<EmptyState title="Nothing" />)
    expect(screen.getByRole('heading', { name: 'Nothing' })).toBeInTheDocument()
  })

  it('AssetIcon draws bundled logos, and a neutral chip for unknown symbols', () => {
    const { container } = render(
      <>
        <AssetIcon symbol="BTC" />
        <AssetIcon symbol="SOL" size={20} />
        <AssetIcon symbol="ZZZ" />
      </>,
    )
    const [btc, sol, unknown] = container.querySelectorAll<HTMLElement>('[aria-hidden]')
    expect(btc.getAttribute('src')).toMatch(/\/btc\.svg\b/)
    expect(btc.style.width).toBe('28px')
    expect(sol.getAttribute('src')).toMatch(/\/sol\.svg\b/)
    expect(sol.style.width).toBe('20px')
    expect(unknown.textContent).toBe('Z')
    expect(unknown).toHaveClass('bg-ink')
  })

  it('Spinner announces itself only with a label; Skeleton is hidden from assistive tech', () => {
    const { container } = render(
      <>
        <Spinner />
        <Spinner label="Loading candles" />
        <Skeleton className="h-4" />
      </>,
    )
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByRole('status')).toHaveTextContent('Loading candles')
    expect(container.querySelector('.h-4')).toHaveAttribute('aria-hidden', 'true')
  })

  it('FullPageLoader shows the logo and a loading status', () => {
    const { container } = render(<FullPageLoader />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading')
    expect(container.querySelector('svg rect')).not.toBeNull()
  })

  it('LogoMark sizes itself', () => {
    const { container } = render(<LogoMark size={40} className="x" />)
    expect(container.querySelector('svg')).toHaveAttribute('width', '40')
  })

  it('every icon renders a decorative 24px-grid svg', () => {
    for (const [name, Icon] of Object.entries(icons)) {
      const { container, unmount } = render(<Icon size={16} />)
      const svg = container.querySelector('svg')
      expect(svg, name).toHaveAttribute('aria-hidden', 'true')
      expect(svg, name).toHaveAttribute('viewBox', '0 0 24 24')
      expect(svg, name).toHaveAttribute('width', '16')
      unmount()
    }
  })
})
