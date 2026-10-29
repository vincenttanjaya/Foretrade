package market

import "math"

// Indicator series are pure: the output has the input's length and holds
// math.NaN() until the indicator has enough history (its warm-up).

func Closes(c []Candle) []float64 {
	out := make([]float64, len(c))
	for i, k := range c {
		out[i] = k.C
	}
	return out
}

func nans(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

// SMA is the simple moving average of the last n values.
func SMA(v []float64, n int) []float64 {
	out := nans(len(v))
	if n <= 0 {
		return out
	}
	sum := 0.0
	for i, x := range v {
		sum += x
		if i >= n {
			sum -= v[i-n]
		}
		if i >= n-1 {
			out[i] = sum / float64(n)
		}
	}
	return out
}

// EMA is seeded with the SMA of the first n values and smoothed by 2/(n+1).
// Leading NaNs are skipped, so an EMA of another indicator (MACD signal) works.
func EMA(v []float64, n int) []float64 {
	out := nans(len(v))
	start := 0
	for start < len(v) && math.IsNaN(v[start]) {
		start++
	}
	if n <= 0 || len(v)-start < n {
		return out
	}
	seed := 0.0
	for _, x := range v[start : start+n] {
		seed += x
	}
	prev := seed / float64(n)
	out[start+n-1] = prev
	k := 2 / float64(n+1)
	for i := start + n; i < len(v); i++ {
		prev = v[i]*k + prev*(1-k)
		out[i] = prev
	}
	return out
}

// RSI is Wilder's relative strength index: the first average gain/loss is
// the mean of the first n changes (value at index n), then Wilder-smoothed.
func RSI(v []float64, n int) []float64 {
	out := nans(len(v))
	if n <= 0 || len(v) <= n {
		return out
	}
	var gain, loss float64
	for i := 1; i <= n; i++ {
		d := v[i] - v[i-1]
		gain += max(d, 0)
		loss += max(-d, 0)
	}
	gain, loss = gain/float64(n), loss/float64(n)
	out[n] = rsi(gain, loss)
	for i := n + 1; i < len(v); i++ {
		d := v[i] - v[i-1]
		gain = (gain*float64(n-1) + max(d, 0)) / float64(n)
		loss = (loss*float64(n-1) + max(-d, 0)) / float64(n)
		out[i] = rsi(gain, loss)
	}
	return out
}

func rsi(gain, loss float64) float64 {
	switch {
	case gain == 0 && loss == 0:
		return 50
	case loss == 0:
		return 100
	}
	return 100 - 100/(1+gain/loss)
}

// ROC is the percent change over n bars.
func ROC(v []float64, n int) []float64 {
	out := nans(len(v))
	if n <= 0 {
		return out
	}
	for i := n; i < len(v); i++ {
		if v[i-n] != 0 {
			out[i] = (v[i]/v[i-n] - 1) * 100
		}
	}
	return out
}

// ATR is Wilder's average true range. True range needs the previous close,
// so it starts at index 1 and the first ATR (mean of n ranges) is at index n.
func ATR(c []Candle, n int) []float64 {
	out := nans(len(c))
	if n <= 0 || len(c) <= n {
		return out
	}
	tr := func(i int) float64 {
		prev := c[i-1].C
		return max(c[i].H-c[i].L, math.Abs(c[i].H-prev), math.Abs(c[i].L-prev))
	}
	atr := 0.0
	for i := 1; i <= n; i++ {
		atr += tr(i)
	}
	atr /= float64(n)
	out[n] = atr
	for i := n + 1; i < len(c); i++ {
		atr = (atr*float64(n-1) + tr(i)) / float64(n)
		out[i] = atr
	}
	return out
}

// Bollinger bands: SMA(n) ± k population standard deviations.
func Bollinger(v []float64, n int, k float64) (upper, mid, lower []float64) {
	mid = SMA(v, n)
	upper, lower = nans(len(v)), nans(len(v))
	for i := range v {
		if math.IsNaN(mid[i]) {
			continue
		}
		ss := 0.0
		for _, x := range v[i-n+1 : i+1] {
			ss += (x - mid[i]) * (x - mid[i])
		}
		sd := math.Sqrt(ss / float64(n))
		upper[i], lower[i] = mid[i]+k*sd, mid[i]-k*sd
	}
	return upper, mid, lower
}

// MACD is EMA(fast) − EMA(slow); the signal line is an EMA of MACD.
func MACD(v []float64, fast, slow, signal int) (macd, sig []float64) {
	f, s := EMA(v, fast), EMA(v, slow)
	macd = nans(len(v))
	for i := range v {
		if !math.IsNaN(f[i]) && !math.IsNaN(s[i]) {
			macd[i] = f[i] - s[i]
		}
	}
	return macd, EMA(macd, signal)
}

// Highest is the max of the previous n values, excluding the current one
// (a close above it is a Donchian breakout).
func Highest(v []float64, n int) []float64 { return window(v, n, math.Max) }

// Lowest is the min of the previous n values, excluding the current one.
func Lowest(v []float64, n int) []float64 { return window(v, n, math.Min) }

// ponytail: O(len·n) scan; a monotonic deque if periods ever reach thousands.
func window(v []float64, n int, pick func(a, b float64) float64) []float64 {
	out := nans(len(v))
	if n <= 0 {
		return out
	}
	for i := n; i < len(v); i++ {
		m := v[i-n]
		for _, x := range v[i-n+1 : i] {
			m = pick(m, x)
		}
		out[i] = m
	}
	return out
}
