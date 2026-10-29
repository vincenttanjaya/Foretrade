import { screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../../lib/api'
import type { ForecastDraft } from '../../lib/types'
import * as fx from '../../test/fixtures'
import { creditsUsed, currentPath, renderPage, stubSession } from '../../test/utils'
import { NewForecastPage } from './NewForecastPage'

const draft: ForecastDraft = {
  shape: 'binary',
  question: 'Will BTC trade above $150k before July 2027?',
  outcomes: ['Yes', 'No'],
  resolutionCriteria: 'Resolves Yes if the Binance BTCUSDT price trades above $150,000 before Jul 1, 2027.',
  resolutionDate: '2027-07-01',
  category: 'crypto',
  priceRule: null,
  context: '',
}

const open = (state?: unknown) =>
  renderPage(<NewForecastPage />, { route: '/forecasts/new', path: '/forecasts/new', state, routes: { '/forecasts/:id': <p>detail page</p> } })

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] }) // a fixed "today" keeps the resolution dates in the future
  vi.setSystemTime(new Date(2026, 9, 2, 12))
  stubSession()
})
afterEach(() => vi.useRealTimers())

describe('NewForecastPage · idea step', () => {
  it('drafts a question from a rough idea', async () => {
    const ask = vi.spyOn(api, 'draftForecast').mockResolvedValue({ draft, credits: { ...fx.credits, used: 19 } })
    const { user } = open()
    expect(screen.getByRole('heading', { name: 'Point it at the future.' })).toBeInTheDocument()
    const box = screen.getByLabelText('Your rough idea')
    const go = screen.getByRole('button', { name: /Draft with AI/ })
    expect(go).toBeDisabled()
    await user.type(box, '  btc 150k ')
    expect(screen.getByText('11/400')).toBeInTheDocument()
    await user.click(go)
    expect(ask).toHaveBeenCalledWith('btc 150k')
    expect(await screen.findByRole('heading', { name: 'Review the question.' })).toBeInTheDocument()
    expect(screen.getByText('Forecast desk · Drafted by the desk')).toBeInTheDocument()
    expect(screen.getByText('“btc 150k”')).toBeInTheDocument()
    expect(screen.getByLabelText('Question')).toHaveValue(draft.question)
    expect(creditsUsed()).toBe('19')
  })

  it("shows how far each range of a long-horizon stock draft is from today's level", async () => {
    const tsm: ForecastDraft = {
      ...draft,
      shape: 'ordered',
      question: 'What will be the NYSE closing price of the TSM ADR on 2031-10-06?',
      outcomes: fx.tsmDraftBuckets,
      resolutionDate: '2031-10-06',
      category: 'markets',
    }
    vi.spyOn(api, 'draftForecast').mockResolvedValue({ draft: tsm, reference: fx.tsmDraftReference, credits: fx.credits })
    const { user } = open()
    await user.type(screen.getByLabelText('Your rough idea'), 'Where will TSM trade in 5 years?')
    await user.click(screen.getByRole('button', { name: /Draft with AI/ }))
    expect(await screen.findByText("Ranges are multiples of today's $485.12 (Yahoo, Oct 6).")).toBeInTheDocument()
    const chips = () => screen.queryAllByText(/vs today$/).map((c) => c.textContent)
    expect(chips()).toEqual([
      'below -48% vs today',
      '-48%…-26% vs today',
      '-26%…0% vs today',
      '0%…+55% vs today',
      '+55%…+106% vs today',
      '+106%…+209% vs today',
      'above +209% vs today',
    ])
    // A label that no longer parses drops the chips (and the note) until it does again.
    await user.clear(screen.getByLabelText('Outcome 4'))
    await user.type(screen.getByLabelText('Outcome 4'), 'somewhere in the middle')
    expect(chips()).toEqual([])
    expect(screen.queryByText(/Ranges are multiples of today/)).not.toBeInTheDocument()
  })

  it('shows no % chips without a measured level', async () => {
    vi.spyOn(api, 'draftForecast').mockResolvedValue({ draft: { ...draft, shape: 'ordered', outcomes: fx.tsmDraftBuckets }, reference: null, credits: fx.credits })
    const { user } = open()
    await user.type(screen.getByLabelText('Your rough idea'), 'TSM later')
    await user.click(screen.getByRole('button', { name: /Draft with AI/ }))
    await screen.findByRole('heading', { name: 'Review the question.' })
    expect(screen.queryByText(/vs today$/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Ranges are multiples of today/)).not.toBeInTheDocument()
  })

  it('drafts with ⌘/Ctrl+Enter and shows errors, re-reading credits on quota', async () => {
    vi.spyOn(api, 'draftForecast').mockRejectedValue(new ApiError(429, 'ai_quota', 'quota'))
    const { user } = open()
    await user.type(screen.getByLabelText('Your rough idea'), 'fed cuts')
    const calls = vi.mocked(api.me).mock.calls.length
    await user.keyboard('{Control>}{Enter}{/Control}')
    expect(await screen.findByRole('alert')).toHaveTextContent('Daily AI credits used up. They reset at 00:00 UTC.')
    expect(vi.mocked(api.me).mock.calls.length).toBe(calls + 1)
  })

  it('fills the idea from an example card and from router state', async () => {
    const { user, unmount } = open()
    await user.click(screen.getByRole('button', { name: /Where will BTC close on Dec 31 2026\?/ }))
    expect(screen.getByLabelText('Your rough idea')).toHaveValue('Where will BTC close on Dec 31 2026?')
    unmount()
    open({ idea: 'From the dashboard' })
    expect(screen.getByLabelText('Your rough idea')).toHaveValue('From the dashboard')
  })

  it('offers a BTC close five days out, inside the week Polymarket lists daily ladders for', async () => {
    const { user, unmount } = open() // Fri Oct 2, 2026
    await user.click(screen.getByRole('button', { name: /Where will BTC close on Wed Oct 7\?/ }))
    expect(screen.getByLabelText('Your rough idea')).toHaveValue('Where will BTC close on Wed Oct 7?')
    unmount()
    vi.setSystemTime(new Date(2026, 11, 29, 23, 30))
    open()
    expect(screen.getByRole('button', { name: /Where will BTC close on Sun Jan 3\?/ })).toBeInTheDocument()
  })

  it('can skip the AI and write it by hand', async () => {
    const { user } = open()
    await user.type(screen.getByLabelText('Your rough idea'), 'Will SOL flip ETH?')
    await user.click(screen.getByRole('button', { name: 'Create manually' }))
    expect(screen.getByText('Forecast desk · Manual draft')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Choose the shape.' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Question')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Continue to Ask' }))
    expect(screen.getByLabelText('Question')).toHaveValue('Will SOL flip ETH?')
    expect(screen.queryByText('Your idea')).toBeNull()
    await user.click(screen.getByRole('button', { name: /Back to your idea/ }))
    expect(screen.getByLabelText('Your rough idea')).toHaveValue('Will SOL flip ETH?')
  })
})

// These journeys type every field key by key: about 3 s alone, but past the 15 s default under a loaded coverage run.
describe('NewForecastPage · manual steps', { timeout: 30_000 }, () => {
  it('guides a manual forecast through valid steps and runs only after reviewing the complete draft', async () => {
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'manual9', status: 'running' }), credits: { ...fx.credits, used: 23 } })
    const { user } = open()
    await user.click(screen.getByRole('button', { name: 'Create manually' }))
    const progress = screen.getByRole('navigation', { name: 'Forecast steps' })
    expect(within(progress).getByRole('button', { name: /Shape/ })).toHaveAttribute('aria-current', 'step')
    expect(within(progress).getByRole('button', { name: /Review/ })).toBeDisabled()
    expect(screen.queryByRole('button', { name: /Run forecast/ })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Continue to Ask' }))
    expect(screen.getByRole('heading', { level: 1, name: 'Ask a clear question.' })).toHaveFocus()
    expect(screen.getByRole('button', { name: 'Continue to Resolve' })).toBeDisabled()
    await user.type(screen.getByLabelText('Question'), draft.question)
    await user.click(screen.getByRole('button', { name: 'Continue to Resolve' }))
    expect(screen.getByRole('button', { name: 'Continue to Review' })).toBeDisabled()
    await user.type(screen.getByLabelText('Resolution date'), draft.resolutionDate)
    expect(screen.getByRole('button', { name: 'Continue to Review' })).toBeDisabled()
    await user.type(screen.getByLabelText('Resolution criteria'), draft.resolutionCriteria)
    await user.selectOptions(screen.getByLabelText('Category'), 'crypto')
    await user.click(screen.getByRole('button', { name: 'Add context for the desk (optional)' }))
    await user.type(screen.getByLabelText('Context'), '  Compare the current cycle with 2021.  ')
    expect(screen.queryByRole('button', { name: /Run forecast/ })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Continue to Review' }))

    expect(screen.getByRole('heading', { level: 1, name: 'Review your forecast.' })).toHaveFocus()
    expect(screen.getByText(draft.question)).toBeInTheDocument()
    expect(screen.getByText(draft.resolutionCriteria)).toBeInTheDocument()
    expect(screen.getByText('Compare the current cycle with 2021.')).toBeInTheDocument()
    expect(screen.getByText('Crypto')).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(create).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: /Run forecast.*5 cr/ }))
    expect(create).toHaveBeenCalledWith({ ...draft, context: 'Compare the current cycle with 2021.' })
    expect(await screen.findByText('detail page')).toBeInTheDocument()
    expect(currentPath()).toBe('/forecasts/manual9')
    expect(creditsUsed()).toBe('23')
  })

  it('retains the draft across previous steps and shape changes, and blocks duplicate outcomes and past dates', async () => {
    const { user } = open({ idea: 'Where will BTC close on Dec 31 2026?' })
    await user.click(screen.getByRole('button', { name: 'Create manually' }))
    await user.click(screen.getByRole('radio', { name: /Ordered/ }))
    await user.click(screen.getByRole('button', { name: 'Continue to Ask' }))
    expect(screen.getByRole('button', { name: 'Continue to Resolve' })).toBeDisabled()
    await user.type(screen.getByLabelText('Outcome 1'), 'Below $80k')
    await user.type(screen.getByLabelText('Outcome 2'), '$80k to $100k')
    await user.type(screen.getByLabelText('Outcome 3'), 'below $80k')
    expect(screen.getByText('Each outcome must be different.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Continue to Resolve' })).toBeDisabled()
    await user.clear(screen.getByLabelText('Outcome 3'))
    await user.type(screen.getByLabelText('Outcome 3'), 'Above $100k')
    await user.click(screen.getByRole('button', { name: 'Continue to Resolve' }))
    await user.type(screen.getByLabelText('Resolution criteria'), 'Per the Binance daily closing price.')
    await user.type(screen.getByLabelText('Resolution date'), '2026-10-01')
    expect(screen.getByRole('button', { name: 'Continue to Review' })).toBeDisabled()
    await user.clear(screen.getByLabelText('Resolution date'))
    await user.type(screen.getByLabelText('Resolution date'), '2026-12-31')
    await user.click(screen.getByRole('button', { name: 'Continue to Review' }))
    expect(screen.getByText('Below $80k')).toBeInTheDocument()

    await user.click(within(screen.getByRole('navigation', { name: 'Forecast steps' })).getByRole('button', { name: /Shape/ }))
    await user.click(screen.getByRole('radio', { name: /Binary/ }))
    await user.click(screen.getByRole('radio', { name: /Ordered/ }))
    await user.click(screen.getByRole('button', { name: 'Continue to Ask' }))
    expect(screen.getByLabelText('Question')).toHaveValue('Where will BTC close on Dec 31 2026?')
    expect(screen.getByLabelText('Outcome 1')).toHaveValue('Below $80k')
    expect(screen.getByLabelText('Outcome 2')).toHaveValue('$80k to $100k')
    expect(screen.getByLabelText('Outcome 3')).toHaveValue('Above $100k')
    await user.click(screen.getByRole('button', { name: 'Continue to Resolve' }))
    expect(screen.getByLabelText('Resolution criteria')).toHaveValue('Per the Binance daily closing price.')
    expect(screen.getByLabelText('Resolution date')).toHaveValue('2026-12-31')
    await user.click(screen.getByRole('button', { name: 'Back to Ask' }))
    await user.clear(screen.getByLabelText('Question'))
    expect(screen.getByRole('button', { name: 'Continue to Resolve' })).toBeDisabled()
    expect(within(screen.getByRole('navigation', { name: 'Forecast steps' })).getByRole('button', { name: /Review/ })).toBeDisabled()
  })
})

describe('NewForecastPage · review step', () => {
  async function review(d: Partial<ForecastDraft> = {}) {
    const r = open({ draft: { ...draft, ...d } })
    await screen.findByRole('heading', { name: 'Review the question.' })
    return r
  }

  it('opens a draft the desk wrote in chat as drafted by the desk, not as an edit', async () => {
    open({ draft, origin: 'ai' })
    expect(await screen.findByText('Forecast desk · Drafted by the desk')).toBeInTheDocument()
    expect(screen.getByLabelText('Question', { exact: true })).toHaveValue(draft.question)
  })

  it('runs the forecast and opens it', async () => {
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f9', status: 'running' }), credits: { ...fx.credits, used: 23 } })
    const { user } = await review({ question: `  ${draft.question}  ` })
    expect(screen.getByText('Forecast desk · Edit and run again')).toBeInTheDocument()
    expect(screen.getByText('4/4')).toBeInTheDocument() // quality checklist
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(create).toHaveBeenCalledWith(draft) // trimmed
    expect(await screen.findByText('detail page')).toBeInTheDocument()
    expect(currentPath()).toBe('/forecasts/f9')
    expect(creditsUsed()).toBe('23')
    expect(screen.getByText('Forecast is running. The specialists are on it.')).toBeInTheDocument()
  })

  it('shows an AI price rule as an auto-resolve chip that can be removed', async () => {
    const rule = { symbol: 'BTC', op: 'above', threshold: 150000, mode: 'by_date' } as const
    vi.spyOn(api, 'draftForecast').mockResolvedValue({ draft: { ...draft, priceRule: rule }, credits: fx.credits })
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f9', status: 'running' }), credits: fx.credits })
    const { user } = open()
    await user.type(screen.getByLabelText('Your rough idea'), 'BTC 150k by July 2027')
    await user.click(screen.getByRole('button', { name: /Draft with AI/ }))
    expect(await screen.findByText(/Auto-resolves from Binance/)).toHaveTextContent(
      'Auto-resolves from Binance · BTC/USDT daily close above $150,000 by Jul 1, 2027',
    )
    await user.click(screen.getByRole('button', { name: 'Remove automatic resolution' }))
    expect(screen.queryByText(/Auto-resolves from Binance/)).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(create).toHaveBeenCalledWith({ ...draft, priceRule: null })
  })

  it('sends a kept price rule', async () => {
    const rule = { symbol: 'BTC', op: 'above', threshold: 150000, mode: 'on_date' } as const
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f9', status: 'running' }), credits: fx.credits })
    const { user } = await review({ priceRule: rule })
    expect(screen.getByText(/Auto-resolves from Binance/)).toHaveTextContent('BTC/USDT daily close above $150,000 on Jul 1, 2027')
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(create).toHaveBeenCalledWith({ ...draft, priceRule: rule })
  })

  it('drops the price rule when the question stops being binary', async () => {
    const { user } = await review({ priceRule: { symbol: 'BTC', op: 'above', threshold: 150000, mode: 'on_date' } })
    await user.click(screen.getByRole('radio', { name: /Categorical/ }))
    expect(screen.queryByText(/Auto-resolves from Binance/)).not.toBeInTheDocument()
    await user.click(screen.getByRole('radio', { name: /Binary/ }))
    expect(screen.queryByText(/Auto-resolves from Binance/)).not.toBeInTheDocument()
  })

  it('sends optional context for the desk, trimmed, with a counter', async () => {
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f9', status: 'running' }), credits: fx.credits })
    const { user } = await review()
    const toggle = screen.getByRole('button', { name: 'Add context for the desk (optional)' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByLabelText('Context')).toBeNull()
    await user.click(toggle)
    const box = screen.getByLabelText('Context')
    expect(box).toHaveAttribute('placeholder', 'Your data, a thesis, constraints… The desk treats it as a claim to check, not as fact.')
    expect(box).toHaveAttribute('maxLength', '4000')
    expect(screen.getByText('0/4,000')).toBeInTheDocument()
    await user.type(box, '  Our OTC desk saw $2bn of bids.  ')
    expect(screen.getByText('34/4,000')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(create).toHaveBeenCalledWith({ ...draft, context: 'Our OTC desk saw $2bn of bids.' })
  })

  it("keeps an older draft's File: blocks as removable S0 chips, without a context Attach file button", async () => {
    const create = vi.spyOn(api, 'createForecast').mockResolvedValue({ forecast: fx.forecast({ id: 'f9', status: 'running' }), credits: fx.credits })
    const context = 'Our OTC desk saw bids.\n\nFile: closes.csv\ndate,close\n2026-09-30,114200\n\nFile: notes.md\nBTC thesis: ETF bid.'
    const { user } = await review({ context })
    expect(screen.queryByLabelText('Attach file')).toBeNull()
    const chips = screen.getByRole('list', { name: 'Files in your notes' })
    expect(within(chips).getByText('S0 · closes.csv · 28 chars')).toBeInTheDocument()
    expect(within(chips).getByText('S0 · notes.md · 20 chars')).toBeInTheDocument()
    expect(screen.getByText('your notes + 2 files')).toBeInTheDocument() // the summary
    await user.click(screen.getByRole('button', { name: 'Remove closes.csv' }))
    expect(screen.getByLabelText('Context')).toHaveValue('Our OTC desk saw bids.\n\nFile: notes.md\nBTC thesis: ETF bid.')
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(create).toHaveBeenCalledWith({ ...draft, context: 'Our OTC desk saw bids.\n\nFile: notes.md\nBTC thesis: ETF bid.' })
  })

  it('opens the context box when an edited draft already has context', async () => {
    await review({ context: 'Kept from the failed run.' })
    expect(screen.getByRole('button', { name: 'Add context for the desk (optional)' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByLabelText('Context')).toHaveValue('Kept from the failed run.')
  })

  it('blocks the run until the draft is valid', async () => {
    const { user } = await review({ question: 'BTC?', resolutionCriteria: '' })
    const run = screen.getByRole('button', { name: /Run forecast/ })
    expect(run).toBeDisabled()
    expect(screen.getByText('Write a question of at least 10 characters.')).toBeInTheDocument()
    expect(screen.getByText('Cites a resolution source')).toHaveTextContent('Cites a resolution source — missing')

    await user.type(screen.getByLabelText('Question'), ' Above 150k by July 2027')
    const date = screen.getByLabelText('Resolution date')
    await user.clear(date)
    expect(screen.getByText('Pick a resolution date.')).toBeInTheDocument()
    expect(screen.getByText('Tomorrow or later')).toBeInTheDocument()
    await user.type(date, '2026-10-01')
    expect(screen.getByText('Must be after today')).toBeInTheDocument()
    expect(date).toHaveAttribute('aria-invalid', 'true')
    await user.clear(date)
    await user.type(date, '2026-10-12')
    expect(screen.getByText('Resolves in 10 days')).toBeInTheDocument()
    expect(run).toBeEnabled()
  })

  it('edits list outcomes: shape switch, add, move, remove and duplicates', async () => {
    const { user } = await review()
    expect(screen.getByText('Binary questions always resolve Yes or No. Switch to Categorical for named options.')).toBeInTheDocument()

    await user.click(screen.getByRole('radio', { name: /Ordered/ }))
    const outcomes = () => screen.getAllByRole('textbox', { name: /^Outcome \d$/ }).map((i) => (i as HTMLInputElement).value)
    expect(outcomes()).toEqual(['', '', ''])
    expect(screen.getByText('Fill in every outcome, or remove the empty ones.')).toBeInTheDocument()

    await user.type(screen.getByLabelText('Outcome 1'), 'Low')
    await user.type(screen.getByLabelText('Outcome 2'), 'Mid')
    await user.type(screen.getByLabelText('Outcome 3'), 'low')
    expect(screen.getByLabelText('Outcome 3')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByText('Each outcome must be different.')).toBeInTheDocument()
    await user.clear(screen.getByLabelText('Outcome 3'))
    await user.type(screen.getByLabelText('Outcome 3'), 'High')

    await user.click(screen.getByRole('button', { name: '+ Add outcome' }))
    expect(outcomes()).toEqual(['Low', 'Mid', 'High', ''])
    await user.click(screen.getByRole('button', { name: 'Remove outcome 4' }))
    await user.click(screen.getByRole('button', { name: 'Move outcome 3 up' }))
    expect(outcomes()).toEqual(['Low', 'High', 'Mid'])
    await user.click(screen.getByRole('button', { name: 'Move outcome 1 down' }))
    expect(outcomes()).toEqual(['High', 'Low', 'Mid'])
    expect(screen.getByRole('button', { name: 'Move outcome 1 up' })).toBeDisabled()

    await user.click(screen.getByRole('radio', { name: /Binary/ }))
    expect(screen.queryByLabelText('Outcome 1')).toBeNull()
    await user.click(screen.getByRole('radio', { name: /Ordered/ }))
    expect(outcomes(), 'switching back restores the list').toEqual(['High', 'Low', 'Mid'])
    await user.click(screen.getByRole('radio', { name: /Categorical/ }))
    expect(outcomes(), 'list shapes share outcomes').toEqual(['High', 'Low', 'Mid'])
  })

  it('caps outcomes at eight and keeps at least two', async () => {
    const { user } = await review({ shape: 'categorical', outcomes: ['A', 'B'] })
    expect(screen.getByRole('button', { name: 'Remove outcome 1' })).toBeDisabled()
    for (let i = 0; i < 6; i++) await user.click(screen.getByRole('button', { name: '+ Add outcome' }))
    expect(screen.getByText('8/8')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '+ Add outcome' })).toBeDisabled()
  })

  it('changes the category and shows run errors', async () => {
    const create = vi.spyOn(api, 'createForecast').mockRejectedValue(new ApiError(400, 'bad_request', 'resolutionDate must be in the future'))
    const { user } = await review()
    await user.selectOptions(screen.getByLabelText('Category'), 'macro')
    await user.type(screen.getByLabelText('Resolution criteria'), ' Per Binance.')
    await user.click(screen.getByRole('button', { name: /Run forecast/ }))
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ category: 'macro', resolutionCriteria: `${draft.resolutionCriteria} Per Binance.` }))
    expect(await screen.findByRole('alert')).toHaveTextContent('resolutionDate must be in the future')
    await waitFor(() => expect(screen.getByRole('button', { name: /Run forecast/ })).toBeEnabled())
  })

  it('goes back to a fresh idea from an edit', async () => {
    const { user } = await review()
    await user.click(screen.getByRole('button', { name: /Start from a new idea/ }))
    expect(screen.getByRole('heading', { name: 'Point it at the future.' })).toBeInTheDocument()
    expect(within(document.body).queryByLabelText('Question')).toBeNull()
  })
})
