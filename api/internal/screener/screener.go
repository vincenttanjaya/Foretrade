// Package screener is the Asia screener (contract "Asia screener"): the
// constituents of ten North Asian and Southeast Asian indices, screened
// against their 200-week moving average in the manner of mungbeans.io, with
// the 14-week RSI and the drawdown from the all-time high. A job (Run) reads
// each listing's lifetime weekly Yahoo chart every weekday after the Asian
// close and stores the computed row; GET /api/screener/asia prices each row at
// its listing's live quote (the market layer's quote feeds, or its watch tier
// for listings no instrument quotes), derives the price columns from it
// against the stored averages, and filters, sorts and pages them. No user
// credits are spent and nothing is traded.
package screener

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

const (
	defaultLimit = 50
	maxLimit     = 100
	maxQuery     = 64
	defaultSort  = "pctVsSma"
	maxQuotes    = 100 // tickers per GET /api/screener/asia/quotes
	pollSeconds  = 60  // how often a client should refresh the rows it shows

	// sourcePrices follows "Prices: " in every web build's footer, so it never
	// starts with its own label.
	sourcePrices = "each listing's own freshest quote: a keyless real-time (SGX: near-real-time) feed of the exchange's " +
		"prices where one is switched on and answering well (KRX, HKEX, HOSE, SGX, TWSE; each row's quote names its source), " +
		"else Yahoo Finance's regular-market quote (keyless, unofficial and delayed); while its market is open a quote is " +
		"read every 10 seconds to 15 minutes, and each row's quote says how far behind it is; the averages are from each " +
		"listing's lifetime weekly bars from Yahoo's chart API, split-adjusted, in its local currency, read by the latest run"
	sourceFX = "Yahoo Finance {CCY}USD=X: the rate the quote feeds hold now (its newest daily close), else the run's"
	// method never calls a delayed price live.
	method = "Price is the listing's own latest quote (the run's price when there is none). The averages are the latest " +
		"run's: weekly closes are the last close of each exchange-local week, the newest possibly in progress. % vs 200WMA = " +
		"(price ÷ the run's mean of the last 200 weekly closes − 1) × 100, n/a under 200 weeks. Direction compares it with " +
		"last week's close against the average ending then. Drawdown = (price ÷ the higher of the run's all-time high and " +
		"the price − 1) × 100, so a new high reads 0% on today's date; the all-time high is the highest weekly high since " +
		"Yahoo's first bar (athFrom). RSI is the run's Wilder RSI over 14 weekly closes. Filters, counts, sorts and the " +
		"market list use these same values. Recomputed every weekday at 10:45 UTC."
	schedule = "Every weekday at 10:45 UTC, after the last Asian close (SET's closing auction ends about 09:40 UTC), and at " +
		"boot when no run completed in the last day or since the latest 10:45 slot; a failed run is retried after 3 hours."
)

func Register(mux *http.ServeMux, d *app.Deps) {
	var q quoteSource
	if d.Market != nil {
		q = d.Market
	}
	register(mux, d, q)
}

func register(mux *http.ServeMux, d *app.Deps, q quoteSource) {
	h := &handler{d: d, quotes: q}
	mux.Handle("GET /api/screener/asia", d.Auth.Require(http.HandlerFunc(h.asia)))
	mux.Handle("GET /api/screener/asia/quotes", d.Auth.Require(http.HandlerFunc(h.live)))
}

// quoteSource is where the rows' live quotes come from: the market layer's
// quote feeds (market.Client.ListingQuotes), which read nothing on the call.
type quoteSource interface {
	ListingQuotes(tickers []string) map[string]market.ListingQuote
}

type handler struct {
	d      *app.Deps
	quotes quoteSource // nil: every row keeps the run's price
}

// Row is one screened listing (contract ScreenerRow).
type Row struct {
	Ticker         string       `json:"ticker"`
	Code           string       `json:"code"`
	Name           string       `json:"name"`     // Yahoo's long name once read, else the list's
	ListName       string       `json:"listName"` // as the constituent list names it
	Market         string       `json:"market"`
	Indices        []Membership `json:"indices"`
	Currency       string       `json:"currency"`
	Status         string       `json:"status"` // ok | insufficient_history | unavailable | failed | pending
	StatusNote     *string      `json:"statusNote"`
	Price          *float64     `json:"price"` // local currency
	PriceAt        *time.Time   `json:"priceAt"`
	PriceUSD       *float64     `json:"priceUsd"`
	FX             *FX          `json:"fx"`
	Weeks          *int         `json:"weeks"`
	SMA200w        *float64     `json:"sma200w"`
	PctVsSMA       *float64     `json:"pctVsSma"`
	PctVsSMAPrev   *float64     `json:"pctVsSmaPrev"`
	Direction      *string      `json:"direction"` // deepening | recovering
	RSI14w         *float64     `json:"rsi14w"`
	ATH            *float64     `json:"ath"`
	ATHAt          *string      `json:"athAt"`   // YYYY-MM-DD, the exchange-local date of the bar (a week's Monday)
	ATHFrom        *string      `json:"athFrom"` // YYYY-MM-DD, the first weekly bar's
	DrawdownPct    *float64     `json:"drawdownPct"`
	UpdatedAt      *time.Time   `json:"updatedAt"` // when the values were computed
	CheckedAt      *time.Time   `json:"checkedAt"` // the latest attempt, successful or not
	TradableSymbol *string      `json:"tradableSymbol"`
	TradeVia       *string      `json:"tradeVia"` // listing | adr
	TradeNote      *string      `json:"tradeNote"`
	// Quote is the live quote the row is priced at; nil when there is none
	// (no Yahoo data, none read yet, or the run's price is newer).
	Quote *Quote `json:"quote"`

	change *float64 // PctVsSMA − PctVsSMAPrev, unrounded: the direction column's sort
}

// Quote is a row's live quote: whose it is, how old, and how fresh the tier
// keeps it (contract ScreenerQuote).
type Quote struct {
	// Source is whose price: "yahoo", or a real-time source ("naver" on KRX;
	// market.ListingQuote); Class "delayed" (Yahoo), "realtime" or
	// "near-realtime".
	Source string `json:"source"`
	Class  string `json:"class"`
	// Tier: "trade" (the quote feed of the instrument trading this listing:
	// the Trade page's quote) or "screener" (the screener-only watch tier).
	Tier        string    `json:"tier"`
	Price       float64   `json:"price"` // local currency: the row's price
	Currency    string    `json:"currency"`
	LastTradeAt time.Time `json:"lastTradeAt"` // the source's last-trade time
	ReadAt      time.Time `json:"readAt"`      // this server's read
	// DelayMinutes is how far the source runs behind the exchange, as it
	// states or as measured (DelayBasis); nil when unknown. Yahoo's is the
	// venue's measured constant, a real-time quote's DelaySeconds / 60.
	DelayMinutes *float64 `json:"delayMinutes"`
	DelayBasis   *string  `json:"delayBasis"` // stated | measured
	// DelaySeconds is computed for every quote: min(readAt, the session's
	// last open moment) − lastTradeAt; TimeBasis is what lastTradeAt is
	// (trade | volume | snapshot); Label the honest label ("real-time ·
	// 04:03:12 UTC", "about 2 min behind · 04:03 UTC", "delayed · last trade
	// 04:03 UTC").
	DelaySeconds float64 `json:"delaySeconds"`
	TimeBasis    string  `json:"timeBasis"`
	Label        string  `json:"label"`
	Session      string  `json:"session"` // the listing's regular session now: open | closed | unknown
	// RefreshSeconds is the tier's re-read interval now; nil while the quote
	// is held until the next open (or the settled read after the close).
	RefreshSeconds *int      `json:"refreshSeconds"`
	StaleAt        time.Time `json:"staleAt"` // past it, without a newer read, the quote is stale
	Stale          bool      `json:"stale"`   // at response time: past StaleAt, or no new trade for 4 days but a holiday
}

// FX is the rate a row's USD price used.
type FX struct {
	Pair       string    `json:"pair"`
	USDPerUnit float64   `json:"usdPerUnit"`
	At         time.Time `json:"at"`
}

// RunView is a run-log row.
type RunView struct {
	Status       string     `json:"status"` // running | completed | failed
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	Tickers      int        `json:"tickers"`
	Done         int        `json:"done"`
	OK           int        `json:"ok"`
	Insufficient int        `json:"insufficient"`
	Unavailable  int        `json:"unavailable"`
	Failed       int        `json:"failed"`
	Error        *string    `json:"error"`
	// DurationSeconds is finished − started (the time so far while running);
	// YahooCalls the HTTP requests the run sent to Yahoo, retries included
	// (nil for runs recorded before it was counted).
	DurationSeconds int  `json:"durationSeconds"`
	YahooCalls      *int `json:"yahooCalls"`
}

type MarketCount struct {
	Market
	Count int `json:"count"`
}

type Source struct {
	Prices string `json:"prices"`
	FX     string `json:"fx"`
	Lists  []List `json:"lists"`
}

type Page struct {
	Rows            []Row          `json:"rows"`
	NextCursor      *string        `json:"nextCursor"`
	Total           int            `json:"total"`  // rows matching filter, market and q
	Counts          map[string]int `json:"counts"` // per filter, within market and q
	Markets         []MarketCount  `json:"markets"`
	AsOf            *time.Time     `json:"asOf"` // the newest computed row
	Run             *RunView       `json:"run"`  // the latest run
	LastCompletedAt *time.Time     `json:"lastCompletedAt"`
	NextRunAt       time.Time      `json:"nextRunAt"`
	Schedule        string         `json:"schedule"`    // when runs happen, as a sentence
	PollSeconds     int            `json:"pollSeconds"` // how often to refresh the rows on screen (GET /api/screener/asia/quotes)
	Source          Source         `json:"source"`
	Method          string         `json:"method"`
}

// Live is GET /api/screener/asia/quotes: the asked rows, priced and derived
// as on the page.
type Live struct {
	Rows            []Row      `json:"rows"`    // in the order asked, unknown tickers left out
	Unknown         []string   `json:"unknown"` // asked tickers that are not constituents
	AsOf            *time.Time `json:"asOf"`
	Run             *RunView   `json:"run"`
	LastCompletedAt *time.Time `json:"lastCompletedAt"`
	PollSeconds     int        `json:"pollSeconds"`
}

// filters bound % vs the 200-week average: below (−50, −5], deep (−70, −50],
// distressed ≤ −70, approaching [0, 15] above, oversold RSI < 30 and below.
var filters = map[string]func(Row) bool{
	"all":         func(Row) bool { return true },
	"below":       func(r Row) bool { return in(r.PctVsSMA, -50, -5, false) },
	"deep":        func(r Row) bool { return in(r.PctVsSMA, -70, -50, false) },
	"distressed":  func(r Row) bool { return r.PctVsSMA != nil && *r.PctVsSMA <= -70 },
	"approaching": func(r Row) bool { return in(r.PctVsSMA, 0, 15, true) },
	"oversold":    func(r Row) bool { return r.RSI14w != nil && *r.RSI14w < 30 && r.PctVsSMA != nil && *r.PctVsSMA < 0 },
}

var filterNames = []string{"all", "below", "deep", "distressed", "approaching", "oversold"}

// zeroCounts lists every filter at 0, so a page's counts name each filter even
// when no row matches it.
func zeroCounts() map[string]int {
	c := make(map[string]int, len(filterNames))
	for _, f := range filterNames {
		c[f] = 0
	}
	return c
}

// in is lo < v ≤ hi, or lo ≤ v ≤ hi when closed.
func in(v *float64, lo, hi float64, closed bool) bool {
	return v != nil && *v <= hi && (*v > lo || closed && *v == lo)
}

// sortKey orders rows: values first (ascending, or descending with "-"),
// rows without one last, then by ticker.
type sortKey struct {
	Null bool    `json:"n,omitempty"`
	Num  float64 `json:"v,omitempty"`
	Str  string  `json:"k,omitempty"`
}

var sorts = map[string]func(Row) sortKey{
	"ticker":    func(r Row) sortKey { return sortKey{Str: r.Ticker} },
	"name":      func(r Row) sortKey { return sortKey{Str: strings.ToLower(r.Name)} },
	"market":    func(r Row) sortKey { return sortKey{Num: float64(marketRank(r.Market))} },
	"price":     func(r Row) sortKey { return num(r.Price) },
	"priceUsd":  func(r Row) sortKey { return num(r.PriceUSD) },
	"pctVsSma":  func(r Row) sortKey { return num(r.PctVsSMA) },
	"direction": func(r Row) sortKey { return num(r.change) },
	"rsi":       func(r Row) sortKey { return num(r.RSI14w) },
	"drawdown":  func(r Row) sortKey { return num(r.DrawdownPct) },
	"athAt":     func(r Row) sortKey { return sortKey{Null: r.ATHAt == nil, Str: deref(r.ATHAt)} },
	"updated":   func(r Row) sortKey { return at(r.shownAt()) },
}

// shownAt is the time the UPDATED column shows: the live quote's last trade,
// else when the run computed the row.
func (r Row) shownAt() *time.Time {
	if r.Quote != nil {
		return &r.Quote.LastTradeAt
	}
	return r.UpdatedAt
}

func num(v *float64) sortKey {
	if v == nil {
		return sortKey{Null: true}
	}
	return sortKey{Num: *v}
}

func at(t *time.Time) sortKey {
	if t == nil {
		return sortKey{Null: true}
	}
	return sortKey{Num: float64(t.Unix())}
}

func marketRank(code string) int {
	return slices.IndexFunc(Markets, func(m Market) bool { return m.Code == code })
}

func compareKeys(a, b sortKey, at, bt string, desc bool) int {
	if a.Null != b.Null {
		if a.Null {
			return 1
		}
		return -1
	}
	c := cmp.Or(cmp.Compare(a.Num, b.Num), strings.Compare(a.Str, b.Str))
	if desc {
		c = -c
	}
	return cmp.Or(c, strings.Compare(at, bt))
}

// cursor is the last row of a page (its sort and key) and every row the walk
// has served so far (Seen: a bitmap over stocks, for the list List names).
// Live values move rows between pages, so a page after the first is the
// first rows, in the current order, that the walk has not served: a row that
// moved ahead of the last one served is not skipped, one that moved behind it
// is not repeated. A cursor without Seen (an earlier release's), or for
// another list, walks by its key.
type cursor struct {
	Sort   string  `json:"s"`
	Key    sortKey `json:"key"`
	Ticker string  `json:"t"`
	Seen   string  `json:"seen,omitempty"`
	List   uint32  `json:"l,omitempty"`
}

// listID names the constituent list a cursor's Seen bitmap indexes.
var listID = func() uint32 {
	h := fnv.New32a()
	for _, s := range stocks {
		h.Write([]byte(s.Ticker + ","))
	}
	return h.Sum32()
}()

// served is the rows c's walk has served, by index in stocks; false when c
// carries none for this list.
func (c *cursor) served() ([]bool, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(c.Seen)
	if c.Seen == "" || c.List != listID || err != nil || len(raw) != (len(stocks)+7)/8 {
		return nil, false
	}
	out := make([]bool, len(stocks))
	for i := range out {
		out[i] = raw[i/8]&(1<<(i%8)) != 0
	}
	return out, true
}

// packSeen is served's bitmap, for a cursor.
func packSeen(seen []bool) string {
	raw := make([]byte, (len(stocks)+7)/8)
	for i, ok := range seen {
		if ok {
			raw[i/8] |= 1 << (i % 8)
		}
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

type query struct {
	filter, market, q, sort string
	desc                    bool
	limit                   int
	after                   *cursor
}

func bad(msg string) error { return &app.BadRequest{Msg: msg} }

func parse(r *http.Request) (query, error) {
	v := r.URL.Query()
	q := query{filter: cmp.Or(v.Get("filter"), "all"), market: strings.ToUpper(strings.TrimSpace(v.Get("market"))),
		q: strings.TrimSpace(v.Get("q")), sort: cmp.Or(v.Get("sort"), defaultSort), limit: defaultLimit}
	if filters[q.filter] == nil {
		return q, bad("Unknown filter: use all, below, deep, distressed, approaching or oversold.")
	}
	if q.market != "" && marketRank(q.market) < 0 {
		return q, bad("Unknown market: use JP, KR, TW, HK, SG, ID, TH, MY, PH or VN.")
	}
	if utf8.RuneCountInString(q.q) > maxQuery {
		return q, bad("Search is limited to 64 characters.")
	}
	name, desc := strings.CutPrefix(q.sort, "-")
	if sorts[name] == nil {
		return q, bad("Unknown sort: use ticker, name, market, price, priceUsd, pctVsSma, direction, rsi, drawdown, athAt or updated, optionally prefixed with -.")
	}
	q.desc = desc
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			return q, bad("limit must be an integer from 1 to 100.")
		}
		q.limit = n
	}
	if s := v.Get("cursor"); s != "" {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		var c cursor
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Sort != q.sort || c.Ticker == "" {
			return q, bad("Invalid cursor.")
		}
		q.after = &c
	}
	return q, nil
}

func (h *handler) asia(w http.ResponseWriter, r *http.Request) {
	q, err := parse(r)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	p, err := h.page(r.Context(), q, time.Now())
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (h *handler) page(ctx context.Context, q query, now time.Time) (Page, error) {
	rows, asOf, err := h.rows(ctx, nil)
	if err != nil {
		return Page{}, err
	}
	p := Page{Rows: []Row{}, Counts: zeroCounts(), AsOf: asOf, NextRunAt: nextSlot(now), Schedule: schedule, PollSeconds: pollSeconds,
		Source: Source{Prices: sourcePrices, FX: sourceFX, Lists: Lists()}, Method: method}
	for _, m := range Markets {
		p.Markets = append(p.Markets, MarketCount{Market: m})
	}
	var matched []Row
	needle := strings.ToLower(q.q)
	for _, row := range rows {
		p.Markets[marketRank(row.Market)].Count++
		if q.market != "" && row.Market != q.market || needle != "" && !matches(row, needle) {
			continue
		}
		for _, f := range filterNames {
			if filters[f](row) {
				p.Counts[f]++
			}
		}
		if filters[q.filter](row) {
			matched = append(matched, row)
		}
	}
	p.Total = len(matched)
	name := strings.TrimPrefix(q.sort, "-")
	key := sorts[name]
	slices.SortFunc(matched, func(a, b Row) int { return compareKeys(key(a), key(b), a.Ticker, b.Ticker, q.desc) })
	start, seen := 0, make([]bool, len(stocks)) // seen: the rows this walk has served
	if c := q.after; c != nil {
		if s, ok := c.served(); ok {
			seen = s
			matched = slices.DeleteFunc(matched, func(r Row) bool { return seen[stockAt[r.Ticker]] })
		} else {
			start, _ = slices.BinarySearchFunc(matched, c, func(r Row, c *cursor) int {
				if compareKeys(key(r), c.Key, r.Ticker, c.Ticker, q.desc) <= 0 {
					return -1
				}
				return 1
			})
			for _, r := range matched[:start] {
				seen[stockAt[r.Ticker]] = true
			}
		}
	}
	end := min(start+q.limit, len(matched))
	p.Rows = append(p.Rows, matched[start:end]...)
	if end < len(matched) {
		for _, r := range p.Rows {
			seen[stockAt[r.Ticker]] = true
		}
		last := matched[end-1]
		raw, _ := json.Marshal(cursor{Sort: q.sort, Key: key(last), Ticker: last.Ticker, Seen: packSeen(seen), List: listID})
		next := base64.RawURLEncoding.EncodeToString(raw)
		p.NextCursor = &next
	}
	p.Run, p.LastCompletedAt, err = h.runs(ctx, now)
	return p, err
}

// live serves GET /api/screener/asia/quotes?tickers=A,B: up to maxQuotes
// rows by ticker, priced and derived as on the page, for refreshing the rows
// a client shows in place.
func (h *handler) live(w http.ResponseWriter, r *http.Request) {
	tickers, unknown, err := parseTickers(r.URL.Query()["tickers"])
	if err != nil {
		app.WriteError(w, err)
		return
	}
	v, err := h.quoteRows(r.Context(), tickers, time.Now())
	if err != nil {
		app.WriteError(w, err)
		return
	}
	v.Unknown = unknown
	httpx.JSON(w, http.StatusOK, v)
}

// parseTickers reads tickers= (comma-separated, repeatable): constituents in
// the order first asked, case-folded and deduplicated, and the rest.
func parseTickers(params []string) (known, unknown []string, err error) {
	unknown = []string{}
	seen := map[string]bool{}
	for _, p := range params {
		for _, t := range strings.Split(p, ",") {
			if t = strings.ToUpper(strings.TrimSpace(t)); t == "" || seen[t] {
				continue
			}
			seen[t] = true
			if _, ok := stockAt[t]; ok {
				known = append(known, t)
			} else {
				unknown = append(unknown, t)
			}
		}
	}
	switch n := len(known) + len(unknown); {
	case n == 0:
		return nil, nil, bad("tickers is required: up to 100 constituent tickers, comma-separated (7203.T,005930.KS).")
	case n > maxQuotes:
		return nil, nil, bad("tickers is limited to 100.")
	}
	return known, unknown, nil
}

// stockAt is each constituent's index in stocks, by ticker.
var stockAt = func() map[string]int {
	out := make(map[string]int, len(stocks))
	for i, s := range stocks {
		out[s.Ticker] = i
	}
	return out
}()

func (h *handler) quoteRows(ctx context.Context, tickers []string, now time.Time) (Live, error) {
	v := Live{Rows: []Row{}, PollSeconds: pollSeconds}
	if len(tickers) > 0 {
		rows, _, err := h.rows(ctx, tickers)
		if err != nil {
			return v, err
		}
		by := make(map[string]Row, len(rows))
		for _, r := range rows {
			by[r.Ticker] = r
		}
		for _, t := range tickers {
			v.Rows = append(v.Rows, by[t])
		}
	}
	if err := h.d.DB.QueryRow(ctx, "select max(computed_at) from screener_rows").Scan(&v.AsOf); err != nil {
		return v, err
	}
	var err error
	v.Run, v.LastCompletedAt, err = h.runs(ctx, now)
	return v, err
}

// matches reports whether a search names the row: its ticker, code, names or
// index, its market's name ("korea" finds every South Korean row) or, exactly,
// its market's code ("kr").
func matches(r Row, needle string) bool {
	for _, s := range []string{r.Ticker, r.Code, r.Name, r.ListName} {
		if strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	if i := marketRank(r.Market); i >= 0 && (strings.Contains(strings.ToLower(Markets[i].Name), needle) || strings.EqualFold(Markets[i].Code, needle)) {
		return true
	}
	return slices.ContainsFunc(r.Indices, func(m Membership) bool { return strings.Contains(strings.ToLower(m.Index), needle) })
}

// storedRow is a ticker's screener_rows row.
type storedRow struct {
	status                                                           string
	reason, name, currency, fxPair                                   *string
	attempted                                                        time.Time
	computed, priceAt, fxAt, firstWeek, athAt                        *time.Time
	price, fxUSD, priceUSD, sma, pctNow, pctPrev, rsi, ath, drawdown *float64
	weeks                                                            *int
}

// rows is every embedded constituent (only those named by only, when not
// nil) joined with its stored row and priced at its live quote, in list order,
// and the newest computed time among them.
func (h *handler) rows(ctx context.Context, only []string) ([]Row, *time.Time, error) {
	todo := stocks
	var where string
	var args []any
	if only != nil {
		todo = make([]Stock, 0, len(only))
		for _, t := range only {
			if i, ok := stockAt[t]; ok {
				todo = append(todo, stocks[i])
			}
		}
		slices.SortFunc(todo, func(a, b Stock) int { return cmp.Compare(stockAt[a.Ticker], stockAt[b.Ticker]) })
		where, args = " where ticker = any($1)", []any{only}
	}
	res, err := h.d.DB.Query(ctx, `select ticker, status, reason, attempted_at, computed_at, yahoo_name, currency, price, price_at,
		fx_usd, fx_pair, fx_at, price_usd, weeks, first_week, sma200w, pct_vs_sma, prev_pct_vs_sma, rsi14w, ath, ath_at, drawdown_pct
		from screener_rows`+where, args...)
	if err != nil {
		return nil, nil, err
	}
	defer res.Close()
	by := map[string]storedRow{}
	for res.Next() {
		var t string
		var s storedRow
		if err := res.Scan(&t, &s.status, &s.reason, &s.attempted, &s.computed, &s.name, &s.currency, &s.price, &s.priceAt,
			&s.fxUSD, &s.fxPair, &s.fxAt, &s.priceUSD, &s.weeks, &s.firstWeek, &s.sma, &s.pctNow, &s.pctPrev, &s.rsi,
			&s.ath, &s.athAt, &s.drawdown); err != nil {
			return nil, nil, err
		}
		by[t] = s
	}
	if err := res.Err(); err != nil {
		return nil, nil, err
	}
	trade := tradables()
	var quotes map[string]market.ListingQuote
	if h.quotes != nil {
		tickers := make([]string, 0, len(todo))
		for _, st := range todo {
			if st.Resolves {
				tickers = append(tickers, st.Ticker)
			}
		}
		quotes = h.quotes.ListingQuotes(tickers)
	}
	var asOf *time.Time
	out := make([]Row, 0, len(todo))
	for _, st := range todo {
		r := Row{Ticker: st.Ticker, Code: st.Code, Name: st.Name, ListName: st.Name, Market: st.Market, Indices: st.Indices,
			Currency: st.Currency, Status: "pending"}
		if !st.Resolves {
			r.Status, r.StatusNote = "unavailable", ptr("Yahoo served no data for this ticker when the list was built")
		}
		s, ok := by[st.Ticker]
		if ok {
			r.Status, r.StatusNote, r.CheckedAt = s.status, s.reason, &s.attempted
			if s.computed != nil {
				r.UpdatedAt, r.Price, r.PriceAt, r.PriceUSD, r.Weeks, r.SMA200w, r.ATH =
					s.computed, s.price, s.priceAt, s.priceUSD, s.weeks, s.sma, s.ath
				r.ATHAt, r.ATHFrom = localDate(s.athAt, st.Market), localDate(s.firstWeek, st.Market)
				r.Name, r.Currency = cmp.Or(deref(s.name), r.Name), cmp.Or(deref(s.currency), r.Currency)
				if s.fxUSD != nil && s.fxPair != nil && s.fxAt != nil {
					r.FX = &FX{Pair: *s.fxPair, USDPerUnit: *s.fxUSD, At: *s.fxAt}
				}
				r.Direction = direction(s.pctNow, s.pctPrev)
				if s.pctNow != nil && s.pctPrev != nil {
					r.change = ptr(*s.pctNow - *s.pctPrev)
				}
				r.PctVsSMA, r.PctVsSMAPrev, r.RSI14w, r.DrawdownPct = round2(s.pctNow), round2(s.pctPrev), round2(s.rsi), round2(s.drawdown)
				if asOf == nil || s.computed.After(*asOf) {
					asOf = s.computed
				}
			}
		}
		if t := trade(st.Ticker); t != nil {
			r.TradableSymbol, r.TradeVia, r.TradeNote = &t.Symbol, &t.Via, &t.Note
		} else if why, ok := market.ListedExcluded(st.Ticker); ok {
			r.TradeNote = &why // research only: the reason cmd/genlisted left it out
		}
		if q, ok := quotes[st.Ticker]; ok {
			r.price(q, s)
		}
		out = append(out, r)
	}
	return out, asOf, nil
}

// zones are the screened markets' exchange time zones (tzdata is embedded via
// package yahoo): a weekly bar opens at the local Monday 00:00, Sunday in UTC.
var zones = func() map[string]*time.Location {
	out := map[string]*time.Location{}
	for code, name := range map[string]string{"JP": "Asia/Tokyo", "KR": "Asia/Seoul", "TW": "Asia/Taipei", "HK": "Asia/Hong_Kong",
		"SG": "Asia/Singapore", "ID": "Asia/Jakarta", "TH": "Asia/Bangkok", "MY": "Asia/Kuala_Lumpur", "PH": "Asia/Manila",
		"VN": "Asia/Ho_Chi_Minh"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			panic(err)
		}
		out[code] = loc
	}
	return out
}()

func localDate(t *time.Time, market string) *string {
	if t == nil {
		return nil
	}
	return ptr(t.In(zones[market]).Format(time.DateOnly))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func round2(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return ptr(math.Round(*v*100) / 100)
}

func (h *handler) runs(ctx context.Context, now time.Time) (*RunView, *time.Time, error) {
	var last *time.Time
	if err := h.d.DB.QueryRow(ctx, "select max(finished_at) from screener_runs where status = 'completed'").Scan(&last); err != nil {
		return nil, nil, err
	}
	var v RunView
	err := h.d.DB.QueryRow(ctx, `select status, started_at, finished_at, tickers, done, ok, insufficient, unavailable, failed, error,
		yahoo_calls from screener_runs order by started_at desc limit 1`).Scan(&v.Status, &v.StartedAt, &v.FinishedAt, &v.Tickers, &v.Done,
		&v.OK, &v.Insufficient, &v.Unavailable, &v.Failed, &v.Error, &v.YahooCalls)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, last, nil
	}
	if err != nil {
		return nil, nil, err
	}
	end := now
	if v.FinishedAt != nil {
		end = *v.FinishedAt
	}
	v.DurationSeconds = max(0, int(end.Sub(v.StartedAt).Round(time.Second)/time.Second))
	return &v, last, nil
}
