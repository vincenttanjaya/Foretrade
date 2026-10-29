package alerts

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// fakeHigh is the highest daily high of the fake market's newest bars of pair
// (Binance serves 5,000, Hyperliquid 320) and that bar's open: the fakes
// build each bar's high as max(open, close) × 1.005.
func fakeHigh(pair string, bars int) (float64, time.Time) {
	var hi float64
	var at time.Time
	for k := bars - 1; k >= 0; k-- { // oldest first: the earliest wins a tie
		if h := max(markettest.Close(pair, k+1), markettest.Close(pair, k)) * 1.005; h > hi {
			hi, at = h, markettest.End.AddDate(0, 0, -k)
		}
	}
	return hi, at
}

func athOf(t *testing.T, e *apptest.Env, symbol string) ATHQuote {
	t.Helper()
	w := e.Do("GET", "/api/alerts/ath?symbol="+symbol, nil)
	if w.Code != 200 {
		t.Fatalf("ath %s: %d %s", symbol, w.Code, w.Body)
	}
	return apptest.JSON[struct{ ATH ATHQuote }](t, w).ATH
}

// fakeQuoteFeed makes symbol a venue "yahoo" quote-feed stock on the SGD
// listing D05.SI for the test (Lookup knows only the configured universe).
func fakeQuoteFeed(t *testing.T, symbol string) market.AssetInfo {
	t.Helper()
	fake := market.AssetInfo{Symbol: symbol, Pair: "D05.SI", Name: "Test SGX listing", Class: market.Stock, Venue: "yahoo",
		Listing: &market.Listing{Region: "asia", SessionVenue: "SGX", Underlying: "SGX: D05 ordinary share", LocalCurrency: "SGD", Yahoo: "D05.SI"}}
	prev := lookup
	lookup = func(s string) (market.AssetInfo, bool) {
		if strings.EqualFold(strings.TrimSpace(s), symbol) {
			return fake, true
		}
		return prev(s)
	}
	t.Cleanup(func() { lookup = prev })
	return fake
}

// sgd is the D05.SI chart with the given closes, its last-price quote moved to last.
func sgd(e *apptest.Env, last float64, closes ...float64) {
	c := yahootest.Daily("D05.SI", "SGD", closes...)
	c.Meta.Price = &last
	e.Yahoo.Set(c)
}

// The high comes from the instrument's own source: Binance daily highs since
// listing for crypto, the Yahoo lifetime high of a USD perp's listing, the
// perp's own history otherwise, and a quote feed's listing in its currency.
func TestDrawdownATHSources(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})

	hi, at := fakeHigh("BTCUSDT", 5000)
	from := markettest.End.AddDate(0, 0, -4999)
	q := athOf(t, e, "btc")
	if q.Symbol != "BTC" || q.High != hi || !q.At.Equal(at) || !q.From.Equal(from) || q.Live || q.Source != "binance" || q.Ticker != "BTCUSDT" ||
		q.Currency != "USD" || q.Basis != "mark" || q.Price != 100_000 || q.FromATHPct != round2((100_000/hi-1)*100) ||
		q.Label != "Binance BTCUSDT daily highs since "+from.Format(time.DateOnly) {
		t.Fatalf("BTC %+v (want high %v at %v)", q, hi, at)
	}

	// A USD perp reads its listing's Yahoo lifetime high, priced against the perp's USD mark.
	if code, msg := errMsg(t, e, "GET", "/api/alerts/ath?symbol=NVDA", ""); code != 502 || !strings.Contains(msg, "unavailable") {
		t.Fatalf("NVDA without Yahoo data: %d %q", code, msg)
	}
	nvda := yahootest.Daily("NVDA", "USD", 100, 250, 180)
	e.Yahoo.Set(nvda)
	q = athOf(t, e, "NVDA")
	if q.High != 252.5 || !q.At.Equal(nvda.Bars[1].T) || !q.From.Equal(nvda.Bars[0].T) || q.Source != "yahoo" || q.Ticker != "NVDA" || q.Currency != "USD" ||
		q.Basis != "mark" || q.Price != 235 || q.Live ||
		q.Label != "Yahoo Finance NVDA lifetime daily highs since "+nvda.Bars[0].T.Format(time.DateOnly)+" (split-adjusted)" {
		t.Fatalf("NVDA %+v", q)
	}
	// A listing chart in another currency than the USD mark is never compared with it.
	e.Yahoo.Set(yahootest.Daily("TSM", "TWD", 1000, 1100))
	if code, _ := errMsg(t, e, "GET", "/api/alerts/ath?symbol=TSM", ""); code != 502 {
		t.Fatalf("TSM in TWD: %d", code)
	}
	// A mark above the whole history is the high itself, set live.
	e.Yahoo.Set(yahootest.Daily("AAPL", "USD", 100, 200))
	if q = athOf(t, e, "AAPL"); q.High != 332 || !q.Live || time.Since(q.At) > time.Minute || q.Price != 332 || q.FromATHPct != 0 {
		t.Fatalf("AAPL above its history %+v", q)
	}

	// A KRW underlying the oracle converts: the perp's own daily history, labelled as such.
	hi, at = fakeHigh("xyz:SMSN", 320)
	from = markettest.End.AddDate(0, 0, -319)
	q = athOf(t, e, "SMSN")
	if q.High != hi || !q.At.Equal(at) || !q.From.Equal(from) || q.Source != "perp" || q.Ticker != "xyz:SMSN" || q.Currency != "USD" || q.Basis != "mark" ||
		q.Price != 202.5 || q.Label != "perp history since "+from.Format(time.DateOnly)+", not the listing's lifetime high" {
		t.Fatalf("SMSN %+v", q)
	}
	if q = athOf(t, e, "XYZ100"); q.Source != "perp" || q.Ticker != "xyz:XYZ100" { // a USD perp with no listing
		t.Fatalf("XYZ100 %+v", q)
	}

	// A quote feed (venue "yahoo"): the listing's high and last price in SGD, not the USD mark.
	fakeQuoteFeed(t, "DBSX")
	sgd(e, 36, 30, 40, 36)
	if q = athOf(t, e, "dbsx"); q.Symbol != "DBSX" || q.High != 40.4 || q.Source != "yahoo" || q.Ticker != "D05.SI" || q.Currency != "SGD" ||
		q.Basis != "listing" || q.Price != 36 || q.FromATHPct != round2((36/40.4-1)*100) || !strings.HasSuffix(q.Label, "(split-adjusted), in SGD") {
		t.Fatalf("quote feed %+v", q)
	}
	// The configured rows: DBS on that listing, HSDT on its USD one, both against the listing's own price;
	// KIOXIA (a JPY underlying) on the perp's split-restated history, labelled as such.
	if q = athOf(t, e, "DBS"); q.Symbol != "DBS" || q.High != 40.4 || q.Source != "yahoo" || q.Ticker != "D05.SI" || q.Currency != "SGD" ||
		q.Basis != "listing" || q.Price != 36 {
		t.Fatalf("DBS %+v", q)
	}
	e.Yahoo.Set(yahootest.Daily("HSDT", "USD", 30, 3))
	if q = athOf(t, e, "HSDT"); q.Ticker != "HSDT" || q.Currency != "USD" || q.Basis != "listing" || q.Price != 3 || q.High != 30.3 {
		t.Fatalf("HSDT %+v", q)
	}
	// A Yahoo high over 100× the price (reverse splits) is withheld, never measured from: on the
	// listing price and on a USD perp's mark alike, for the preview and for a new trigger.
	e.Yahoo.Set(yahootest.Daily("HSDT", "USD", 294, 3))
	if q = athOf(t, e, "HSDT"); q.High != 296.94 || q.FromATHPct != round2((3/296.94-1)*100) {
		t.Fatalf("HSDT under 100× %+v", q)
	}
	e.Yahoo.Set(yahootest.Daily("HSDT", "USD", 27_000_000, 2.66))
	const hsdt = "HSDT has no usable all-time high: Yahoo's split-adjusted lifetime high for HSDT is over 100× the price, " +
		"which reverse splits produce, so a drawdown alert can't measure from it."
	if code, msg := errMsg(t, e, "GET", "/api/alerts/ath?symbol=HSDT", ""); code != 400 || msg != hsdt {
		t.Fatalf("HSDT over 100×: %d %q", code, msg)
	}
	if code, msg := errMsg(t, e, "POST", "/api/alerts", `{"symbol":"HSDT","trigger":"drawdown","drawdownPct":40}`); code != 400 || msg != hsdt {
		t.Fatalf("HSDT trigger over 100×: %d %q", code, msg)
	}
	e.Yahoo.Set(yahootest.Daily("NVDA", "USD", 30_000, 180)) // 30,300 against the $235 mark
	if code, msg := errMsg(t, e, "GET", "/api/alerts/ath?symbol=NVDA", ""); code != 400 || !strings.HasPrefix(msg, "NVDA has no usable all-time high") {
		t.Fatalf("NVDA over 100× its mark: %d %q", code, msg)
	}
	var n int
	if err := e.DB.QueryRow(ctx, "select count(*) from alerts where trigger = 'drawdown'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("stored drawdown triggers: %d %v", n, err)
	}
	if q = athOf(t, e, "KIOXIA"); q.Source != "perp" || q.Ticker != "xyz:KIOXIA" || q.Currency != "USD" || q.Basis != "mark" ||
		!strings.HasPrefix(q.Label, "perp history since ") || !strings.HasSuffix(q.Label, ", not the listing's lifetime high") {
		t.Fatalf("KIOXIA %+v", q)
	}
	if code, msg := errMsg(t, e, "GET", "/api/alerts/ath?symbol=PEPE", ""); code != 400 || msg != `unknown symbol "PEPE"` {
		t.Fatalf("unknown: %d %q", code, msg)
	}
}

// POST checks the rule, resolves and stores the high with its source and
// coverage, accepts a price already past the threshold (it fires on the next
// check) and the list carries each active trigger's current drawdown.
func TestDrawdownCreate(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	const shape = "A drawdown alert takes no op, price, schedule, notional, stopLoss or takeProfit."
	const pct = "drawdownPct must be 0.01–99.99 (% below the all-time high)."
	const sell = "sellPct must be a whole number from 1 to 100 (% of the paper long)."
	for body, want := range map[string]string{
		`{"symbol":"PEPE","trigger":"drawdown","drawdownPct":20}`:                                          `unknown symbol "PEPE"`,
		`{"symbol":"SOL","trigger":"drawdown"}`:                                                            pct,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":0.004}`:                                        pct,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":99.996}`:                                       pct,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":-5}`:                                           pct,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"op":"below"}`:                              shape,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"price":150}`:                               shape,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"every":"day","at":"08:00"}`:                shape,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"action":"sell","notional":100}`:            shape,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"stopLoss":100}`:                            shape,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"action":"buy"}`:                            "A drawdown alert notifies or sells: action must be notify or sell.",
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"sellPct":50}`:                              "sellPct is for action sell only.",
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"action":"sell","sellPct":0}`:               sell,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"action":"sell","sellPct":101}`:             sell,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"action":"sell","sellPct":33.5}`:            sell,
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"source":"autopilot"}`:                      "source must be manual, ticket or agent.",
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"proposalId":"x"}`:                          "proposalId must be a UUID.",
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"prompt":"Research SOL every day"}`:         "Research fields require action research.",
		`{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"note":"` + strings.Repeat("x", 201) + `"}`: "note must be at most 200 characters.",
	} {
		if code, msg := errMsg(t, e, "POST", "/api/alerts", body); code != 400 || msg != want {
			t.Errorf("%.80s: %d %q, want %q", body, code, msg, want)
		}
	}

	hi, at := fakeHigh("BTCUSDT", 5000)
	a := create(t, e, `{"symbol":"btc","trigger":"drawdown","drawdownPct":12.345,"note":" Deep dip "}`)
	dd := a.Drawdown
	if a.Symbol != "BTC" || a.Trigger != "drawdown" || a.Op != nil || a.Price != nil || a.Action != "notify" || a.Notional != nil || a.Note != "Deep dip" ||
		a.Status != "active" || dd == nil || dd.Pct != 12.35 || dd.SellPct != nil || dd.High != hi || !dd.At.Equal(at) || dd.Source != "binance" ||
		dd.Ticker != "BTCUSDT" || dd.Basis != "mark" || dd.Live || dd.Threshold != hi*(10000-1235)/10000 || dd.Outcome != nil ||
		dd.Price == nil || *dd.Price != 100_000 || *dd.FromATHPct != round2((100_000/hi-1)*100) {
		t.Fatalf("created %+v drawdown %+v", a, dd)
	}
	if stored := load(t, e, a.ID).Drawdown; stored == nil || stored.High != hi || stored.Pct != 12.35 || !stored.From.Equal(dd.From) || stored.Price != nil {
		t.Fatalf("stored %+v", stored)
	}
	if s := create(t, e, `{"symbol":"SOL","trigger":"drawdown","drawdownPct":20,"action":"sell"}`); *s.Drawdown.SellPct != 100 || s.Action != "sell" {
		t.Fatalf("default sellPct %+v", s.Drawdown)
	}
	pid := "6f1c2c1e-1111-4000-8000-0000000000d1"
	card := `{"symbol":"ETH","trigger":"drawdown","drawdownPct":30,"action":"sell","sellPct":25,"source":"agent","proposalId":"` + pid + `"}`
	if c := create(t, e, card); *c.Drawdown.SellPct != 25 || *c.ProposalID != pid || c.Source != "agent" {
		t.Fatalf("agent card %+v", c)
	}
	if code, msg := errMsg(t, e, "POST", "/api/alerts", card); code != 409 || msg != "This alert was already set." {
		t.Fatalf("replay: %d %q", code, msg)
	}

	// Already past the threshold: set anyway, and the next check fires it.
	solHigh, _ := fakeHigh("SOLUSDT", 5000)
	past := create(t, e, `{"symbol":"SOL","trigger":"drawdown","drawdownPct":1}`)
	if *past.Drawdown.Price > past.Drawdown.Threshold || solHigh*0.99 < 200 {
		t.Fatalf("not past the threshold: %+v", past.Drawdown)
	}

	// The list: each active trigger's current price and drawdown; a fired one keeps none.
	listed := map[string]Alert{}
	for _, x := range apptest.JSON[struct{ Alerts []Alert }](t, e.Do("GET", "/api/alerts", nil)).Alerts {
		listed[x.ID] = x
	}
	if d := listed[a.ID].Drawdown; d == nil || d.Price == nil || *d.Price != 100_000 || *d.FromATHPct != *dd.FromATHPct || d.Label == "" {
		t.Fatalf("listed %+v", d)
	}
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if f := load(t, e, past.ID); f.Status != "fired" || *f.FiredPrice != 200 || *f.Drawdown.Outcome != "notified" || f.TradeID != nil {
		t.Fatalf("past: %+v %+v", f, f.Drawdown)
	}
	if d := apptest.JSON[struct{ Alerts []Alert }](t, e.Do("GET", "/api/alerts", nil)).Alerts; d[len(d)-1].ID != past.ID || d[len(d)-1].Drawdown.Price != nil {
		t.Fatalf("fired trigger listed with a price: %+v", d[len(d)-1].Drawdown)
	}
	// Drawdown triggers count toward the 20 active automations: a, s and c are active.
	for range 17 {
		create(t, e, `{"symbol":"SMSN","trigger":"drawdown","drawdownPct":50}`)
	}
	if code, msg := errMsg(t, e, "POST", "/api/alerts", `{"symbol":"SMSN","trigger":"drawdown","drawdownPct":70}`); code != 409 ||
		msg != "You have 20 active alerts. Cancel one first." {
		t.Fatalf("cap: %d %q", code, msg)
	}
}

// Check ratchets the stored high to a higher checked price, fires at the
// threshold exactly (not a cent above), once, and skips disabled accounts.
func TestDrawdownCheck(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	hi, _ := fakeHigh("SOLUSDT", 5000)
	a := create(t, e, `{"symbol":"SOL","trigger":"drawdown","drawdownPct":10,"note":"Trim zone"}`)

	price(e, "SOLUSDT", hi+10)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	up := load(t, e, a.ID)
	if up.Status != "active" || up.Drawdown.High != hi+10 || !up.Drawdown.Live || time.Since(up.Drawdown.At) > time.Minute ||
		up.Drawdown.Threshold != threshold(hi+10, 1000) {
		t.Fatalf("ratchet %+v", up.Drawdown)
	}
	price(e, "SOLUSDT", hi) // below the new high: no ratchet back
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a.ID).Drawdown.High != hi+10 {
		t.Fatalf("lower price moved the high: %v", err)
	}
	level := threshold(hi+10, 1000)
	price(e, "SOLUSDT", level+0.01)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a.ID).Status != "active" {
		t.Fatalf("fired above the threshold: %v", err)
	}
	price(e, "SOLUSDT", level)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := Check(ctx, e.Deps); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	f := load(t, e, a.ID)
	if f.Status != "fired" || *f.FiredPrice != level || f.FiredAt == nil || *f.Drawdown.Outcome != "notified" || f.TradeID != nil {
		t.Fatalf("fired %+v %+v", f, f.Drawdown)
	}
	got := apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items
	if len(got) != 1 || got[0].Kind != "drawdown_alert" || got[0].Title != "SOL fell 10% below its all-time high" ||
		got[0].Body != "Now "+paper.Money(level)+" · ATH "+paper.Money(hi+10)+" · Trim zone" || got[0].Link != "/automations?tab=history&alert="+a.ID {
		t.Fatalf("bell %+v", got)
	}

	// A disabled account's triggers neither ratchet nor fire; with none active, no market call.
	off := create(t, e, `{"symbol":"ETH","trigger":"drawdown","drawdownPct":5}`)
	e.DB.Exec(ctx, "update users set disabled_at = now() where id = $1", e.User)
	price(e, "ETHUSDT", 1)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, off.ID).Status != "active" {
		t.Fatalf("disabled: %v", err)
	}
	e.Binance.Down("", true)
	price(e, "ETHUSDT", 1)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatalf("no active trigger, yet: %v", err)
	}
	e.DB.Exec(ctx, "update users set disabled_at = null where id = $1", e.User)
	if err := Check(ctx, e.Deps); err == nil || load(t, e, off.ID).Status != "active" {
		t.Fatalf("market down: %v", err)
	}
}

// A sell trigger sells its share of the current paper long at the mark with
// the usual slippage and fee, in the claim's transaction; with no long, or a
// short, it notifies "nothing to sell" and never touches the short.
func TestDrawdownSell(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "SOL", Side: "buy", Notional: new(10_000.0)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "BNB", Side: "sell", Notional: new(1_000.0)}); err != nil {
		t.Fatal(err)
	}
	qtyOf := func(sym string) float64 {
		t.Helper()
		pf, err := e.Paper.Portfolio(ctx, e.User)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pf.Positions {
			if p.Symbol == sym {
				return p.Qty
			}
		}
		return 0
	}
	long, short := qtyOf("SOL"), qtyOf("BNB")
	half := create(t, e, `{"symbol":"SOL","trigger":"drawdown","drawdownPct":10,"action":"sell","sellPct":50}`)
	none := create(t, e, `{"symbol":"ETH","trigger":"drawdown","drawdownPct":10,"action":"sell"}`)
	shorted := create(t, e, `{"symbol":"BNB","trigger":"drawdown","drawdownPct":10,"action":"sell"}`)

	solLevel := threshold(half.Drawdown.High, 1000)
	price(e, "SOLUSDT", solLevel)
	price(e, "ETHUSDT", threshold(none.Drawdown.High, 1000))
	price(e, "BNBUSDT", threshold(shorted.Drawdown.High, 1000))
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if err := Check(ctx, e.Deps); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	trades, _ := e.Paper.Trades(ctx, e.User, 10)
	alertTrades := slices.DeleteFunc(slices.Clone(trades), func(tr paper.Trade) bool { return tr.Source != "alert" })
	if len(alertTrades) != 1 {
		t.Fatalf("%d alert trades: %+v", len(alertTrades), alertTrades)
	}
	tr := alertTrades[0]
	fill := paper.FillPrice(solLevel, false)
	note := DrawdownLabel("SOL", 10, half.Drawdown.High, "USD")
	if tr.Symbol != "SOL" || tr.Side != "sell" || tr.Qty != long*50/100 || tr.Price != fill || math.Abs(tr.Fee-tr.Qty*fill*paper.FeeRate) > 1e-9 ||
		tr.Note != note || !strings.HasPrefix(note, "Drawdown alert: SOL 10% below ATH $") || tr.Ref == nil || tr.Ref.Kind != "alert" || tr.Ref.ID != half.ID {
		t.Fatalf("sale %+v ref %+v", tr, tr.Ref)
	}
	if h := load(t, e, half.ID); h.Status != "fired" || *h.Drawdown.Outcome != "sold" || h.TradeID == nil || *h.TradeID != tr.ID {
		t.Fatalf("sold alert %+v", h)
	}
	if q := qtyOf("SOL"); math.Abs(q-(long-tr.Qty)) > 1e-12 {
		t.Fatalf("SOL left %v, want %v", q, long-tr.Qty)
	}
	for _, id := range []string{none.ID, shorted.ID} {
		if a := load(t, e, id); a.Status != "fired" || *a.Drawdown.Outcome != "nothing_to_sell" || a.TradeID != nil {
			t.Fatalf("nothing to sell %+v", a.Drawdown)
		}
	}
	if q := qtyOf("BNB"); q != short || q >= 0 {
		t.Fatalf("the short moved: %v → %v", short, q)
	}

	// The rest of the long, 100%: the position closes exactly.
	all := create(t, e, `{"symbol":"SOL","trigger":"drawdown","drawdownPct":15,"action":"sell"}`)
	price(e, "SOLUSDT", threshold(all.Drawdown.High, 1500))
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if q := qtyOf("SOL"); q != 0 || *load(t, e, all.ID).Drawdown.Outcome != "sold" {
		t.Fatalf("100%% left %v", q)
	}

	got := map[string]Notification{}
	for _, n := range apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items {
		got[n.ID] = n
	}
	n := got["drawdown_order:"+half.ID]
	if n.Title != "Drawdown sell filled · SOL" || n.Link != "/account" || n.Body != fmt.Sprintf("10%% below ATH %s · sold %s SOL (50%% of the long) at %s · realized %s",
		paper.Money(half.Drawdown.High), fmtQty(tr.Qty), paper.Money(tr.Price), signed(tr.RealizedPnl)) {
		t.Fatalf("sale bell %+v", n)
	}
	if n := got["drawdown_alert:"+none.ID]; n.Title != "ETH fell 10% below its all-time high" || !strings.HasSuffix(n.Body, " · Nothing to sell: no paper long") {
		t.Fatalf("nothing-to-sell bell %+v", n)
	}
}

// Exactly once under concurrency: checks and direct fires racing for one sell
// trigger fill one sale, which closes the long and never opens a short.
func TestDrawdownSellRace(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "SOL", Side: "buy", Notional: new(5_000.0)}); err != nil {
		t.Fatal(err)
	}
	a := create(t, e, `{"symbol":"SOL","trigger":"drawdown","drawdownPct":10,"action":"sell"}`)
	level := threshold(a.Drawdown.High, 1000)
	price(e, "SOLUSDT", level)
	quotes, err := e.Market.Assets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			var err error
			if i%2 == 0 {
				err = Check(ctx, e.Deps)
			} else {
				err = fireDrawdown(ctx, e.Deps, a.ID, level, quotes)
			}
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var sales, positions int
	e.DB.QueryRow(ctx, "select count(*) from trades where user_id = $1 and source = 'alert' and ref_id = $2", e.User, a.ID).Scan(&sales)
	e.DB.QueryRow(ctx, "select count(*) from positions where user_id = $1", e.User).Scan(&positions)
	if f := load(t, e, a.ID); sales != 1 || positions != 0 || f.Status != "fired" || *f.Drawdown.Outcome != "sold" || f.TradeID == nil {
		t.Fatalf("%d sales, %d positions, alert %+v", sales, positions, f.Drawdown)
	}
}

// The claim and the sale commit together: a sale that cannot price rolls the
// claim back for the next check, and a refused sale marks the trigger failed.
func TestDrawdownSellAtomic(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "SOL", Side: "buy", Notional: new(2_000.0)}); err != nil {
		t.Fatal(err)
	}
	a := create(t, e, `{"symbol":"SOL","trigger":"drawdown","drawdownPct":10,"action":"sell"}`)
	level := threshold(a.Drawdown.High, 1000)
	if err := fireDrawdown(ctx, e.Deps, a.ID, level, nil); !errors.Is(err, market.ErrUpstream) {
		t.Fatalf("no quotes: %v", err)
	}
	var trades int
	e.DB.QueryRow(ctx, "select count(*) from trades where user_id = $1 and source = 'alert'", e.User).Scan(&trades)
	if f := load(t, e, a.ID); f.Status != "active" || f.Drawdown.Outcome != nil || f.FiredAt != nil || trades != 0 {
		t.Fatalf("rolled back? %+v, %d trades", f, trades)
	}
	price(e, "SOLUSDT", level)
	if err := Check(ctx, e.Deps); err != nil || *load(t, e, a.ID).Drawdown.Outcome != "sold" {
		t.Fatalf("next check: %v", err)
	}

	// A refused sale (the paper engine does not know this test listing): failed, with why, and a bell item.
	fakeQuoteFeed(t, "DBSX")
	sgd(e, 36, 30, 40, 36)
	r := create(t, e, `{"symbol":"DBSX","trigger":"drawdown","drawdownPct":10,"action":"sell"}`)
	if _, err := e.DB.Exec(ctx, "insert into positions (user_id, symbol, qty, avg_price) values ($1, 'DBSX', 10, 30)", e.User); err != nil {
		t.Fatal(err)
	}
	level = threshold(40.4, 1000)
	sgd(e, level, 30, 40, 36)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	f := load(t, e, r.ID)
	if f.Status != "failed" || f.Error == nil || *f.Error != `unknown symbol "DBSX"` || f.Drawdown.Outcome != nil || *f.FiredPrice != level {
		t.Fatalf("refused %+v", f)
	}
	items := apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items
	if i := slices.IndexFunc(items, func(n Notification) bool { return n.ID == "drawdown_failed:"+r.ID }); i < 0 ||
		items[i].Title != "Drawdown sell not filled · DBSX" || items[i].Body != `unknown symbol "DBSX"` {
		t.Fatalf("bell %+v", items)
	}
}

// A quote feed compares the listing's last price with its high, both in the
// listing's currency: it ratchets and fires on SGD prices, never on a quote
// in another currency, and the bell says SGD.
func TestDrawdownQuoteFeed(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	fakeQuoteFeed(t, "DBSX")
	sgd(e, 36, 30, 40, 36)
	a := create(t, e, `{"symbol":"DBSX","trigger":"drawdown","drawdownPct":10}`)
	if dd := a.Drawdown; dd.Basis != "listing" || dd.Currency != "SGD" || dd.High != 40.4 || *dd.Price != 36 || dd.Threshold != threshold(40.4, 1000) {
		t.Fatalf("created %+v", dd)
	}
	e.Binance.Down("", true) // a notify quote feed needs no USD mark
	sgd(e, 42, 30, 40, 36)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a.ID).Drawdown.High != 42 {
		t.Fatalf("ratchet in SGD: %v %+v", err, load(t, e, a.ID).Drawdown)
	}
	usd := yahootest.Daily("D05.SI", "USD", 1, 1)
	e.Yahoo.Set(usd)
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a.ID).Status != "active" {
		t.Fatalf("a USD quote fired an SGD trigger: %v", err)
	}
	level := threshold(42, 1000)
	sgd(e, level, 30, 40, 36)
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if f := load(t, e, a.ID); f.Status != "fired" || *f.FiredPrice != level || *f.Drawdown.Outcome != "notified" {
		t.Fatalf("fired %+v", f)
	}
	items := apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items
	if len(items) != 1 || items[0].Title != "DBSX fell 10% below its all-time high" || items[0].Body != "Now SGD 37.80 · ATH SGD 42.00" {
		t.Fatalf("bell %+v", items)
	}
}

// A sell trigger on a quote feed whose quote is stale waits for a fresh one:
// the engine would refuse the sale (paper.HaltedError) and the trigger would
// end failed instead of selling once the listing is quoted again.
func TestDrawdownSellWaitsForAFreshQuote(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	c := yahootest.Daily("D05.SI", "SGD", 30, 40, 36)
	old := time.Now().Add(-market.QuoteStaleAfter - time.Hour)
	c.Meta.PriceTime = &old
	e.Yahoo.Set(c)
	e.Yahoo.SetFX("SGD", 0.78)
	if _, err := e.DB.Exec(ctx, "insert into positions (user_id, symbol, qty, avg_price) values ($1, 'DBS', 10, 30)", e.User); err != nil {
		t.Fatal(err)
	}
	a := create(t, e, `{"symbol":"DBS","trigger":"drawdown","drawdownPct":10,"action":"sell"}`)
	if *a.Drawdown.Price > a.Drawdown.Threshold {
		t.Fatalf("not past the threshold: %+v", a.Drawdown)
	}
	if as, err := e.Market.Assets(ctx); err != nil || !slices.ContainsFunc(as, func(x market.Asset) bool { return x.Symbol == "DBS" && x.Halted }) {
		t.Fatalf("DBS not halted: %v", err)
	}
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	var qty float64
	if err := e.DB.QueryRow(ctx, "select qty from positions where user_id = $1 and symbol = 'DBS'", e.User).Scan(&qty); err != nil || qty != 10 {
		t.Fatalf("position %v %v", qty, err)
	}
	if got := load(t, e, a.ID); got.Status != "active" || got.Error != nil || got.Drawdown.Outcome != nil {
		t.Fatalf("a stale quote ended the trigger: %+v", got)
	}
}

// A price alert on a quote-feed stock can be set in the listing's own
// currency: checked against the local quote (not the USD mark), refused in
// another currency, on crypto or perps, or for an order, and stated in that
// currency in the bell.
func TestPriceAlertInLocalCurrency(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	sgd(e, 36, 30, 40, 36)
	e.Yahoo.SetFX("SGD", 0.78)
	for body, want := range map[string]string{
		`{"symbol":"DBS","op":"below","price":37,"currency":"SGD"}`:                               "DBS is already below SGD 37.00 (now SGD 36.00).",
		`{"symbol":"DBS","op":"below","price":35,"currency":"JPY"}`:                               "DBS alerts are set in USD or SGD.",
		`{"symbol":"BTC","op":"below","price":35,"currency":"SGD"}`:                               "BTC alerts are set in USD.",
		`{"symbol":"DBS","op":"below","price":35,"currency":"SGD","action":"buy","notional":100}`: "only a notify alert takes SGD",
	} {
		if w := e.Do("POST", "/api/alerts", body); w.Code != 400 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	a := create(t, e, `{"symbol":"DBS","op":"below","price":35,"currency":"sgd","note":"Add more"}`)
	// A legacy USD alert retains its original comparison and saved units.
	usd, err := scan(e.DB.QueryRow(ctx, `insert into alerts (user_id, symbol, op, price)
		values ($1, 'DBS', 'below', 20) returning `+columns, e.User))
	if err != nil {
		t.Fatal(err)
	}
	if a.Currency == nil || *a.Currency != "SGD" || *a.Price != 35 || usd.Currency != nil {
		t.Fatalf("created %+v / %+v", a, usd)
	}
	if err := Check(ctx, e.Deps); err != nil || load(t, e, a.ID).Status != "active" {
		t.Fatalf("fired at SGD 36: %v", err)
	}
	sgd(e, 34.5, 30, 40, 34.5)                                                 // SGD 34.50 ≈ $26.9: under the SGD level, above the $20 one
	e.Market.Now = func() time.Time { return time.Now().Add(2 * time.Minute) } // the SGD 36 read is over a minute old
	if err := Check(ctx, e.Deps); err != nil {
		t.Fatal(err)
	}
	if f := load(t, e, a.ID); f.Status != "fired" || *f.FiredPrice != 34.5 || *f.Currency != "SGD" {
		t.Fatalf("fired %+v", f)
	}
	if load(t, e, usd.ID).Status != "active" {
		t.Fatal("the USD alert fired")
	}
	items := apptest.JSON[struct{ Items []Notification }](t, e.Do("GET", "/api/notifications", nil)).Items
	if len(items) != 1 || items[0].Title != "DBS fell below SGD 35.00" || items[0].Body != "Now SGD 34.50 · Add more" {
		t.Fatalf("bell %+v", items)
	}
}
