package market

import (
	"math"
	"regexp"
	"strings"
)

// The drift-only headline is a backward-looking number: half a surviving
// listing's own CAGR still promises NVDA +16.9%/yr. Without the model's
// scenarios (inputs with a basis) the card does not overpromise: its drift is
// capped at the listing's market's long-run price return plus 2 points a
// year, never above 10%/yr. Scenario views are not capped.

const (
	driftCapPremium = 2.0  // points a year over the benchmark's long-run price CAGR
	driftCapCeiling = 10.0 // %/yr: the drift-only headline never assumes more
	benchMinYears   = 10.0 // a benchmark CAGR needs this much history to count as long-run
)

// DriftCap is how a drift-only view's annual return was capped. The uncapped
// halved drift stays on the card's history (driftHalvedPct) for transparency.
type DriftCap struct {
	Benchmark        string   `json:"benchmark"`        // Benchmarks key ("SPX", "TWII"); "" without one (the 10%/yr ceiling alone)
	Source           string   `json:"source"`           // the benchmark's series; "" without one
	Since            *string  `json:"since"`            // its first close
	BenchmarkCAGRPct *float64 `json:"benchmarkCagrPct"` // its price CAGR over that whole history
	CapPct           float64  `json:"capPct"`           // min(benchmark + 2, 10), annual price return in %
	UncappedPct      float64  `json:"uncappedPct"`      // the halved drift as an annual price return in %
	Applied          bool     `json:"applied"`          // the halved drift was above the cap
}

var (
	twseRe  = regexp.MustCompile(`\bTWSE\b|\bTaiwan\b`)
	krxRe   = regexp.MustCompile(`\bKRX\b`)
	tseRe   = regexp.MustCompile(`\bTSE\b`)
	chinaRe = regexp.MustCompile(`\bHKEX\b|\bSSE\b|\bSTAR\b|\bA-share\b`)
	sgxRe   = regexp.MustCompile(`\bSGX\b`)
	idxRe   = regexp.MustCompile(`\bIDX\b`)
)

// HomeBenchmark is the Benchmarks key of a listing's home market: the S&P 500
// for US listings; for an Asian one its home index (an ADR's underlying
// exchange: TSM → TWII, SONY → N225), the Hang Seng for mainland and Hong Kong
// shares, and the S&P 500 when its home index is not on the allowlist (India,
// the Philippines, ADRs with no local listing). "" for crypto.
func HomeBenchmark(info AssetInfo) string {
	l := info.Listing
	if l == nil {
		return ""
	}
	if l.Region != "asia" {
		return "SPX"
	}
	ref := l.Underlying + " " + info.Name
	switch {
	case l.SessionVenue == KRX || krxRe.MatchString(ref):
		return "KOSPI"
	case twseRe.MatchString(ref):
		return "TWII"
	case l.SessionVenue == TSE || tseRe.MatchString(ref):
		return "N225"
	case l.SessionVenue == HKEX || l.SessionVenue == SSE || chinaRe.MatchString(ref):
		return "HSI"
	case l.SessionVenue == SGX || sgxRe.MatchString(ref):
		return "STI"
	case l.SessionVenue == IDX || idxRe.MatchString(ref):
		return "JKSE"
	}
	return "SPX"
}

// CapDrift caps the annual log drift mu (HalvedDrift) of a drift-only view at
// ln(1 + cap) with cap = min(bench's long-run price CAGR + 2, 10) %/yr (not
// below 0); bench
// nil (no benchmark, or it is unavailable) or under 10 years leaves the 10%/yr
// ceiling alone. A drift below the cap, or negative, is kept.
func CapDrift(mu float64, key string, bench *DailySeries) (float64, DriftCap) {
	c := DriftCap{CapPct: driftCapCeiling, UncappedPct: (math.Exp(mu) - 1) * 100}
	if bench != nil && bench.Years() >= benchMinYears {
		first, last := bench.Closes[0], bench.Closes[len(bench.Closes)-1]
		if first.C > 0 && last.C > 0 {
			cagr := (math.Pow(last.C/first.C, 1/bench.Years()) - 1) * 100
			since := day(first.T)
			c.Benchmark, c.Source, c.Since, c.BenchmarkCAGRPct = strings.ToUpper(key), bench.Source, &since, &cagr
			c.CapPct = max(min(cagr+driftCapPremium, driftCapCeiling), 0)
		}
	}
	if capMu := math.Log1p(c.CapPct / 100); mu > capMu {
		mu, c.Applied = capMu, true
	}
	return mu, c
}
