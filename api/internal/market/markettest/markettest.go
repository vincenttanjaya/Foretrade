// Package markettest is a fake Binance market-data API for tests: the 24h
// ticker and klines endpoints with deterministic series for any pair, movable
// ticker prices, switchable outages and halts. It also answers Hyperliquid's
// POST /info metaAndAssetCtxs (point Client.HyperliquidBase at URL): BTC
// funding 0.00125 %/h and OI $1.2B, SOL negative funding, TON delisted, PAXG
// unlisted. It does not import market, so market's own tests use it.
package markettest

import (
	"cmp"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// End is every series' newest fixed fixture bar; it may already be closed relative to the caller's clock.
var End = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

const history = 5000 // bars available per pair and interval

var base = map[string]float64{
	"BTCUSDT": 100_000, "ETHUSDT": 4000, "SOLUSDT": 200, "BNBUSDT": 600, "XRPUSDT": 2.5, "DOGEUSDT": 0.2,
	"xyz:NVDA": 235, "xyz:TSLA": 369, "xyz:AAPL": 332, "xyz:MSFT": 521, "xyz:AMZN": 251, "xyz:GOOGL": 342, "xyz:META": 726, "xyz:SP500": 7722, "xyz:XYZ100": 30762,
	// The Asian perps at their 2026-10-06 marks, USD.
	"xyz:TSM": 485, "xyz:BABA": 110.6, "xyz:SMSN": 202.5, "xyz:SKHX": 1325, "xyz:HYUNDAI": 259, "xyz:SOFTBANK": 39.9, "xyz:MINIMAX": 31.9, "xyz:ZHIPU": 91.6, "xyz:EWJ": 99.7, "xyz:EWY": 188.7,
	// The second batch at their 2026-10-06 marks, USD (KIOXIA post-split).
	"xyz:PLTR": 189.91, "xyz:COIN": 188.34, "xyz:HOOD": 114.35, "xyz:MSTR": 164.85, "xyz:INTC": 116.09, "xyz:AMD": 631.95, "xyz:ORCL": 143.42,
	"xyz:NFLX": 67.83, "xyz:CRCL": 83.665, "xyz:MU": 1057.9, "xyz:SNDK": 1694.5, "xyz:CRWV": 87.94,
	"xyz:SKHY": 190.35, "xyz:CXMT": 8.3641, "xyz:UNITREE": 68.413, "xyz:GIGADEV": 52.267, "xyz:KIOXIA": 118.68, "xyz:EWT": 118.16, "xyz:KSTR": 22.27,
	"ADAUSDT": 0.8, "AVAXUSDT": 30, "LINKUSDT": 20, "SUIUSDT": 3, "TONUSDT": 5, "PAXGUSDT": 2600,
}

var seconds = map[string]int64{"15m": 900, "1h": 3600, "4h": 14400, "1d": 86400}

// KioxiaSplit is when xyz:KIOXIA's mark switched to post-split units (3-for-1).
// Like the real dex, the fake serves its history unadjusted: bars that closed
// before then are three times the series (volume unchanged, in contracts) and
// the daily bar across the switch opens there.
var KioxiaSplit = time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)

// Price is a pair's last price: its ticker lastPrice and the close of its newest bar.
func Price(pair string) float64 {
	if p, ok := base[pair]; ok {
		return p
	}
	return 10
}

// Close is the close k bars before the newest one: a rising trend with two
// oscillations, so indicators, crossovers and stops all get exercised.
func Close(pair string, k int) float64 {
	x := float64(k)
	return Price(pair) * math.Exp(-0.0004*x) * (1 + 0.06*math.Sin(x/7) + 0.02*math.Sin(x/2.3))
}

type Binance struct {
	URL string

	mu        sync.Mutex
	down      map[string]bool    // pair → failing; "" → every request
	halted    map[string]bool    // pair → halted (Halt); "" → every pair
	price     map[string]float64 // pair → ticker lastPrice override (SetPrice)
	delisted  map[string]bool
	requests  map[string]int
	contexts  map[string]map[string]any
	retention map[string]int
	rows      map[string][]map[string]any
}

func New(t testing.TB) *Binance {
	t.Helper()
	b := &Binance{down: map[string]bool{}, halted: map[string]bool{}, price: map[string]float64{}, delisted: map[string]bool{}, requests: map[string]int{}, contexts: map[string]map[string]any{}, retention: map[string]int{}, rows: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(srv.Close)
	b.URL = srv.URL
	return b
}

// Down makes pair's requests fail ("" = every request) until called with false.
// A down pair is left out of the ticker; its klines answer 500.
func (b *Binance) Down(pair string, down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down[pair] = down
}

// Halt makes pair halted ("" = every pair) until called with false: its ticker
// row keeps its last price and trade count but its closeTime is weeks old, as
// Binance answered for TONUSDT (status BREAK) on 2026-10-05. The klines keep
// their series.
func (b *Binance) Halt(pair string, halted bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.halted[pair] = halted
}

// SetPrice moves pair's ticker lastPrice to p, as a live market would; the
// klines keep their fixed series.
func (b *Binance) SetPrice(pair string, p float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.price[pair] = p
}

// Delist controls upstream xyz metadata independently of informational US sessions.
func (b *Binance) Delist(pair string, on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.delisted[pair] = on
}
func (b *Binance) SetContext(pair, field string, value any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.contexts[pair] == nil {
		b.contexts[pair] = map[string]any{}
	}
	b.contexts[pair][field] = value
}
func (b *Binance) SetRetention(pair string, bars int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retention[pair] = bars
}
func (b *Binance) SetCandleRows(pair string, rows []map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rows[pair] = rows
}
func (b *Binance) Requests(key string) int { b.mu.Lock(); defer b.mu.Unlock(); return b.requests[key] }
func (b *Binance) record(key string)       { b.mu.Lock(); defer b.mu.Unlock(); b.requests[key]++ }

func (b *Binance) last(pair string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := b.price[pair]; ok {
		return p
	}
	return Price(pair)
}

// closeTime is pair's ticker closeTime: now, or 619 h back when halted.
func (b *Binance) closeTime(pair string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.halted[""] || b.halted[pair] {
		return time.Now().Add(-619 * time.Hour).UnixMilli()
	}
	return time.Now().UnixMilli()
}

func (b *Binance) isDown(pair string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.down[""] || b.down[pair]
}

func (b *Binance) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if b.isDown("") {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case "/api/v3/ticker/24hr":
		b.record("binance:ticker")
		if b.isDown("binance") {
			http.Error(w, "down", 500)
			return
		}
		var pairs []string
		json.Unmarshal([]byte(q.Get("symbols")), &pairs)
		rows := []map[string]any{}
		for _, p := range pairs {
			if b.isDown(p) {
				continue
			}
			rows = append(rows, map[string]any{"symbol": p, "lastPrice": num(b.last(p)), "count": 5000, "closeTime": b.closeTime(p),
				"priceChangePercent": num(math.Round((Price(p)/Close(p, 24)-1)*10000) / 100),
				"highPrice":          num(Price(p) * 1.02), "lowPrice": num(Price(p) * 0.97), "quoteVolume": num(Price(p) * 1e4)})
		}
		json.NewEncoder(w).Encode(rows)
	case "/api/v3/klines":
		pair, iv := q.Get("symbol"), seconds[q.Get("interval")]
		if b.isDown(pair) || iv == 0 {
			http.Error(w, "bad request", http.StatusInternalServerError)
			return
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		k0 := int64(0)
		if end, err := strconv.ParseInt(q.Get("endTime"), 10, 64); err == nil {
			k0 = max(0, (End.Unix()*1000-end+iv*1000-1)/(iv*1000)) // first bar opening at or before endTime
		}
		rows := [][]any{}
		for k := min(k0+int64(limit), history) - 1; k >= k0; k-- {
			o, c := Close(pair, int(k+1)), Close(pair, int(k))
			open := (End.Unix() - k*iv) * 1000
			rows = append(rows, []any{open, num(o), num(max(o, c) * 1.005), num(min(o, c) * 0.995), num(c),
				num(1000 + 400*math.Sin(float64(k)/3)), open + iv*1000 - 1})
		}
		json.NewEncoder(w).Encode(rows)
	case "/info":
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 400)
			return
		}
		var in struct {
			Type, Dex string
			Req       struct {
				Coin, Interval     string
				StartTime, EndTime int64
			}
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad body", 400)
			return
		}
		if in.Type == "candleSnapshot" {
			pair, iv := in.Req.Coin, seconds[in.Req.Interval]
			b.record("candle:" + pair)
			if iv == 0 || !strings.HasPrefix(pair, "xyz:") || b.isDown(pair) || b.isDown("xyz") {
				http.Error(w, "down", 500)
				return
			}
			b.mu.Lock()
			override, custom := b.rows[pair]
			retained, ok := b.retention[pair]
			b.mu.Unlock()
			if custom {
				json.NewEncoder(w).Encode(override)
				return
			}
			if !ok {
				retained = history
				if in.Req.Interval == "1d" {
					retained = 320
					if pair == "xyz:SP500" {
						retained = 202
					}
				}
			}
			out := []map[string]any{}
			for k := retained - 1; k >= 0; k-- {
				ms := (End.Unix() - int64(k)*iv) * 1000
				if ms > in.Req.EndTime || ms+iv*1000 <= in.Req.StartTime {
					continue
				}
				o, c := Close(pair, k+1), Close(pair, k)
				if cut := KioxiaSplit.UnixMilli(); pair == "xyz:KIOXIA" && ms < cut {
					o *= 3
					if ms+iv*1000 <= cut {
						c *= 3
					}
				}
				out = append(out, map[string]any{"t": ms, "T": ms + iv*1000 - 1, "s": pair, "i": in.Req.Interval, "o": num(o), "h": num(max(o, c) * 1.005), "l": num(min(o, c) * .995), "c": num(c), "v": "1000"})
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		if in.Type != "metaAndAssetCtxs" {
			http.Error(w, "unknown type", 400)
			return
		}
		key := "crypto:meta"
		names := []string{"BTC", "ETH", "SOL", "BNB", "XRP", "DOGE", "ADA", "AVAX", "LINK", "SUI", "TON", "HYPE"}
		if in.Dex == "xyz" {
			key = "xyz:meta"
			names = []string{"xyz:NVDA", "xyz:TSLA", "xyz:AAPL", "xyz:MSFT", "xyz:AMZN", "xyz:GOOGL", "xyz:META", "xyz:SP500", "xyz:XYZ100",
				"xyz:TSM", "xyz:BABA", "xyz:SMSN", "xyz:SKHX", "xyz:HYUNDAI", "xyz:SOFTBANK", "xyz:MINIMAX", "xyz:ZHIPU", "xyz:EWJ", "xyz:EWY",
				"xyz:PLTR", "xyz:COIN", "xyz:HOOD", "xyz:MSTR", "xyz:INTC", "xyz:AMD", "xyz:ORCL", "xyz:NFLX", "xyz:CRCL", "xyz:MU", "xyz:SNDK", "xyz:CRWV",
				"xyz:SKHY", "xyz:CXMT", "xyz:UNITREE", "xyz:GIGADEV", "xyz:KIOXIA", "xyz:EWT", "xyz:KSTR"}
		}
		b.record(key)
		if b.isDown(in.Dex) && in.Dex != "" {
			http.Error(w, "down", 500)
			return
		}
		universe := []map[string]any{}
		ctxs := []map[string]any{}
		for _, name := range names {
			if b.isDown(name) {
				continue
			}
			pair := name
			if in.Dex == "" {
				pair = name + "USDT"
			}
			b.mu.Lock()
			delisted := b.delisted[name]
			overrides := map[string]any{}
			for k, v := range b.contexts[name] {
				overrides[k] = v
			}
			b.mu.Unlock()
			universe = append(universe, map[string]any{"name": name, "isDelisted": delisted || name == "TON"})
			funding := cmp.Or(map[string]string{"BTC": "0.0000125", "SOL": "-0.00002"}[name], "0.00001")
			x := map[string]any{"funding": funding, "openInterest": "12000", "premium": "0.0002", "markPx": num(b.last(pair)), "oraclePx": num(Price(pair)), "prevDayPx": num(Price(pair) * .98), "dayNtlVlm": "2500000000"}
			for k, v := range overrides {
				x[k] = v
			}
			ctxs = append(ctxs, x)
		}
		json.NewEncoder(w).Encode([]any{map[string]any{"universe": universe}, ctxs})
	default:
		http.NotFound(w, r)
	}
}

func num(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
