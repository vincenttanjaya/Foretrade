package aipf

import (
	"context"
	"encoding/json"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"testing"
)

func TestPortfolioOverlapPrivateAndFresh(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	other := storetest.User(t, e.DB)
	var id string
	err := e.DB.QueryRow(ctx, `insert into ai_portfolios(user_id,name,thesis,prompt,holdings,backtest) values($1,'same basket','x','x','[{"symbol":"BTC","weight":1}]','{}') returning id`, e.User).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	type staticPayload struct{ holdings, backtest string }
	readStored := func(basketID string) staticPayload {
		t.Helper()
		var stored staticPayload
		if err := e.DB.QueryRow(ctx, "select holdings::text,backtest::text from ai_portfolios where id=$1", basketID).Scan(&stored.holdings, &stored.backtest); err != nil {
			t.Fatal(err)
		}
		return stored
	}
	original := readStored(id)
	wantStored := original
	if _, _, err := e.Paper.Order(ctx, e.User, paper.OrderRequest{Symbol: "BTC", Side: "buy", Notional: new(1000.0)}); err != nil {
		t.Fatal(err)
	}
	fetch := func() map[string]any {
		w := e.Do("GET", "/api/ai-portfolios/"+id, "")
		if w.Code != 200 {
			t.Fatalf("get %d %s", w.Code, w.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if stored := readStored(id); stored != wantStored {
			t.Fatalf("detail read changed static basket JSON: got %+v, want %+v", stored, wantStored)
		}
		return body["portfolio"].(map[string]any)
	}
	first := fetch()
	overlap, ok := first["overlap"].(map[string]any)
	if !ok {
		t.Fatal("private overlap missing")
	}
	if len(overlap["holdings"].([]any)) != 1 {
		t.Fatal("own holdings missing")
	}
	if w := e.DoAs(e.Session(other), "GET", "/api/ai-portfolios/"+id, ""); w.Code != 404 {
		t.Fatalf("foreign basket %d", w.Code)
	}
	firstHolding := overlap["holdings"].([]any)[0].(map[string]any)
	if firstHolding["side"] != "long" || firstHolding["bookPct"] != float64(100) || firstHolding["basketPct"] != float64(100) || firstHolding["shared"] != true {
		t.Fatalf("wrong overlap weights: %+v", firstHolding)
	}
	pair := overlap["correlations"].([]any)[0].(map[string]any)
	if pair["rho"] != float64(1) || pair["days"].(float64) < 20 {
		t.Fatalf("common ticker measurement: %+v", pair)
	}
	var otherID string
	if err := e.DB.QueryRow(ctx, `insert into ai_portfolios(user_id,name,thesis,prompt,holdings,backtest) select $1,name,thesis,prompt,holdings,backtest from ai_portfolios where id=$2 returning id`, other, id).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	otherOriginal := readStored(otherID)
	if _, _, err := e.Paper.Order(ctx, other, paper.OrderRequest{Symbol: "ETH", Side: "sell", Notional: new(1000.0)}); err != nil {
		t.Fatal(err)
	}
	otherResponse := apptest.JSON[struct {
		Portfolio struct{ Overlap paper.Overlap }
	}](t, e.DoAs(e.Session(other), "GET", "/api/ai-portfolios/"+otherID, nil)).Portfolio.Overlap
	if len(otherResponse.Holdings) != 1 || otherResponse.Holdings[0].Symbol != "ETH" || otherResponse.Holdings[0].Side != "short" || otherResponse.Holdings[0].Shared || len(otherResponse.Correlations) != 1 || otherResponse.Correlations[0].Rho == nil {
		t.Fatalf("other caller projection: %+v", otherResponse)
	}
	if stored := readStored(otherID); stored != otherOriginal {
		t.Fatalf("other caller read changed static basket JSON: got %+v, want %+v", stored, otherOriginal)
	}
	if fetch()["overlap"].(map[string]any)["holdings"].([]any)[0].(map[string]any)["symbol"] != "BTC" {
		t.Fatal("other user's projection polluted first caller")
	}
	// An unavailable basket asset must label comparison coverage, even when caller risk is measurable.
	if _, err := e.DB.Exec(ctx, `update ai_portfolios set holdings='[{"symbol":"APT","weight":1}]' where id=$1`, id); err != nil {
		t.Fatal(err)
	}
	wantStored = readStored(id) // this explicit fixture mutation is not a detail read
	e.Binance.Down("APTUSDT", true)
	unknown := fetch()["overlap"].(map[string]any)
	coverage := unknown["coverage"].(map[string]any)
	if coverage["reason"] == nil || len(coverage["missingSymbols"].([]any)) == 0 {
		t.Fatal("basket comparison unknown omitted from coverage")
	}
	if _, err := e.DB.Exec(ctx, `update ai_portfolios set holdings='[{"symbol":"BTC","weight":1}]' where id=$1`, id); err != nil {
		t.Fatal(err)
	}
	wantStored = original
	if _, err := e.Paper.Reset(ctx, e.User); err != nil {
		t.Fatal(err)
	}
	second := fetch()["overlap"].(map[string]any)
	if len(second["holdings"].([]any)) != 0 || second["revision"] == overlap["revision"] {
		t.Fatal("stale caller projection")
	}
	if stored := readStored(otherID); stored != otherOriginal {
		t.Fatalf("first caller reads/reset changed other basket JSON: got %+v, want %+v", stored, otherOriginal)
	}
	if used(t, e) != 0 || len(e.Bedrock.Requests()) != 0 {
		t.Fatal("read charged credits or called model")
	}
}
