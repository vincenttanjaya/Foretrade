package chat

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

func TestUniverseChatSourceDiscipline(t *testing.T) {
	p := systemPrompt(time.Now(), "")
	for _, bad := range []string{"stocks such as NVDA", "78 crypto pairs", "Live Binance spot data exists only for this universe"} {
		if strings.Contains(p, bad) {
			t.Errorf("stale mixed-source prose:%s", bad)
		}
	}
	perps := 0 // the prompt counts them from the universe; Yahoo quote-feed rows are their own group
	for _, a := range market.StockPerps {
		if a.Venue != "yahoo" {
			perps++
		}
	}
	for _, want := range []string{fmt.Sprintf("%d crypto", len(market.CryptoUniverse)), fmt.Sprintf("%d stock/index", perps), "TSM is TSMC", "Hyperliquid perp mark", "sessionVenue", "closed market", "forecast price rules remain crypto-only"} {
		if !strings.Contains(strings.ToLower(p), strings.ToLower(want)) {
			t.Errorf("missing source/session/boundary discipline%q", want)
		}
	}
	for _, symbol := range []string{"MUBARAK", "VIRTUAL", "PENDLE", "SP500", "XYZ100", "AAPL"} {
		q, ok := parseQuickOrder("buy $500 of " + symbol)
		if !ok || q.symbol != symbol {
			t.Errorf("tradable quick order rejected%s:%+v %v", symbol, q, ok)
		}
	}
}
