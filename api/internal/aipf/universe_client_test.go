package aipf

import "github.com/vincenttanjaya/Foretrade/api/internal/market"

func testMarketClient(base string) *market.Client {
	c := market.NewClient(base)
	c.HyperliquidBase = base
	if base == "" {
		c.HyperliquidBase = "http://127.0.0.1:1"
	}
	return c
}
