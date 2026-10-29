package market

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// US and Asian stocks and indices trade here as trade.xyz's perps on
// Hyperliquid's xyz dex (HIP-3; public info API, no key): coin "xyz:NVDA",
// "xyz:SMSN". Marks are USD, distinct from oracle/candle prices and are not
// underlying exchange prints; trade.xyz's oracle converts a KRW, JPY or HKD
// underlying at the prevailing FX rate. Paper orders fill at the perp mark24/7.
// Provider oracle-update age is unavailable. trade.xyz runs the oracle and can
// halt or delist a market: one that is missing, delisted or unpriced is
// dropped, like a non-core Binance pair.

// perpCtx is one market's numbers in a metaAndAssetCtxs answer (all strings).
type perpCtx struct{ Funding, OpenInterest, Premium, MarkPx, PrevDayPx, DayNtlVlm string }

// dexBook is one xyz-dex answer by universe symbol (live, priced markets
// only) and when it was read, or the error reading it: both are cached for
// assetsTTL, so Assets and Derivatives share one upstream call.
type dexBook struct {
	bySymbol map[string]perpCtx
	at       time.Time
	err      error
}

// dayRange is a perp's high and low over the last 24 hourly bars.
type dayRange struct{ hi, lo float64 }

// rangeTTL: hourly highs and lows move slowly. Each perp's 24-bar
// candleSnapshot weighs about 21 (20 per info request plus 1 per 60 bars
// returned), so the 38 perps every 10 s would be ~4,800 weight a minute,
// four times Hyperliquid's per-IP budget (1,200 weight a minute). Every 5
// minutes they cost ~800 in one burst, plus 120 a minute for the xyz context
// read (weight 20, cached assetsTTL) and 20 for the crypto one
// (derivativesTTL): under 80% of the burst minute's budget and about a quarter
// of the average, leaving the rest to history reads.
const rangeTTL = 5 * time.Minute

// xyzMarkets is the cached xyz-dex answer.
func (c *Client) xyzMarkets(ctx context.Context) (dexBook, error) {
	return c.xyzMarketsTTL(ctx, assetsTTL)
}
func (c *Client) xyzMarketsTTL(ctx context.Context, ttl time.Duration) (dexBook, error) {
	book, err := c.xyz.get(ctx, ttl, func() (dexBook, error) {
		m, err := c.fetchMeta(ctx, "xyz")
		if errors.Is(err, context.Canceled) {
			return dexBook{}, err // the caller gave up: not worth caching
		}
		by := map[string]perpCtx{}
		for _, a := range StockPerps {
			if x, ok := m[a.Pair]; ok {
				by[a.Symbol] = x
			}
		}
		return dexBook{by, time.Now().UTC(), err}, nil
	})
	return book, cmp.Or(err, book.err)
}

// perpQuotes is the stock and index rows of Assets: the mark, its change from
// the price 24 h before (prevDayPx), 24h notional volume, the 24h high and low
// from hourly candles when available. Missing measured bounds remain unavailable;
// session metadata is projected at response read time. nil when xyz fails.
func (c *Client) perpQuotes(ctx context.Context) map[string]Asset {
	return c.perpQuotesWithFresh(ctx, false)
}
func (c *Client) perpQuotesWithFresh(ctx context.Context, fresh bool) map[string]Asset {
	ttl := assetsTTL
	if fresh {
		ttl = 0
	}
	book, err := c.xyzMarketsTTL(ctx, ttl)
	if err != nil {
		slog.Warn("market: stock perps dropped, hyperliquid failed", "err", err)
		return nil
	}
	ranges := c.dayRanges(ctx)
	out := map[string]Asset{}
	for _, a := range StockPerps {
		x, ok := book.bySymbol[a.Symbol]
		if !ok {
			continue
		}
		mark, prev := num(x.MarkPx), num(x.PrevDayPx)
		as := Asset{Symbol: a.Symbol, Pair: a.Pair, Name: a.Name, Price: mark, Volume24hUsd: num(x.DayNtlVlm),
			Range24hSource: "unavailable", Class: a.Class, Venue: a.Venue}
		if prev > 0 {
			as.Change24hPct = math.Round((mark/prev-1)*100_000) / 1000 // 3 dp, like Binance's priceChangePercent
			if !finite(as.Change24hPct) {
				continue
			} // an overflowing scalar cannot poison the mixed DTO
		}
		if r, ok := ranges[a.Symbol]; ok {
			as.High24h, as.Low24h = max(r.hi, mark), min(r.lo, mark)
			as.Range24hSource = "hourly_candles"
		}
		out[a.Symbol] = as
	}
	return out
}

// dayRanges reads each perp's last 24 hourly bars, at once, at most every
// rangeTTL. A failed read leaves that symbol out.
func (c *Client) dayRanges(ctx context.Context) map[string]dayRange {
	m, _ := c.ranges.get(ctx, rangeTTL, func() (map[string]dayRange, error) {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		got := make([]dayRange, len(StockPerps))
		var wg sync.WaitGroup
	loop:
		for i, a := range StockPerps {
			// The range owner admits before spawning and joins all owners below.
			select {
			case c.historySlots <- struct{}{}:
			case <-rctx.Done():
				break loop
			}
			wg.Go(func() {
				defer func() { <-c.historySlots }()
				bars, err := c.fetchPerpCandlesAdmitted(rctx, a.Pair, "1h", 24, 0)
				if err != nil {
					return
				}
				r := dayRange{math.Inf(-1), math.Inf(1)}
				for _, b := range bars {
					r.hi, r.lo = max(r.hi, b.H), min(r.lo, b.L)
				}
				got[i] = r
			})
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err // the caller gave up: try again next time
		}
		out := map[string]dayRange{}
		for i, a := range StockPerps {
			if got[i].lo > 0 && got[i].hi >= got[i].lo {
				out[a.Symbol] = got[i]
			}
		}
		return out, nil
	})
	return m
}

// fetchPerpCandles reads one candleSnapshot: the newest limit bars of coin
// that opened at or before endTime (unix ms; 0 = now). Hyperliquid serves a
// time window (the bars overlapping [startTime, endTime], at most 5,000, the
// newest ones it keeps), so the window is limit bars wide and the answer is
// trimmed to limit. Empty history before endTime is not an error.
func (c *Client) fetchPerpCandles(ctx context.Context, coin, interval string, limit int, endTime int64) ([]Candle, error) {
	select {
	case c.historySlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.historySlots }()
	return c.fetchPerpCandlesAdmitted(ctx, coin, interval, limit, endTime)
}

func (c *Client) fetchPerpCandlesAdmitted(ctx context.Context, coin, interval string, limit int, endTime int64) ([]Candle, error) {
	end := endTime
	if end == 0 {
		end = time.Now().UnixMilli()
	}
	body, err := json.Marshal(map[string]any{"type": "candleSnapshot", "req": map[string]any{
		"coin": coin, "interval": interval, "startTime": max(int64(0), end-int64(limit)*BarSeconds(interval)*1000), "endTime": end}})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		T        int64  `json:"t"`
		Symbol   string `json:"s"`
		Interval string `json:"i"` // open time, unix ms
		// The close time; declared so encoding/json, which matches keys
		// case-insensitively, cannot write "T" into the open time.
		CloseT int64  `json:"T"`
		Open   string `json:"o"`
		High   string `json:"h"`
		Low    string `json:"l"`
		Close  string `json:"c"`
		Volume string `json:"v"` // in shares (index units), like Binance's base volume
	}
	if err := c.info(ctx, string(body), &rows); err != nil {
		return nil, err
	}
	out := make([]Candle, 0, len(rows))
	for _, r := range rows {
		values := []string{r.Open, r.High, r.Low, r.Close, r.Volume}
		parsed := make([]float64, 5)
		for i, v := range values {
			f, e := strconv.ParseFloat(v, 64)
			if e != nil || !finite(f) || (i < 4 && f <= 0) || (i == 4 && f < 0) {
				return nil, fmt.Errorf("%w: invalid hyperliquid OHLCV", ErrUpstream)
			}
			parsed[i] = f
		}
		o, h, l, cl, v := parsed[0], parsed[1], parsed[2], parsed[3], parsed[4]
		if r.Symbol != coin || r.Interval != interval || r.T <= 0 || r.T%1000 != 0 || r.CloseT != r.T+BarSeconds(interval)*1000-1 || h < max(o, cl) || l > min(o, cl) || l > h {
			return nil, fmt.Errorf("%w: malformed hyperliquid candle identity/range/time", ErrUpstream)
		}
		if r.T > end {
			continue
		}
		out = append(out, Candle{T: r.T / 1000, O: o, H: h, L: l, C: cl, V: v})
	}
	slices.SortFunc(out, func(a, b Candle) int { return cmp.Compare(a.T, b.T) })
	// Identical duplicates are harmless; conflicting duplicates are not history.
	unique := out[:0]
	for _, b := range out {
		if len(unique) > 0 && unique[len(unique)-1].T == b.T {
			if unique[len(unique)-1] != b {
				return nil, fmt.Errorf("%w: conflicting duplicate hyperliquid candle", ErrUpstream)
			}
			continue
		}
		unique = append(unique, b)
	}
	out = unique

	out = out[max(len(out)-limit, 0):]
	if s, ok := splits[coin]; ok {
		if out, err = c.adjustSplit(ctx, coin, interval, s, out); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 && endTime == 0 {
		return nil, fmt.Errorf("%w: no candles for %s %s", ErrUpstream, coin, interval)
	}
	return out, nil
}

// split is a corporate action trade.xyz left unadjusted in a perp's history:
// at is when the mark switched to post-split units, ratio the new shares per old.
type split struct {
	at    time.Time
	ratio float64
}

// splits by perp. Every candle read (charts, indicators, backtests, AI
// portfolios, signals and any high computed from history) goes through
// fetchPerpCandlesAdmitted, so all of them see post-split units.
//
// Kioxia (TSE 285A), 3-for-1, effective 2026-09-28. Read from the xyz dex on
// 2026-10-06: hourly bars through 06:00Z that day still trade near 345, the
// 07:00Z bar has no trades and the 08:00Z bar opens at 114.93; the daily close
// goes 357.0 (09-27) → 114.12 (09-28) while daily notional stays in line.
// Candle volume is in contracts, so it scales up by the ratio as prices scale down.
var splits = map[string]split{
	"xyz:KIOXIA": {time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC), 3},
}

// finer is the next shorter interval, used to rebuild a bar a split falls inside.
var finer = map[string]string{"1d": "4h", "4h": "1h", "1h": "15m"}

// adjustSplit restates bars in post-split units: prices of bars that closed
// before s.at divided by the ratio, their volume multiplied by it. A bar s.at
// falls inside (Kioxia's 2026-09-28 daily bar opens pre-split and closes
// post-split) is rebuilt from its adjusted finer bars; when those are no
// longer retained it is left out rather than served half-adjusted.
func (c *Client) adjustSplit(ctx context.Context, coin, interval string, s split, bars []Candle) ([]Candle, error) {
	at, step := s.at.Unix(), BarSeconds(interval)
	out := make([]Candle, 0, len(bars))
	for _, b := range bars {
		switch {
		case b.T+step <= at:
			out = append(out, Candle{T: b.T, O: b.O / s.ratio, H: b.H / s.ratio, L: b.L / s.ratio, C: b.C / s.ratio, V: b.V * s.ratio})
		case b.T < at:
			rebuilt, ok, err := c.splitBar(ctx, coin, interval, b.T)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, rebuilt)
			} else {
				slog.Warn("market: split-day bar left out, finer history unavailable", "pair", coin, "interval", interval, "t", b.T)
			}
		default:
			out = append(out, b)
		}
	}
	return out, nil
}

// splitBar aggregates the interval bar opening at t from its finer bars
// (themselves split-adjusted). A closed result is kept for the client's
// lifetime: history does not change.
func (c *Client) splitBar(ctx context.Context, coin, interval string, t int64) (Candle, bool, error) {
	sub, step := finer[interval], BarSeconds(interval)
	if sub == "" {
		return Candle{}, false, nil
	}
	key := fmt.Sprintf("%s/%s/%d", coin, interval, t)
	c.mu.Lock()
	b, ok := c.splitBars[key]
	c.mu.Unlock()
	if ok {
		return b, true, nil
	}
	subStep := BarSeconds(sub)
	parts, err := c.fetchPerpCandlesAdmitted(ctx, coin, sub, int(step/subStep), (t+step)*1000-1)
	if err != nil {
		return Candle{}, false, err
	}
	parts = slices.DeleteFunc(parts, func(p Candle) bool { return p.T < t || p.T >= t+step })
	open := t+step > time.Now().Unix()
	if len(parts) == 0 || parts[0].T != t || (!open && len(parts) != int(step/subStep)) {
		return Candle{}, false, nil
	}
	b = Candle{T: t, O: parts[0].O, H: parts[0].H, L: parts[0].L, C: parts[len(parts)-1].C}
	for i, p := range parts {
		if p.T != t+int64(i)*subStep {
			return Candle{}, false, nil // a gap: not the whole bar
		}
		b.H, b.L, b.V = max(b.H, p.H), min(b.L, p.L), b.V+p.V
	}
	if !open {
		c.mu.Lock()
		if c.splitBars == nil {
			c.splitBars = map[string]Candle{}
		}
		c.splitBars[key] = b
		c.mu.Unlock()
	}
	return b, true, nil
}

// fetchMeta reads one dex's metaAndAssetCtxs ("" = Hyperliquid's own perps)
// as its live markets by coin name: delisted and unpriced ones left out.
func (c *Client) fetchMeta(ctx context.Context, dex string) (map[string]perpCtx, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body := `{"type":"metaAndAssetCtxs"}`
	if dex != "" {
		body = `{"type":"metaAndAssetCtxs","dex":"` + dex + `"}`
	}
	var answer []json.RawMessage // [meta, assetCtxs], aligned by index
	if err := c.info(ctx, body, &answer); err != nil {
		return nil, err
	}
	var meta struct {
		Universe []struct {
			Name       string `json:"name"`
			IsDelisted bool   `json:"isDelisted"`
		} `json:"universe"`
	}
	var ctxs []perpCtx
	if len(answer) != 2 || json.Unmarshal(answer[0], &meta) != nil || json.Unmarshal(answer[1], &ctxs) != nil || len(ctxs) != len(meta.Universe) {
		return nil, fmt.Errorf("%w: hyperliquid: unexpected metaAndAssetCtxs answer", ErrUpstream)
	}
	out := map[string]perpCtx{}
	seen := map[string]bool{}
	for i, u := range meta.Universe {
		if u.Name == "" || seen[u.Name] {
			return nil, fmt.Errorf("%w: duplicate/empty hyperliquid identity", ErrUpstream)
		}
		seen[u.Name] = true
		x := ctxs[i]
		mark := num(x.MarkPx)
		if !u.IsDelisted && finite(mark) && mark > 0 {
			out[u.Name] = x
		}
	}

	return out, nil
}

// info POSTs body to Hyperliquid's info API and decodes the answer into out.
func (c *Client) info(ctx context.Context, body string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cmp.Or(c.HyperliquidBase, "https://api.hyperliquid.xyz")+"/info", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if errors.Is(err, context.Canceled) {
		return err // the caller gave up; Hyperliquid did not fail
	}
	if err != nil {
		return fmt.Errorf("%w: hyperliquid: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: hyperliquid: status %d", ErrUpstream, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("%w: hyperliquid: decode: %w", ErrUpstream, err)
	}
	return nil
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
