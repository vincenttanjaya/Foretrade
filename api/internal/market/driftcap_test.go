package market

import (
	"math"
	"testing"
	"time"
)

// benchSeries is n years of daily closes compounding at cagr %/yr, ending today.
func benchSeries(key string, years, cagr float64) DailySeries {
	end := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	n := int(years * 365)
	s := DailySeries{Symbol: key, Ticker: Benchmarks[key], Source: "Yahoo Finance daily closes (" + Benchmarks[key] + ")", PerYear: 365, Kind: "price"}
	for i := 0; i <= n; i++ {
		t := end.AddDate(0, 0, i-n)
		s.Closes = append(s.Closes, DayClose{T: t, C: 100 * math.Pow(1+cagr/100, float64(i)/365)})
	}
	return s
}

func TestCapDrift(t *testing.T) {
	spx := benchSeries("SPX", 98, 6.1)
	// NVDA's halved drift (15.64%/yr log, +16.9%/yr) capped at the S&P 500's 6.1% + 2 = 8.1%/yr.
	mu, c := CapDrift(0.1564, "SPX", &spx)
	if !c.Applied || c.Benchmark != "SPX" || c.BenchmarkCAGRPct == nil || math.Abs(*c.BenchmarkCAGRPct-6.1) > 0.01 ||
		math.Abs(c.CapPct-8.1) > 0.01 || math.Abs(mu-math.Log(1.081)) > 1e-4 || math.Abs(c.UncappedPct-16.93) > 0.01 ||
		c.Since == nil || *c.Since != spx.Closes[0].T.Format(time.DateOnly) || c.Source != "Yahoo Finance daily closes (^GSPC)" {
		t.Errorf("NVDA: %v %+v", mu, c)
	}
	// Never above 10%/yr, whatever the benchmark did.
	hot := benchSeries("NDX", 40, 12)
	if mu, c := CapDrift(0.2, "NDX", &hot); c.CapPct != 10 || math.Abs(mu-math.Log(1.10)) > 1e-12 || !c.Applied {
		t.Errorf("hot benchmark: %v %+v", mu, c)
	}
	// No benchmark (crypto), or one too short to be long-run: the ceiling alone.
	short := benchSeries("SPX", 5, 6)
	for _, b := range []*DailySeries{nil, &short} {
		if mu, c := CapDrift(0.25, "", b); c.CapPct != 10 || c.Benchmark != "" || c.BenchmarkCAGRPct != nil || math.Abs(mu-math.Log(1.10)) > 1e-12 {
			t.Errorf("ceiling only: %v %+v", mu, c)
		}
	}
	// A drift under the cap, or negative, stands; a falling benchmark never forces a negative cap.
	for _, m := range []float64{0.05, -0.1} {
		if mu, c := CapDrift(m, "SPX", &spx); mu != m || c.Applied {
			t.Errorf("drift %v: %v %+v", m, mu, c)
		}
	}
	down := benchSeries("N225", 30, -3)
	if mu, c := CapDrift(0.05, "N225", &down); c.CapPct != 0 || mu != 0 {
		t.Errorf("falling benchmark: %v %+v", mu, c)
	}
}

func TestHomeBenchmark(t *testing.T) {
	for sym, want := range map[string]string{
		"NVDA": "SPX", "SP500": "SPX", "AAPL": "SPX", "TSM": "TWII", "UMC": "TWII", "EWT": "TWII", "SONY": "N225", "SOFTBANK": "N225",
		"SMSN": "KOSPI", "KB": "KOSPI", "BABA": "HSI", "MINIMAX": "HSI", "CXMT": "HSI", "KSTR": "HSI", "DBS": "STI", "TLK": "JKSE",
		"BBCA": "JKSE", "INFY": "SPX", "SE": "SPX", "PHI": "SPX", "CPNG": "SPX", "BTC": "",
	} {
		info, ok := Lookup(sym)
		if got := HomeBenchmark(info); !ok || got != want {
			t.Errorf("HomeBenchmark(%s) = %q, want %q", sym, got, want)
		}
		if want != "" && Benchmarks[want] == "" {
			t.Errorf("%s: %s is not a benchmark", sym, want)
		}
	}
}
