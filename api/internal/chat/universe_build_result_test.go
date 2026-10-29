package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

func TestUniverseBuildResultSavedBenchmarkWindow(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	for _, tc := range []struct{ name, bt, want string }{
		{"stock", `{"returnPct":12,"benchmarkReturnPct":9,"maxDrawdownPct":4,"benchmarkSymbol":"SP500","benchmarkLabel":"S&P 500 perp","windowStart":100,"windowEnd":200,"bars":31}`, "SP500"},
		{"legacy", `{"returnPct":12,"benchmarkReturnPct":9,"maxDrawdownPct":4}`, "BTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.DB.Exec(context.Background(), `insert into ai_portfolios (user_id,name,thesis,prompt,holdings,backtest) values ($1,$2,'',$2,'[]',$3::jsonb)`, e.User, tc.name, tc.bt); err != nil {
				t.Fatal(err)
			}
			result, err := (handler{d: e.Deps}).buildResult(context.Background(), e.User, "portfolio", tc.name, time.Now().Add(-time.Hour))
			if err != nil || result == nil {
				t.Fatalf("result %+v %v", result, err)
			}
			data, _ := json.Marshal(result)
			var fields map[string]any
			json.Unmarshal(data, &fields)
			if fields["benchmarkSymbol"] != tc.want {
				t.Fatalf("card lost persisted/legacy identity %s", data)
			}
			if tc.want == "SP500" && (fields["benchmarkLabel"] != "S&P 500 perp" || fields["windowStart"] != float64(100) || fields["windowEnd"] != float64(200) || fields["bars"] != float64(31)) {
				t.Fatalf("card window lost %s", data)
			}
		})
	}
}
