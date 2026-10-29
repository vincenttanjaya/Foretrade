import { act, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { api } from '../../lib/api'
import * as fx from '../../test/fixtures'
import { renderPage, stubSession } from '../../test/utils'
import { StrategiesPage } from './StrategiesPage'
import { StrategyDetailPage } from './StrategyDetailPage'

beforeEach(() => {
  stubSession()
  vi.spyOn(api, 'autopilots').mockResolvedValue({ autopilots: [] })
})

it.each([
  [true, false], [false, false], [true, true], [false, true],
])('preserves publish=%s with older refresh finishing first=%s', async (toPublished, readFirst) => {
  const oldForward = fx.forward({ basis: 'saved' })
  const metrics = fx.strategy().metrics
  const endT = Math.floor(Date.parse(oldForward.since) / 1000 / 14400) * 14400 - 14400
  const saved = fx.strategy({ forward: oldForward, createdAt: oldForward.since, community: fx.community(),
    metrics: { ...metrics, endT, startT: endT - (metrics.endT - metrics.startT) } })
  const newForward = fx.forward({ basis: 'published', since: '2026-10-05T09:00:00.000001Z', metrics: null, equity: [], benchmark: [], trades: [], reason: 'too_new', asOf: null, early: true })
  const published = { ...saved, community: fx.community({ published: true, publishedAt: newForward.since }), forward: newForward }
  const initial = toPublished ? saved : published
  const latest = toPublished ? published : saved
  const expectedLabel = toPublished ? 'Since publish' : 'Since saved'
  const expectedControl = toPublished ? 'Unpublish' : 'Publish to community'
  let finishOldRead!: (value: { strategy: typeof initial }) => void
  const oldRead = new Promise<{ strategy: typeof initial }>((resolve) => { finishOldRead = resolve })
  vi.spyOn(api, 'strategy').mockResolvedValueOnce({ strategy: initial }).mockReturnValueOnce(oldRead)
  let finishPublish!: (value: { strategy: typeof latest }) => void
  const publish = new Promise<{ strategy: typeof latest }>((resolve) => { finishPublish = resolve })
  vi.spyOn(api, 'publishStrategy').mockReturnValue(publish)
  const { user } = renderPage(<StrategyDetailPage />, { route: '/strategies/s1', path: '/strategies/:id' })
  const forward = await screen.findByRole('region', { name: 'Live forward' })
  await user.click(within(forward).getByRole('button', { name: 'Refresh live forward' }))
  if (toPublished) {
    await user.click(screen.getByRole('button', { name: 'Publish to community' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Publish' }))
  } else await user.click(screen.getByRole('button', { name: 'Unpublish' }))
  if (readFirst) await act(async () => { finishOldRead({ strategy: initial }); await oldRead })
  await act(async () => { finishPublish({ strategy: latest }); await publish })
  await waitFor(() => expect(forward).toHaveTextContent(expectedLabel))
  expect(screen.getByRole('button', { name: expectedControl })).toBeInTheDocument()
  if (!readFirst) await act(async () => { finishOldRead({ strategy: initial }); await oldRead })
  expect(screen.getByRole('region', { name: 'Live forward' })).toHaveTextContent(expectedLabel)
  expect(screen.getByRole('button', { name: expectedControl })).toBeInTheDocument()
})

it('does not claim retained measured figures for stale unavailable anchored history', async () => {
  const forward = fx.forward({ stale: true, metrics: null, early: true, reason: 'history_unavailable', asOf: '2026-10-01T00:00:00Z', equity: [], benchmark: [], trades: [] })
  vi.spyOn(api, 'strategy').mockResolvedValue({ strategy: fx.strategy({ forward }) })
  renderPage(<StrategyDetailPage />, { route: '/strategies/s1', path: '/strategies/:id' })
  const region = await screen.findByRole('region', { name: 'Live forward' })
  expect(region).toHaveTextContent('No measured window')
  expect(region).toHaveTextContent('complete history')
  expect(region).toHaveTextContent('Stale')
  expect(region).toHaveTextContent('Source as of 2026-10-01 00:00:00 UTC')
  expect(within(region).queryByRole('table')).toBeNull()
  expect(region).not.toHaveTextContent('The figures retain their original measured window')
})

it.each(['#', 'Sharpe'])('clears obsolete %s sort semantics and cue while Labs uses forward return', async (column) => {
  const measured = fx.forward()
  const rows = [
    fx.strategySummary({ id: 'api-first', name: 'API first', forward: { ...measured, metrics: { ...measured.metrics!, totalReturnPct: 1 } } }),
    fx.strategySummary({ id: 'api-second', name: 'Forward first', forward: { ...measured, metrics: { ...measured.metrics!, totalReturnPct: 5 } } }),
  ]
  vi.spyOn(api, 'strategies').mockResolvedValue({ strategies: rows })
  vi.spyOn(api, 'strategy').mockResolvedValue({ strategy: fx.strategy() })
  const { user } = renderPage(<StrategiesPage />, { route: '/strategies?tab=top', path: '/strategies' })
  await screen.findByRole('link', { name: 'API first' })
  if (column === 'Sharpe') await user.click(screen.getByRole('button', { name: 'Sharpe' }))
  const header = within(screen.getByRole('table')).getByRole('columnheader', { name: column })
  const direction = column === '#' ? 'ascending' : 'descending'
  expect(header).toHaveAttribute('aria-sort', direction)
  expect(header.querySelector('[aria-hidden]')).not.toHaveClass('opacity-0')
  await user.selectOptions(screen.getByRole('combobox', { name: 'Labs strategy order' }), 'forward')
  const table = screen.getByRole('table')
  expect(within(table).getAllByRole('row')[1]).toHaveTextContent('Forward first')
  for (const th of within(table).getAllByRole('columnheader')) {
    expect(th).toHaveAttribute('aria-sort', 'none')
    expect(th.querySelector('[aria-hidden]')).toHaveClass('opacity-0')
  }
  await user.selectOptions(screen.getByRole('combobox', { name: 'Labs strategy order' }), 'default')
  expect(header).toHaveAttribute('aria-sort', direction)
  expect(header.querySelector('[aria-hidden]')).not.toHaveClass('opacity-0')
  await user.selectOptions(screen.getByRole('combobox', { name: 'Labs strategy order' }), 'forward')
  await user.click(screen.getByRole('button', { name: column }))
  expect(screen.getByRole('combobox', { name: 'Labs strategy order' })).toHaveValue('default')
  expect(header).toHaveAttribute('aria-sort', direction === 'ascending' ? 'descending' : 'ascending')
  expect(header.querySelector('[aria-hidden]')).not.toHaveClass('opacity-0')
})
