import type { ForecastDraft, LinkedMarket, PredictionEvent, PredictionMarket } from '../lib/types'

export const linkedMarket: LinkedMarket = {
  marketId: '123', conditionId: `0x${'a'.repeat(64)}`, marketSlug: 'bitcoin-above-100000', eventSlug: 'bitcoin-october',
  question: 'Will Bitcoin be above $100,000 on October 12?',
  description: 'Resolves Yes using the one-minute Binance BTCUSDT candle at noon ET on October 12.\nOtherwise No.',
  resolutionSource: 'https://www.binance.com/en/trade/BTC_USDT', endDate: '2026-10-12T16:00:00Z', asOf: '2026-10-05T12:00:00Z',
}
export const linkedDraft: ForecastDraft = {
  shape: 'binary', question: linkedMarket.question, outcomes: ['Yes', 'No'], resolutionCriteria: linkedMarket.description,
  resolutionDate: '2026-10-12', category: 'crypto', priceRule: null, context: '', linkedMarket,
}
export const predictionMarket: PredictionMarket = {
  id: '123', marketSlug: linkedMarket.marketSlug, conditionId: linkedMarket.conditionId, question: linkedMarket.question,
  yesPrice: 0.3, change24hPp: null, volumeUsd: 10000, volume24hUsd: 2000, endDate: linkedMarket.endDate, thin: false,
  rule: { symbol: 'BTC', op: 'above', strike: 100000, kind: 'close' }, estimateUnavailable: null,
  estimate: { p: 0.45, method: 'lognormal', spot: 95000, volAnnPct: 52, days: 7.2, source: 'Binance daily closes',
    spotAsOf: '2026-10-04T23:59:59Z', windowStart: '2026-07-07T00:00:00Z', windowEnd: '2026-10-04T23:59:59Z', closes: 90,
    assumptions: 'Zero drift; completed daily closes; proxy for price at the market end.', edgePp: 15, evPer100: 50, signal: 'yes' },
  desk: { forecastId: 'f-own', question: linkedMarket.question, p: 0.4, match: 'exact', edgePp: 10, evPer100: 33.33, signal: 'yes' },
}
export const predictionEvent: PredictionEvent = {
  slug: linkedMarket.eventSlug, title: 'Bitcoin in October', url: 'https://polymarket.com/event/bitcoin-october',
  category: 'crypto', endDate: linkedMarket.endDate, volume24hUsd: 2000, markets: [predictionMarket],
}
export const predictionDraft = { draft: linkedDraft, market: predictionMarket, event: { slug: predictionEvent.slug, title: predictionEvent.title, url: predictionEvent.url } }
export const predictionBoard = { events: [predictionEvent], asOf: linkedMarket.asOf, stale: false }
