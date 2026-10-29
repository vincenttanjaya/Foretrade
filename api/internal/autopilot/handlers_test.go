package autopilot

import (
	"maps"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"github.com/vincenttanjaya/Foretrade/api/internal/strategy"
)

type deployResp struct {
	Autopilot Autopilot
	Trade     *paper.Trade
	Portfolio *paper.Portfolio
}

// saved stores an own strategy whose entry rule holds on every bar.
func saved(t *testing.T, e *apptest.Env, s strategy.Spec) string {
	t.Helper()
	var id string
	if err := e.DB.QueryRow(ctx, `insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades)
		values ($1, $2, '', 'p', $3, '{}', '[]', '[]', '[]') returning id`, e.User, s.Name, s).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

const labsID = "967c9273-c207-560a-9ae7-43707b7d1451" // BTC EMA Trend Rider

func TestHandlers(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	for _, r := range [][2]string{{"POST", "/api/autopilots"}, {"GET", "/api/autopilots"}, {"GET", "/api/autopilots/x"}, {"POST", "/api/autopilots/x/stop"}} {
		if w := e.DoAs(nil, r[0], r[1], "{}"); w.Code != 401 {
			t.Errorf("%s %s without a session: %d", r[0], r[1], w.Code)
		}
	}
	id := saved(t, e, spec(nil))
	for body, code := range map[string]int{
		`{"strategyId":"` + id + `","allocationUsd":50,"maxDrawdownPct":10}`:                             400,
		`{"strategyId":"` + id + `","allocationUsd":5000,"maxDrawdownPct":60}`:                           400,
		`{"strategyId":"nope","allocationUsd":5000,"maxDrawdownPct":10}`:                                 404,
		`{"strategyId":"6f1c2b9e-1111-4222-8333-944455556666","allocationUsd":5000,"maxDrawdownPct":10}`: 404,
		`{"strategyId":`: 400,
	} {
		if w := e.Do("POST", "/api/autopilots", body); w.Code != code {
			t.Errorf("%.70s: %d %s", body, w.Code, w.Body)
		}
	}
	// Guardrail: every entry carries a stop (fixed or trailing) and a target, from the spec or the overrides.
	guard := "Autopilot entries always carry a stop and a target: set stopLossPct (or trailingStopPct) and takeProfitPct."
	for _, tc := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{}, guard},
		{map[string]any{"stopLossPct": 5}, guard},
		{map[string]any{"takeProfitPct": 10}, guard},
		{map[string]any{"stopLossPct": nil, "takeProfitPct": 10}, guard},
		{map[string]any{"stopLossPct": 0.05, "takeProfitPct": 10}, "stopLossPct must be 0.1 to 50."},
		{map[string]any{"stopLossPct": 5, "takeProfitPct": 60}, "takeProfitPct must be 0.1 to 50."},
		{map[string]any{"trailingStopPct": 0.2, "takeProfitPct": 10}, "trailingStopPct must be 0.5 to 50."},
	} {
		body := map[string]any{"strategyId": id, "allocationUsd": 5000, "maxDrawdownPct": 10}
		maps.Copy(body, tc.body)
		if w := e.Do("POST", "/api/autopilots", body); w.Code != 400 || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%v: %d %s", tc.body, w.Code, w.Body)
		}
	}
	// A Labs strategy without a target (BTC EMA Trend Rider: 5% stop) needs one too.
	if w := e.Do("POST", "/api/autopilots", map[string]any{"strategyId": labsID, "allocationUsd": 1000, "maxDrawdownPct": 5}); w.Code != 400 ||
		!strings.Contains(w.Body.String(), guard) {
		t.Fatalf("labs without a target: %d %s", w.Code, w.Body)
	}

	// Deploy: the entry rule holds on the last close, so it fills at once with the overridden levels.
	w := e.Do("POST", "/api/autopilots", map[string]any{"strategyId": id, "allocationUsd": 5000, "maxDrawdownPct": 10,
		"stopLossPct": 5, "takeProfitPct": 10, "trailingStopPct": nil})
	dep := apptest.JSON[deployResp](t, w)
	a := dep.Autopilot
	if w.Code != 201 || a.Status != "running" || a.Symbol != "SOL" || a.Interval != "4h" || a.StrategyID != id || a.PositionQty <= 0 ||
		a.MarkPrice == nil || a.Equity == nil || a.DrawdownPct == nil || a.NextCheckAt == nil || len(a.Log) != 2 ||
		a.Log[1].Text != "Deployed with $5,000; checks every closed 4h bar · stop 5% · target 10%." || a.Log[0].Kind != "entry" ||
		*a.StopLossPct != 5 || *a.TakeProfitPct != 10 || a.TrailingStopPct != nil {
		t.Fatalf("deploy: %d %s", w.Code, w.Body)
	}
	if pos := position(*dep.Portfolio, "SOL"); pos == nil || !near(*pos.StopLoss, *a.MarkPrice*0.95) || !near(*pos.TakeProfit, *a.MarkPrice*1.1) {
		t.Fatalf("entry levels: %+v", pos)
	}
	if dep.Trade == nil || dep.Trade.Source != "autopilot" || dep.Trade.Side != "buy" || dep.Portfolio == nil || position(*dep.Portfolio, "SOL") == nil {
		t.Fatalf("deploy fill: %s", w.Body)
	}
	if next := a.NextCheckAt.Unix(); next != *a.LastBarT+2*iv {
		t.Fatalf("next check %v after bar %v", next, *a.LastBarT)
	}
	if w := e.Do("POST", "/api/autopilots", map[string]any{"strategyId": labsID, "allocationUsd": 1000, "maxDrawdownPct": 5, "takeProfitPct": 12}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "You already have an autopilot running (Always). Stop it first.") {
		t.Fatalf("second deploy: %d %s", w.Code, w.Body)
	}

	type list struct{ Autopilots []Autopilot }
	if l := apptest.JSON[list](t, e.Do("GET", "/api/autopilots", nil)); len(l.Autopilots) != 1 || l.Autopilots[0].ID != a.ID {
		t.Fatalf("list: %+v", l)
	}
	type detail struct {
		Autopilot Autopilot
		Trades    []paper.Trade
	}
	if d := apptest.JSON[detail](t, e.Do("GET", "/api/autopilots/"+a.ID, nil)); d.Autopilot.ID != a.ID || len(d.Trades) != 1 || d.Trades[0].ID != dep.Trade.ID {
		t.Fatalf("detail: %+v", d)
	}
	other := e.Session(storetest.User(t, e.DB))
	for _, r := range [][2]string{{"GET", "/api/autopilots/" + a.ID}, {"POST", "/api/autopilots/" + a.ID + "/stop"}, {"GET", "/api/autopilots/nope"}} {
		if w := e.DoAs(other, r[0], r[1], "{}"); w.Code != 404 {
			t.Errorf("%s %s as another user: %d", r[0], r[1], w.Code)
		}
	}

	// Stop and close at market.
	w = e.Do("POST", "/api/autopilots/"+a.ID+"/stop", map[string]bool{"closePosition": true})
	stopped := apptest.JSON[deployResp](t, w)
	if s := stopped.Autopilot; w.Code != 200 || s.Status != "stopped" || *s.StopReason != "user" || s.PositionQty != 0 || s.StoppedAt == nil ||
		s.NextCheckAt != nil || s.Log[0].Text != "Stopped by you." || s.Log[1].Kind != "exit" {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if stopped.Trade == nil || stopped.Trade.Side != "sell" || stopped.Trade.Source != "autopilot" || position(*stopped.Portfolio, "SOL") != nil {
		t.Fatalf("closing fill: %s", w.Body)
	}
	if d := apptest.JSON[detail](t, e.Do("GET", "/api/autopilots/"+a.ID, nil)); len(d.Trades) != 2 || d.Trades[0].ID != stopped.Trade.ID {
		t.Fatalf("detail after stop: %+v", d.Trades)
	}
	if w := e.Do("POST", "/api/autopilots/"+a.ID+"/stop", "{}"); w.Code != 409 || !strings.Contains(w.Body.String(), "This autopilot is not running.") {
		t.Fatalf("stop twice: %d %s", w.Code, w.Body)
	}

	// Redeploy with a trailing stop only, and stop keeping the position: it stays in the account.
	w = e.Do("POST", "/api/autopilots", map[string]any{"strategyId": id, "allocationUsd": 2000, "maxDrawdownPct": 20, "trailingStopPct": 3, "takeProfitPct": 8})
	again := apptest.JSON[deployResp](t, w)
	if g := again.Autopilot; w.Code != 201 || g.StopLossPct != nil || *g.TrailingStopPct != 3 || *g.TakeProfitPct != 8 ||
		!near(*position(*again.Portfolio, "SOL").StopLoss, *g.MarkPrice*0.97) {
		t.Fatalf("trailing-only deploy: %d %s", w.Code, w.Body)
	}
	w = e.Do("POST", "/api/autopilots/"+again.Autopilot.ID+"/stop", map[string]bool{"closePosition": false})
	kept := apptest.JSON[deployResp](t, w)
	if k := kept.Autopilot; w.Code != 200 || k.Status != "stopped" || k.PositionQty != 0 || kept.Trade != nil ||
		!strings.HasPrefix(k.Log[1].Text, "Kept Long") || position(*kept.Portfolio, "SOL") == nil {
		t.Fatalf("stop, keep: %d %s", w.Code, w.Body)
	}
	if l := apptest.JSON[list](t, e.Do("GET", "/api/autopilots", nil)); len(l.Autopilots) != 2 || l.Autopilots[0].ID != again.Autopilot.ID {
		t.Fatalf("list, newest first: %+v", l)
	}

	// A Labs strategy deploys too; with market data down it still answers 201, logging why it could not check.
	e.Binance.Down("", true)
	fresh(e)
	w = e.Do("POST", "/api/autopilots", map[string]any{"strategyId": labsID, "allocationUsd": 1000, "maxDrawdownPct": 5, "takeProfitPct": 12})
	down := apptest.JSON[deployResp](t, w)
	if d := down.Autopilot; w.Code != 201 || d.Name != "BTC EMA Trend Rider" || d.Symbol != "BTC" || d.MarkPrice != nil || d.Equity != nil ||
		*d.StopLossPct != 5 || *d.TakeProfitPct != 12 || // the Labs stop, the override target
		d.Log[0].Kind != "error" || down.Portfolio != nil || down.Trade != nil {
		t.Fatalf("deploy with market down: %d %s", w.Code, w.Body)
	}
}
