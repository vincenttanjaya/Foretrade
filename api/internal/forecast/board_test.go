package forecast

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func TestParseRule(t *testing.T) {
	for q, want := range map[string]*PriceQuestion{
		"Will the price of Bitcoin be above $86,000 on October 6?":                  {"BTC", "above", 86000, "close"},
		"will the price of ethereum be greater than $4,200 on October 6?":           {"ETH", "above", 4200, "close"},
		"Will the price of Bitcoin be less than $74,000 on October 6?":              {"BTC", "below", 74000, "close"},
		"Will Solana close below $150 on October 31?":                               {"SOL", "below", 150, "close"},
		"Will XRP be above $2.5 on October 9?":                                      {"XRP", "above", 2.5, "close"},
		"Will Bitcoin reach $150,000 in October?":                                   {"BTC", "above", 150000, "touch"},
		"Will Bitcoin dip to $60,000 by December 31, 2026?":                         {"BTC", "below", 60000, "touch"},
		"Will Ethereum Classic reach $30 in October?":                               {"ETC", "above", 30, "touch"},
		"Will Bitcoin Cash dip to $300 before 2027?":                                {"BCH", "below", 300, "touch"},
		"Will BNB reach $1k in October?":                                            {"BNB", "above", 1000, "touch"},
		"Will the price of Bitcoin be between $74,000 and $76,000 on October 6?":    nil, // a range
		"Will Variational FDV be above $500M one day after launch?":                 nil,
		"Will Hyperliquid reach $60 in October?":                                    nil, // not in the universe
		"Will PAX Gold reach $4,000 in October?":                                    nil, // gold markets price the metal
		"Will Bitcoin hit $100k by December 31?":                                    nil, // not a phrasing the model reads
		"Clarity Act (H.R.3633) signed into law in 2026?":                           nil,
		"Will the Fed decrease interest rates by 25 bps after the October meeting?": nil,
	} {
		got := parseRule(q)
		if (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Errorf("%q: %+v, want %+v", q, got, want)
		}
	}
}

// Hand values (σ√T ≈ 0.143346 for 50% over 30 days): above 110 ≈ 0.2531 and
// a touch of it twice that; a reached barrier is certain. The quant leans
// only at 10+ points (a low-confidence read), never on a thin market, and
// prices $100 on the side it leans to.
func TestQuantEstimate(t *testing.T) {
	month := 30 * 24 * time.Hour
	for _, c := range []struct {
		name   string
		r      PriceQuestion
		spot   float64
		yes    float64
		thin   bool
		p      float64
		method string
		edge   float64
		signal string
		ev     *float64
	}{
		{"close above", PriceQuestion{"BTC", "above", 110, "close"}, 100, 0.12, false, 0.2531, "lognormal", 13.3, "yes", ptr(110.92)},
		{"close below", PriceQuestion{"BTC", "below", 90, "close"}, 100, 0.35, false, 0.2312, "lognormal", -11.9, "no", ptr(18.28)},
		{"touch up", PriceQuestion{"BTC", "above", 110, "touch"}, 100, 0.45, false, 0.5061, "touch", 5.6, "wait", nil},
		{"touch down", PriceQuestion{"BTC", "below", 90, "touch"}, 100, 0.61, false, 0.4623, "touch", -14.8, "no", ptr(37.87)},
		{"already reached", PriceQuestion{"BTC", "above", 95, "touch"}, 100, 0.97, false, 1, "touch", 3, "wait", nil},
		{"thin", PriceQuestion{"BTC", "above", 110, "close"}, 100, 0.05, true, 0.2531, "lognormal", 20.3, "wait", nil},
	} {
		e := quantEstimate(c.r, c.spot, 0.5, month, c.yes, c.thin)
		if e.P != c.p || e.Method != c.method || e.EdgePp != c.edge || e.Signal != c.signal || (e.EVPer100 == nil) != (c.ev == nil) ||
			c.ev != nil && *e.EVPer100 != *c.ev || e.Spot != c.spot || e.VolAnnPct != 50 || e.Days != 30 {
			t.Errorf("%s: %+v (ev %v)", c.name, e, deref(e.EVPer100))
		}
	}
	// A report's lean keeps MarketOdds' rules: ±5 points at medium confidence.
	if l := lean(0.53, 0.48, false, "medium"); l.EdgePp != 5 || l.Signal != "yes" || *l.EVPer100 != 10.42 {
		t.Fatalf("medium lean: %+v", l)
	}
	if l := lean(0.43, 0.48, false, "low"); l.Signal != "wait" || l.EVPer100 != nil {
		t.Fatalf("low lean under 10 points: %+v", l)
	}
	if l := lean(0.2, 0, false, "medium"); l.Signal != "yes" || l.EVPer100 != nil {
		t.Fatalf("no EV at a zero price: %+v", l)
	}
}

// t0 is the board's clock in the handler tests: far ahead, so drafts stay
// valid for the real POST /api/forecasts whatever today is.
var t0 = time.Date(2099, 10, 5, 3, 0, 0, 0, time.UTC)

// bm is one board market as Gamma lists it, with an id and a 24h volume.
func bm(id, question, end string, yes float64, volume any, mods ...func(map[string]any)) map[string]any {
	return gammaMarket(question, end, yes, volume, append([]func(map[string]any){func(m map[string]any) {
		m["id"], m["volume24hr"], m["oneDayPriceChange"] = id, 1234.5, 0.0125
		m["slug"], m["conditionId"], m["description"], m["acceptingOrders"] = "leaf-"+id, "0x"+strings.Repeat("a", 64), question+" Complete original rules.", true
	}}, mods...)...)
}

func ev(slug, title, end string, vol24 float64, markets ...map[string]any) map[string]any {
	return map[string]any{"slug": slug, "title": title, "endDate": end, "volume24hr": vol24, "markets": markets, "active": true, "closed": false}
}

const (
	aboveSlug = "bitcoin-above-on-october-6-2099"
	q98       = "Will the price of Bitcoin be above $98,000 on October 6?"
	q100      = "Will the price of Bitcoin be above $100,000 on October 6?"
	fedSlug   = "fed-decision-in-october-2099"
	fedCut    = "Will the Fed decrease interest rates by 25 bps after the October 2099 meeting?"
)

var fedRules = strings.Repeat("This market will resolve to Yes if the Fed cuts by 25 bps. ", 30) // 1,770 characters

// boardListing is the fake Gamma's listing: tag → events.
func boardListing() map[string][]map[string]any {
	tomorrow, month := "2099-10-06T16:00:00Z", "2099-11-01T04:00:00Z"
	clarity := ev("clarity-act-signed-into-law-in-2099", "Clarity Act signed into law in 2099?", "2100-01-01T05:00:00Z", 18782,
		bm("301", "Clarity Act (H.R.3633) signed into law in 2099?", "2100-01-01T05:00:00Z", 0.05, 2.3e7))
	return map[string][]map[string]any{
		"crypto": {
			ev(aboveSlug, "Bitcoin above ___ on October 6?", tomorrow, 74659,
				bm("103", "Will the price of Bitcoin be above $102,000 on October 6?", tomorrow, 0.2, 500), // thin
				bm("101", q98, tomorrow, 0.7, 20000),
				bm("102", q100, tomorrow, 0.48, 30000),
				bm("104", "Will the price of Bitcoin be above $104,000 on October 6?", tomorrow, 0.1, 9000, func(m map[string]any) { m["closed"] = true }),
				bm("105", "Will the price of Bitcoin be less than $96,000 on October 6?", tomorrow, 0.1, 2000)),
			ev("what-price-will-bitcoin-hit-in-october-2099", "What price will Bitcoin hit in October?", month, 426969,
				bm("201", "Will Bitcoin reach $110,000 in October?", month, 0.3, 1e5),
				bm("202", "Will Bitcoin dip to $90,000 in October?", month, 0.35, 1e5, func(m map[string]any) { m["description"] = "Any Binance 1m low at or under $90,000." })),
			ev("bitcoin-up-or-down-october-5-2099-10pm-et", "Bitcoin Up or Down - October 5, 10PM ET", tomorrow, 9e5, // intraday
				bm("601", "Will Bitcoin go up?", tomorrow, 0.5, 1e5)),
			ev("ethereum-above-soon", "Ethereum above ___ soon?", "2099-10-05T03:30:00Z", 8e5, // ends within the hour
				bm("602", "Will the price of Ethereum be above $4,000 on October 5?", "2099-10-05T03:30:00Z", 0.5, 1e5)),
			ev("updown", "Bitcoin Up or Down on October 6?", tomorrow, 7e5, // not Yes/No
				bm("603", "Bitcoin Up or Down on October 6?", tomorrow, 0.5, 1e5, func(m map[string]any) { m["outcomes"] = `["Up", "Down"]` })),
			ev("bitcoin-above-on-october-5-2099", "Bitcoin above ___ on October 5?", "2099-10-05T16:00:00Z", 1000,
				bm("501", "Will the price of Bitcoin be above $99,000 on October 5?", "2099-10-05T16:00:00Z", 0.6, 4000)),
			ev("toncoin-above-on-october-6-2099", "Toncoin above ___ on October 6?", tomorrow, 900,
				bm("701", "Will the price of Toncoin be above $5 on October 6?", tomorrow, 0.5, 4000)),
			clarity,
		},
		"economy": {
			ev(fedSlug, "Fed Decision in October?", "2099-10-29T18:00:00Z", 537358,
				bm("401", fedCut, "2099-10-29T18:00:00Z", 0.82, 5e6, func(m map[string]any) { m["description"] = fedRules }),
				bm("402", "Will there be no change in Fed interest rates after the October 2099 meeting?", "2099-10-29T18:00:00Z", 0.16, 4e6)),
			clarity, // in both listings: "all" lists it once, as crypto
		},
	}
}

// fakeGamma serves the board's listing (/events?tag_slug=), events by slug,
// markets by id and an empty public search, counting each kind of read.
type fakeGamma struct {
	URL   string
	mu    sync.Mutex
	down  bool
	delay time.Duration
	hits  map[string]int // the tag, "slug:<slug>", "id:<id>", "search"
	tags  map[string][]map[string]any
	byID  map[string]map[string]any // GET /markets?id=: markets outside the listings, with their "events"
}

func newFakeGamma(t *testing.T) *fakeGamma {
	g := &fakeGamma{hits: map[string]int{}, tags: boardListing(), byID: map[string]map[string]any{}}
	for _, events := range g.tags {
		for _, e := range events {
			for _, m := range e["markets"].([]map[string]any) {
				leaf := maps.Clone(m)
				leaf["events"] = []map[string]any{{"slug": e["slug"], "title": e["title"], "active": true, "closed": false}}
				g.byID[m["id"].(string)] = leaf
			}
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		g.mu.Lock()
		down, delay := g.down, g.delay
		key := q.Get("tag_slug")
		switch {
		case r.URL.Path == "/events" && q.Get("slug") != "":
			key = "slug:" + q.Get("slug")
		case r.URL.Path == "/markets":
			key = "id:" + q.Get("id")
		case r.URL.Path == "/public-search":
			key = "search"
		}
		g.hits[key]++
		g.mu.Unlock()
		time.Sleep(delay)
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		out := []map[string]any{}
		switch {
		case r.URL.Path == "/events" && q.Get("tag_slug") != "":
			if q.Get("active") != "true" || q.Get("closed") != "false" || q.Get("order") != "volume24hr" || q.Get("ascending") != "false" || q.Get("limit") != "30" {
				http.Error(w, "unexpected listing "+r.URL.RawQuery, http.StatusBadRequest)
				return
			}
			out = g.tags[q.Get("tag_slug")]
		case r.URL.Path == "/events":
			for _, evs := range g.tags {
				for _, e := range evs {
					if e["slug"] == q.Get("slug") && len(out) == 0 {
						out = append(out, e)
					}
				}
			}
		case r.URL.Path == "/markets":
			if m, ok := g.byID[q.Get("id")]; ok {
				out = append(out, m)
			}
		case r.URL.Path == "/public-search":
			json.NewEncoder(w).Encode(map[string]any{"events": []any{}})
			return
		default:
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	g.URL = srv.URL
	return g
}

func (g *fakeGamma) count(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hits[key]
}

func (g *fakeGamma) set(f func(g *fakeGamma)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

// clock is a board clock tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// boardEnv is the forecast routes with a board on the test's clock and fake Gamma.
func boardEnv(t *testing.T, o apptest.Options) (*apptest.Env, *fakeGamma, *clock) {
	c := &clock{t: t0}
	e := apptest.New(t, func(mux *http.ServeMux, d *app.Deps) {
		b := newBoard(d)
		b.now = c.now
		register(mux, d, b)
	}, o)
	g := newFakeGamma(t)
	e.PolymarketBase = g.URL
	return e, g, c
}

type boardBody struct {
	Events []BoardEvent
	AsOf   time.Time
	Stale  bool
}

func getBoard(t *testing.T, e *apptest.Env, query string) boardBody {
	t.Helper()
	w := e.Do("GET", "/api/predictions/markets"+query, nil)
	if w.Code != 200 {
		t.Fatalf("board%s: %d %s", query, w.Code, w.Body)
	}
	return apptest.JSON[boardBody](t, w)
}

func slugs(evs []BoardEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Slug)
	}
	return out
}

func boardRow(t *testing.T, evs []BoardEvent, id string) BoardMarket {
	t.Helper()
	for _, e := range evs {
		for _, m := range e.Markets {
			if m.ID == id {
				return m
			}
		}
	}
	t.Fatalf("market %s not on the board", id)
	return BoardMarket{}
}

// The listing keeps the open Yes/No markets that end over an hour out and are
// not intraday, ordered price questions first by strike; price questions get
// the quant estimate from the outlook's spot and σ (none for a halted pair);
// "all" lists an event tagged both ways once; the filters and sorts hold.
func TestBoardList(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	e.Binance.Halt("TONUSDT", true)
	for q, msg := range map[string]string{"?category=sports": "category must be all, crypto or macro.", "?sort=hot": "sort must be edge, volume or ending."} {
		if w := e.Do("GET", "/api/predictions/markets"+q, nil); w.Code != 400 || !strings.Contains(w.Body.String(), msg) {
			t.Errorf("%s: %d %s", q, w.Code, w.Body)
		}
	}
	if w := e.DoAs(nil, "GET", "/api/predictions/markets", nil); w.Code != 401 {
		t.Fatalf("no session: %d", w.Code)
	}

	b := getBoard(t, e, "?sort=volume")
	if got := strings.Join(slugs(b.Events), " "); got != fedSlug+" what-price-will-bitcoin-hit-in-october-2099 "+aboveSlug+
		" clarity-act-signed-into-law-in-2099 bitcoin-above-on-october-5-2099 toncoin-above-on-october-6-2099" {
		t.Fatalf("all by volume: %s", got)
	}
	if b.Stale || !b.AsOf.Equal(t0) || g.count("crypto") != 1 || g.count("economy") != 1 {
		t.Fatalf("asOf %v stale %v, reads crypto %d economy %d", b.AsOf, b.Stale, g.count("crypto"), g.count("economy"))
	}
	above := b.Events[2]
	var ids []string
	for _, m := range above.Markets {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "105,101,102,103" || above.Category != "crypto" || above.URL != "https://polymarket.com/event/"+aboveSlug ||
		above.Title != "Bitcoin above ___ on October 6?" || *above.Volume24hUSD != 74659 || b.Events[3].Category != "crypto" || b.Events[0].Category != "macro" {
		t.Fatalf("event: %+v", above)
	}
	if fed := b.Events[0].Markets; fed[0].ID != "401" || fed[1].ID != "402" || fed[0].Rule != nil || fed[0].Estimate != nil || fed[0].Desk != nil {
		t.Fatalf("crowd-only markets by Yes price: %+v", fed)
	}

	m := boardRow(t, b.Events, "101")
	if m.Rule == nil || *m.Rule != (PriceQuestion{"BTC", "above", 98000, "close"}) || m.Estimate != nil || m.EstimateUnavailable == nil || m.YesPrice != .7 || *m.Change24hPp != 1.3 || *m.Volume24hUSD != 1234.5 || *m.VolumeUSD != 20000 || m.Thin {
		t.Fatalf("stale data keeps crowd/rule only: %+v", m)
	}
	if m := boardRow(t, b.Events, "103"); !m.Thin || m.Estimate != nil {
		t.Fatalf("thin: %+v", m)
	}
	if m := boardRow(t, b.Events, "202"); m.Estimate != nil || m.EstimateUnavailable == nil || *m.EstimateUnavailable != "unverified_touch_history" {
		t.Fatalf("touch applicability: %+v", m)
	}
	if m := boardRow(t, b.Events, "701"); m.Rule == nil || m.Estimate != nil {
		t.Fatalf("halted: %+v", m)
	}

	if b := getBoard(t, e, "?category=macro"); strings.Join(slugs(b.Events), " ") != fedSlug+" clarity-act-signed-into-law-in-2099" || b.Events[1].Category != "macro" {
		t.Fatalf("macro: %v", slugs(b.Events))
	}
	ending := slugs(getBoard(t, e, "?category=crypto&sort=ending").Events)
	if !slices.Equal(ending[:2], []string{"bitcoin-above-on-october-5-2099", aboveSlug}) || ending[len(ending)-1] != "clarity-act-signed-into-law-in-2099" {
		t.Fatalf("ending: %v", ending)
	}
	// Biggest edge first; the events with no edge (crowd only, or thin, or halted) after, by volume.
	edges := getBoard(t, e, "").Events
	last := math.Inf(1)
	for i, ev := range edges {
		best, ok := -1.0, false
		for _, m := range ev.Markets {
			if m.Estimate != nil && !m.Thin {
				best, ok = math.Max(best, math.Abs(m.Estimate.EdgePp)), true
			}
		}
		if !ok {
			if rest := slugs(edges[i:]); strings.Join(rest, " ") != fedSlug+" what-price-will-bitcoin-hit-in-october-2099 "+aboveSlug+" clarity-act-signed-into-law-in-2099 bitcoin-above-on-october-5-2099 toncoin-above-on-october-6-2099" {
				t.Fatalf("edgeless events by volume: %v", rest)
			}
			break
		}
		if best > last {
			t.Fatalf("edge order: %v at %d after %v (%v)", best, i, last, slugs(edges))
		}
		last = best
	}
	if g.count("crypto") != 1 || g.count("economy") != 1 {
		t.Fatalf("the listings come from the cache: crypto %d economy %d", g.count("crypto"), g.count("economy"))
	}
}

// Legacy question-only, daily-close rewrites, below complements and ladder
// approximations never gain an exact overlay on the board's canonical leaves.
func TestBoardDeskReads(t *testing.T) {
	e, _, _ := boardEnv(t, apptest.Options{})
	ctx := context.Background()
	fedMarket := func() *MarketOdds {
		return &MarketOdds{Source: "Polymarket", Question: fedCut, URL: "https://polymarket.com/event/" + fedSlug, YesPrice: 0.8, Match: "exact", AsOf: t0}
	}
	fedDraft := Draft{Shape: "binary", Question: "Will the Fed cut by 25 bps in October 2099?", Outcomes: yesNo, ResolutionDate: "2099-10-29"}
	older := stored(t, e, e.User, fedDraft, Result{Probabilities: ps("Yes", 0.9, "No", 0.1), Confidence: "medium", Market: fedMarket()}, "")
	if _, err := e.DB.Exec(ctx, `update forecasts set completed_at = now() - interval '1 hour' where id = $1`, older); err != nil {
		t.Fatal(err)
	}
	stored(t, e, e.User, fedDraft, Result{Probabilities: ps("Yes", 0.3, "No", 0.7), Confidence: "medium", Market: fedMarket()}, "")

	below := Draft{Shape: "binary", Question: "Will the Binance BTC/USDT daily close on 2099-10-06 be below $100,000?", Outcomes: yesNo,
		ResolutionDate: "2099-10-06", PriceRule: &PriceRule{"BTC", "below", 100000, "on_date"}}
	stored(t, e, e.User, below, Result{Probabilities: ps("Yes", 0.6, "No", 0.4), Confidence: "medium",
		Market: &MarketOdds{Question: q100, URL: "https://polymarket.com/event/" + aboveSlug, YesPrice: 0.52, Match: "exact"}}, "")

	ladder := Draft{Shape: "ordered", Question: "Where will the Binance BTC/USDT daily close be on 2099-10-06?",
		Outcomes: []string{"< $96,000", "$96,000 – $100,000", "$100,000 – $104,000", "≥ $104,000"}, ResolutionDate: "2099-10-06"}
	stored(t, e, e.User, ladder, Result{Probabilities: ps("< $96,000", 0.1, "$96,000 – $100,000", 0.3, "$100,000 – $104,000", 0.4, "≥ $104,000", 0.2),
		Confidence: "medium", Ladder: []*MarketOdds{
			{Question: q98, URL: "https://polymarket.com/event/" + aboveSlug, Strike: 98000, Match: "exact"},
			{Question: "Will the price of Bitcoin be above $102,000 on October 6?", URL: "https://polymarket.com/event/" + aboveSlug, Strike: 102000, Match: "close"},
		}}, "")

	// None of these count.
	noChange := Draft{Shape: "binary", Question: "No change at the Fed in October 2099?", Outcomes: yesNo, ResolutionDate: "2099-10-29"}
	noChangeMarket := func() *MarketOdds {
		return &MarketOdds{Question: "Will there be no change in Fed interest rates after the October 2099 meeting?", URL: "https://polymarket.com/event/" + fedSlug, Match: "exact"}
	}
	res := Result{Probabilities: ps("Yes", 0.9, "No", 0.1), Confidence: "high", Market: noChangeMarket()}
	stored(t, e, storetest.User(t, e.DB), noChange, res, "") // another user's
	resolved := stored(t, e, e.User, noChange, res, "")
	archived := stored(t, e, e.User, noChange, res, "")
	superseded := stored(t, e, e.User, noChange, res, "")
	stored(t, e, e.User, noChange, Result{Probabilities: ps("Yes", 0.2, "No", 0.8), Confidence: "high"}, superseded) // its newer run has no market
	if _, err := e.DB.Exec(ctx, `update forecasts set outcome = 'Yes', resolved_at = now(), resolved_by = 'manual' where id = $1`, resolved); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `update forecasts set archived_from_status = status, status = 'archived' where id = $1`, archived); err != nil {
		t.Fatal(err)
	}

	b := getBoard(t, e, "")
	if b.Events[0].Slug != fedSlug {
		t.Fatalf("without exact overlays, fallback volume leads: %v", slugs(b.Events))
	}
	for id, want := range map[string]*DeskRead{"401": nil, "102": nil, "101": nil, "103": nil, "402": nil, "105": nil} {
		got := boardRow(t, b.Events, id).Desk
		if (got == nil) != (want == nil) || got != nil && (got.ForecastID != want.ForecastID || got.Question != want.Question || got.P != want.P ||
			got.Match != want.Match || got.EdgePp != want.EdgePp || got.Signal != want.Signal || (got.EVPer100 == nil) != (want.EVPer100 == nil) ||
			got.EVPer100 != nil && *got.EVPer100 != *want.EVPer100) {
			t.Errorf("desk on %s: %+v, want %+v", id, got, want)
		}
	}
	// The cached listing stays the caller's alone: another user sees no desk reads.
	other := apptest.JSON[boardBody](t, e.DoAs(e.Session(storetest.User(t, e.DB)), "GET", "/api/predictions/markets", nil))
	for _, ev := range other.Events {
		for _, m := range ev.Markets {
			if m.Desk != nil {
				t.Fatalf("another user sees %s's desk read: %+v", m.ID, m.Desk)
			}
		}
	}
}

// A listing is read from Gamma once per 5 minutes per category, by one
// request at a time; a failed read serves the last listing as stale and is
// not retried for a minute; nothing to serve (or a listing an hour old) is a
// 502.
func TestBoardCache(t *testing.T) {
	e, g, c := boardEnv(t, apptest.Options{})
	g.set(func(g *fakeGamma) { g.delay = 50 * time.Millisecond })
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if w := e.Do("GET", "/api/predictions/markets?category=crypto", nil); w.Code != 200 {
				t.Errorf("concurrent: %d", w.Code)
			}
		})
	}
	wg.Wait()
	if g.count("crypto") != 1 || g.count("economy") != 0 {
		t.Fatalf("one read for six concurrent requests: %d", g.count("crypto"))
	}
	g.set(func(g *fakeGamma) { g.delay = 0 })
	c.add(4 * time.Minute)
	getBoard(t, e, "?category=crypto")
	if g.count("crypto") != 1 {
		t.Fatalf("under 5 minutes: %d reads", g.count("crypto"))
	}
	c.add(time.Minute)
	if b := getBoard(t, e, "?category=crypto"); g.count("crypto") != 2 || b.Stale || !b.AsOf.Equal(c.now()) {
		t.Fatalf("5 minutes on: %d reads, %+v", g.count("crypto"), b.AsOf)
	}
	fresh := c.now()

	g.set(func(g *fakeGamma) { g.down = true })
	c.add(5 * time.Minute)
	for i, want := range []int{3, 3} { // the second request is inside the minute after the failure
		if b := getBoard(t, e, "?category=crypto"); !b.Stale || !b.AsOf.Equal(fresh) || len(b.Events) == 0 || g.count("crypto") != want {
			t.Fatalf("down %d: stale %v asOf %v, %d reads", i, b.Stale, b.AsOf, g.count("crypto"))
		}
	}
	c.add(61 * time.Second)
	if b := getBoard(t, e, "?category=crypto"); !b.Stale || g.count("crypto") != 4 {
		t.Fatalf("retry after a minute: %d reads", g.count("crypto"))
	}
	// "all" serves what it can: crypto stale, macro never read.
	if b := getBoard(t, e, ""); !b.Stale || len(b.Events) == 0 || g.count("economy") != 1 || b.Events[0].Category != "crypto" {
		t.Fatalf("all, half down: %+v", b)
	}
	g.set(func(g *fakeGamma) { g.down = false })
	c.add(61 * time.Second)
	if b := getBoard(t, e, "?category=crypto"); b.Stale || g.count("crypto") != 5 || !b.AsOf.Equal(c.now()) {
		t.Fatalf("recovered: stale %v, %d reads", b.Stale, g.count("crypto"))
	}
	g.set(func(g *fakeGamma) { g.down = true })
	c.add(time.Hour)
	if w := e.Do("GET", "/api/predictions/markets?category=crypto", nil); w.Code != 502 || apptest.ErrorCode(t, w) != "upstream" || g.count("crypto") != 6 {
		t.Fatalf("an hour-old listing is not served: %d %s (%d reads)", w.Code, w.Body, g.count("crypto"))
	}
	if w := e.Do("GET", "/api/predictions/markets?category=macro", nil); w.Code != 502 || g.count("economy") != 2 {
		t.Fatalf("macro, never read, down: %d (%d reads)", w.Code, g.count("economy"))
	}
	if w := e.Do("GET", "/api/predictions/markets?category=macro", nil); w.Code != 502 || g.count("economy") != 2 {
		t.Fatalf("no retry within the minute: %d (%d reads)", w.Code, g.count("economy"))
	}
	e.PolymarketBase = ""
	c.add(2 * time.Hour)
	if w := e.Do("GET", "/api/predictions/markets", nil); w.Code != 502 {
		t.Fatalf("no POLYMARKET_BASE: %d", w.Code)
	}
}

type draftBody struct {
	Draft  Draft
	Market BoardMarket
	Event  struct{ Slug, Title, URL string }
}

// Free drafts retain the full manual predicate and always re-read canonical ID.
func TestBoardDraft(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	getBoard(t, e, "")
	for _, id := range []string{"101", "105", "202", "401"} {
		w := e.Do("GET", "/api/predictions/markets/"+id+"/draft", nil)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", id, w.Code, w.Body)
		}
		d := apptest.JSON[draftBody](t, w)
		g.mu.Lock()
		raw := g.byID[id]
		q, rules := raw["question"], strings.TrimSpace(raw["description"].(string))
		g.mu.Unlock()
		if d.Draft.Question != q || d.Draft.ResolutionCriteria != rules || d.Draft.PriceRule != nil || d.Draft.LinkedMarket == nil || d.Draft.LinkedMarket.MarketID != id || d.Market.Desk != nil {
			t.Fatalf("manual canonical draft %s: %+v", id, d)
		}
		if id == "401" && (d.Draft.Category != "macro" || d.Draft.ResolutionCriteria != strings.TrimSpace(fedRules)) {
			t.Fatalf("complete macro rules: %+v", d.Draft)
		}
	}
	for id, want := range map[string]int{"104": 404, "12345": 404, "abc": 404, "1e3": 404, "501": 409} {
		if w := e.Do("GET", "/api/predictions/markets/"+id+"/draft", nil); w.Code != want {
			t.Errorf("%s: %d %s", id, w.Code, w.Body)
		}
	}
	if g.count("id:101") != 1 || credits(t, e) != 0 {
		t.Fatal("draft must re-read ID, free")
	}
	g.set(func(g *fakeGamma) { g.down = true })
	if w := e.Do("GET", "/api/predictions/markets/101/draft", nil); w.Code != 502 {
		t.Fatalf("cached draft on outage: %d", w.Code)
	}
	e.PolymarketBase = ""
	if w := e.Do("GET", "/api/predictions/markets/101/draft", nil); w.Code != 502 {
		t.Fatal(w.Code)
	}
}

// Canonical links require the identical complete binary/manual predicate.
func TestValidateLinkedMarket(t *testing.T) {
	today := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	good := func() Draft {
		l := &LinkedMarket{MarketID: "900", ConditionID: "0x" + strings.Repeat("a", 64), MarketSlug: "leaf-900", EventSlug: "parent", Question: "Will Bitcoin be above $74,000 on October 31?", Description: strings.Repeat("Rules. ", 500), EndDate: "2099-11-01T04:00:00Z", AsOf: today}
		l.Description = strings.TrimSpace(l.Description)
		return Draft{Shape: "binary", Question: l.Question, Outcomes: slices.Clone(yesNo), ResolutionCriteria: l.Description, ResolutionDate: "2099-11-01", LinkedMarket: l}
	}
	d := good()
	if err := validate(&d, today); err != nil {
		t.Fatalf("full linked criteria: %v", err)
	}
	for name, change := range map[string]func(*Draft){"ordered": func(d *Draft) { d.Shape = "ordered" }, "slug": func(d *Draft) { d.LinkedMarket.EventSlug = "../x" }, "legacy": func(d *Draft) { d.LinkedMarket.MarketID = "" }, "above rule": func(d *Draft) { d.PriceRule = &PriceRule{"BTC", "above", 1e5, "on_date"} }, "below rule": func(d *Draft) { d.PriceRule = &PriceRule{"BTC", "below", 1e5, "on_date"} }, "edited rules": func(d *Draft) { d.ResolutionCriteria = "Different" }} {
		d := good()
		change(&d)
		var bad *app.BadRequest
		if err := validate(&d, today); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
	d = good()
	d.LinkedMarket = nil
	if err := validate(&d, today); err == nil {
		t.Fatal("ordinary criteria over2000 accepted")
	}
	if d := repair(Draft{Shape: "binary", Question: "Will the Fed cut in December?", LinkedMarket: &LinkedMarket{EventSlug: "x", Question: "y"}}, "fed", today); d.LinkedMarket != nil {
		t.Fatal("model must never link")
	}
}

// A linked run and update preserve the canonical snapshot, own Yes and no matcher.
func TestLinkedMarketRun(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{Reply: panel(t, map[string]llmtest.Reply{
		"base_rate": {Text: opinion(.8, "Base.")}, "quant": {Text: opinion(.8, "Quant.")}, "skeptic": {Text: opinion(.8, "Skeptic.")}, "aggregator": {Text: `{"probabilities":[{"outcome":"Yes","p":0.8}],"summary":"We lean Yes.","confidence":"medium"}`}}, false)})
	getBoard(t, e, "?category=crypto")
	w := e.Do("GET", "/api/predictions/markets/101/draft", nil)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	d := apptest.JSON[draftBody](t, w).Draft
	f := waitRun(t, e, create(t, e, d))
	if f.LinkedMarket.AsOf.After(time.Now().Add(time.Minute)) {
		t.Fatal("creation trusted client snapshot timestamp")
	}
	m := f.Result.Market
	if f.Status != "completed" || m == nil || m.Question != q98 || m.Match != "exact" || m.YesPrice != .7 || m.EdgePp != 10 || m.Signal != "yes" || !SameLinkedMarket(f.LinkedMarket, d.LinkedMarket) || g.count("search") != 0 || g.count("slug:"+aboveSlug) != 0 {
		t.Fatalf("canonical run: %+v market %+v", f, m)
	}
	if dr := boardRow(t, getBoard(t, e, "?category=crypto").Events, "101").Desk; dr == nil || dr.ForecastID != f.ID || dr.P != .8 {
		t.Fatalf("canonical overlay: %+v", dr)
	}
	w = e.Do("POST", "/api/forecasts/"+f.ID+"/update", map[string]any{})
	if w.Code != 201 {
		t.Fatal(w.Body)
	}
	up := waitRun(t, e, apptest.JSON[forecastBody](t, w).Forecast.ID)
	if !SameLinkedMarket(up.LinkedMarket, f.LinkedMarket) || !up.LinkedMarket.AsOf.Equal(f.LinkedMarket.AsOf) || up.Result.Market == nil || up.Result.Market.Question != q98 {
		t.Fatalf("immutable update: %+v", up)
	}
	before := credits(t, e)
	d.Question += " edited"
	if w := e.Do("POST", "/api/forecasts", d); w.Code != 400 {
		t.Fatalf("edited pin: %d %s", w.Code, w.Body)
	}
	if credits(t, e) != before || g.count("search") != 0 {
		t.Fatal("edited pin spent/fell back")
	}
}
