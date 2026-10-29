package forecast

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func canonicalLeaf(id, question, rules string) map[string]any {
	m := bm(id, question, "2099-11-01T04:00:00Z", .31, 5000)
	m["slug"], m["conditionId"], m["description"] = "leaf-"+id, "0x"+strings.Repeat("a", 64), rules
	m["resolutionSource"], m["acceptingOrders"] = nil, true
	m["events"] = []map[string]any{{"slug": "parent", "title": "Original parent", "active": true, "closed": false}}
	return m
}

func TestCanonicalDraftKeepsFullPredicateFresh(t *testing.T) {
	for _, c := range []struct{ question, rules string }{
		{"Will Bitcoin be above $74,000 on October 31?", "At noon America/New_York use the final Binance BTCUSDT 1m Close, strictly greater than $74,000. "},
		{"Will Bitcoin be below $74,000 on October 31?", "At noon America/New_York use the final Binance BTCUSDT 1m Close, strictly less than $74,000. "},
		{"Will Bitcoin reach $150,000 in October?", "Use any Binance BTCUSDT 1m High at least $150,000 within the complete October America/New_York calendar month. "},
	} {
		t.Run(c.question, func(t *testing.T) {
			e, g, _ := boardEnv(t, apptest.Options{})
			question, rules := c.question, strings.TrimSpace(strings.Repeat(c.rules, 35))
			g.set(func(g *fakeGamma) { g.byID["900"] = canonicalLeaf("900", question, rules) })
			w := e.Do("GET", "/api/predictions/markets/900/draft", nil)
			if w.Code != 200 {
				t.Fatalf("draft: %d %s", w.Code, w.Body)
			}
			v := apptest.JSON[map[string]json.RawMessage](t, w)
			var draft map[string]json.RawMessage
			json.Unmarshal(v["draft"], &draft)
			var q, criteria string
			json.Unmarshal(draft["question"], &q)
			json.Unmarshal(draft["resolutionCriteria"], &criteria)
			if q != question || criteria != rules || string(draft["priceRule"]) != "null" {
				t.Fatalf("rewritten/clipped predicate: %s", v["draft"])
			}
			var link map[string]any
			json.Unmarshal(draft["linkedMarket"], &link)
			if link["marketId"] != "900" || link["marketSlug"] != "leaf-900" || link["description"] != rules || link["eventSlug"] != "parent" || link["resolutionSource"] != nil {
				t.Fatalf("canonical snapshot: %+v", link)
			}
			g.set(func(g *fakeGamma) { g.byID["900"]["closed"] = true })
			if w := e.Do("GET", "/api/predictions/markets/900/draft", nil); w.Code != 404 {
				t.Fatalf("action used cached closed child: %d %s", w.Code, w.Body)
			}
			if credits(t, e) != 0 {
				t.Fatal("free draft spent")
			}
		})
	}
}

func TestLegacyPinnedLookupNeverFallsBack(t *testing.T) {
	e, _, _ := boardEnv(t, apptest.Options{})
	h := handler{e.Deps}
	d := Draft{Shape: "binary", Question: "Will Bitcoin be above $74,000?", LinkedMarket: &LinkedMarket{EventSlug: "missing", Question: "Original question"}}
	if m, err := h.findMarket(context.Background(), d); m != nil || err == nil {
		t.Fatalf("legacy pin must refuse exact lookup, got %+v %v", m, err)
	}
	if len(e.Bedrock.Requests()) != 0 {
		t.Fatal("pinned lookup invoked paid matcher")
	}
}

func TestGammaRejectsMalformedNoAndEventState(t *testing.T) {
	for _, price := range []string{`["0.3","NaN"]`, `["0.3","1.1"]`, `["0.3","-0.1"]`, `["0.3","wat"]`} {
		var m gammaEventMarket
		raw := canonicalLeaf("900", "Will Bitcoin be above $74,000?", "Complete rules.")
		raw["outcomePrices"] = price
		b, _ := json.Marshal(raw)
		json.Unmarshal(b, &m)
		if _, _, ok := m.open(); ok {
			t.Errorf("accepted malformed No %s", price)
		}
	}
	for _, flag := range []string{"closed", "archived"} {
		var e gammaEvent
		raw := ev("parent", "Parent", "2099-11-01T04:00:00Z", 100, canonicalLeaf("900", "Will Bitcoin be above $74,000?", "Rules"))
		raw[flag] = true
		b, _ := json.Marshal(raw)
		json.Unmarshal(b, &e)
		if _, ok := boardEvent(e, "crypto", t0); ok {
			t.Errorf("accepted %s event", flag)
		}
	}
}

func TestTouchBoardDoesNotInventFullContractProbability(t *testing.T) {
	e, _, _ := boardEnv(t, apptest.Options{})
	m := canonicalLeaf("900", "Will Bitcoin reach $150,000 in October?", "Any Binance 1m high during October ET.")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{ev("parent", "Touch", "2099-11-01T04:00:00Z", 100, m)})
	}))
	t.Cleanup(s.Close)
	e.PolymarketBase = s.URL
	row := boardRow(t, getBoard(t, e, "?category=crypto").Events, "900")
	if row.Estimate != nil {
		t.Fatalf("unverified past/source/window touch estimate: %+v", row.Estimate)
	}
}

func TestCanonicalOverCapDraftDeclinesWithoutClipping(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	g.set(func(g *fakeGamma) {
		g.byID["900"] = canonicalLeaf("900", "Will Bitcoin be above $74,000?", strings.Repeat("界", 8001))
	})
	if w := e.Do("GET", "/api/predictions/markets/900/draft", nil); w.Code != 409 {
		t.Fatalf("over-cap complete rules: %d %s", w.Code, w.Body)
	}
	_ = time.UTC
}

func TestPinnedRepriceLiveOddsAndStakeCheckRules(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	g.set(func(g *fakeGamma) {
		g.byID["900"] = canonicalLeaf("900", "Will Bitcoin be below $74,000 on October 31?", "Use final 1m Close at noon ET, strictly below.")
	})
	w := e.Do("GET", "/api/predictions/markets/900/draft", nil)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	d := apptest.JSON[draftBody](t, w).Draft
	res := Result{Probabilities: ps("Yes", .6, "No", .4), Confidence: "medium", Market: &MarketOdds{Question: d.Question, URL: eventURL("parent"), YesPrice: .31, Match: "exact", AsOf: t0}}
	id := stored(t, e, e.User, d, res, "")
	if _, err := e.DB.Exec(context.Background(), `update forecasts set resolution_criteria=$2,linked_market=$3 where id=$1`, id, d.ResolutionCriteria, d.LinkedMarket); err != nil {
		t.Fatal(err)
	}
	g.set(func(g *fakeGamma) { g.byID["900"]["outcomePrices"] = `["0.42","0.58"]` })
	if m, err := LiveOdds(context.Background(), e.Deps, id); err != nil || m.YesPrice != .42 {
		t.Fatalf("own below Yes must reprice canonical ID: %+v %v", m, err)
	}
	g.set(func(g *fakeGamma) { g.byID["900"]["description"] = "Rules changed to midnight UTC." })
	if _, err := LiveOdds(context.Background(), e.Deps, id); err == nil {
		t.Fatal("changed predicate accepted")
	}
	if w := e.Do("POST", "/api/forecasts/"+id+"/market-position", map[string]any{"side": "yes", "stake": 100}); w.Code != 409 {
		t.Fatalf("changed rule stake: %d %s", w.Code, w.Body)
	}
	w = e.Do("POST", "/api/forecasts/"+id+"/market", map[string]any{"rematch": true})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	f := apptest.JSON[forecastBody](t, w).Forecast
	if f.Result.Market.LinkStatus != "changed" || f.Result.Market.Signal != "wait" || f.Result.Market.YesPrice != .42 {
		t.Fatalf("changed report flag: %+v", f.Result.Market)
	}
	if len(e.Bedrock.Requests()) != 0 {
		t.Fatal("pinned refresh invoked matcher")
	}
}

func TestCanonicalPredicateValidationBeforeSpend(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	g.set(func(g *fakeGamma) {
		g.byID["900"] = canonicalLeaf("900", "Will Bitcoin be above $74,000 on October 31?", "Final Binance 1m Close at noon ET, strictly above.")
	})
	d := apptest.JSON[draftBody](t, e.Do("GET", "/api/predictions/markets/900/draft", nil)).Draft
	for name, change := range map[string]func(*Draft){"question": func(d *Draft) { d.Question += " edited" }, "criteria": func(d *Draft) { d.ResolutionCriteria = "Arbitrary posted rules." }, "date": func(d *Draft) { d.ResolutionDate = "2099-12-31" }, "priceRule": func(d *Draft) { d.PriceRule = &PriceRule{"BTC", "above", 74000, "on_date"} }} {
		x := d
		change(&x)
		if w := e.Do("POST", "/api/forecasts", x); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	g.set(func(g *fakeGamma) { g.byID["900"]["description"] = "Upstream changed predicate." })
	if w := e.Do("POST", "/api/forecasts", d); w.Code != 409 {
		t.Fatalf("rules changed before spend: %d %s", w.Code, w.Body)
	}
	if credits(t, e) != 0 || len(e.Bedrock.Requests()) != 0 {
		t.Fatal("invalid linked predicate spent or invoked model")
	}
}

func TestQuantInputsUseClosedFreshBarsAndExposeWindow(t *testing.T) {
	e, _, _ := boardEnv(t, apptest.Options{})
	// The fixture's daily bars are 2026; the 2099 board clock must not invent current spot.
	m := canonicalLeaf("900", "Will Bitcoin be above $120,000 on December 31?", "Original noon ET rules.")
	var raw gammaEventMarket
	b, _ := json.Marshal(m)
	json.Unmarshal(b, &raw)
	bm, _ := boardMarket(raw, "parent", t0)
	ev := []BoardEvent{{Markets: []BoardMarket{bm}}}
	newBoard(e.Deps).estimate(context.Background(), ev, t0)
	if ev[0].Markets[0].Estimate != nil {
		t.Fatalf("stale daily-close proxy: %+v", ev[0].Markets[0].Estimate)
	}
}

func TestBoardPrivateOverlayNeedsCanonicalPredicateAndNoPayloadCopy(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	leaf := canonicalLeaf("900", "Will Bitcoin be above $74,000 on October 31?", "Noon ET final Binance 1m Close, strictly above.")
	g.set(func(g *fakeGamma) {
		g.byID["900"] = leaf
		g.tags["crypto"] = []map[string]any{ev("parent", "Parent", "2099-11-01T04:00:00Z", 100, leaf)}
	})
	d := apptest.JSON[draftBody](t, e.Do("GET", "/api/predictions/markets/900/draft", nil)).Draft
	res := Result{Probabilities: ps("Yes", .6, "No", .4), Confidence: "medium", Market: &MarketOdds{Question: d.Question, URL: eventURL("parent"), Match: "exact", YesPrice: .31}}
	id := stored(t, e, e.User, d, res, "")
	if row := boardRow(t, getBoard(t, e, "?category=crypto").Events, "900"); row.Desk != nil {
		t.Fatalf("legacy question-only overlay falsely exact: %+v", row.Desk)
	}
	if _, err := e.DB.Exec(context.Background(), `update forecasts set resolution_criteria=$2,linked_market=$3 where id=$1`, id, d.ResolutionCriteria, d.LinkedMarket); err != nil {
		t.Fatal(err)
	}
	w := e.Do("GET", "/api/predictions/markets?category=crypto", nil)
	row := boardRow(t, apptest.JSON[boardBody](t, w).Events, "900")
	if row.Desk == nil || row.Desk.ForecastID != id || row.Desk.P != .6 {
		t.Fatalf("canonical overlay: %+v", row.Desk)
	}
	if strings.Contains(w.Body.String(), "linkedMarket") || strings.Contains(w.Body.String(), "Noon ET final") {
		t.Fatal("listing duplicates complete canonical rules")
	}
	other := storetest.User(t, e.DB)
	if w := e.DoAs(e.Session(other), "GET", "/api/predictions/markets?category=crypto", nil); boardRow(t, apptest.JSON[boardBody](t, w).Events, "900").Desk != nil {
		t.Fatal("private overlay entered shared cache")
	}
	if _, err := e.DB.Exec(context.Background(), `update forecasts set resolution_criteria='Edited rules' where id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if row := boardRow(t, getBoard(t, e, "?category=crypto").Events, "900"); row.Desk != nil {
		t.Fatal("edited predicate retained exact overlay")
	}
}

func TestCachedBrowseDropsNowIneligibleChild(t *testing.T) {
	e, g, c := boardEnv(t, apptest.Options{})
	leaf := canonicalLeaf("900", "Will Bitcoin be above $74,000?", "Noon ET rules.")
	leaf["endDate"] = t0.Add(70 * time.Minute).Format(time.RFC3339)
	g.set(func(g *fakeGamma) {
		g.tags["crypto"] = []map[string]any{ev("parent", "Parent", leaf["endDate"].(string), 100, leaf)}
	})
	getBoard(t, e, "?category=crypto")
	c.add(11 * time.Minute)
	g.set(func(g *fakeGamma) { g.down = true })
	b := getBoard(t, e, "?category=crypto")
	if len(b.Events) != 0 || !b.Stale {
		t.Fatalf("cached child still actionable inside 1h: %+v", b)
	}
}

func TestQuantProxyMetadataExcludesOpenCandle(t *testing.T) {
	e, _, _ := boardEnv(t, apptest.Options{})
	day := t0.UTC().Truncate(24 * time.Hour)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/ticker/24hr" {
			rows := []map[string]any{}
			for _, a := range market.Universe {
				rows = append(rows, map[string]any{"symbol": a.Pair, "lastPrice": "100", "closeTime": time.Now().UnixMilli()})
			}
			json.NewEncoder(w).Encode(rows)
			return
		}
		rows := []any{}
		for i := 90; i >= 0; i-- {
			price := 100 * math.Exp(.1*math.Sin(float64(i)))
			if i == 0 {
				price = 900
			}
			rows = append(rows, []any{day.AddDate(0, 0, -i).UnixMilli(), "100", "110", "90", fmt.Sprint(price), "10"})
		}
		json.NewEncoder(w).Encode(rows)
	}))
	t.Cleanup(s.Close)
	e.Market = testMarketClient(s.URL)
	m := canonicalLeaf("900", "Will Bitcoin be above $120,000 on December 31?", "Noon ET rules.")
	var raw gammaEventMarket
	b, _ := json.Marshal(m)
	json.Unmarshal(b, &raw)
	row, _ := boardMarket(raw, "parent", t0)
	ev := []BoardEvent{{Markets: []BoardMarket{row}}}
	newBoard(e.Deps).estimate(context.Background(), ev, t0)
	q := ev[0].Markets[0].Estimate
	if q == nil || q.Spot == 900 || !q.SpotAsOf.Equal(day) || !q.WindowEnd.Equal(day) || q.Closes != 90 || q.Source == "" || q.Assumptions == "" || !q.WindowStart.Equal(day.AddDate(0, 0, -90)) {
		t.Fatalf("proxy metadata/open candle: %+v", q)
	}
}

func TestEmptyBoardCachesSuccessfulEmptySample(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	g.set(func(g *fakeGamma) { g.tags["crypto"] = []map[string]any{} })
	for range 2 {
		if b := getBoard(t, e, "?category=crypto"); len(b.Events) != 0 || b.Stale {
			t.Fatalf("empty sample: %+v", b)
		}
	}
	if g.count("crypto") != 1 {
		t.Fatalf("empty sample bypassed 5m cache: %d", g.count("crypto"))
	}
}

func TestLegacyPinnedReadDoesNotClaimExact(t *testing.T) {
	e, _, _ := boardEnv(t, apptest.Options{})
	d := Draft{Shape: "binary", Question: q98, Outcomes: yesNo, ResolutionDate: "2099-10-06"}
	id := stored(t, e, e.User, d, Result{Probabilities: ps("Yes", .8, "No", .2), Market: &MarketOdds{Question: q98, URL: eventURL(aboveSlug), Match: "exact", Signal: "yes", YesPrice: .7, VolumeUSD: ptr(5000.0)}}, "")
	if _, err := e.DB.Exec(context.Background(), `update forecasts set linked_market=$2 where id=$1`, id, &LinkedMarket{EventSlug: aboveSlug, Question: q98}); err != nil {
		t.Fatal(err)
	}
	f := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast
	if m := f.Result.Market; m.Match == "exact" || m.LinkStatus != "legacy" || m.Signal != "wait" {
		t.Fatalf("legacy exact claim on read: %+v", m)
	}
}

func TestBoardRejectsUnstableIdentity(t *testing.T) {
	for _, field := range []string{"id", "conditionId", "slug"} {
		t.Run(field, func(t *testing.T) {
			m := canonicalLeaf("900", "Will Bitcoin be above $74,000?", "Complete rules.")
			m[field] = "../bad"
			var raw gammaEventMarket
			b, _ := json.Marshal(m)
			json.Unmarshal(b, &raw)
			if _, ok := boardMarket(raw, "parent", t0); ok {
				t.Fatal("listing accepted unstable identity")
			}
		})
	}
}

func TestCanonicalSourceIsBounded(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	g.set(func(g *fakeGamma) {
		m := canonicalLeaf("900", "Will Bitcoin be above $74,000?", "Complete rules.")
		m["resolutionSource"] = strings.Repeat("s", 2049)
		g.byID["900"] = m
	})
	if w := e.Do("GET", "/api/predictions/markets/900/draft", nil); w.Code != 404 {
		t.Fatalf("unbounded source snapshot: %d", w.Code)
	}
}

func TestLinkedCreatePreservesMaximumUnicodeRules(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{Reply: panel(t, map[string]llmtest.Reply{
		"base_rate": {Text: opinion(.5, "Base.")}, "quant": {Text: opinion(.5, "Quant.")}, "skeptic": {Text: opinion(.5, "Skeptic.")},
		"aggregator": {Text: `{"probabilities":[{"outcome":"Yes","p":0.5},{"outcome":"No","p":0.5}],"summary":"Uncertain.","confidence":"medium"}`},
	}, false)})
	rules := strings.Repeat("🧭", maxLinkedCriteria)
	g.set(func(g *fakeGamma) { g.byID["900"] = canonicalLeaf("900", "Will Bitcoin be above $74,000?", rules) })
	d := apptest.JSON[draftBody](t, e.Do("GET", "/api/predictions/markets/900/draft", nil)).Draft
	// An escaped surrogate pair uses twelve wire bytes for one Unicode rune.
	// Both the manual predicate and its canonical snapshot must fit unchanged.
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	w := e.Do("POST", "/api/forecasts", strings.ReplaceAll(string(body), "🧭", `\ud83e\udded`))
	if w.Code != 201 {
		t.Fatalf("complete Unicode rules: %d %.200s", w.Code, w.Body)
	}
	f := waitRun(t, e, apptest.JSON[forecastBody](t, w).Forecast.ID)
	if f.ResolutionCriteria != rules || f.LinkedMarket.Description != rules {
		t.Fatal("complete Unicode rules were changed or clipped")
	}
}

func TestBoardFailureRechecksMaximumStaleAge(t *testing.T) {
	c := &clock{t: t0.Add(59 * time.Minute)}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.add(2 * time.Minute)
		http.Error(w, "unavailable", 503)
	}))
	t.Cleanup(s.Close)
	b := newBoard(&app.Deps{PolymarketBase: s.URL})
	b.now = c.now
	b.lists["crypto"].events, b.lists["crypto"].at = []BoardEvent{{Slug: "cached-parent"}}, t0
	if ev, _, _, err := b.read(context.Background(), "crypto"); err == nil || len(ev) != 0 {
		t.Fatalf("failed refresh served a sample beyond 1h: %+v %v", ev, err)
	}
}

func TestPinnedUpstreamChangesConflictBeforeSpend(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"leaf slug": func(m map[string]any) { m["slug"] = "revised-leaf" },
		"parent":    func(m map[string]any) { m["events"] = []map[string]any{{"slug": "revised-parent", "active": true}} },
		"closed":    func(m map[string]any) { m["closed"] = true },
		"paused":    func(m map[string]any) { m["acceptingOrders"] = false },
		"missing":   func(m map[string]any) { m["id"] = "901" },
	} {
		t.Run(name, func(t *testing.T) {
			e, g, _ := boardEnv(t, apptest.Options{})
			g.set(func(g *fakeGamma) {
				g.byID["900"] = canonicalLeaf("900", "Will Bitcoin be above $74,000?", "Original complete rules.")
			})
			d := apptest.JSON[draftBody](t, e.Do("GET", "/api/predictions/markets/900/draft", nil)).Draft
			g.set(func(g *fakeGamma) { change(g.byID["900"]) })
			if w := e.Do("POST", "/api/forecasts", d); w.Code != 409 {
				t.Fatalf("pinned upstream change: %d %s", w.Code, w.Body)
			}
			if credits(t, e) != 0 || len(e.Bedrock.Requests()) != 0 {
				t.Fatal("pinned upstream change spent or invoked a model")
			}
		})
	}
}

func TestPrivateDeskOverlaySeparatesSameQuestionLeaves(t *testing.T) {
	e, g, _ := boardEnv(t, apptest.Options{})
	const question, rules = "Will Bitcoin be above $74,000 on October 31?", "Final Binance 1m Close at noon ET, strictly above."
	first, decoy := canonicalLeaf("900", question, rules), canonicalLeaf("901", question, rules)
	decoy["conditionId"] = "0x" + strings.Repeat("b", 64)
	g.set(func(g *fakeGamma) {
		g.byID["900"], g.byID["901"] = first, decoy
		g.tags["crypto"] = []map[string]any{ev("parent", "Parent", "2099-11-01T04:00:00Z", 100, first, decoy)}
	})
	ids := map[string]string{}
	for _, id := range []string{"900", "901"} {
		d := apptest.JSON[draftBody](t, e.Do("GET", "/api/predictions/markets/"+id+"/draft", nil)).Draft
		p := .6
		if id == "901" {
			p = .8
		}
		ids[id] = stored(t, e, e.User, d, Result{Probabilities: ps("Yes", p, "No", 1-p), Confidence: "medium", Market: &MarketOdds{Question: question, URL: eventURL("parent"), Match: "exact", YesPrice: .31}}, "")
		if _, err := e.DB.Exec(context.Background(), "update forecasts set linked_market=$2,resolution_criteria=$3 where id=$1", ids[id], d.LinkedMarket, d.ResolutionCriteria); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.DB.Exec(context.Background(), "update forecasts set completed_at=now()-interval '1 hour' where id=$1", ids["900"]); err != nil {
		t.Fatal(err)
	}
	b := getBoard(t, e, "?category=crypto")
	for _, id := range []string{"900", "901"} {
		if d := boardRow(t, b.Events, id).Desk; d == nil || d.ForecastID != ids[id] {
			t.Errorf("newer same-question decoy must neither steal nor starve leaf %s: %+v", id, d)
		}
	}
}
