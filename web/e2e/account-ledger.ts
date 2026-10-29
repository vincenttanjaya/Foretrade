// Mock financial reads follow the approved Go opening-lot FIFO and equity replay semantics.
// These helpers simulate a paper ledger; the API's DB/SDK tests own server implementation proof.
import type { EquityRange, MarketPosition, Point, SourcePnl, Trade } from '../src/lib/types'
const DUST = 1e-9 // approved paper engine's flat remainder threshold

export function attributeLots(trades: Trade[], price: (symbol: string) => number, key: (trade: Trade) => string = trade => trade.source): SourcePnl[] {
  const rows = new Map<string, SourcePnl>()
  const row = (source: string) => {
    if (!rows.has(source)) rows.set(source, { source, trades: 0, realizedPnl: 0, unrealizedPnl: 0, fees: 0, totalPnl: 0 })
    return rows.get(source)!
  }
  const books = new Map<string, { qty: number; price: number; source: string }[]>()
  for (const trade of trades) {
    const direction = trade.side === 'buy' ? 1 : -1
    let left = trade.qty
    const book = books.get(trade.symbol) ?? []
    while (book.length && left > DUST && Math.sign(book[0].qty) !== direction) {
      const lot = book[0], take = Math.min(Math.abs(lot.qty), left), source = row(lot.source)
      source.realizedPnl += (trade.price - lot.price) * take * Math.sign(lot.qty)
      source.fees += trade.fee * take / trade.qty
      lot.qty -= take * Math.sign(lot.qty); left -= take
      if (Math.abs(lot.qty) < DUST) book.shift()
    }
    if (left > DUST) {
      const source = row(key(trade))
      source.trades++; source.fees += trade.fee * left / trade.qty
      book.push({ qty: left * direction, price: trade.price, source: key(trade) })
    }
    books.set(trade.symbol, book)
  }
  for (const [symbol, book] of books) for (const lot of book) row(lot.source).unrealizedPnl += (price(symbol) - lot.price) * lot.qty
  return [...rows.values()].map(r => ({ ...r, realizedPnl: r.realizedPnl - r.fees, totalPnl: r.realizedPnl - r.fees + r.unrealizedPnl }))
    .sort((a, b) => Math.abs(b.totalPnl) - Math.abs(a.totalPnl) || a.source.localeCompare(b.source))
}

export const EQUITY_GRIDS = { '1d': [86400, '15m', 900], '7d': [604800, '1h', 3600], '30d': [2592000, '4h', 14400] } as const
export function replayPoints(trades: Trade[], stakes: MarketPosition[], grid: number[], price: (symbol: string, t: number) => number, startingBalance = 100000): Point[] {
  let cash = startingBalance, i = 0
  const qty = new Map<string, number>()
  return grid.map(t => {
    while (i < trades.length && Math.floor(Date.parse(trades[i].createdAt) / 1000) <= t) {
      const trade = trades[i++], signed = trade.qty * (trade.side === 'buy' ? 1 : -1)
      cash -= signed * trade.price + trade.fee
      qty.set(trade.symbol, (qty.get(trade.symbol) ?? 0) + signed)
      if (Math.abs(qty.get(trade.symbol)!) < DUST) qty.delete(trade.symbol)
    }
    const settled = stakes.reduce((sum, p) => sum + (p.settledAt && Math.floor(Date.parse(p.settledAt) / 1000) <= t ? p.pnl ?? 0 : 0), 0)
    return { t, v: cash + settled + [...qty].reduce((sum, [symbol, units]) => sum + units * price(symbol, t), 0) }
  })
}

export function summarizeEquity(range: EquityRange, points: Point[]) {
  const startEquity = points[0].v, endEquity = points.at(-1)!.v
  let peak = startEquity, drawdown = 0
  for (const p of points) { peak = Math.max(peak, p.v); if (peak > 0) drawdown = Math.max(drawdown, (peak - p.v) / peak * 100) }
  const round = (n: number) => Math.round(n * 100) / 100
  return { scope: 'selected_range' as const, range, startEquity, endEquity, returnPct: startEquity > 0 ? round((endEquity / startEquity - 1) * 100) : null, maxDrawdownPct: round(drawdown) }
}
