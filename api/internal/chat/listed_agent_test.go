package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// mainPromptAndSchemas is the system prompt plus every tool's name,
// description and schema in bytes at main 1d332e8 (measured on that tree,
// 2026-10-13): the listed rows may add at most 1 KB a turn.
const mainPromptAndSchemas = 79924

func promptAndSchemas(t *testing.T) (int, []string) {
	t.Helper()
	a := &agentRun{d: &app.Deps{}}
	n := len(systemPrompt(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), ""))
	var schemas []string
	for _, tl := range a.tools() {
		b, err := json.Marshal(map[string]any{"name": tl.Name, "description": tl.Description, "schema": tl.Schema})
		if err != nil {
			t.Fatal(err)
		}
		n += len(b)
		schemas = append(schemas, string(b))
	}
	return n, schemas
}

// The agent is told the truth about the listed Asian tickers in about 600
// bytes, by count and index; no tool schema names one, and every enum keeps
// the named universe only.
func TestPromptAndSchemaSize(t *testing.T) {
	n, schemas := promptAndSchemas(t)
	if n > mainPromptAndSchemas+1024 {
		t.Fatalf("prompt and schemas %d bytes, main %d", n, mainPromptAndSchemas)
	}
	t.Logf("prompt and schemas %d bytes (+%d over main)", n, n-mainPromptAndSchemas)
	for _, s := range schemas {
		for _, a := range market.Listed {
			if strings.Contains(s, `"`+a.Symbol+`"`) || strings.Contains(s, " "+a.Symbol+",") {
				t.Fatalf("a tool schema names listed %s: %.120s", a.Symbol, s)
			}
		}
	}
	p := systemPrompt(time.Now(), "")
	for _, want := range []string{
		", plus 604 Asian stocks listed by their Yahoo ticker (Nikkei 225, KOSPI 200, FTSE TWSE Taiwan 50, Hang Seng Index, Straits Times Index, IDX LQ45, SET50, FTSE Bursa Malaysia KLCI, VN30), paper-tradable on the Trade page at their freshest quote (real-time where the app reads the exchange's own feed, else Yahoo's delayed price). Tickers are this app's own",
		"Assets outside the configured 245 instruments and the listed Asian tickers (other equities",
		"A listed Asian ticker (6501.T, 000810.KS, 0005.HK) is paper-tradable on the Trade page (/trade/<ticker>), and get_stock_quote's paper field says so. Your trade, alert and analysis tools take only the instruments named above. For a listed ticker, give the full analysis from get_stock_quote and research, then point to its Trade page instead of a trade card.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "6501.T (") || strings.Contains(p, "BBNI.JK") {
		t.Error("the prompt lists listed rows one by one")
	}
	if got := symbols(); len(got) != 245 {
		t.Fatalf("tool enum has %d symbols", len(got))
	}
}

// get_market_overview counts the listed rows instead of listing them, except
// the ones the user holds.
func TestOverviewOmitsListed(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for sym, ccy := range map[string]string{"6501.T": "JPY", "000810.KS": "KRW"} {
		ch := yahootest.Daily(sym, ccy, 100, 101)
		ch.Meta.PriceTime = &now
		e.Yahoo.Set(ch)
	}
	e.Yahoo.SetFX("JPY", 0.0066)
	e.Yahoo.SetFX("KRW", 0.00072)
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "6501.T", Side: "buy", Qty: new(10.0)}); err != nil {
		t.Fatal(err)
	}
	e.Market.Touch("000810.KS")
	if q, err := e.Market.Quote(ctx, "000810.KS"); err != nil || q.Price <= 0 {
		t.Fatalf("000810.KS is priced: %v", err)
	}
	a := &agentRun{d: e.Deps, userID: e.User}
	out, err := a.marketOverview(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Assets []market.Asset
		Listed struct {
			Count int
			Note  string
		}
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	var held, other bool
	for _, x := range got.Assets {
		held = held || x.Symbol == "6501.T"
		other = other || x.Symbol == "000810.KS"
	}
	if !held || other || got.Listed.Count != 604 || got.Listed.Note != "Asian stocks by Yahoo ticker (index constituents): use get_stock_quote" ||
		!strings.HasPrefix(a.summary, "108 assets") {
		t.Fatalf("held %v other %v listed %+v summary %q", held, other, got.Listed, a.summary)
	}
	// get_stock_quote's paper note for a listed row, exactly.
	q := paperRead("6501.T", func() *market.AssetInfo { x, _ := market.Lookup("6501.T"); return &x }())
	if q.Symbol == nil || *q.Symbol != "6501.T" || q.Note != "Paper-tradable as 6501.T on the Trade page (/trade/6501.T): fills use its freshest quote, the price above (Yahoo's delayed regular-market trade unless priceSource names a real-time feed; outside market hours, the last close). This chat's trade and alert tools do not take listed tickers yet." {
		t.Fatalf("listed paper note %+v", q)
	}
}
