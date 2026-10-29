package yahootest_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

func TestFakeSource(t *testing.T) {
	f := yahootest.New()
	f.Set(yahootest.Daily("HSDT", "USD", 2.5, 2.6, 2.65))
	f.SetFX("SGD", 0.78)
	f.SetErr("DOWN", yahootest.Down)
	ctx := context.Background()
	c, err := f.Chart(ctx, "hsdt", "max", "1d")
	if err != nil || len(c.Bars) != 3 || c.Bars[2].T != yahootest.End || c.Bars[0].T.Weekday() != 4 || *c.Meta.Price != 2.65 {
		t.Fatalf("%+v %v", c, err)
	}
	qs, err := f.Quotes(ctx, []string{"HSDT", "NOPE", "DOWN"})
	if len(qs) != 1 || qs["HSDT"].Price != 2.65 || !errors.Is(err, yahoo.ErrUnavailable) || yahoo.IsNotFound(err) {
		t.Fatalf("%+v %v", qs, err)
	}
	if _, err := f.Quote(ctx, "NOPE"); !yahoo.IsNotFound(err) {
		t.Fatal(err)
	}
	if r, err := f.FX(ctx, "sgd"); err != nil || r.USD != 0.78 || r.Symbol != "SGDUSD=X" {
		t.Fatalf("%+v %v", r, err)
	}
	if h, err := f.FXHistory(ctx, "SGD", "1y"); err != nil || len(h) != 30 {
		t.Fatalf("%d %v", len(h), err)
	}
	if f.Calls("HSDT") != 2 {
		t.Fatalf("calls %d", f.Calls("HSDT"))
	}
}

// The fake server speaks the documents the real client reads.
func TestServeRoundTripsThroughTheClient(t *testing.T) {
	f := yahootest.New()
	f.Set(yahootest.Daily("D05.SI", "SGD", 77, 78, 78.37))
	f.SetFX("SGD", 0.7819)
	f.SetErr("DOWN", yahootest.Down)
	c := yahoo.New(yahoo.Config{Base: f.Serve(t)})
	ctx := context.Background()
	ch, err := c.Chart(ctx, "D05.SI", "5y", "1d")
	want := yahootest.Daily("D05.SI", "SGD", 77, 78, 78.37)
	if err != nil || len(ch.Bars) != 3 || ch.Bars[2] != want.Bars[2] || ch.Meta.Currency != "SGD" || *ch.Meta.Price != 78.37 ||
		!ch.Meta.PriceTime.Equal(yahootest.End) || ch.Meta.Exchange() != "NYSE" {
		t.Fatalf("%+v %v", ch, err)
	}
	qs, err := c.Quotes(ctx, []string{"D05.SI", "SGDUSD=X", "NOPE"})
	if err != nil || len(qs) != 2 || qs["D05.SI"].Via != "spark" || qs["SGDUSD=X"].Price != 0.7819 {
		t.Fatalf("%+v %v", qs, err)
	}
	if r, err := c.FX(ctx, "SGD"); err != nil || r.USD != 0.7819 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := c.Chart(ctx, "NOPE", "1y", "1d"); !yahoo.IsNotFound(err) {
		t.Fatal(err)
	}
}

// The fake counts what the client would send: one per chart, quote and FX
// read, one per spark batch of 20.
func TestFakeCountsCalls(t *testing.T) {
	f := yahootest.New()
	f.Set(yahootest.Daily("A", "USD", 1, 2))
	f.SetFX("JPY", 0.0067)
	var n atomic.Int64
	ctx := yahoo.WithCallCount(context.Background(), &n)
	f.Chart(ctx, "A", "1y", "1d")
	f.Quote(ctx, "A")
	syms := make([]string, 21)
	for i := range syms {
		syms[i] = "A"
	}
	f.Quotes(ctx, syms)
	f.FX(ctx, "JPY")
	f.FX(ctx, "USD")
	f.FXHistory(ctx, "JPY", "1mo")
	if n.Load() != 1+1+2+1+1 {
		t.Fatalf("counted %d", n.Load())
	}
}
