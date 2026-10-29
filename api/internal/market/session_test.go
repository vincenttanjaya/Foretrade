package market

import (
	"context"
	"testing"
	"time"
)

func TestUSSessionVerifiedCalendar(t *testing.T) {
	for _, tc := range []struct {
		at   string
		open bool
		next string
	}{
		{"2026-03-09T13:29:59Z", false, "2026-03-09T13:30:00Z"},
		{"2026-03-09T13:30:00Z", true, "2026-03-09T20:00:00Z"},
		{"2026-03-09T20:00:00Z", false, "2026-03-10T13:30:00Z"},
		{"2026-11-02T14:30:00Z", true, "2026-11-02T21:00:00Z"},
		{"2026-07-03T15:00:00Z", false, "2026-07-06T13:30:00Z"},
		{"2026-11-27T17:59:59Z", true, "2026-11-27T18:00:00Z"},
		{"2026-11-27T18:00:00Z", false, "2026-11-30T14:30:00Z"},
		{"2027-12-31T14:30:00Z", true, "2027-12-31T21:00:00Z"},
		{"2028-07-03T17:00:00Z", false, "2028-07-05T13:30:00Z"},
		{"2028-11-24T18:00:00Z", false, "2028-11-27T14:30:00Z"},
		{"2028-04-14T15:00:00Z", false, "2028-04-17T13:30:00Z"},
	} {
		t.Run(tc.at, func(t *testing.T) {
			at, _ := time.Parse(time.RFC3339, tc.at)
			want, _ := time.Parse(time.RFC3339, tc.next)
			open, next := USSession(at)
			if open != tc.open || !next.Equal(want) {
				t.Fatalf("open%v next%s want%v %s", open, next, tc.open, want)
			}
		})
	}
	for _, text := range []string{"2025-12-31T15:00:00Z", "2029-01-02T15:00:00Z", "2028-12-31T15:00:00Z"} {
		at, _ := time.Parse(time.RFC3339, text)
		open, next := USSession(at)
		if open || !next.IsZero() {
			t.Errorf("unverified boundary %s: %v %s", text, open, next)
		}
	}
}

func TestSessionIsReadTimeResponseLocal(t *testing.T) {
	c := testMarketClient("http://127.0.0.1:1")
	open, _ := USSession(time.Now())
	old := "open"
	want := "closed"
	if open {
		old, want = "closed", "open"
	}
	c.assets.val = []Asset{{Symbol: "AAPL", Class: Stock, Venue: "hyperliquid", Price: 332, Session: &old}, {Symbol: "BTC", Class: Crypto, Venue: "binance", Price: 100000}}
	c.assets.at = time.Now()
	rows, err := c.Assets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Session == nil || *rows[0].Session != want {
		t.Fatalf("session frozen in quote cache: %+v want%s", rows[0], want)
	}
	if *c.assets.val[0].Session != old || rows[1].Session != nil || rows[1].SessionChangeAt != nil {
		t.Fatal("read mutated shared snapshot or decorated crypto")
	}
	*rows[0].Session = "forged"
	if *c.assets.val[0].Session != old {
		t.Fatal("session pointer aliases cached snapshot")
	}
}
