package screener

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// fakeQuotes is a quote source holding fixed quotes by ticker.
type fakeQuotes map[string]market.ListingQuote

func (f fakeQuotes) ListingQuotes(tickers []string) map[string]market.ListingQuote {
	out := map[string]market.ListingQuote{}
	for _, t := range tickers {
		if q, ok := f[t]; ok {
			out[t] = q
		}
	}
	return out
}

func liveEnv(t *testing.T, q fakeQuotes) *apptest.Env {
	return apptest.New(t, func(mux *http.ServeMux, d *app.Deps) { register(mux, d, q) }, apptest.Options{})
}

// run is a computed row as a run stores it, in KRW at 0.00075 USD.
type run struct {
	price, sma, pct, ath float64
	prev                 *float64
	priceAt, athAt       time.Time
	ccy                  string
}

func seedRun(t *testing.T, e *apptest.Env, ticker string, r run) {
	t.Helper()
	ccy := r.ccy
	if ccy == "" {
		ccy = "KRW"
	}
	if _, err := e.DB.Exec(context.Background(), `insert into screener_rows (ticker, status, attempted_at, computed_at, yahoo_name,
		currency, price, price_at, fx_usd, fx_pair, fx_at, price_usd, weeks, first_week, sma200w, pct_vs_sma, prev_pct_vs_sma, rsi14w,
		ath, ath_at, drawdown_pct) values ($1, 'ok', $2, $2, 'Yahoo ' || $1, $9, $3::float8, $2, 0.00075, 'KRWUSD=X', $2,
		$3::float8 * 0.00075, 260, '2001-01-01', $4, $5, $6, 45, $7::float8, $8, ($3::float8 / $7::float8 - 1) * 100)`,
		ticker, r.priceAt, r.price, r.sma, r.pct, r.prev, r.ath, r.athAt, ccy); err != nil {
		t.Fatal(err)
	}
}

// The day's runs: Monday 2026-10-12 10:45 UTC, priced at the KRX close.
var (
	runAt   = time.Date(2026, 10, 12, 6, 30, 0, 0, time.UTC)
	tradeAt = time.Date(2026, 10, 14, 3, 40, 0, 0, time.UTC) // Wednesday 12:40 KST
)

func quote(price float64, at time.Time, tier string) market.ListingQuote {
	d := 20.0
	return market.ListingQuote{Source: "yahoo", Tier: tier, Price: price, Currency: "KRW", LastTradeAt: at, ReadAt: at.Add(20 * time.Minute),
		Session: "open", Refresh: 5 * time.Minute, StaleAt: at.Add(27 * time.Minute), DelayMinutes: &d, DelayBasis: "measured",
		FX: &yahoo.Rate{Ccy: "KRW", USD: 0.0007, Time: at, Symbol: "KRWUSD=X"}}
}

func near2(v *float64, want float64) bool { return v != nil && math.Abs(*v-want) < 0.006 }

// Each row is priced at its listing's quote, and the price columns follow it
// against the run's averages: % vs the run's 200-week mean, direction against
// last week's, the drawdown against the higher of the run's high and the
// quote (a new high reads 0% on the quote's date); RSI stays the run's. The
// run's price stands when it is newer or in another currency; a row never
// computed still gets the live price.
func TestAsiaLivePrices(t *testing.T) {
	prev := -20.0
	q := fakeQuotes{
		"005930.KS": quote(60000, tradeAt, "screener"),               // below the 200WMA, deeper than last week
		"000660.KS": quote(240000, tradeAt, "screener"),              // above the run's high
		"005380.KS": quote(90000, runAt.Add(-time.Hour), "screener"), // older than the run's price
		"035420.KS": quote(150000, tradeAt, "trade"),
		"7203.T":    quote(2500, tradeAt, "screener"), // a row the run never read
		"373220.KS": quote(300000, tradeAt, "trade"),
	}
	q["035420.KS"] = func(x market.ListingQuote) market.ListingQuote { x.Currency = "USD"; return x }(q["035420.KS"])
	jpy := q["7203.T"]
	jpy.Currency, jpy.FX, jpy.Refresh = "JPY", &yahoo.Rate{Ccy: "JPY", USD: 0.0066, Time: tradeAt, Symbol: "JPYUSD=X"}, 0
	q["7203.T"] = jpy
	e := liveEnv(t, q)
	seedRun(t, e, "005930.KS", run{price: 75000, sma: 80000, pct: -6.25, prev: &prev, ath: 100000, priceAt: runAt, athAt: runAt.AddDate(-2, 0, 0)})
	seedRun(t, e, "000660.KS", run{price: 200000, sma: 150000, pct: 10, prev: &prev, ath: 220000, priceAt: runAt, athAt: runAt.AddDate(0, -1, 0)})
	seedRun(t, e, "005380.KS", run{price: 95000, sma: 100000, pct: -5, prev: &prev, ath: 120000, priceAt: runAt, athAt: runAt.AddDate(-1, 0, 0)})
	seedRun(t, e, "035420.KS", run{price: 160000, sma: 200000, pct: -20, prev: &prev, ath: 300000, priceAt: runAt, athAt: runAt.AddDate(-3, 0, 0)})
	row := func(ticker string) Row {
		t.Helper()
		p := apptest.JSON[Page](t, e.Do("GET", "/api/screener/asia?q="+ticker, nil))
		if len(p.Rows) != 1 {
			t.Fatalf("%s: %d rows", ticker, len(p.Rows))
		}
		return p.Rows[0]
	}

	s := row("005930.KS")
	if *s.Price != 60000 || !s.PriceAt.Equal(tradeAt) || !near2(s.PriceUSD, 42) || s.FX == nil || s.FX.USDPerUnit != 0.0007 ||
		!near2(s.PctVsSMA, -25) || !near2(s.PctVsSMAPrev, -20) || *s.Direction != "deepening" || !near2(s.DrawdownPct, -40) ||
		*s.ATH != 100000 || *s.ATHAt != "2024-10-12" || *s.RSI14w != 45 || !s.UpdatedAt.Equal(runAt) {
		t.Fatalf("Samsung %+v", s)
	}
	if v := s.Quote; v == nil || v.Source != "yahoo" || v.Tier != "screener" || v.Price != 60000 || v.Currency != "KRW" ||
		!v.LastTradeAt.Equal(tradeAt) || !v.ReadAt.Equal(tradeAt.Add(20*time.Minute)) || *v.DelayMinutes != 20 || *v.DelayBasis != "measured" ||
		v.Session != "open" || *v.RefreshSeconds != 300 || !v.StaleAt.Equal(tradeAt.Add(27*time.Minute)) || v.Stale {
		t.Fatalf("Samsung quote %+v", v)
	}
	// A new high: 0% on the quote's exchange-local date.
	h := row("000660.KS")
	if *h.ATH != 240000 || *h.ATHAt != "2026-10-14" || *h.DrawdownPct != 0 || !near2(h.PctVsSMA, 60) || *h.Direction != "recovering" {
		t.Fatalf("SK hynix %+v", h)
	}
	// The run's price is newer than the quote, or in another currency: the run's values stand.
	for _, ticker := range []string{"005380.KS", "035420.KS"} {
		if r := row(ticker); r.Quote != nil || !r.PriceAt.Equal(runAt) || *r.PctVsSMA != map[string]float64{"005380.KS": -5, "035420.KS": -20}[ticker] {
			t.Fatalf("%s %+v", ticker, r)
		}
	}
	// Never computed: the live price and USD, no derived columns.
	if r := row("7203.T"); r.Status != "pending" || *r.Price != 2500 || r.Currency != "JPY" || !near2(r.PriceUSD, 16.5) ||
		r.FX.Pair != "JPYUSD=X" || r.PctVsSMA != nil || r.DrawdownPct != nil || r.Quote == nil || r.Quote.RefreshSeconds != nil {
		t.Fatalf("pending %+v", r)
	}
	// Filters, counts and sorts follow the live values: SK hynix's run had it
	// approaching (10%), its quote puts it at 60%.
	p := apptest.JSON[Page](t, e.Do("GET", "/api/screener/asia?market=KR&sort=pctVsSma&limit=4", nil))
	if got := tickersOf(p.Rows); !slices.Equal(got, []string{"005930.KS", "035420.KS", "005380.KS", "000660.KS"}) ||
		p.Counts["below"] != 3 || p.Counts["approaching"] != 0 {
		t.Fatalf("by pctVsSma %v %v", got, p.Counts)
	}
	if a := apptest.JSON[Page](t, e.Do("GET", "/api/screener/asia?filter=approaching", nil)); a.Total != 0 {
		t.Fatalf("approaching %v", tickersOf(a.Rows))
	}
	byUpdated := apptest.JSON[Page](t, e.Do("GET", "/api/screener/asia?sort=-updated&limit=6", nil))
	if got := tickersOf(byUpdated.Rows); !slices.Equal(got[:4], []string{"000660.KS", "005930.KS", "373220.KS", "7203.T"}) {
		t.Fatalf("by the shown time: %v", got) // the quotes' last trade, then the runs'
	}
}

func tickersOf(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Ticker
	}
	return out
}

// Last week's value is the run's own pctVsSma once the quote is in a later
// exchange-local week than the run's price (a Monday quote before Monday's
// run).
func TestAsiaLiveWeekRoll(t *testing.T) {
	friday := time.Date(2026, 10, 9, 6, 30, 0, 0, time.UTC) // Friday's KRX close
	monday := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC) // Monday 10:00 KST, before Monday's run
	prev := -30.0
	e := liveEnv(t, fakeQuotes{"005930.KS": quote(84000, monday, "screener")})
	seedRun(t, e, "005930.KS", run{price: 76000, sma: 80000, pct: -5, prev: &prev, ath: 100000, priceAt: friday, athAt: friday.AddDate(-1, 0, 0)})
	r := apptest.JSON[Page](t, e.Do("GET", "/api/screener/asia?q=005930.KS", nil)).Rows[0]
	if !near2(r.PctVsSMA, 5) || !near2(r.PctVsSMAPrev, -5) || *r.Direction != "recovering" {
		t.Fatalf("a new week %+v", r)
	}
	if !laterWeek(monday, friday, zones["KR"]) || laterWeek(friday, friday.Add(-72*time.Hour), zones["KR"]) {
		t.Fatal("laterWeek")
	}
	// Sunday 23:00 UTC is Monday in Seoul.
	if !laterWeek(time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC), friday, zones["KR"]) {
		t.Fatal("the week is the exchange's")
	}
}

// GET /api/screener/asia/quotes returns the asked rows exactly as the page
// shows them, in the order asked, unknown tickers named apart; none, over
// 100 or no session is refused.
func TestAsiaQuotesEndpoint(t *testing.T) {
	prev := -20.0
	e := liveEnv(t, fakeQuotes{"005930.KS": quote(60000, tradeAt, "screener")})
	seedRun(t, e, "005930.KS", run{price: 75000, sma: 80000, pct: -6.25, prev: &prev, ath: 100000, priceAt: runAt, athAt: runAt.AddDate(-2, 0, 0)})
	w := e.Do("GET", "/api/screener/asia/quotes?tickers=7203.t,%20005930.KS,NOPE.KS&tickers=7203.T", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	v := apptest.JSON[Live](t, w)
	if got := tickersOf(v.Rows); !slices.Equal(got, []string{"7203.T", "005930.KS"}) || !slices.Equal(v.Unknown, []string{"NOPE.KS"}) ||
		v.PollSeconds != 60 || v.AsOf == nil || !v.AsOf.Equal(runAt) || v.Run != nil || v.LastCompletedAt != nil {
		t.Fatalf("%+v", v)
	}
	page := apptest.JSON[Page](t, e.Do("GET", "/api/screener/asia?q=005930.KS", nil)).Rows[0]
	if a, b := v.Rows[1], page; *a.Price != *b.Price || *a.PctVsSMA != *b.PctVsSMA || *a.Direction != *b.Direction ||
		*a.DrawdownPct != *b.DrawdownPct || a.Quote == nil || !reflect.DeepEqual(a.Quote, b.Quote) || *a.TradableSymbol != "SMSN" {
		t.Fatalf("quotes row %+v, page row %+v", a, b)
	}
	if r := v.Rows[0]; r.Status != "pending" || r.Price != nil || r.Quote != nil || *r.TradableSymbol != "TM" {
		t.Fatalf("7203.T %+v", r)
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = Stocks()[i].Ticker
	}
	for _, q := range []string{"", "tickers=", "tickers=%20,%20", "tickers=" + strings.Join(many, ",")} {
		if w := e.Do("GET", "/api/screener/asia/quotes?"+q, nil); w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
			t.Fatalf("%.40s: %d", q, w.Code)
		}
	}
	if w := e.Do("GET", "/api/screener/asia/quotes?"+url.Values{"tickers": {strings.Join(many[:100], ",")}}.Encode(), nil); w.Code != 200 ||
		len(apptest.JSON[Live](t, w).Rows) != 100 {
		t.Fatalf("100 tickers: %d", w.Code)
	}
	if w := e.DoAs(nil, "GET", "/api/screener/asia/quotes?tickers=7203.T", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
}

// A cursor walk while live values move serves every matching row exactly
// once: a row that moved ahead of the last one served comes next instead of
// being skipped, one that moved behind it is not served again. A cursor
// without the served rows (an earlier release's) still walks by its key.
func TestAsiaCursorWalkWhileValuesMove(t *testing.T) {
	var kr []string
	for _, s := range stocks {
		if s.Market == "KR" && s.Resolves && len(kr) < 6 {
			kr = append(kr, s.Ticker)
		}
	}
	q := fakeQuotes{}
	e := liveEnv(t, q)
	prev := -20.0
	for i, ticker := range kr { // −10% … −15% against a 200-week mean of 100
		seedRun(t, e, ticker, run{price: 95, sma: 100, pct: -5, prev: &prev, ath: 200, priceAt: runAt, athAt: runAt.AddDate(-1, 0, 0)})
		q[ticker] = quote(90-float64(i), tradeAt, "trade")
	}
	walk := func(cursor *string) Page {
		t.Helper()
		u := "/api/screener/asia?filter=below&market=KR&sort=pctVsSma&limit=2"
		if cursor != nil {
			u += "&cursor=" + url.QueryEscape(*cursor)
		}
		w := e.Do("GET", u, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", u, w.Code, w.Body)
		}
		return apptest.JSON[Page](t, w)
	}
	p1 := walk(nil)
	if got := tickersOf(p1.Rows); !slices.Equal(got, []string{kr[5], kr[4]}) || p1.NextCursor == nil || p1.Total != 6 {
		t.Fatalf("page 1 %v", got)
	}
	// Between pages kr[0] falls to −20% (ahead of the cursor) and kr[5] rises
	// to −10.5% (behind it).
	q[kr[0]], q[kr[5]] = quote(80, tradeAt, "trade"), quote(89.5, tradeAt, "trade")
	p2 := walk(p1.NextCursor)
	if got := tickersOf(p2.Rows); !slices.Equal(got, []string{kr[0], kr[3]}) || p2.NextCursor == nil || p2.Total != 6 {
		t.Fatalf("page 2 %v", got)
	}
	p3 := walk(p2.NextCursor)
	if got := tickersOf(p3.Rows); !slices.Equal(got, []string{kr[2], kr[1]}) || p3.NextCursor != nil {
		t.Fatalf("page 3 %v (next %v)", got, p3.NextCursor)
	}
	// An earlier release's cursor (no served rows), or one for another list,
	// walks by its key: after −14% (kr[4]).
	for _, c := range []cursor{
		{Sort: "pctVsSma", Key: sortKey{Num: -14}, Ticker: kr[4]},
		{Sort: "pctVsSma", Key: sortKey{Num: -14}, Ticker: kr[4], Seen: packSeen(make([]bool, len(stocks))), List: listID + 1},
	} {
		raw, _ := json.Marshal(c)
		old := base64.RawURLEncoding.EncodeToString(raw)
		if got := tickersOf(walk(&old).Rows); !slices.Equal(got, []string{kr[3], kr[2]}) {
			t.Fatalf("a keyset cursor %+v: %v", c, got)
		}
	}
	// A served set that does not decode walks by its key too.
	raw, _ := json.Marshal(cursor{Sort: "pctVsSma", Key: sortKey{Num: -14}, Ticker: kr[4], Seen: "!!", List: listID})
	bad := base64.RawURLEncoding.EncodeToString(raw)
	if got := tickersOf(walk(&bad).Rows); !slices.Equal(got, []string{kr[3], kr[2]}) {
		t.Fatalf("an unreadable served set walks by its key: %v", got)
	}
}

// The page's texts never call a delayed price live, and the price source
// does not repeat the "Prices: " label every web build puts before it.
// A real-time quote reaches the row with its class, computed delay, time
// basis and label as the market layer made them.
func TestAsiaRealtimeQuote(t *testing.T) {
	q := quote(61000, tradeAt, "screener")
	d := 0.05
	q.Source, q.Class, q.DelayMinutes, q.DelaySeconds, q.TimeBasis, q.Label, q.Refresh =
		"naver", market.ClassRealtime, &d, 3, "volume", "real-time · 03:40:00 UTC", 10*time.Second
	e := liveEnv(t, fakeQuotes{"005930.KS": q})
	p := apptest.JSON[Page](t, e.Do("GET", "/api/screener/asia?q=005930.KS", nil))
	if len(p.Rows) != 1 || p.Rows[0].Quote == nil {
		t.Fatalf("%+v", p.Rows)
	}
	v := p.Rows[0].Quote
	if v.Source != "naver" || v.Class != "realtime" || v.DelaySeconds != 3 || v.TimeBasis != "volume" ||
		v.Label != "real-time · 03:40:00 UTC" || *v.DelayMinutes != 0.05 || *v.RefreshSeconds != 10 || *p.Rows[0].Price != 61000 {
		t.Fatalf("quote %+v", v)
	}
}

func TestAsiaTexts(t *testing.T) {
	for name, text := range map[string]string{"method": method, "source.prices": sourcePrices, "source.fx": sourceFX, "schedule": schedule} {
		if strings.Contains(strings.ToLower(text), "live") {
			t.Errorf("%s says live: %q", name, text)
		}
	}
	if strings.HasPrefix(strings.ToLower(sourcePrices), "prices") {
		t.Errorf("source.prices repeats its label: %q", sourcePrices)
	}
}

// QuoteWatch offers every constituent Yahoo serves; the market layer keeps
// the 62 no quote feed reads (5 perp-traded, 23 ADR-traded, 34 research-only
// local lines), never a .PS row.
func TestQuoteWatch(t *testing.T) {
	rows := QuoteWatch()
	if len(rows) != 755 || slices.ContainsFunc(rows, func(r market.WatchRow) bool { return strings.HasSuffix(r.Ticker, ".PS") }) {
		t.Fatalf("%d rows", len(rows))
	}
	c := market.NewClient("")
	if n := c.Watch(rows); n != 62 {
		t.Fatalf("watched %d", n)
	}
	trade := tradables()
	var perp, adr, research int
	for _, r := range rows {
		if a, ok := market.ByYahoo(r.Ticker); ok && a.QuoteFeed() {
			continue // the trade tier reads it
		}
		switch tr := trade(r.Ticker); {
		case tr == nil:
			research++
		case tr.Via == "adr":
			adr++
		default:
			perp++
		}
	}
	if perp != 5 || adr != 23 || research != 34 {
		t.Fatalf("perp %d adr %d research %d", perp, adr, research)
	}
}
