package main

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// The Asia screener's boot run reads the first three constituents from the
// fake Yahoo feed (SCREENER_MAX_TICKERS=3) without a user or credits; the
// endpoint lists every constituent, the PSEi as unavailable, and pages them.
func TestIntegrationScreener(t *testing.T) {
	s := boot(t, options{yahoo: func(f *yahootest.Fake) {
		bars := make([]yahoo.Bar, 230)
		end := time.Date(2026, 9, 27, 15, 0, 0, 0, time.UTC) // a Monday in Tokyo
		for i := range bars {
			c := 900 - float64(i)
			bars[i] = yahoo.Bar{T: end.AddDate(0, 0, -7*(len(bars)-1-i)), O: c, H: c + 5, L: c - 5, C: c, V: 100}
		}
		price, at := bars[len(bars)-1].C, end.Add(4*24*time.Hour)
		f.Set(yahoo.Chart{Meta: yahoo.Meta{Symbol: "1332.T", Currency: "JPY", Timezone: "Asia/Tokyo", LongName: "Nissui Corporation",
			Price: &price, PriceTime: &at}, Bars: bars})
		f.Set(yahoo.Chart{Meta: yahoo.Meta{Symbol: "1605.T", Currency: "JPY", Timezone: "Asia/Tokyo"}, Bars: bars[:50]})
		f.SetFX("JPY", 0.0063)
	}})
	c := s.signIn(t)
	var page any
	eventually(t, 20*time.Second, "the screener's boot run", func() bool {
		page = c.call("GET", "/api/screener/asia?q=1332.T", nil, 200)
		return !isNull(t, page, "run") && str(t, page, "run.status") == "completed"
	})
	shape(t, page, "", screenerPageKeys)
	shape(t, page, "run", "status startedAt finishedAt tickers done ok insufficient unavailable failed error durationSeconds yahooCalls")
	shapeAll(t, page, "rows", screenerRowKeys)
	if num(t, page, "run.tickers") != 3 || num(t, page, "run.ok") != 1 || num(t, page, "run.insufficient") != 1 || num(t, page, "run.unavailable") != 1 ||
		num(t, page, "run.yahooCalls") < 3 || num(t, page, "run.durationSeconds") < 0 || num(t, page, "pollSeconds") != 60 {
		// three charts at least; the JPY rate adds a call only when the quote feed has not
		// read it first (the Yahoo client caches each rate, so then it costs the run nothing)
		t.Fatalf("run %v", page)
	}
	next := isTime(t, page, "nextRunAt")
	if wd := next.Weekday(); wd == time.Saturday || wd == time.Sunday || next.Hour() != 10 || next.Minute() != 45 {
		t.Fatalf("next run %v", next)
	}
	row := list(t, page, "rows")[0]
	if str(t, row, "status") != "ok" || str(t, row, "name") != "Nissui Corporation" || num(t, row, "price") != 671 || num(t, row, "weeks") != 230 ||
		str(t, row, "direction") != "deepening" || str(t, row, "fx.pair") != "JPYUSD=X" || math.Abs(num(t, row, "priceUsd")-671*0.0063) > 1e-9 ||
		num(t, row, "pctVsSma") >= 0 || str(t, row, "tradableSymbol") != "1332.T" || str(t, row, "tradeVia") != "listing" ||
		str(t, row, "tradeNote") != "TSE: 1332 (Nikkei 225), JPY→USD at Yahoo FX" {
		t.Fatalf("row %v", row) // a listed constituent trades under its own ticker
	}
	if n := s.count(t, "select count(*) from screener_rows"); n != 3 {
		t.Fatalf("%d rows", n)
	}
	if n := s.count(t, "select coalesce(sum(credits), 0) from ai_usage"); n != 0 {
		t.Fatalf("credits spent on the screener: %d", n)
	}

	all := c.call("GET", "/api/screener/asia?limit=100", nil, 200)
	if num(t, all, "total") != 785 || len(list(t, all, "rows")) != 100 || isNull(t, all, "nextCursor") || len(list(t, all, "source.lists")) != 10 {
		t.Fatalf("all: total %v", num(t, all, "total"))
	}
	ph := c.call("GET", "/api/screener/asia?market=PH&limit=100", nil, 200)
	for _, r := range list(t, ph, "rows") {
		if str(t, r, "status") != "unavailable" {
			t.Fatalf("PSEi row %v", r)
		}
		// Research only, and saying why; PLDT trades as its ADR.
		if str(t, r, "ticker") != "TEL.PS" && (!isNull(t, r, "tradableSymbol") ||
			str(t, r, "tradeNote") != "No keyless Yahoo data for this listing (Yahoo answered 404 on 2026-10-13)") {
			t.Fatalf("PSEi row %v", r)
		}
	}
	if num(t, ph, "total") != 30 {
		t.Fatalf("PH %v", num(t, ph, "total"))
	}
	samsung := list(t, c.call("GET", "/api/screener/asia?q=005930.KS", nil, 200), "rows")[0]
	if str(t, samsung, "tradableSymbol") != "SMSN" || str(t, samsung, "tradeVia") != "listing" || str(t, samsung, "status") != "pending" {
		t.Fatalf("Samsung %v", samsung)
	}
	if tsmc := list(t, c.call("GET", "/api/screener/asia?q=2330.TW", nil, 200), "rows")[0]; str(t, tsmc, "tradableSymbol") != "TSM" ||
		str(t, tsmc, "tradeVia") != "adr" {
		t.Fatalf("TSMC %v", tsmc)
	}
	c.fail("GET", "/api/screener/asia?filter=cheap", nil, 400, "bad_request")
	c.fail("GET", "/api/screener/asia?sort=drop", nil, 400, "bad_request")
	s.client(t).fail("GET", "/api/screener/asia", nil, 401, "unauthorized")
}

// screenerQuoteKeys is the contract's ScreenerQuote.
const screenerQuoteKeys = "source class tier price currency lastTradeAt readAt delayMinutes delayBasis delaySeconds timeBasis label " +
	"session refreshSeconds staleAt stale"

// The screener's live columns through the real poller, Yahoo client and
// database: a listed constituent (6501.T) priced from the Trade page's quote
// feed, an ADR-traded one (7203.T, which trades as TM) from the screener's own
// watch tier at its own Tokyo quote, never the ADR's; each derived against the
// stored run; the quotes endpoint returns the same rows in place.
func TestIntegrationScreenerLive(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	s := boot(t, options{yahoo: func(f *yahootest.Fake) {
		for sym, price := range map[string]float64{"6501.T": 4000, "7203.T": 3000} {
			h := yahootest.Daily(sym, "JPY", price, price)
			h.Meta.PriceTime, h.Meta.Timezone = &now, "Asia/Tokyo"
			f.Set(h)
		}
		f.Set(yahootest.Daily("TM", "USD", 190))
		f.SetFX("JPY", 0.0066)
	}})
	before := now.Add(-48 * time.Hour)
	for _, ticker := range []string{"6501.T", "7203.T"} {
		if _, err := s.db.Exec(context.Background(), `insert into screener_rows (ticker, status, attempted_at, computed_at, yahoo_name,
			currency, price, price_at, fx_usd, fx_pair, fx_at, price_usd, weeks, first_week, sma200w, pct_vs_sma, prev_pct_vs_sma,
			rsi14w, ath, ath_at, drawdown_pct) values ($1, 'ok', $2, $2, $1, 'JPY', 4500, $2, 0.0065, 'JPYUSD=X', $2, 29.25, 300,
			'2000-01-03', 5000, -10, -12, 41, 4800, '2024-03-04', -6.25)`, ticker, before); err != nil {
			t.Fatal(err)
		}
	}
	c := s.signIn(t)
	var v any
	eventually(t, 40*time.Second, "both quotes (the watch tier reads after every instrument)", func() bool {
		v = c.call("GET", "/api/screener/asia/quotes?tickers=6501.T,7203.T,NOPE.T", nil, 200)
		rows := list(t, v, "rows")
		return len(rows) == 2 && !isNull(t, rows[0], "quote") && !isNull(t, rows[1], "quote")
	})
	shape(t, v, "", "rows unknown asOf run lastCompletedAt pollSeconds")
	shapeAll(t, v, "rows", screenerRowKeys)
	if u := list(t, v, "unknown"); len(u) != 1 || u[0] != "NOPE.T" {
		t.Fatalf("unknown %v", u)
	}
	for i, want := range []struct {
		ticker, tier string
		price        float64
		trade        string
	}{{"6501.T", "trade", 4000, "6501.T"}, {"7203.T", "screener", 3000, "TM"}} {
		r := list(t, v, "rows")[i]
		shape(t, r, "quote", screenerQuoteKeys)
		if str(t, r, "ticker") != want.ticker || str(t, r, "quote.tier") != want.tier || num(t, r, "price") != want.price ||
			num(t, r, "quote.price") != want.price || str(t, r, "quote.source") != "yahoo" || str(t, r, "quote.currency") != "JPY" ||
			num(t, r, "quote.delayMinutes") != 15 || str(t, r, "quote.delayBasis") != "measured" ||
			!isTime(t, r, "quote.lastTradeAt").Equal(now) || !isTime(t, r, "priceAt").Equal(now) || isTime(t, r, "quote.readAt").Before(now) ||
			!slices.Contains([]string{"open", "closed"}, str(t, r, "quote.session")) || str(t, r, "tradableSymbol") != want.trade {
			t.Fatalf("%s: %v", want.ticker, r)
		}
		isTime(t, r, "quote.staleAt")
		pct, dd := (want.price/5000-1)*100, (want.price/4800-1)*100
		if !near(num(t, r, "pctVsSma"), math.Round(pct*100)/100) || str(t, r, "direction") != map[bool]string{true: "deepening", false: "recovering"}[pct < -12] ||
			!near(num(t, r, "drawdownPct"), math.Round(dd*100)/100) || num(t, r, "rsi14w") != 41 || !near(num(t, r, "priceUsd"), want.price*0.0066) ||
			str(t, r, "fx.pair") != "JPYUSD=X" || num(t, r, "fx.usdPerUnit") != 0.0066 {
			t.Fatalf("%s derived: %v", want.ticker, r)
		}
	}
	// The page carries the same rows, its filters counting the live values.
	page := c.call("GET", "/api/screener/asia?q=7203.T", nil, 200)
	if r := list(t, page, "rows")[0]; str(t, r, "quote.tier") != "screener" || num(t, r, "price") != 3000 || num(t, page, "counts.below") != 1 {
		t.Fatalf("page %v", page) // −40%: below
	}
	c.fail("GET", "/api/screener/asia/quotes", nil, 400, "bad_request")
	s.client(t).fail("GET", "/api/screener/asia/quotes?tickers=7203.T", nil, 401, "unauthorized")
}

// The screener page's and row's contract fields, for shape.
const (
	screenerPageKeys = "rows nextCursor total counts markets asOf run lastCompletedAt nextRunAt schedule pollSeconds source method"
	screenerRowKeys  = "ticker code name listName market indices currency status statusNote price priceAt priceUsd fx weeks " +
		"sma200w pctVsSma pctVsSmaPrev direction rsi14w ath athAt athFrom drawdownPct updatedAt checkedAt tradableSymbol tradeVia tradeNote quote"
)
