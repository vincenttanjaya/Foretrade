package paper

import (
	"context"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

func TestAccountOriginRefsOwnedForeignDeletedAndMissing(t *testing.T) {
	pool := storetest.Schema(t)
	user, other := storetest.User(t, pool), storetest.User(t, pool)
	e := NewEngine(pool, testMarketClient(markettest.New(t).URL))
	ctx := context.Background()
	insert := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, q+" returning id::text", args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	agent := insert(`insert into live_agents(persona_id,name) values('account-ref-agent','owned copy target')`)
	for _, kind := range []string{"alert", "copy"} {
		t.Run(kind, func(t *testing.T) {
			makeTarget := func(uid string) string {
				if kind == "alert" {
					return insert(`insert into alerts(user_id,symbol,op,price) values($1,'SOL','below',175)`, uid)
				}
				return insert(`insert into copy_plans(user_id,agent_id,ratio,max_usd,status) values($1,$2,0.1,1000,'stopped')`, uid, agent)
			}
			owned, foreign, deleted := makeTarget(user), makeTarget(other), makeTarget(user)
			missing := "6f1c2c1e-1111-4000-8000-000000000099"
			var ids []string
			for _, id := range []string{owned, foreign, deleted, missing} {
				n := 100.0
				tr, _, err := e.Order(ctx, user, OrderRequest{Symbol: "SOL", Side: "buy", Notional: &n, Source: kind, RefID: &id, Note: "stored unverified label"})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, tr.ID)
			}
			table := "alerts"
			if kind == "copy" {
				table = "copy_plans"
			}
			if _, err := pool.Exec(ctx, "delete from "+table+" where id=$1", deleted); err != nil {
				t.Fatal(err)
			}
			trades, err := e.Trades(ctx, user, 200)
			if err != nil {
				t.Fatal(err)
			}
			found := map[string]Trade{}
			for _, tr := range trades {
				found[tr.ID] = tr
			}
			for i, id := range ids {
				tr, ok := found[id]
				if !ok || tr.Notional != 100 || tr.Source != kind {
					t.Fatalf("booked fill dropped or changed: %+v", tr)
				}
				if i == 0 {
					if tr.Ref == nil || tr.Ref.ID != owned || tr.Ref.Kind != kind {
						t.Fatalf("owned target unavailable: %+v", tr)
					}
				} else if tr.Ref != nil {
					t.Errorf("foreign/deleted/missing target fabricated verified ref: %+v", tr.Ref)
				}
			}
		})
	}
}
