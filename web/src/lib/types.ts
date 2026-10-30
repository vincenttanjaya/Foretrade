// Types for the whole HTTP contract (docs/API-CONTRACT.md). Keep in sync with the Go structs.
// Timestamps are ISO strings except `t` fields (unix seconds). Percentages are already ×100,
// probabilities are 0..1.

// ── Auth & credits ──────────────────────────────────────────────────────────
export type RiskProfile = 'conservative' | 'balanced' | 'aggressive'
export type Memory = { id: string; text: string; at: string }
export type Profile = {
  enabled: boolean
  riskProfile: RiskProfile | null
  style: CopilotStyle | null
  focusSymbols: string[]
  customPrompt: string
  memories: Memory[]
  revision: number
  updatedAt: string
}
export type ProfilePreferences = Pick<Profile, 'enabled' | 'riskProfile' | 'style' | 'focusSymbols' | 'customPrompt'>
export type UpdateProfileRequest = ProfilePreferences & { revision: number }
export type MemoryProposal = { proposalId: string; text: string; status: 'open' | 'confirmed' | 'deleted'; memoryId?: string }
export type ConfirmMemoryRequest = Pick<MemoryProposal, 'proposalId' | 'text'>
export type User = { id: string; email: string; name: string; role: 'user' | 'admin'; profile: Profile }
export type Credits = { used: number; limit: number; resetsAt: string } // per user per UTC day

// ── Market ──────────────────────────────────────────────────────────────────
export type Interval = '15m' | '1h' | '4h' | '1d'
/** Trade charts offer longer calendar bars without changing strategy/analysis intervals. */
export type ChartInterval = Interval | '1w' | '1M'

export type Asset = {
  symbol: string
  pair: string
  name: string
  price: number
  change24hPct: number
  high24h: number
  low24h: number
  volume24hUsd: number
  class: 'crypto' | 'stock' | 'index'
  venue: 'binance' | 'hyperliquid' | 'yahoo' // yahoo: a quote-feed stock (contract §2), priced in USD from Yahoo
  session: 'open' | 'closed' | null // regular session of `sessionVenue` (NYSE before the field existed)
  sessionChangeAt: string | null
  range24hSource: 'ticker' | 'hourly_candles' | 'session' | 'unavailable' // session: a quote feed's latest regular session
  // Optional so an API without them still renders: null for crypto.
  region?: 'us' | 'asia' | null
  sessionVenue?: 'NYSE' | 'KRX' | 'TSE' | 'HKEX' | 'SSE' | 'SGX' | 'SET' | 'TWSE' | 'IDX' | 'BURSA' | 'HOSE' | null
  underlying?: string | null // short reference, e.g. "NYSE: TSM ADS = 5 TWSE 2330 shares"
  localCurrency?: 'USD' | 'KRW' | 'JPY' | 'HKD' | 'CNY' | 'SGD' | 'IDR' | 'THB' | 'MYR' | 'VND' | 'TWD' | null // the listing's currency; prices here stay USD
  quoteFeed?: QuoteFeed | null // venue "yahoo" rows only
}

/**
 * One row of GET /api/market/instruments (contract §2): every instrument the API trades, priced or not, halted or not.
 * `listed` rows are the Asian index constituents generated from the screener's lists (symbol = the Yahoo ticker,
 * "6501.T"); the others are the named universe that SYMBOLS mirrors.
 */
export type Instrument = {
  symbol: string
  pair: string
  name: string
  class: Asset['class']
  venue: Asset['venue']
  region: 'us' | 'asia' | null
  sessionVenue: NonNullable<Asset['sessionVenue']> | null
  localCurrency: NonNullable<Asset['localCurrency']> | null
  underlying: string | null
  yahoo: string | null // the Yahoo ticker ("D05.SI", "6501.T"); null for crypto and XYZ100
  listed: boolean
  index: string | null // a listed row's index ("Nikkei 225"), '' once it left every list; null for every named row
}

/** A quote-feed row's quote (contract §2): the USD price is localPrice × fx; outside the session it is the last close. */
export type QuoteFeed = {
  symbol: string // Yahoo ticker, "D05.SI"
  exchange: string
  currency: string
  localPrice: number
  previousClose: number | null
  fx: number // USD per 1 unit of `currency`; 1 for USD
  fxSymbol: string // "SGDUSD=X"; '' for USD
  fxAsOf: string | null
  asOf: string // when localPrice was set
  marketState: 'open' | 'closed' | 'unknown'
  stale: boolean
  // Real-time quotes (RT-QUOTES §5): whose price this is, its own last-trade time, how far behind it ran when read, and
  // the honest label. Absent from an API before them: the price is Yahoo's delayed last trade at `asOf`.
  source?: QuoteSource
  class?: QuoteClass // yahoo is "delayed"
  lastTradeAt?: string // the served quote's own last-trade time (UTC) = asOf
  readAt?: string // this server's read of it (UTC)
  delaySeconds?: number // min(readAt, the last moment the session was open) − lastTradeAt, ≥ 0
  timeBasis?: QuoteTimeBasis
  label?: string // "real-time · 04:20:15 UTC" | "about 2 min behind · 04:18 UTC" | "delayed · last trade 04:00 UTC"
}
/** Whose quote: Yahoo's delayed feed or a venue's real-time source (KRX naver, HKEX tencent, HOSE vietcap, SGX sgx, TWSE mis then yahoo-tw). */
export type QuoteSource = 'yahoo' | 'naver' | 'tencent' | 'vietcap' | 'sgx' | 'mis' | 'yahoo-tw' | (string & {})
/** realtime: trade by trade; near-realtime: SGX's once-a-minute snapshot; delayed: Yahoo. */
export type QuoteClass = 'realtime' | 'near-realtime' | 'delayed'
/** Where `lastTradeAt` comes from: the source's trade time, the read that first showed the current volume (Naver), or the snapshot minute (SGX). */
export type QuoteTimeBasis = 'trade' | 'volume' | 'snapshot'

export type Candle = { t: number; o: number; h: number; l: number; c: number; v: number } // t = open time

/** Hyperliquid perp context; crypto paper fills use Binance spot, stock and index perps use their Hyperliquid marks. */
export type Derivatives = {
  symbol: string
  venue: 'Hyperliquid'
  markPrice: number
  fundingRatePct: number // hourly × 100
  fundingAnnualizedPct: number
  openInterestUsd: number
  premiumPct: number
  volume24hUsd: number
  asOf: string
}

export type Analysis = {
  symbol: string
  interval: string
  price: number
  return1dPct: number
  return7dPct: number
  return30dPct: number
  realizedVolAnnPct: number
  rsi14: number
  sma20: number
  sma50: number
  sma200: number | null
  ema20: number
  atr14: number
  support: number
  resistance: number
  trend: 'uptrend' | 'downtrend' | 'range'
}

export type PriceOutlook = {
  symbol: string
  spot: number
  horizonDays: number
  volAnnPct: number
  buckets: { label: string; lo: number | null; hi: number | null; p: number }[] // low→high, p sums to 1
  p5: number
  p50: number
  p95: number
  // Additive, long-horizon outlook (contract "price_outlook"): older cards and the Copilot plan may omit both.
  volBasis?: OutlookVolBasis // the daily closes volAnnPct came from
  longHorizon?: OutlookLongHorizon // only when horizonDays > 365
  // Additive ("Price outlook view"): the fields above are the zero-drift volatility-only baseline; these
  // carry the headline view. Cards stored before it lack every one of them.
  horizon?: OutlookHorizon
  history?: OutlookHistory | null
  view?: OutlookView
  scenarios?: OutlookScenario[]
  timeToTarget?: TimeToTarget
  warnings?: string[]
}

/** "90d": the last 90 daily closes (horizons up to 365 days); "long-run": every daily close the API serves. */
export type OutlookVolBasis = { window: '90d' | 'long-run'; days: number; since: string /* YYYY-MM-DD */; source?: string }

/**
 * Where the horizon came from: the user's words, a "by when" question (days null: no at-horizon panel), the agent's
 * choice with its reason ("chosen"), or the agent's choice with none given ("agent": the API accepts
 * it instead of a round trip and the reason says the agent chose it). "user": the reason quotes the
 * user's timeframe in words the parser does not read, shown as given.
 */
export type OutlookHorizon = { days: number | null; basis: 'explicit' | 'by_when' | 'chosen' | 'agent' | 'user'; reason: string }

/** The asset's own daily closes (for a stock perp, the listing's Yahoo history). Percents are already ×100. */
export type OutlookHistory = {
  source: string // "Yahoo Finance daily closes (NYSE: TSM)"
  from: string // YYYY-MM-DD
  to: string
  years: number
  volAnnPct: number | null
  cagrPct: { y5: number | null; y10: number | null; all: number | null }
  maxDrawdownPct: number | null // negative
  maxDrawdownPeak: string | null
  maxDrawdownTrough?: string | null
  recovered: string | null
  driftHalvedPct?: number | null // the halved historical drift ×100 (annual log), before any cap; null under 2 years
}

/** pEnd/pTouch as OutlookMultiple, under the view, with the volatility-only baseline's alongside. */
export type OutlookViewMultiple = { x: number; pEnd: number; pTouch: number; baselinePEnd: number; baselinePTouch: number }

/** The headline distribution, carrying a direction: the agent's scenarios, the historical drift halved, or none. */
export type OutlookView = {
  kind: 'scenarios' | 'historical_drift' | 'none'
  label: string
  muAnnPct: number | null // historical_drift only: the log drift the view uses (halved, then capped), ×100
  driftCap?: OutlookDriftCap // historical_drift only
  median: number
  p5: number
  p25: number
  p75: number
  p95: number
  pAbove: number // P(price at the horizon ≥ today): the card's gain chance, each scenario's spread included
  pAboveByWeights?: number // the share of scenario weight above today (for the agent's labelled weight sums)
  changePct: number // median vs spot
  annualPct: number | null // horizons of a year or more
  buckets?: { label: string; lo: number | null; hi: number | null; p: number; pBaseline: number }[] // cut at multiples of spot; absent without a horizon
  multiples?: OutlookViewMultiple[] // 0.5, 2, 5, 10 (+ the target multiple)
  listingSpot: number | null // the listing's last close, when it differs from the perp's mark by more than 2%
  volAnnPct?: number // the σ the view is spread by
  volAssumed?: boolean // no measured history: volAnnPct is the API's labelled 30%/yr assumption
}

/**
 * How a drift-only view's return was capped so a backward-looking number does not overpromise: at the listing's home
 * benchmark's long-run price return + 2 points a year, never above 10%/yr. Percents are annual price returns ×100.
 */
export type OutlookDriftCap = {
  benchmark: string // "SPX" | "TWII" | "N225" | "KOSPI" | "HSI" | "STI" | "JKSE"; "" without one (the 10%/yr ceiling alone)
  source: string
  since: string | null // the benchmark's first close, YYYY-MM-DD
  benchmarkCagrPct: number | null
  capPct: number
  uncappedPct: number // the halved drift before the cap
  applied: boolean // the halved drift was above capPct
}

/** Where a scenario input came from: a verified number in a source or tool result, model knowledge, or an assumption. */
export type ScenarioInput = {
  field: 'eps' | 'growthPct' | 'pe' | 'level' | 'cagrPct' | (string & {})
  value: number
  year?: number // eps: the EPS year; growthPct: the exit year the growth runs to
  basis: string // chat: "[2]" | "quote"; desk: "F3" | "S4" | "H1"; both: "model knowledge, as of 2025-06" | "assumption"
  kind: 'source' | 'model_knowledge' | 'assumption'
  verified: boolean
}

export type OutlookScenario = {
  name: string
  kind: 'bear' | 'base' | 'bull' | 'tail' | (string & {})
  p: number
  level: number
  changePct: number
  annualPct: number | null
  inputs: ScenarioInput[]
}

/** "By when": the odds of reaching multiple × spot within each number of years, and the annual return each needs. */
export type TimeToTarget = {
  multiple: number
  target: number
  rows: { years: number; pEnd: number; pTouch: number; baselinePEnd: number; baselinePTouch: number; extrapolated: boolean }[]
  medianYears: number | null // null: not within 30 years
  // The median lies past twice the scenarios' horizon, so it assumes their returns continue; absent on older cards
  medianExtrapolated?: boolean
  byScenario: { name: string; kind: string; annualPct: number; years: number | null }[]
  requiredCagr: { years: number; pct: number }[]
  ownCagrPct: number | null
  ownSince: string | null // YYYY-MM-DD
}

/** pEnd is P(S_T ≥ x·spot) for x > 1 and P(S_T ≤ x·spot) for x < 1; pTouch the chance of trading at x·spot before the horizon. */
export type OutlookMultiple = { x: number; pEnd: number; pTouch: number }

export type OutlookLongHorizon = {
  multiples: OutlookMultiple[] // x in 0.5, 2, 5, 10; zero drift
  tenX: { years: number; pTouch: number }[] // P(touching 10x spot) within 1/3/5/10 years, those within the horizon
  // A labelled assumption, not the baseline: the historical drift halved; null under 2 years of history.
  driftCase: { label: string; muAnnPct: number; p50: number; multiples: OutlookMultiple[] } | null
}

// ── Paper trading ───────────────────────────────────────────────────────────
export type Side = 'buy' | 'sell'
export type TradeSource = 'manual' | 'copilot' | 'agent' | 'portfolio' | 'stop_loss' | 'take_profit' | 'autopilot' | 'alert' | 'copy'

export type Position = {
  symbol: string
  qty: number // negative = short
  avgPrice: number
  markPrice: number
  marketValue: number
  unrealizedPnl: number
  unrealizedPnlPct: number
  stopLoss: number | null
  takeProfit: number | null
}

export type Portfolio = {
  cash: number
  equity: number
  startingBalance: number
  unrealizedPnl: number
  realizedPnl: number
  totalPnl: number
  totalPnlPct: number
  grossExposure: number
  buyingPower: number // 2*equity - grossExposure, floored at 0
  positions: Position[]
  revision?: number // bumps on every committed account change; absent from APIs that predate it
  predictionStakes?: number // Σ cost of open paper prediction positions; in equity at cost (absent on older accounts)
}

export type Trade = {
  id: string
  symbol: string
  side: Side
  qty: number
  price: number
  notional: number
  fee: number
  realizedPnl: number
  source: TradeSource
  note: string
  createdAt: string
  ref?: TradeRef | null // where it came from; only GET /api/portfolio/trades fills it (optional for older fixtures)
}

/** Links a fill back to its chat (/chat/:id), AI portfolio (/portfolios/:id), autopilot (/strategies/:strategyId) alert (/automations), or copy plan (/account#copy-plans). */
export type TradeRef = { kind: 'chat' | 'portfolio' | 'autopilot' | 'alert' | 'copy'; id: string; label: string }

/** PATCH /api/portfolio/positions/:symbol: an absent key keeps that level, null clears it. `expect` (an agent card's
 * `current`) applies it only while the position still has those levels; otherwise 409. */
export type ProtectionRequest = {
  stopLoss?: number | null
  takeProfit?: number | null
  expect?: { stopLoss: number | null; takeProfit: number | null }
}

/** Exactly one of `notional` (USD, ≥ 10) or `qty` (> 0). */
export type OrderRequest = {
  symbol: string
  side: Side
  notional?: number
  qty?: number
  stopLoss?: number
  takeProfit?: number
  source?: Exclude<TradeSource, 'stop_loss' | 'take_profit'>
  note?: string
  proposalId?: string // the agent card being confirmed: a repeat is 409 conflict
}

// ── Agent chat ──────────────────────────────────────────────────────────────
export type TradeProposal = {
  symbol: string
  side: Side
  notional: number
  price: number
  stopLoss: number | null
  takeProfit: number | null
  rationale: string
  proposalId?: string // absent on cards stored before proposal ids
  // The forecast-desk report behind it (outcome/p = its headline: binary Yes and P(Yes)). stance: how the desk reads this
  // side, from a price rule on the card's symbol (null without one; absent on older cards). view: an
  // ordered report's view in brief, which the card states instead of the top range when it leads (viewReading).
  forecast?: {
    id: string
    question: string
    outcome: string
    p: number
    stance?: 'supports' | 'opposes' | 'split' | null
    view?: ForecastSummary['view']
  } | null
  status?: 'open' | 'filled' // stored chats: 'filled' once an order used proposalId
  trade?: Trade | null // the fill, when filled
}

export type ProtectionProposal = {
  symbol: string
  side: 'long' | 'short'
  qty: number // absolute
  markPrice: number
  current: { stopLoss: number | null; takeProfit: number | null }
  proposed: { stopLoss: number | null; takeProfit: number | null } // null = keep current
  rationale: string
}

export type SourceRef = {
  n: number
  title: string
  url: string
  source: string
  publishedAt: string | null
  kind?: 'wire' | 'news' | 'web' | 'page' | 'market' // the tool that numbered it first (market: get_market_odds); absent on older cards
}

/** A portfolio retains its selected comparison and measured window; strategies compare with buy & hold. */
export type BuildResult = {
  benchmarkSymbol: string | null
  benchmarkLabel: string | null
  windowStart: number | null
  windowEnd: number | null
  bars: number | null
  id: string
  name: string
  returnPct: number
  benchmarkReturnPct: number
  sharpe: number | null
  maxDrawdownPct: number
  holdings: { symbol: string; weight: number }[]
}

export type Card =
  | { type: 'memory'; data: MemoryProposal }
  | { type: 'trade_proposal'; data: TradeProposal }
  | { type: 'price_outlook'; data: PriceOutlook }
  | { type: 'sources'; data: { items: SourceRef[] } } // numbered like the [n] citations in the answer text
  // view: an ordered report's view in brief (ForecastSummary.view); absent on other shapes and older cards.
  | {
      type: 'forecast'
      data: { id: string; question: string; shape: ForecastShape; headline: { outcome: string; p: number }; resolutionDate: string; view?: ForecastSummary['view'] }
    }
  | { type: 'followups'; data: { prompts: string[] } } // one-tap next questions under the latest answer
  | { type: 'protection'; data: ProtectionProposal } // new stop / target for a held position; Confirm sends the PATCH
  | { type: 'price_alert'; data: PriceAlertProposal } // an automation; Set alert sends POST /api/alerts with its proposalId
  // Tap-to-run actions. Stored chats fill forecast / result with what the user started from the card.
  // forecast.view: the completed run's view in brief; viewHeadline the API's reading of it (ViewHeadline).
  | { type: 'forecast_draft'; data: { draft: ForecastDraft; forecast: DraftRun | null } }
  | {
      type: 'build_action'
      data: { kind: 'portfolio' | 'strategy'; prompt: string; symbol: string | null; interval: string | null; cost: number; result: BuildResult | null }
    }

/** The run a forecast_draft card started: its headline (null until completed) and, for an ordered report, its view. */
export type DraftRun = { id: string; status: ForecastStatus; headline: OutcomeP | null; view?: ForecastSummary['view']; viewHeadline?: string }

export type ChatMessage = {
  id: string
  role: 'user' | 'assistant'
  content: string // markdown
  cards: Card[]
  attachments: ChatAttachmentMetadata[]
  createdAt: string
}

export type ChatSummary = { id: string; title: string; updatedAt: string }
export type Chat = {
  id: string
  title: string
  messages: ChatMessage[]
  pending?: boolean // an answer is still being generated (e.g. the client left mid-stream): poll every 2 s until false
}

export type ChatMode = 'fast' | 'quality'
/** auto: the agent reads each question as an investor's, a trader's or both; investor or trader fixes the framing. */
export type ChatLens = 'auto' | 'investor' | 'trader'
export type ChatImageMetadata = { kind: 'image'; mediaType: 'image/png' | 'image/jpeg' | 'image/webp' | 'image/gif'; name: string; size: number }
export type ChatDocumentMediaType =
  | 'application/pdf'
  | 'text/csv'
  | 'text/plain'
  | 'text/markdown'
  | 'text/html'
  | 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'
  | 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'
// pages: optional, when the server counted them (PDF)
export type ChatDocumentMetadata = { kind: 'document'; mediaType: ChatDocumentMediaType; name: string; size: number; pages?: number }
export type ChatAttachmentMetadata = ChatImageMetadata | ChatDocumentMetadata
export type ChatImageInput = Omit<ChatImageMetadata, 'size'> & { data: string }
export type ChatDocumentInput = Omit<ChatDocumentMetadata, 'size' | 'pages'> & { data: string }
export type ChatAttachmentInput = ChatImageInput | ChatDocumentInput
/** A file sent with a forecast run or update: the chat attachment input, the same limits and checks. */
export type AttachmentInput = ChatAttachmentInput
export type ChatStreamRequest = { chatId?: string; message: string; mode?: ChatMode; lens?: ChatLens; context?: { symbol?: string; forecastId?: string }; attachments?: ChatAttachmentInput[] }

/** One server-sent event of POST /api/chat/stream, discriminated by `event`. */
export type ChatStreamEvent =
  | { event: 'chat'; data: { chatId: string; title: string } }
  | { event: 'tool_call'; data: { id: string; name: string; label: string; input: Record<string, unknown> } }
  | { event: 'tool_result'; data: { id: string; name: string; ok: boolean; summary: string } }
  | { event: 'delta'; data: { text: string } }
  | { event: 'card'; data: Card }
  | { event: 'done'; data: { messageId: string; credits: Credits } }
  | { event: 'error'; data: { code: string; message: string } }

// ── Copilot ─────────────────────────────────────────────────────────────────
export type CopilotStyle = 'scalp' | 'day' | 'swing'
export type CopilotBias = 'long' | 'short' | 'auto'
export type CopilotPlanRequest = { symbol: string; style: CopilotStyle; bias: CopilotBias }

export type Plan = {
  symbol: string
  style: string
  direction: 'long' | 'short' | 'neutral'
  entry: number
  stopLoss: number
  takeProfit: number
  riskReward: number
  confidence: number // 0..100
  horizon: string
  rationale: string[]
  invalidation: string
  suggestedNotional: number
  catalysts: string[] // 0..3 short news-driven items
  interval: Interval // the style's chart interval
  higherTrend: { interval: '1d'; trend: 'uptrend' | 'downtrend' | 'range' } | null // null when the daily analysis failed
  desk: DeskRead | null // the user's forecast desk on the symbol, stance for this plan's direction (null when neutral)
  generatedAt: string
}

/** GET /api/copilot/desk: the user's newest live forecast on a symbol (a price rule on it first, else one naming it). */
export type DeskRead = {
  forecastId: string
  question: string
  resolutionDate: string // YYYY-MM-DD
  outcome: string // the headline: binary → 'Yes' with p = P(Yes); else the likeliest outcome
  p: number
  lean: string // 'No 72%' when a binary Yes is under 50%, else '<outcome> <p>%'
  priceRule: PriceRule | null
  stance: 'supports' | 'opposes' | 'split' | null // for a long (the desk endpoint) or the plan's side; null without a price rule on the symbol
}

// ── Forecast desk ───────────────────────────────────────────────────────────
export type ForecastShape = 'binary' | 'ordered' | 'categorical'
export type ForecastCategory = 'markets' | 'crypto' | 'macro' | 'tech' | 'politics' | 'sports' | 'other'
export type ForecastStatus = 'running' | 'completed' | 'failed' | 'archived'
export type Level = 'low' | 'medium' | 'high'

export type ForecastDraft = {
  shape: ForecastShape
  question: string
  outcomes: string[] // binary: ["Yes","No"]; ordered: low→high buckets; categorical: 2..8 names
  resolutionCriteria: string
  resolutionDate: string // YYYY-MM-DD, future
  category: ForecastCategory
  priceRule?: PriceRule | null // binary Binance price questions only: resolves automatically (the API always sends it; omitted = null)
  context?: string // the user's notes for the desk (S0, unverified), ≤ 4,000 chars; the draft endpoint returns ''
  linkedMarket?: LinkedMarket | null // full external predicate; linked forecasts resolve manually in Foretrade
}

export type LinkedMarket = {
  marketId: string
  conditionId: string
  marketSlug: string // unique leaf, distinct from the event slug
  eventSlug: string
  question: string
  description: string // complete original predicate, up to 8,000 Unicode runes
  resolutionSource: string | null
  endDate: string // original provider end instant, not an invented observation time
  asOf: string
}
export type PredictionCategory = 'all' | 'crypto' | 'macro'
export type PredictionSort = 'edge' | 'volume' | 'ending'
export type PredictionMarket = {
  id: string
  marketSlug: string
  conditionId: string
  question: string
  yesPrice: number // this leaf's own Yes, including below questions
  change24hPp: number | null
  volumeUsd: number | null
  volume24hUsd: number | null
  endDate: string
  thin: boolean
  rule: { symbol: string; op: 'above' | 'below'; strike: number; kind: 'close' | 'touch' } | null
  estimateUnavailable: string | null
  estimate: {
    p: number
    method: 'lognormal'
    spot: number
    volAnnPct: number
    days: number
    source: string
    spotAsOf: string
    windowStart: string
    windowEnd: string
    closes: number
    assumptions: string
    edgePp: number
    evPer100: number | null
    signal: 'yes' | 'no' | 'wait'
  } | null
  desk: {
    forecastId: string
    question: string
    p: number
    match: 'exact'
    edgePp: number
    evPer100: number | null
    signal: 'yes' | 'no' | 'wait'
  } | null
}
export type PredictionEvent = {
  slug: string
  title: string
  url: string
  category: 'crypto' | 'macro'
  endDate: string
  volume24hUsd: number | null
  markets: PredictionMarket[]
}
export type PredictionBoard = { events: PredictionEvent[]; asOf: string; stale: boolean }
export type PredictionDraft = { draft: ForecastDraft; market: PredictionMarket; event: Pick<PredictionEvent, 'slug' | 'title' | 'url'> }

/** Resolves a binary question from the Binance <symbol>USDT daily closes; its date is resolutionDate. */
export type PriceRule = { symbol: string; op: 'above' | 'below'; threshold: number; mode: 'on_date' | 'by_date' }

export type ForecastSummary = {
  id: string
  shape: ForecastShape
  question: string
  category: string
  status: ForecastStatus
  archivedFromStatus?: Exclude<ForecastStatus, 'archived'> | null
  resolutionDate: string
  createdAt: string
  completedAt: string | null
  headline: { outcome: string; p: number } | null // top outcome (binary: Yes)
  resolvedOutcome: string | null // the outcome that happened; null = unresolved
  replay: boolean // a shared historical-replay row
  parentId?: string | null // the run this one updates (an update); null for a first run
  // An ordered row's desk view in brief; absent for other shapes and unparseable buckets.
  // `basis` is the view's DeskViewBasis kind: a row states its median only when it is not "none".
  view?: {
    median: DeskQ
    p10: DeskQ
    p90: DeskQ
    pAbove: DeskQ | null
    unit: DeskUnit
    flat: boolean
    headline: 'view' | 'bucket' | (string & {})
    basis?: DeskViewBasis['kind']
  }
}

export type OutcomeP = { outcome: string; p: number }

export type Specialist = {
  role: 'base_rate' | 'quant' | 'skeptic'
  name: string
  probabilities: OutcomeP[]
  reasoning: string
  keyFactors: string[]
  confidence: Level
  // "Desk view and methods": numeric questions only; absent on binary/categorical runs and older results.
  method?: SpecialistMethod // set by the server from the tool calls that actually ran
  anchor?: OutcomeP[] // its method's tool distribution over the outcomes, where it started
  stats?: SpecialistStats
  model?: ModelScenario[] // the quant's valuation scenarios as the tool computed them
}

/** The method a specialist actually used. judgment: it never called its tool; fallback: every call failed. */
export type SpecialistMethod = 'history' | 'fundamentals' | 'volatility' | 'regime' | 'judgment' | 'fallback'

/** A quantile of a bucket distribution; approx when it falls in an open-ended range (an extrapolated tail). */
export type DeskQ = { value: number; approx?: boolean }

/** A specialist's own distribution, read as numbers; statedMedian is the median it wrote (null when it gave none). */
export type SpecialistStats = { median: DeskQ; p10: DeskQ; p90: DeskQ; pAbove: DeskQ | null; statedMedian: number | null }

/** One valuation scenario as scenario_model priced it: p is the quant's own weight, price the level the inputs give. */
export type ModelScenario = { name: string; kind: string; p: number; price: number; vsRefPct: number | null; inputs: ScenarioInput[] }

export type ForecastResult = {
  probabilities: OutcomeP[]
  ensemble: OutcomeP[]
  specialists: Specialist[]
  summary: string
  drivers: { factor: string; direction: 'up' | 'down'; weight: Level }[] // "up" raises the top outcome
  // materialId: the user's material ("D2") this claim comes from; its url is then null
  evidence: { claim: string; source: string; stance: 'supports' | 'opposes' | 'neutral'; asOf: string; url: string | null; materialId?: string }[]
  flags: { type: 'conflict' | 'stale' | 'duplicate' | 'low_information'; note: string }[]
  whatWouldChange: string[]
  confidence: Level
  knowledgeNote: string
  sources: ForecastSource[] // web research pack the specialists saw ([] when search was unavailable; deep: up to S1..S24)
  research: {
    queries: string[] // every query searched, in order (deep: round 1 then round 2)
    // What research the panel actually saw. Absent on older results (derive: replay row → replay,
    // ≥ 1 source → live, else none).
    status?: 'live' | 'wire' | 'carried' | 'replay' | 'none'
    note?: string // absent for live
    // Deep research: absent on older results, replays and when search was disabled.
    depth?: 'deep' | 'quick'
    angles?: ResearchAngle[] // deep only
    rounds?: number // deep only: 1 | 2
    searches?: number // Brave calls the run spent
    candidates?: number // deep only: unique results screened
  }
  // The research ledger, F1..Fn in ledger order. The API always sends an array ([] for quick, wire, none and
  // replay runs); optional here because fixtures and cached older results may lack it.
  findings?: ForecastFinding[]
  watch?: WatchItem[] // 0..4 pending signals; absent when none
  market?: MarketOdds | null // the matching Polymarket market (binary live runs; null when none or for replays)
  changeNote?: string // updates only: what moved the estimate since the previous run (numeric runs: the reasons, in words)
  // Numeric updates whose prior and new runs both have a view: the change, written by the server from the
  // two stored views ("Median $570 → $611 (+7.2%) · chance at or above today 58% → 62%"); absent otherwise.
  viewChange?: string
  marketCheckedAt?: string // when POST /market last searched Polymarket (absent until a search)
  ladder?: MarketOdds[] // ordered live runs, Polymarket's strike ladder, ascending, 2..8 rows; absent when none
  // The volatility-only reference P(Yes) for binary price-rule runs, as of the run; null or absent otherwise.
  reference?: VolatilityReference | null
  guard?: 'shrink' | null // the server reset the desk's P(Yes) to the reference (moved toward 50% uncited)
  // "Desk view and methods": ordered questions whose buckets parse. Older ordered rows get a derived view on
  // GET (no reference); binary and categorical results and unparseable buckets have none of these.
  view?: DeskView
  scenarios?: DeskScenario[] // 2..5, low → high
  methods?: DeskMethods // numeric runs with the method plan on
  measured?: DeskMeasured // when the subject's daily history was measured (H1)
}

/**
 * The desk's distribution read as numbers: median, 50% and 80% ranges and P(at or above today), interpolated
 * from the final buckets (uniform within a range, exponential tails, so a quantile in an open range is approx). Percents
 * are ×100; pAbove is 0..1. `lead` is the server's one-sentence statement of these numbers.
 */
export type DeskView = {
  scale: 'log' | 'linear'
  unit: DeskUnit
  horizonDays: number
  reference: DeskReference | null // today's measured level; null for derived views and when no spot was measured
  median: DeskQ
  p10: DeskQ
  p25: DeskQ
  p75: DeskQ
  p90: DeskQ
  pAbove: DeskQ | null // P(resolves at or above the reference); null unless the basis anchors the centre
  changePct: number | null // median vs the reference (log scale)
  change: number | null // median − reference (linear scale)
  annualPct: number | null // horizons of a year or more
  flat: boolean // no range stands out: the page never says "most likely"
  headline: 'view' | 'bucket'
  edges: { value: number; vsRefPct: number | null }[] // the finite range edges, low → high
  lead: string
  basis: DeskViewBasis
  derived?: boolean // computed on read from an older result stored without a view
}

/**
 * What the view's centre rests on. The desk states a median, a change or P(at or above today) only on evidence: the
 * quant's valuation scenarios with a cited input its source states (`scenarios`), or the measured history's lognormal
 * at the capped drift, an assumption (`capped_drift`). `none` (an older row, a failed measurement, no subject): the
 * ranges only; the median stays in the data but is not a claim. `label` is the server's one line for the page.
 */
export type DeskViewBasis = { kind: 'scenarios' | 'capped_drift' | 'none'; label: string }

/** "$" | "S$" | "NT$" | "€" … before the number, "%" | "bp" after it, as the question's ranges write them. */
export type DeskUnit = { prefix: string; suffix: string }

/** Today's level of the question's subject, from a market data source (never from model or web text). */
export type DeskReference = { value: number; label: string; asOf: string; source: string }

/**
 * One valuation or tail scenario. Its level and inputs come from the tools; p is the desk's final
 * probability over the scenario's range [lo, hi) (so it cannot contradict the buckets), modelP the tool's own weight.
 */
export type DeskScenario = {
  kind: 'tail' | 'bear' | 'base' | 'bull' | (string & {})
  name: string
  p: number
  modelP: number | null
  lo: number | null
  hi: number | null
  level: number
  vsRefPct: number | null
  annualPct: number | null
  inputs: ScenarioInput[]
  thesis: string
  mustBeTrue: string[]
  sourceIds: string[] // F/S/H ids the thesis and conditions cite that exist in this run
}

/** How the specialists' methods were weighed into the final distribution. */
export type DeskMethods = {
  plan: 'long_stock' | 'long_crypto' | 'short' | 'long_level' | (string & {})
  weights: { role: Specialist['role']; method: string; weight: number; why: string | null }[]
  weightsFrom: 'desk' | 'prior'
  pool: OutcomeP[] // the weighted linear pool the aggregator started from
  agreement: 'independent_agree' | 'diverse' | 'duplicate'
  // sharpness | width: the server blended the final halfway back to the pool; volatility: it mixed a final resting on the
  // capped drift toward the lognormal at H1's full-history σ centred on the final's own median (so the median, its
  // change and pace stay; only the width, and the chance with it, move toward the measured volatility), after any blend
  guard: 'sharpness' | 'width' | 'volatility' | null
  note: string // the desk's method note, plus each server check's sentence
}

/** The subject's measured daily history (H1). Percents ×100 (drawdown negative); window multiples are ×. */
export type DeskMeasured = {
  symbol: string
  name: string
  source: string
  currency: string
  from: string // YYYY-MM-DD
  to: string
  sessions: number
  volAnnPct: { y1: number | null; y5: number | null; all: number | null }
  cagrPct: { y5: number | null; y10: number | null; all: number | null }
  maxDrawdown: { pct: number; peak: string; trough: string; recovered: string | null } | null
  window: { days: number; n: number; independent: number; p10: number; p50: number; p90: number; pUp: number } | null
  url: string | null // the listing's history page; null for crypto
}

/** A zero-drift lognormal outlook over 90-day realized volatility; by_date rules use first passage. */
export type VolatilityReference = {
  kind: 'volatility'
  pYes: number
  spot: number
  volAnnPct: number // annualized realized volatility, percent (52 = 52%/yr)
  horizonDays: number
  asOf: string // ISO
}

/** Desk vs market: Polymarket's odds for the same question, priced against the desk's final P(Yes). */
export type MarketOdds = {
  source: 'Polymarket'
  question: string
  url: string // https://polymarket.com/event/<event slug>
  yesPrice: number // 0..1: the market's P(Yes) for our question
  volumeUsd: number | null
  endDate: string // market end, ISO
  match: 'exact' | 'close' // close = same event, date or wording differs
  edgePp: number // (desk P(Yes) − yesPrice) × 100, 1 dp
  evYes: number // EV per $1 Yes contract bought at yesPrice
  evNo: number // per $1 No contract
  signal: 'yes' | 'no' | 'wait'
  thin?: boolean // volumeUsd null or < 1,000: signal is then 'wait'. Absent on older results (derive it from volumeUsd)
  outcome?: string // categorical: the option this market prices ("Nvidia"); the desk side is that option's P
  strike?: number // ladder rows only: yesPrice = P(close above strike), edgePp = (desk P(above) − yesPrice) × 100
  asOf: string
  linkStatus?: 'current' | 'changed' | 'unavailable' | 'legacy'
}

/** A paper YES/NO position on a forecast's matched Polymarket market; settled by the forecast's resolution. */
export type MarketPosition = {
  id: string
  forecastId: string
  forecastQuestion: string
  question: string // the market's, as matched at entry
  url: string
  outcome: string // 'Yes' for binary, else the option the market prices
  side: 'yes' | 'no'
  price: number // per $1 contract, incl. 1¢ slippage
  contracts: number
  cost: number
  maxPayout: number // contracts × $1
  status: 'open' | 'won' | 'lost'
  payout: number | null // null while open
  pnl: number | null // payout − cost once settled
  markPrice: number | null // open: this side's price in the forecast's current market (same question), else null
  createdAt: string
  settledAt: string | null
}

export type ForecastSource = { id: string /* "S1".. */; title: string; url: string; source: string; publishedAt: string | null; read: boolean }

/** One atomic, dated claim from the research, with the sources that state it. Model-written: plain text only. */
export type ForecastFinding = {
  id: string // "F1".., in ledger order
  claim: string // ≤ 300 chars, no URLs
  sourceIds: string[] // 1..4 ids in result.sources ("S3"); a material finding: its material's id ("D2")
  asOf: string | null // "YYYY-MM-DD" | "YYYY-MM" | null
  angle: string // an id in result.research.angles ("A1"); "" for a material finding
  // binary: yes | no | context; ordered: higher | lower | center | tail | context; categorical: an exact outcome or context
  direction: string
  strength: 'strong' | 'moderate' | 'slight' | 'minimal'
  origin?: 'material' // from the user's attached files (numbered after the web ledger); absent for web research
  ref?: string // material findings: where in the file ("p. 4", "Sheet Revenue"); may be ""
}

/** One finding the desk read from a material, in the question's direction vocabulary. Plain text only. */
export type MaterialFinding = {
  claim: string
  ref: string // "p. 4", "Sheet Revenue", "" when unknown
  asOf: string | null
  direction: string
  strength: ForecastFinding['strength']
}

/**
 * A file the user attached to a forecast and what the desk read from it (D1..D5). The bytes are never
 * stored: only this digest. Summary, findings and excerpt are model-written from the user's file: plain text only.
 */
export type ForecastMaterial = {
  id: string // "D1".."D5"
  kind: 'image' | 'document'
  mediaType: string
  name: string // ≤ 120 chars
  size: number
  pages?: number
  status: 'pending' | 'read' | 'failed'
  error?: 'unreadable' | 'timeout' | 'interrupted' // failed only
  summary: string // "" unless read
  relevance?: 'direct' | 'background' | 'unrelated'
  asOf: string | null
  findings: MaterialFinding[] // [] unless read
  excerpt: string // ≤ 2,000 chars of quoted passages; "" unless read
  readAt: string | null
  reusedFrom?: string // the forecast whose digest this copies
}

/** The digest stage in the live trace; null when the run has no materials. */
export type MaterialsStep = {
  status: 'pending' | 'running' | 'done'
  total: number // materials on the run (reused + new)
  reused: number // copied digests: read at once
  read: number // read so far, reused included
  failed: number
}

export type ResearchAngleKind = 'comparables' | 'primary' | 'catalysts' | 'resolution' | 'drivers' | 'sentiment' | 'other'

/** One line of the research plan and what it yielded. */
export type ResearchAngle = { id: string /* "A1".. */; kind: ResearchAngleKind; label: string; queries: string[]; findings: number }

/** A pending signal to watch before the resolution date; branch directions use the findings' vocabulary. */
export type WatchItem = { signal: string; detail: string; branches: { if: string; then: string; direction: string }[] }

/** One phase of the deep research stage in the live trace (append-only, in order). */
export type ResearchStepLog = {
  phase: 'plan' | 'search' | 'screen' | 'read' | 'extract' | 'gap'
  round: number // 0 for plan, 1 or 2 otherwise
  status: 'running' | 'done' | 'failed' | 'skipped'
  count: number // plan: angles · search: unique results · screen: admitted · read: pages read · extract: new findings · gap: queries
  of: number // search: Brave calls · screen: candidates · read: attempted · extract: batches; else 0
}

export type Forecast = ForecastDraft & {
  id: string
  status: ForecastStatus
  archivedFromStatus?: Exclude<ForecastStatus, 'archived'> | null
  error: string | null
  result: ForecastResult | null
  createdAt: string
  completedAt: string | null
  resolution: { outcome: string; resolvedAt: string; by: 'auto' | 'manual' } | null // null = unresolved
  replay: { asOf: string /* YYYY-MM-DD */; anchorP: number /* P(Yes), as-of lognormal outlook */ } | null
  parentId?: string | null // the run this one updates; null for a first run
  history?: ForecastPoint[] // the lineage's completed runs, oldest → newest (this one included when completed); [] for a replay
  progress?: ForecastProgress | null // the run's live trace; null before its first write, for older runs and replays
  materials?: ForecastMaterial[] // the user's attached files (digests only); [] or absent when none
}

/** A run's live pipeline trace, written as each stage moves. */
export type ForecastProgress = {
  research: {
    status: 'pending' | 'running' | 'done'
    queries: string[]
    found: number
    read: number
    mode: 'live' | 'wire' | 'carried' | 'none' | null // set once done
    // Optional because traces stored before it lack them.
    depth?: 'deep' | 'quick' | null // null until the stage picks one
    angles?: string[] // angle labels once planned
    searches?: number // Brave calls so far
    findings?: number // ledger size so far
    steps?: ResearchStepLog[] // [] until the first deep phase
  }
  specialists: { role: Specialist['role']; status: 'pending' | 'running' | 'done' | 'failed'; top: OutcomeP | null /* binary: Yes; else the highest */ }[]
  aggregator: { status: 'pending' | 'running' | 'done' }
  market: { status: 'pending' | 'done' | 'skipped'; matched: boolean }
  materials?: MaterialsStep | null // absent on older traces
  updatedAt: string
}

/** One completed run in a forecast's probability history: its headline outcome (binary: Yes) and p. */
export type ForecastPoint = { id: string; outcome: string; p: number; at: string /* completedAt */; median?: number /* the run's desk median, only where its view stated one */ }

/** Scored resolved forecasts. Numbers are null when n = 0; the anchor fields cover the rows with a volatility reference. */
export type TrackRecord = {
  n: number
  brier: number | null
  baselineBrier: number | null // the uniform forecast's (0.25 binary)
  anchorBrier: number | null // the volatility-only formula's, over the anchorN paired rows; null when none
  logLoss: number | null
  hitRate: number | null // the top outcome happened
  bins: { lo: number; hi: number; n: number; meanP: number | null; observed: number | null }[] // 5 reliability bins
  skill?: number | null // 1 − brier / baselineBrier; null when n = 0
  brierCi90?: [number, number] | null // bootstrap 90% interval of the mean Brier; null when n < 5
  anchorDiffCi90?: [number, number] | null // 90% interval of mean(panel Brier − anchor Brier) over the paired rows; null when anchorN < 5
  // Absent on older payloads (replay-only anchor, every row paired).
  anchorN?: number // rows with an anchor
  anchorSkill?: number | null // 1 − mean(panel Brier on the paired rows) / anchorBrier
  claim?: TrackClaim // what the product may claim; absent on older payloads (read as too_few)
}

/**
 * The score claim the server backs. basis "formula" needs minN paired rows, "uniform" minN rows and none
 * anchored; null is too_few. status is ahead or behind only when the whole 90% range is on one side; skill only when ahead.
 */
export type TrackClaim = {
  status: 'ahead' | 'no_edge' | 'behind' | 'too_few'
  basis: 'formula' | 'uniform' | null
  minN: number // 50
  n: number // the questions counting toward the basis: anchorN when any row is anchored, else n
  skill: number | null // anchorSkill (formula) or skill (uniform), when ahead
}

// ── Strategy studio ─────────────────────────────────────────────────────────
export type Indicator =
  | { kind: 'price' } // close
  | { kind: 'sma' | 'ema' | 'rsi' | 'roc' | 'atr' | 'highest' | 'lowest' | 'volume_sma'; period: number }
  | { kind: 'bb_upper' | 'bb_lower'; period: number; stdDev: number }
  | { kind: 'macd' | 'macd_signal'; fast: number; slow: number; signal: number }
  | { kind: 'volume' }
  | { kind: 'const'; value: number }

export type Condition = { left: Indicator; op: '>' | '<' | 'crosses_above' | 'crosses_below'; right: Indicator }
export type RuleSet = { all?: Condition[]; any?: Condition[] } // both present → all AND (any)

export type StrategySpec = {
  name: string
  symbol: string
  interval: Interval
  bars: number // 200..3000
  longEntry?: RuleSet
  longExit?: RuleSet
  shortEntry?: RuleSet
  shortExit?: RuleSet
  stopLossPct?: number
  takeProfitPct?: number
  positionPct: number // 1..100
  feePct: number // per side
  trailingStopPct?: number // 0.5..50: trails the best price since entry; the tighter of it and the stop applies
}

// Strategies saved before sortino, volatilityAnnPct, calmar and feesPct existed lack them (calmar reads null): render "—".
export type Metrics = {
  totalReturnPct: number
  annualizedReturnPct: number | null // null when the backtest spans under 90 days
  buyHoldReturnPct: number
  sharpe: number
  sortino?: number
  volatilityAnnPct?: number
  calmar: number | null // null when annualized is null or max drawdown is 0
  feesPct?: number // fees paid, % of starting capital
  maxDrawdownPct: number
  winRatePct: number | null // null when no trade closed
  trades: number
  profitFactor: number
  exposurePct: number
  startT: number
  endT: number
  bars: number
  // Quote-feed runs only, the bars a year their ratios are annualized with (their own trading sessions,
  // (bars − 1) × 365 days / (endT − startT)); absent on crypto and perp runs, which annualize 24/7 bars.
  barsPerYear?: number
}

export type BacktestTrade = {
  side: 'long' | 'short'
  entryT: number
  exitT: number
  entry: number
  exit: number
  pnlPct: number
  reason: 'signal' | 'stop_loss' | 'trailing_stop' | 'take_profit' | 'end'
}

export type Point = { t: number; v: number } // unix seconds

export type BacktestResult = { metrics: Metrics; equity: Point[]; benchmark: Point[]; trades: BacktestTrade[] }

export type Strategy = {
  id: string
  name: string
  description: string
  author: string
  prompt: string
  spec: StrategySpec
  metrics: Metrics
  equity: Point[] // starts at 100
  benchmark: Point[] // buy & hold
  trades: BacktestTrade[] // last 100
  explanation: string
  shortSample: boolean // window under 180 days or fewer than 10 trades
  createdAt: string
  community?: CommunityInfo | null // null for built-in Labs (optional for older fixtures)
  forward?: StrategyForward | null // every API body; optional/null only for legacy fixtures
}

export type StrategySummary = {
  id: string
  name: string
  author: string
  symbol: string
  interval: string
  metrics: Metrics
  sparkline: number[] // ≤ 60 equity values
  shortSample: boolean // window under 180 days or fewer than 10 trades: ranked after full samples on Top strategies
  createdAt: string
  community?: CommunityInfo | null // set for scope=mine and scope=community, null for top
  forward?: ForwardSummary | null // every scope; optional/null only for legacy fixtures
}

/**
 * The live-forward (out-of-sample) run, the same rules on every bar after they were fixed. `since` is the
 * publish (basis "published"), the save of an own unpublished one ("saved") or the Labs launch ("launch").
 */
export type ForwardSummary = {
  since: string
  basis: 'published' | 'saved' | 'launch'
  metrics: Metrics | null // null until 3 eligible closed bars, or while anchored history is unavailable
  early: boolean // metrics null, or the window under 30 days, or under 3 trades
  asOf: string | null // newest observed closed candle's close; never request time
  stale: boolean // delayed source, or an older measured run retained after failed refresh
  reason: 'too_new' | 'history_unavailable' | 'market_unavailable' | 'timeout' | 'busy' | null
}

export type StrategyForward = ForwardSummary & {
  equity: Point[] // both start at 100 on the run's first bar; [] while metrics is null
  benchmark: Point[]
  trades: BacktestTrade[] // last 100
}

/** A saved strategy's standing in the community (publish / star / copy). */
export type CommunityInfo = {
  published: boolean
  publishedAt: string | null
  owner: string // the owner's display name
  mine: boolean
  stars: number
  starred: boolean // the caller starred it
  copies: number
  deploys: number // distinct users with an autopilot on it or on a copy
  copiedFrom: { id: string; name: string; owner: string } | null // null when not a copy or the source is no longer visible
}

export type GenerateStrategyRequest = { prompt: string; symbol?: string; interval?: string }

// ── AI backtest arena ───────────────────────────────────────────────────────
export type ArenaCadence = '1d' | '4h'
export type ArenaPersonaId = 'momentum' | 'contrarian' | 'macro' | 'custom'
export type ArenaPersona = { id: ArenaPersonaId; name: string; description: string }
export type ArenaStatus = 'running' | 'completed' | 'failed' | 'cancelled'

export type ArenaOptions = {
  personas: ArenaPersona[]
  knowledgeCutoff: string // YYYY-MM-DD
  latestEnd: string // yesterday UTC
  cadences: { cadence: ArenaCadence; minDays: number; maxDays: number; defaultStart: string; defaultEnd: string }[]
  maxAgents: number
  maxSteps: number
  creditsPerAgentStep: number
}

export type ArenaMetrics = {
  totalReturnPct: number
  annualizedReturnPct: number | null // null under 90 days; persistent live desks can age beyond this
  sharpe: number
  sortino: number
  calmar: number | null
  maxDrawdownPct: number
  volatilityAnnPct: number
  winRatePct: number | null // null without sells
  fills: number
  turnoverPct: number
  exposurePct: number
  feesUsd: number
  vsBtcPct: number
  vsBasketPct: number
}

export type ArenaAgent = {
  index: number
  name: string
  personaId: ArenaPersonaId
  instruction: string
  status: 'active' | 'benched'
  rank: number
  errors: number
  equity: Point[] // USD; [0] = { t: start, v: startingCash }, then one point per finished step
  metrics: ArenaMetrics
  lastReasoning: string
}

export type ArenaBenchmark = { id: 'btc' | 'basket'; name: string; equity: Point[]; metrics: ArenaMetrics } // cut to stepsDone

export type ArenaRun = {
  id: string
  name: string
  status: ArenaStatus
  error: string | null
  cadence: ArenaCadence
  start: string // YYYY-MM-DD, inclusive
  end: string
  steps: number
  stepsDone: number
  universe: string[]
  startingCash: number
  model: string
  knowledgeCutoff: string
  contaminated: boolean
  credits: number // charged minus refunds
  agents: ArenaAgent[]
  benchmarks: ArenaBenchmark[]
  createdAt: string
  completedAt: string | null
}

export type ArenaRunSummary = {
  id: string
  name: string
  status: ArenaStatus
  cadence: ArenaCadence
  start: string
  end: string
  steps: number
  stepsDone: number
  contaminated: boolean
  leader: { name: string; totalReturnPct: number } | null // null until completed
  btcReturnPct: number
  createdAt: string
}

export type ArenaFill = { symbol: string; side: Side; qty: number; price: number; notional: number; fee: number; realizedPnl: number }

export type ArenaStep = {
  step: number
  t: number // decision time
  equity: number // at the step's close
  cash: number
  positions: { symbol: string; qty: number; avgPrice: number; mark: number; weightPct: number }[]
  targets: { symbol: string; weightPct: number }[] | null // null = held
  fills: ArenaFill[]
  tools: { name: string; input: Record<string, unknown>; ok: boolean; summary: string }[]
  reasoning: string // model text, may echo untrusted headlines: render as plain text only
  error: string | null
}

/** `name` (≤ 40) and `instruction` (10..1000, required) are for the custom agent only. */
export type ArenaAgentRequest = { personaId: ArenaPersonaId; name?: string; instruction?: string }
export type CreateArenaRunRequest = { cadence: ArenaCadence; start: string; end: string; agents: ArenaAgentRequest[]; name?: string }

// ── Autopilot (a strategy trading the paper account) ────────────────────────
export type AutopilotLogEntry = { t: string; kind: 'deploy' | 'entry' | 'exit' | 'sync' | 'stop' | 'error'; text: string }

export type Autopilot = {
  id: string
  strategyId: string
  name: string
  symbol: string
  interval: Interval
  status: 'running' | 'stopped'
  stopReason: 'user' | 'max_drawdown' | 'account_disabled' | null
  allocationUsd: number
  maxDrawdownPct: number
  positionQty: number // signed: negative = short
  avgPrice: number
  // These five are null when market data is unavailable.
  markPrice: number | null
  equity: number | null
  pnl: number | null
  pnlPct: number | null
  drawdownPct: number | null
  lastBarT: number | null // open time of the last closed bar it decided on
  nextCheckAt: string | null // while running
  log: AutopilotLogEntry[] // newest first
  startedAt: string
  stoppedAt: string | null
  // The snapshot's risk levels in percent; null = none.
  stopLossPct?: number | null
  takeProfitPct?: number | null
  trailingStopPct?: number | null
}

export type DeployAutopilotRequest = {
  strategyId: string
  allocationUsd: number
  maxDrawdownPct: number
  // Override the spec's levels; every deploy needs a stop (or trailing stop) and a target.
  stopLossPct?: number
  takeProfitPct?: number
  trailingStopPct?: number
}

// ── AI portfolios ───────────────────────────────────────────────────────────
export type AIPortfolio = {
  id: string
  name: string
  thesis: string
  prompt: string
  holdings: { symbol: string; weight: number; rationale: string; driver?: HoldingDriver | null }[] // driver null on older portfolios (optional for older fixtures)
  backtest: {
    benchmarkSymbol: 'BTC' | 'SP500'
    benchmarkLabel: string
    windowStart: number
    windowEnd: number
    bars: number
    series: Point[] // common consecutive closed daily bars, normalized to 100
    benchmark: Point[] // the persisted selected comparison
    returnPct: number
    benchmarkReturnPct: number
    maxDrawdownPct: number
    volatilityAnnPct: number | null
  }
  createdAt: string
  overlap: PortfolioOverlap | null // response-local caller projection; absent from list/build
  invested?: AIPortfolioInvested | null // the caller's paper position in it; null until invested (optional for older fixtures)
}

export type AIPortfolioInvested = {
  benchmarkSymbol: 'BTC' | 'SP500'
  benchmarkLabel: string
  benchmarkReturnPct: number | null
  benchmarkSeries: Point[] | null
  windowStart: number | null
  windowEnd: number | null
  bars: number
  amount: number // Σ buy notional of its invest legs
  trades: number
  firstAt: string
  realizedPnl: number
  unrealizedPnl: number
  totalPnl: number
  returnPct: number // totalPnl / amount × 100
  btcReturnPct: number | null // BTC-only compatibility; null for SP500 or unavailable history
  series: Point[] | null // null on list; detail's measured paper-buy index includes fees, so need not start at 100
  btcSeries: Point[] | null // BTC-only compatibility; null for SP500, list or unavailable history
}

/** Why the AI holds an asset: a desk forecast (id = the forecast), the wire's sentiment, or available daily-history stats. */
export type HoldingDriver = { kind: 'forecast' | 'sentiment' | 'stats'; id: string | null; label: string }

// ---------------------------------------------------------------- Market intelligence (Brave Search + crawl4ai)

/** GET /api/news?region= and NewsItem.regions. */
export type NewsRegion = 'crypto' | 'us' | 'east_asia' | 'southeast_asia'

export type NewsItem = {
  id: string
  title: string
  url: string
  source: string
  description: string
  publishedAt: string | null
  symbols: string[]
  topic: 'asset' | 'macro'
  regions: NewsRegion[] // a story can touch several; [] = global or unclassified
  hasContent: boolean
}

export type Sentiment = { symbol: string; score: number /* -1..1 */; label: 'bullish' | 'bearish' | 'neutral'; reason: string }

/** A wire item a brief's bullets cite, as stored with the brief (GET /api/news/brief), so a citation outlives the wire's newest 100. */
export type BriefSource = Pick<NewsItem, 'id' | 'title' | 'url' | 'source' | 'publishedAt'>

export type MarketBrief = {
  id: string
  headline: string
  summary: string
  bullets: { text: string; symbols: string[]; sources: string[] /* NewsItem ids */ }[]
  sources?: BriefSource[] // every cited id's item; absent from APIs older than the brief's stored sources
  sentiment: Sentiment[]
  regions?: { region: NewsRegion; summary: string }[] // optional per-region breakdown
  articleCount: number
  window: { from: string; to: string }
  createdAt: string
}

/** GET /api/news/pulse: alternative.me's Crypto Fear & Greed Index (daily), null until the server has fetched it. */
export type MarketPulse = {
  fearGreed: { value: number /* 0..100 */; label: string; asOf: string; source: 'alternative.me' } | null
}

// ── Signals board & account performance ─────────────────────────────────────
export type SignalSide = 'bullish' | 'bearish'

/**
 * A pick on the momentum screen (rule steady-v1): it stays until its target (2 ATR in its favour), its invalidation
 * (2 ATR against it) or validUntil (pickedAt + 72 h), when it leaves and is scored. score, factors and volTier are this
 * board's; the other fields are from the pick. A pick of a board stored before steady-v1 (a legacy board, shown until
 * the first steady refresh replaces it) has invalidation 0: the steady rule drops it unrecorded and never scores it.
 */
export type SignalPick = {
  symbol: string
  side: SignalSide
  score: number // -100..100
  priceAtPick: number
  pickedAt: string
  target: number // priceAtPick ± 2 × ATR14, never model-moved
  invalidation: number // priceAtPick ∓ 2 × ATR14, 4 significant figures (may be ≤ 0 when 2 ATR is over half the price); 0 on a legacy board
  validUntil: string
  volTier: 'low' | 'medium' | 'high'
  factors: {
    return7dPct: number
    return30dPct: number
    rsi14: number
    trend: 'uptrend' | 'downtrend' | 'range'
    volAnnPct: number
    drawdownPct: number // below the 50-day high
    sentiment: number | null // -1..1, from the latest brief when under 24 h old
  }
  rationale: string // model text: render as plain text
  headlines: { title: string; url: string; source: string }[] // ≤ 2, untrusted web text
}

export type SignalBoard = {
  id: string
  createdAt: string
  nextAt: string // createdAt + 4 h − 10 min: when the next refresh starts, so the new board lands before this one is 4 h old
  bullish: SignalPick[] // ≤ 4, strongest first
  bearish: SignalPick[]
  method: string
}

/**
 * GET /api/signals `refresh`: "running" while a refresh runs, "due" when one should start, "retrying" while the last
 * one failed and the next try waits for retryAt, else "idle". failures counts the failed refreshes in a row.
 */
export type SignalsRefresh = {
  state: 'running' | 'due' | 'retrying' | 'idle'
  failures: number
  lastErrorAt: string | null
  retryAt: string | null
}

/**
 * GET /api/signals. `board` is the newest stored board that still validates, null only when none does; `refreshing`
 * is true while a refresh runs or is due (always with a null board); `firstBoard` is true only when no board has ever
 * been stored. The three are absent from APIs older than the never-empty board and then read as false. `refresh` is
 * absent from older APIs. `historyUnavailable` is true when a 1h candle read for the legacy rows failed: they are then
 * left out whole, and the ledger's exits still show (before steady-v1, every candle-read row: history was then []).
 */
export type SignalsResponse = {
  board: SignalBoard | null
  history: RemovedPick[]
  refreshing?: boolean
  asOf?: string | null // the shown board's createdAt; null with no board
  firstBoard?: boolean
  historyUnavailable?: boolean
  refresh?: SignalsRefresh
  record?: SignalRecord // always sent from steady-v1 on; absent from older APIs, which keep no ledger
}

/**
 * One side's track record over scored picks (each scored 72 h after its pick against a random pick from the same
 * list). The excess CI is 99%: the record is re-read every 4 h. Nulls while nothing is scored (the CI also while
 * fewer than 2 three-day blocks have a pick). verdict is "insufficient" until 200 scored picks over 30 blocks.
 */
export type SignalRecordSide = {
  scored: number
  blocks: number
  movedItsWayPct: number | null
  randomMovedItsWayPct: number | null
  avgResultPct: number | null
  avgResultNetPct: number | null
  avgRandomPct: number | null
  excessPct: number | null
  excessLo99: number | null
  excessHi99: number | null
  targetHit: number
  invalidated: number
  expired: number
  verdict: 'insufficient' | 'no_difference' | 'better' | 'worse'
}

/** One side of the out-of-sample replay (95% CIs). */
export type SignalReplaySide = {
  picks: number
  targetFirstPct: number
  invalidatedFirstPct: number
  expiredPct: number
  randomTargetFirstPct: number
  randomInvalidatedFirstPct: number
  excessPct: number
  excessLo95: number
  excessHi95: number
}

/** GET /api/signals `record`: the live ledger's record since the first steady-v1 pick, and the replay reference. */
export type SignalRecord = {
  rule: 'steady-v1'
  since: string | null // the earliest pick; null before any
  horizonHours: 72
  costPct: 0.1
  minPicks: 200
  minBlocks: 30
  confidence: 0.99
  open: number // unscored and picked within the last 7 days
  unscored: number // unscored and older than that
  all: SignalRecordSide
  bullish: SignalRecordSide
  bearish: SignalRecordSide
  replay: {
    from: string // date-only
    to: string
    picks: number
    days: number
    blocks: number
    movedItsWayPct: number
    randomMovedItsWayPct: number
    avgResultPct: number
    avgResultNetPct: number
    avgRandomPct: number
    excessPct: number
    excessLo95: number
    excessHi95: number
    bullish: SignalReplaySide
    bearish: SignalReplaySide
  }
}

// ── Asia screener: GET /api/screener/asia, recomputed weekly ──────────
export type ScreenerFilter = 'all' | 'below' | 'deep' | 'distressed' | 'approaching' | 'oversold'
export type ScreenerMarket = 'JP' | 'KR' | 'TW' | 'HK' | 'SG' | 'ID' | 'TH' | 'MY' | 'PH' | 'VN'
export type ScreenerSortKey =
  | 'ticker' | 'name' | 'market' | 'price' | 'priceUsd' | 'pctVsSma' | 'direction' | 'rsi' | 'drawdown' | 'athAt' | 'updated'
export type ScreenerSort = ScreenerSortKey | `-${ScreenerSortKey}` // "-" = descending; rows without the value come last
export type ScreenerRow = {
  ticker: string // Yahoo's, e.g. "7203.T"
  code: string
  name: string // Yahoo's name once read, else the list's
  listName: string
  market: ScreenerMarket
  indices: { index: string; source: string; sourceKind: 'official' | 'secondary'; asOf: string }[]
  currency: string // local (Yahoo's once read)
  status: 'ok' | 'insufficient_history' | 'unavailable' | 'failed' | 'pending'
  statusNote: string | null
  price: number | null // local currency
  priceAt: string | null
  priceUsd: number | null
  fx: { pair: string; usdPerUnit: number; at: string } | null // null for USD listings
  weeks: number | null
  sma200w: number | null
  pctVsSma: number | null // % vs the 200-week average; null under 200 weeks
  pctVsSmaPrev: number | null
  direction: 'deepening' | 'recovering' | null
  rsi14w: number | null
  ath: number | null
  athAt: string | null // YYYY-MM-DD, exchange-local: the week (its Monday) of the highest weekly high
  athFrom: string | null // YYYY-MM-DD: the high covers Yahoo's bars from here
  drawdownPct: number | null // ≤ 0
  updatedAt: string | null
  checkedAt: string | null
  tradableSymbol: string | null
  tradeVia: 'listing' | 'adr' | null
  tradeNote: string | null
  // The listing's own current quote, which `price` and the price-derived columns follow (never an ADR's or a perp's).
  // null: no live quote, so the row's price is the run's. Absent from an API before the live screener (rows keep the
  // run's values and dates then).
  quote?: ScreenerQuote | null
}
/** A screener row's live quote: whose it is, when it last traded, how far behind the exchange it runs, and how fresh. */
export type ScreenerQuote = {
  source: QuoteSource
  tier: 'trade' | 'screener' // trade: the market layer's quote-feed cache (the Trade page's quote); screener: the screener-only tier
  price: number // local currency, = row.price
  currency: string
  lastTradeAt: string // the source's last-trade time, UTC
  readAt: string // when the server last read it
  delayMinutes: number | null // how far the source runs behind the exchange (stated or measured); null unknown
  delayBasis: 'stated' | 'measured' | null
  session: 'open' | 'closed' | 'unknown' // the listing's regular session now
  refreshSeconds: number | null // the tier's re-read interval now (a real-time source's: 10, 30); null while held to the next open
  staleAt: string // without a newer read the quote is stale after this
  stale: boolean // at response time
  // Real-time quotes (RT-QUOTES §5), absent before them; a real-time quote's delayMinutes is delaySeconds / 60.
  class?: QuoteClass
  delaySeconds?: number
  timeBasis?: QuoteTimeBasis
  label?: string
}
export type ScreenerRun = {
  status: 'running' | 'completed' | 'failed'
  startedAt: string
  finishedAt: string | null
  tickers: number
  done: number
  ok: number
  insufficient: number
  unavailable: number
  failed: number
  error: string | null
  durationSeconds?: number // finished − started (elapsed while running); absent before the live screener
  yahooCalls?: number | null // HTTP requests the run sent to Yahoo, retries included; null for runs before it counted
}
export type ScreenerList = {
  index: string
  market: ScreenerMarket
  count: number
  asOf: string
  sourceKind: 'official' | 'secondary' | 'mixed'
  sources: string[]
}
export type ScreenerPage = {
  rows: ScreenerRow[]
  nextCursor: string | null
  total: number // rows matching filter + market + q
  counts: Record<ScreenerFilter, number> // per filter within market + q; the API lists every filter, 0 included
  markets: { code: ScreenerMarket; name: string; count: number }[]
  asOf: string | null
  run: ScreenerRun | null
  lastCompletedAt: string | null
  nextRunAt: string
  source: { prices: string; fx: string; lists: ScreenerList[] }
  method: string
  // The live screener's (absent before it: no in-place refresh then): how often to refresh the loaded rows, and the
  // run schedule as one sentence.
  pollSeconds?: number
  schedule?: string
}
/** GET /api/screener/asia/quotes?tickers=…: the loaded rows again (same shape and values as the page), for in-place refresh. */
export type ScreenerQuotes = {
  rows: ScreenerRow[] // request order
  unknown: string[] // tickers the screener does not list
  asOf: string | null
  run?: ScreenerRun | null
  lastCompletedAt: string | null
  pollSeconds: number
}
export type ScreenerQuery ={ filter?: ScreenerFilter; market?: ScreenerMarket; q?: string; sort?: ScreenerSort; limit?: number; cursor?: string }

export type EquityRange = '1d' | '7d' | '30d'
export type EquityCurve = { range: EquityRange; interval: '15m' | '1h' | '4h'; startingBalance: number; points: Point[] } // USD

/** P&L of the lots each trade source opened (first in, first out); realizedPnl is net of its fees. */
export type SourcePnl = {
  source: string
  trades: number
  realizedPnl: number
  unrealizedPnl: number
  fees: number
  totalPnl: number
}

// ── Portfolio health ───────────────────────────────────────────────
export type ConcentrationLevel = 'low' | 'medium' | 'high'
export type ReviewHolding = {
  symbol: string
  side: 'long' | 'short'
  value: number // |qty × mark|
  pct: number // of gross exposure
  beta: number | null // to BTC over the window; null under 20 shared days
  stopLoss: number | null
  halted: boolean
  markAvailable: boolean
  stopStatus: 'valid' | 'dormant' | 'crossed' | 'missing' | 'unknown'
}
export type ReviewCoverage = { modeledGrossExposure: number; missingSymbols: string[]; commonDays: number; reason: string | null }
export type ReviewPair = { a: string; b: string; rho: number | null; days: number }
export type PortfolioOverlap = { revision: number; asOf: string; coverage: ReviewCoverage; holdings: { symbol: string; side: 'long' | 'short'; bookPct: number; basketPct: number; shared: boolean }[]; correlations: ReviewPair[] }
/** GET /api/portfolio/review: the open book at live marks against the last 90 daily closes. Percentages are ×100. */
export type PortfolioReview = {
  asOf: string
  revision: number
  window: { days: number; bars: number } // requested returns / aligned grid size
  coverage: ReviewCoverage
  positions: number
  equity: number
  grossExposure: number
  netExposure: number
  exposurePct: number | null
  predictionStakes: number
  predictionStakesPct: number | null
  holdings: ReviewHolding[] // largest first
  concentration: { level: ConcentrationLevel; top1Pct: number; top3Pct: number; hhi: number; effective: number } | null
  betaBtc: number | null // a 1% BTC move ≈ betaBtc % of equity
  volAnnPct: number | null
  var: { pct: number; usd: number; cvarUsd: number } | null // 1-day 95% historical VaR; a loss, ≥ 0
  worstDay: { date: string; pnl: number; pnlPct: number } | null
  drawdown30dPct: number | null
  stress: { scenario: string; shockPct: number; estPnl: number; estPnlPct: number }[]
  correlations: ReviewPair[]
  clusters: { symbols: string[]; rho: number; pct: number; days: number }[] // all-pairs directional groups
  stops: { covered: number; uncovered: string[]; uncoveredPct: number; atRiskUsd: number }
  notes: string[] // server-written facts
}

// ── Signals history & strategy optimizer ──────────────────────────
/**
 * A pick that left the board (GET /api/signals `history`: at most 12, newest removedAt first): a ledger exit (steady-v1)
 * or, while a board stored before steady-v1 is retained (7 days after the switch), a legacy row (`legacy: true`).
 * removedAt is the first board without it. exitPrice is the level for target_hit / invalidated, or the last 1h close
 * at or before validUntil for expired; on a legacy row, the last 1h close at or before removedAt (exitAt: that bar's
 * close). returnPct is the exit move signed in the pick's direction. result72Pct / random72Pct are the fixed 72 h score
 * (the pick and a random pick from the same list, both in the pick's direction), null until scoredAt and absent on a
 * legacy row, which is never scored.
 */
export type RemovedPick = {
  symbol: string
  side: SignalSide
  pickedAt: string
  removedAt: string
  priceAtPick: number
  target: number
  invalidation: number // 0 on a legacy row
  exitAt: string
  exitPrice: number
  returnPct: number
  result72Pct?: number | null // null until scoredAt; absent on a legacy row
  random72Pct?: number | null
  scoredAt?: string | null
  // A ledger exit: target_hit | invalidated | expired. A legacy row (the reasons read from the board stored
  // before steady-v1): target_hit | expired, else what the next board's refresh recorded: outranked (still above the
  // threshold, pushed out by stronger picks), score_fell, no_data (no fresh analysis or bar), or left (none recorded:
  // boards stored before reasons were recorded, and the switch to steady-v1).
  reason: 'target_hit' | 'invalidated' | 'expired' | 'score_fell' | 'outranked' | 'no_data' | 'left'
  legacy?: true // a pick of a board stored before steady-v1: never in the ledger, never scored, never in `record`
}

/** One stop/target pair's backtest; null = none. */
export type OptimizeCell = {
  stopLossPct: number | null
  takeProfitPct: number | null
  sharpe: number
  totalReturnPct: number
  maxDrawdownPct: number
  trades: number
}
export type OptimizeResult = { grid: OptimizeCell[]; best: OptimizeCell[]; current: OptimizeCell } // grid: 5 stops × 5 targets, stops outer

// ── Automations: price alerts, paper limit / stop-entry orders, the notifications bell ──
export type AlertOp = 'above' | 'below'
export type AlertAction = 'notify' | 'buy' | 'sell' | 'research'
export type NotifyWhen = 'always' | 'changed' | 'invalidated'
export type Thesis = { statement: string; invalidation: string }
export type ResearchVerdict = 'holds' | 'weakened' | 'invalidated' | 'unknown'
export type AlertStatus = 'active' | 'fired' | 'failed' | 'cancelled' | 'paused'
export type AlertTrigger = 'price' | 'schedule' | 'odds' | 'drawdown' // schedules; odds on a forecast's Polymarket market (notify only); all-time-high drawdowns
export type AlertEvery = 'day' | 'weekdays' | 'week' // weekdays: Monday to Friday
export type AlertRun = {
  id: string; at: string; status: 'ok' | 'failed' | 'running' | 'interrupted' | 'cancelled'
  body: string; error: string | null; reportChatId: string | null; excerpt: string | null
  verdict: ResearchVerdict | null
}
export type AlertRunPage = { runs: AlertRun[]; nextCursor: string | null }

export type Alert = {
  id: string
  symbol: string
  prompt: string | null
  thesis: Thesis | null
  notifyWhen: NotifyWhen | null
  quotaStreak: number
  trigger: AlertTrigger
  op: AlertOp | null // price only: above fires at mark ≥ price, below at mark ≤ price; checked every 15 s
  price: number | null
  every: AlertEvery | null // schedules only
  at: string | null // 'HH:MM' UTC
  weekday: number | null // 0 = Sunday, weekly only
  nextRunAt: string | null
  lastRun: AlertRun | null // a schedule's newest run
  action: AlertAction
  notional: number | null // USD, buy/sell only
  stopLoss: number | null
  takeProfit: number | null
  note: string
  source: 'manual' | 'ticket' | 'agent'
  proposalId: string | null // the agent card it came from
  status: AlertStatus
  tradeId: string | null // the fill of a buy/sell alert
  error: string | null // why its order failed
  firedPrice: number | null // odds: the Yes price that fired it
  createdAt: string
  firedAt: string | null
  forecastId?: string | null // odds only (symbol is then '', price the Yes level 0..1); the API sends null otherwise
  market?: OddsAlertMarket | null // odds only: the forecast's stored market, fresh after each re-price
  marketSlug?: string | null // direct odds: unique leaf, never event slug
  marketId?: string | null
  linkedMarket?: LinkedMarket | null
  drawdown?: Drawdown | null // drawdown triggers only; the API sends null otherwise
  currency?: string | null // price triggers: the level's currency when not USD (price and firedPrice in it); null is USD
}

/**
 * What a drawdown trigger measures from (GET /api/alerts/ath): the instrument's all-time high, where it came
 * from and what it covers. Binance daily highs since listing (crypto), the listing's Yahoo lifetime high (USD stocks,
 * ADRs and ETFs, quote feeds) or the perp's own history ("perp history since …, not the listing's lifetime high").
 */
export type AllTimeHigh = {
  ath: number // in currency; ratchets up to a higher checked price
  athAt: string // its daily bar's open (UTC), or the check that raised it
  athLive: boolean // set by a checked price, not read from the daily history
  coveredFrom: string // the first daily bar read
  source: 'binance' | 'yahoo' | 'perp'
  sourceLabel: string
  ticker: string // whose highs: 'BTCUSDT', 'NVDA', 'xyz:KIOXIA', 'D05.SI'
  currency: string // 'USD', or a quote-feed listing's currency
  basis: 'mark' | 'listing' // the price compared: the USD price, or a quote feed's listing price in currency
}

/** GET /api/alerts/ath: the high a drawdown trigger on symbol would use, and the current price it compares. */
export type AthQuote = AllTimeHigh & { symbol: string; price: number; fromAthPct: number }

/** A drawdown trigger (Alert.drawdown): fires once at price ≤ ath × (1 − pct / 100). */
export type Drawdown = AllTimeHigh & {
  pct: number // 0.01..99.99
  sellPct: number | null // action sell: the whole % of the current paper long sold (never opens or adds to a short)
  threshold: number // in currency
  outcome: 'notified' | 'sold' | 'nothing_to_sell' | null // once fired
  price: number | null // the current price and its distance from the high: active triggers only, null when unavailable
  fromAthPct: number | null
}

/** An odds trigger's market: the forecast's result.market as the alert list reads it. */
export type OddsAlertMarket = { question: string; url: string; outcome: string | null; yesPrice: number; asOf: string }

/** POST /api/alerts. Not crossed yet: an above alert needs price > mark, a below one price < mark. */
export type AlertRequest = {
  symbol?: string // optional for general scheduled research
  prompt?: string // research only, 10..500 Unicode code points
  thesis?: Thesis // each field at most 300 code points
  notifyWhen?: NotifyWhen // research only; default always
  trigger?: AlertTrigger // default price
  op?: AlertOp // price only
  price?: number
  every?: AlertEvery // schedule only
  at?: string // 'HH:MM' UTC
  weekday?: number // week only, 0 = Sunday
  action?: AlertAction // default notify
  notional?: number // buy/sell: ≥ 10
  stopLoss?: number
  takeProfit?: number
  note?: string // ≤ 200 chars
  source?: Alert['source'] // default manual
  proposalId?: string
  forecastId?: string // odds only
  marketSlug?: string // direct odds: exactly one of marketSlug or forecastId
  marketId?: string // optional direct numeric-ID cross-check
  drawdownPct?: number // drawdown only: 0.01..99.99 below the all-time high
  sellPct?: number // drawdown sell only: 1..100 (default 100)
  currency?: string // price notify only: a quote-feed stock's own currency (its quoteFeed.currency); omitted is USD
}

/** The agent's price_alert card; nothing is set until Set alert. */
export type PriceAlertProposal = {
  proposalId: string
  symbol: string
  prompt?: string
  thesis?: Thesis
  notifyWhen?: NotifyWhen
  trigger?: AlertTrigger // absent = price (older cards)
  op: AlertOp | null // null for a schedule, as are price, markPrice and distancePct
  price: number | null
  markPrice: number | null
  currency?: string | null // price notify proposal: original listing currency; null/absent is USD for older cards
  distancePct: number | null // (price − markPrice) / markPrice × 100
  every?: AlertEvery | null
  at?: string | null
  weekday?: number | null
  action: AlertAction
  notional: number | null
  stopLoss: number | null
  takeProfit: number | null
  note: string
  forecastId?: string // odds cards: symbol '', price the Yes level, markPrice the market's stored Yes price
  market?: { question: string; url: string; outcome: string | null }
  // drawdown cards (set_drawdown_alert): op and price null, markPrice the current price, distancePct its
  // distance to the threshold
  drawdownPct?: number
  sellPct?: number
  ath?: AllTimeHigh
  threshold?: number
  fromAthPct?: number
}

export type NotificationKind =
  | 'alert'
  | 'alert_order'
  | 'alert_failed'
  | 'odds_alert' // a fired prediction-market odds trigger, linking to its forecast
  | 'drawdown_alert' // a drawdown trigger fired (notify, or nothing to sell)
  | 'drawdown_order' // its sale of the paper long filled
  | 'drawdown_failed' // its sale was refused
  | 'workflow' // a schedule's brief
  | 'workflow_order'
  | 'workflow_failed'
  | 'workflow_research'
  | 'stop_loss'
  | 'take_profit'
  | 'autopilot'
  | 'copy_order'
  | 'copy_stopped'
  | 'forecast_completed'
  | 'forecast_failed'
  | 'forecast_resolved'

/** One bell item, derived on read from the last 14 days. */
export type Notification = { id: string; kind: NotificationKind; title: string; body: string; symbol: string | null; link: string; at: string }

// Bell items also sent by email (SES) and to the user's own Lark custom bot (contract "Notification channels").
export type NotificationChannels = {
  email: { enabled: boolean; address: string; available: boolean } // available: the server can send email
  lark: { enabled: boolean; webhook: string | null; signed: boolean } // webhook masked; signed: a signing secret is saved
}
export type UpdateNotificationChannels = { email?: boolean; lark?: boolean; larkWebhook?: string; larkSecret?: string }
export type ChannelTestResult = { email: string; lark: string } // "sent" | "off" | the send's error

// Shared live paper desk; no public provider/model identity.
export type LiveDesk = {
  enabled: boolean; status: 'live' | 'starting' | 'paused' | 'stale' | 'outage'
  startedAt: string | null; lastStepAt: string | null; nextStepAt: string | null
  cadence: '4h'; startingCash: 100000; universe: string[]; steps: number
  method: string; lastError: string | null; stale: boolean
  historyStartAt: string | null; historyLimited: boolean; chartWindowBars: 360
}
export type LivePosition = { symbol: string; qty: number; avgPrice: number; mark: number; marketValue: number; unrealizedPnl: number; weightPct: number }
export type LiveAgent = {
  id: string; personaId: 'momentum' | 'contrarian' | 'macro'; name: string; mandate: string
  status: 'active' | 'benched'; benchedUntil: string | null; rank: number; errors: number; steps: number
  equity: Point[]; cash: number; equityNow: number; positions: LivePosition[]; metrics: ArenaMetrics
  lastReasoning: string; lastStepAt: string | null; lastError: string | null; followers: number
}
export type LiveBenchmark = { id: 'btc' | 'basket'; name: string; equity: Point[]; metrics: ArenaMetrics }
export type LiveBoard = { desk: LiveDesk; agents: LiveAgent[]; benchmarks: LiveBenchmark[]; myPlans: CopyPlan[] }
export type CopyPlan = {
  id: string; agentId: string; agentName: string; personaId: LiveAgent['personaId']; ratio: number; maxUsd: number
  status: 'active' | 'stopped'; stopReason: 'user' | 'account_disabled' | null
  positions: { symbol: string; qty: number }[]; deployedUsd: number | null; invested: number | null; trades: number
  realizedPnl: number | null; unrealizedPnl: number | null; pnl: number | null
  log: { t: string; kind: 'start' | 'order' | 'skip' | 'sync' | 'stop'; text: string }[]
  createdAt: string; stoppedAt: string | null
}
