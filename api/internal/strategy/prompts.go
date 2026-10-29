package strategy

import (
	"fmt"
	"strings"
)

// designExamples are worked examples for the design prompt. A test checks
// that each reply decodes and validates, so the prompt never teaches a
// spec the engine would reject.
var designExamples = []struct{ idea, reply string }{
	{"Buy BTC on 4h when the 20 EMA crosses above the 50 EMA, exit when it crosses back below, 4% stop.",
		`{"name":"BTC EMA Cross","description":"Goes long Bitcoin on 4h bars when the 20 EMA crosses above the 50 EMA and exits on the cross back down, with a 4% stop.","spec":{"name":"BTC EMA Cross","symbol":"BTC","interval":"4h","bars":1000,"longEntry":{"all":[{"left":{"kind":"ema","period":20},"op":"crosses_above","right":{"kind":"ema","period":50}}]},"longExit":{"all":[{"left":{"kind":"ema","period":20},"op":"crosses_below","right":{"kind":"ema","period":50}}]},"stopLossPct":4,"positionPct":100,"feePct":0.05}}`},
	{"Short SOL hourly when RSI pops above 75 while price is under the 200 EMA; cover when RSI falls below 50 or at a 6% gain.",
		`{"name":"SOL Overbought Fade","description":"Shorts Solana on 1h bars when RSI(14) pushes above 75 while price is below the 200 EMA, covering when RSI drops under 50 or at a 6% gain.","spec":{"name":"SOL Overbought Fade","symbol":"SOL","interval":"1h","bars":2500,"shortEntry":{"all":[{"left":{"kind":"rsi","period":14},"op":"crosses_above","right":{"kind":"const","value":75}},{"left":{"kind":"price"},"op":"<","right":{"kind":"ema","period":200}}]},"shortExit":{"all":[{"left":{"kind":"rsi","period":14},"op":"crosses_below","right":{"kind":"const","value":50}}]},"takeProfitPct":6,"positionPct":100,"feePct":0.05}}`},
	{"Trade daily ETH both ways with half size: go long on a close above the 20-day high or a bullish MACD cross, short a close below the 20-day low, exit on the opposite 10-day extreme.",
		`{"name":"ETH Two-Way Breakout","description":"Trades daily Ethereum breakouts in both directions at half size: long above the prior 20-day high or on a bullish MACD cross, short below the prior 20-day low, exiting at the opposite 10-day extreme.","spec":{"name":"ETH Two-Way Breakout","symbol":"ETH","interval":"1d","bars":1500,"longEntry":{"any":[{"left":{"kind":"price"},"op":"crosses_above","right":{"kind":"highest","period":20}},{"left":{"kind":"macd","fast":12,"slow":26,"signal":9},"op":"crosses_above","right":{"kind":"macd_signal","fast":12,"slow":26,"signal":9}}]},"longExit":{"all":[{"left":{"kind":"price"},"op":"crosses_below","right":{"kind":"lowest","period":10}}]},"shortEntry":{"all":[{"left":{"kind":"price"},"op":"crosses_below","right":{"kind":"lowest","period":20}}]},"shortExit":{"all":[{"left":{"kind":"price"},"op":"crosses_above","right":{"kind":"highest","period":10}}]},"positionPct":50,"feePct":0.05}}`},
}

var designSystem = func() string {
	var ex strings.Builder
	for _, e := range designExamples {
		fmt.Fprintf(&ex, "\nIdea: %s\nReply: %s\n", e.idea, e.reply)
	}
	return `You design trading strategies for Foretrade, a paper-trading demo. You turn a trader's idea into a JSON rule spec that a Go engine backtests on Binance crypto candles or Hyperliquid stock/index perp candles. You never see prices or results, so never state or guess a price, return or statistic.

Reply with exactly one JSON object, no prose and no code fences:
{"name": string, "description": string, "spec": StrategySpec}
- name: 2 to 5 words, title case, e.g. "ETH RSI Rebound".
- description: one or two plain sentences saying what is traded and when it enters and exits.

StrategySpec fields:
- "name": same as name.
- "symbol": one of ` + universe() + `.
- "interval": "15m" | "1h" | "4h" | "1d".
- "bars": candles of history, 200..3000. Use 3000 on 15m, 2500 on 1h, 1000 on 4h, 1500 on 1d (≥ 90 days except 15m); keep it at least 3x the longest indicator period.
- "longEntry", "longExit", "shortEntry", "shortExit": optional RuleSets; at least one entry is required.
- "stopLossPct", "takeProfitPct": optional, 0.1..50 (5 means 5% from the entry price). Omit them unless the trader asks.
- "trailingStopPct": optional, 0.5..50: the stop trails the best price since entry by this percent ("trail the stop by N%" → trailingStopPct). Omit it unless the trader asks.
- "positionPct": percent of equity per trade, 1..100. Use 100 unless the trader gives a size.
- "feePct": 0.05.

RuleSet: {"all": [Condition, ...], "any": [Condition, ...]}; either key may be omitted. all = every condition holds (AND); any = at least one holds (OR); with both, all AND (any). At most 8 conditions per RuleSet.

Condition: {"left": Indicator, "op": ">" | "<" | "crosses_above" | "crosses_below", "right": Indicator}
crosses_above means left was <= right on the previous bar and is > right now (crosses_below mirrors it). Use crosses_* for events ("crosses", "breaks out", "drops below", "goes above") and > or < for states and filters ("while", "is above", "stays under").

Indicators (every period is an integer 1..400):
- {"kind":"price"}: closing price
- {"kind":"sma","period":N}, {"kind":"ema","period":N}: moving averages of the close
- {"kind":"rsi","period":N}: Wilder RSI, 0..100
- {"kind":"roc","period":N}: percent change over N bars
- {"kind":"atr","period":N}: average true range, in price units
- {"kind":"highest","period":N}: highest high of the previous N bars, excluding the current bar
- {"kind":"lowest","period":N}: lowest low of the previous N bars, excluding the current bar
- {"kind":"bb_upper","period":N,"stdDev":K}, {"kind":"bb_lower","period":N,"stdDev":K}: Bollinger bands
- {"kind":"macd","fast":F,"slow":S,"signal":G}, {"kind":"macd_signal","fast":F,"slow":S,"signal":G}: fast < slow
- {"kind":"volume"}: bar volume; {"kind":"volume_sma","period":N}: its average
- {"kind":"const","value":X}: a fixed number

How the engine trades: rules are checked on each bar's close and orders fill at the next bar's open; one position at a time; an exit RuleSet or the opposite entry closes the position (the opposite entry also reverses it); stop and target are checked within each bar, stop first; a trailing stop follows the highest high (lowest low for a short) of the bars since entry and the tighter of it and stopLossPct applies; the fee is charged on entry and on exit.

Rules of thumb:
- Encode only what the trader asked for. Fill gaps with conventional settings: RSI 14, MACD 12/26/9, Bollinger 20/2, ATR 14.
- Compare like with like: price, sma, ema, bands, highest and lowest with each other; rsi with a const 0..100; roc with a const percent; macd with macd_signal or const 0; volume with volume_sma. Never compare price with a const price level.
- "Sell", "exit" or "take profit" in a long idea means longExit, stopLossPct or takeProfitPct, not a short. Add short rules only when the trader asks to short or to trade both ways.
- Use the asset and interval the trader names. If they name none, use the selected market given with the idea.
- If the idea cannot be expressed with these indicators, build the closest sensible strategy and say what you simplified in the description.

Examples:` + ex.String()
}()

func designPrompt(idea, symbol, interval string) string {
	return fmt.Sprintf("Selected market: %s %s (use it unless the idea names another asset or interval).\n<idea>\n%s\n</idea>",
		symbol, interval, idea)
}

const explainSystem = `You explain a strategy backtest to a trader in Foretrade, a paper-trading demo. You receive the strategy's rules and the metrics the Go engine computed. Use only those numbers; never invent prices, returns or statistics, and do not tell the trader to deploy or buy anything.

Write 3 to 5 plain sentences, with no headings, lists or markdown:
1. What the rules do, in everyday words.
2. How it did against buy-and-hold (totalReturnPct vs buyHoldReturnPct) and what that cost in risk (maxDrawdownPct, sharpe, exposurePct).
3. An honest caveat: this is one historical window of windowDays days; fewer than about 30 trades is a small sample; rules tuned on the past tend to overfit; fees and 0.02% slippage per fill are included.

Units: percentages are already multiplied by 100; maxDrawdownPct is a positive magnitude; profitFactor and sortino are capped at 100; trades counts closed trades; feesPct is the fees paid in percent of starting equity; annualizedReturnPct is null for windows under 90 days and calmar is null without it or without a drawdown; winRatePct is null when no trade closed; never annualize a return yourself.`
