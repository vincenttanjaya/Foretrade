package forecast

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// A $1 contract costs its side's price + 1¢ (at most 99¢); $100 of Yes at 85¢
// buys 116.28 contracts that win $16.28.
func TestFillMath(t *testing.T) {
	for _, c := range []struct {
		yes   float64
		side  string
		price float64
	}{{0.85, "yes", 0.86}, {0.85, "no", 0.16}, {0.995, "yes", 0.99}, {0, "no", 0.99}, {0, "yes", 0.01}, {0.0045, "yes", 0.0145}} {
		if got := fillPrice(c.yes, c.side); got != c.price {
			t.Errorf("fillPrice(%v, %s) = %v, want %v", c.yes, c.side, got, c.price)
		}
	}
	contracts := 100 / fillPrice(0.85, "yes")
	if math.Round(contracts*100)/100 != 116.28 || math.Round((contracts-100)*100)/100 != 16.28 {
		t.Fatalf("contracts %v", contracts)
	}
}

type positionBody struct {
	Position  MarketPosition
	Portfolio paper.Portfolio
}

type positionsBody struct{ Positions []MarketPosition }

// Taking a position re-prices live, fills at +1¢ from cash with equity
// unchanged, and lists on the run and its updates; every refusal is a 4xx/502
// that spends nothing.
func TestOpenPosition(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	yes, closed := 0.85, false
	slug := "largest-company-end-of-december-2026"
	e.PolymarketBase = gammaEvents(t, slug, func() []map[string]any {
		return []map[string]any{gammaMarket(nvdaQ, "2027-01-01T00:00:00Z", yes, 1.3e6, func(m map[string]any) { m["closed"] = closed })}
	})
	id := stored(t, e, e.User, nvdaDraft, nvdaResult(), "")
	start, err := e.Paper.Portfolio(ctx, e.User)
	if err != nil {
		t.Fatal(err)
	}
	post := func(fid string, body any) (int, string) {
		w := e.Do("POST", "/api/forecasts/"+fid+"/market-position", body)
		return w.Code, w.Body.String()
	}

	// The price moved since the report: the fill uses the live one.
	yes = 0.84
	w := e.Do("POST", "/api/forecasts/"+id+"/market-position", map[string]any{"side": "no", "stake": 100})
	got := apptest.JSON[positionBody](t, w)
	p, pf := got.Position, got.Portfolio
	if w.Code != 201 || p.Side != "no" || p.Outcome != "Nvidia" || p.Price != 0.17 || !near(p.Contracts, 100/0.17) || p.Cost != 100 || p.MaxPayout != p.Contracts ||
		p.Status != "open" || p.Payout != nil || p.Pnl != nil || p.MarkPrice == nil || !near(*p.MarkPrice, 0.16) || p.Question != nvdaQ ||
		p.URL != "https://polymarket.com/event/"+slug || p.ForecastID != id || p.ForecastQuestion != nvdaDraft.Question || p.ID == "" || p.SettledAt != nil {
		t.Fatalf("buy no: %d %s", w.Code, w.Body)
	}
	if !near(pf.Cash, start.Cash-100) || pf.PredictionStakes != 100 || !near(pf.Equity, start.Equity) || pf.Revision != start.Revision+1 {
		t.Fatalf("portfolio: %+v, before %+v", pf, start)
	}
	if m := apptest.JSON[forecastBody](t, e.Do("GET", "/api/forecasts/"+id, nil)).Forecast.Result.Market; m.YesPrice != 0.84 {
		t.Fatalf("the live price is stored: %+v", m)
	}
	if !strings.Contains(w.Body.String(), `"payout":null,"pnl":null`) {
		t.Fatalf("open: payout and pnl are null: %s", w.Body)
	}

	// An update of the run: the parent is superseded; the child lists the parent's position too.
	child := stored(t, e, e.User, nvdaDraft, nvdaResult(), id)
	if code, body := post(id, map[string]any{"side": "yes", "stake": 50}); code != 409 || !strings.Contains(body, "A newer run of this forecast exists; take positions on it.") {
		t.Fatalf("superseded: %d %s", code, body)
	}
	if code, body := post(child, map[string]any{"side": "yes", "stake": 50}); code != 201 {
		t.Fatalf("buy yes on the child: %d %s", code, body)
	}
	list := apptest.JSON[positionsBody](t, e.Do("GET", "/api/market-positions?forecastId="+child, nil)).Positions
	if len(list) != 2 || list[0].ForecastID != child || list[0].Side != "yes" || list[0].Price != 0.85 || list[1].ForecastID != id ||
		list[0].MarkPrice == nil || *list[0].MarkPrice != 0.84 {
		t.Fatalf("child lineage: %+v", list)
	}
	if list := apptest.JSON[positionsBody](t, e.Do("GET", "/api/market-positions?forecastId="+id, nil)).Positions; len(list) != 1 || list[0].Side != "no" {
		t.Fatalf("the parent lists only its own: %+v", list)
	}
	if list := apptest.JSON[positionsBody](t, e.Do("GET", "/api/market-positions", nil)).Positions; len(list) != 2 {
		t.Fatalf("all: %+v", list)
	}
	if w := e.Do("GET", "/api/market-positions?forecastId=nope", nil); w.Code != 400 {
		t.Fatalf("bad forecastId: %d", w.Code)
	}
	other := e.Session(storetest.User(t, e.DB))
	if w := e.DoAs(other, "GET", "/api/market-positions?forecastId="+id, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"positions":[]`) {
		t.Fatalf("another user's: %d %s", w.Code, w.Body)
	}

	// Refusals: nothing is spent.
	before, _ := e.Paper.Portfolio(ctx, e.User)
	noMarket := nvdaResult()
	noMarket.Market = nil
	bare := stored(t, e, e.User, nvdaDraft, noMarket, "")
	resolved := stored(t, e, e.User, nvdaDraft, nvdaResult(), "")
	running, _ := (&handler{e.Deps}).insert(ctx, e.User, nvdaDraft, nil, nil)
	if _, err := e.DB.Exec(ctx, `update forecasts set outcome = 'Nvidia', resolved_at = now(), resolved_by = 'manual' where id = $1`, resolved); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, id string
		body     any
		code     int
		msg      string
	}{
		{"side", child, map[string]any{"side": "maybe", "stake": 50}, 400, "side must be yes or no."},
		{"small stake", child, map[string]any{"side": "yes", "stake": 5}, 400, "stake must be $10 to $1,000."},
		{"big stake", child, map[string]any{"side": "yes", "stake": 1000.01}, 400, "stake must be $10 to $1,000."},
		{"no stake", child, map[string]any{"side": "yes"}, 400, "stake must be $10 to $1,000."},
		{"bad json", child, `{"side":`, 400, ""},
		{"unknown", "00000000-0000-0000-0000-000000000000", map[string]any{"side": "yes", "stake": 50}, 404, ""},
		{"running", running.ID, map[string]any{"side": "yes", "stake": 50}, 409, "Only a completed forecast takes positions."},
		{"resolved", resolved, map[string]any{"side": "yes", "stake": 50}, 409, "This forecast has resolved."},
		{"no market", bare, map[string]any{"side": "yes", "stake": 50}, 409, "This forecast has no matched market."},
	} {
		if code, body := post(c.id, c.body); code != c.code || !strings.Contains(body, c.msg) {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
	}
	if w := e.DoAs(other, "POST", "/api/forecasts/"+child+"/market-position", map[string]any{"side": "yes", "stake": 50}); w.Code != 404 {
		t.Errorf("another user's forecast: %d", w.Code)
	}
	closed = true
	if code, body := post(child, map[string]any{"side": "yes", "stake": 50}); code != 502 || !strings.Contains(body, `"upstream"`) {
		t.Errorf("closed market: %d %s", code, body)
	}
	closed = false
	if _, err := e.DB.Exec(ctx, `update accounts set cash = 40 where user_id = $1`, e.User); err != nil {
		t.Fatal(err)
	}
	if code, body := post(child, map[string]any{"side": "yes", "stake": 50}); code != 400 || !strings.Contains(body, "insufficient_buying_power") {
		t.Errorf("more than the cash: %d %s", code, body)
	}
	var n int
	if err := e.DB.QueryRow(ctx, `select count(*) from market_positions where user_id = $1`, e.User).Scan(&n); err != nil || n != 2 {
		t.Fatalf("positions after refusals: %d %v", n, err)
	}
	if after, _ := e.Paper.Portfolio(ctx, e.User); after.PredictionStakes != before.PredictionStakes || after.Revision != before.Revision {
		t.Fatalf("a refusal spent: %+v → %+v", before, after)
	}
}
