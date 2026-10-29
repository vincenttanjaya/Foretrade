package yahoo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// Spark answers HTTP 400 past 20 symbols (checked 2026-10-13 with 21, 25, 50
// and 100), and an answer the client cannot read turns spark off for an
// hour: no request may carry more than 20, whatever the caller asks.
func TestQuotesNeverExceedSparkLimit(t *testing.T) {
	f := yahootest.New()
	var syms []string
	for i := range 742 {
		s := fmt.Sprintf("T%03d.T", i)
		syms = append(syms, s)
		if i%3 == 0 {
			f.Set(yahootest.Daily(s, "JPY", 100, 101))
		}
	}
	c := yahoo.New(yahoo.Config{Base: f.Serve(t), Unpaced: true})
	qs, err := c.Quotes(context.Background(), syms)
	if err != nil || len(qs) != 248 {
		t.Fatalf("%d quotes, %v", len(qs), err)
	}
	sparks := f.Sparks()
	if len(sparks) != 38 || !c.Batched() {
		t.Fatalf("%d spark requests, batched %v", len(sparks), c.Batched())
	}
	for i, n := range sparks {
		if n > 20 || n < 1 {
			t.Fatalf("spark request %d carried %d symbols", i, n)
		}
	}
}
