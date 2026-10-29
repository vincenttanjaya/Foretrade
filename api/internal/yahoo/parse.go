package yahoo

import (
	"cmp"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"time"
)

// The v8 chart and v7 spark documents, as far as this client reads them.
// Numbers are *float64 so a null (or an integer sent as a float) parses.
type chartDoc struct {
	Chart struct {
		Result []chartResult `json:"result"`
		Error  *apiError     `json:"error"`
	} `json:"chart"`
}

type sparkDoc struct {
	Spark *struct {
		Result []struct {
			Symbol   string        `json:"symbol"`
			Response []chartResult `json:"response"`
		} `json:"result"`
		Error *apiError `json:"error"`
	} `json:"spark"`
}

type apiError struct {
	Code string `json:"code"` // "Not Found", "Bad Request"; its description is never surfaced
}

type chartResult struct {
	Meta       rawMeta    `json:"meta"`
	Timestamp  []*float64 `json:"timestamp"`
	Indicators struct {
		Quote []struct {
			Open   []*float64 `json:"open"`
			High   []*float64 `json:"high"`
			Low    []*float64 `json:"low"`
			Close  []*float64 `json:"close"`
			Volume []*float64 `json:"volume"`
		} `json:"quote"`
		Adjclose []struct {
			Adjclose []*float64 `json:"adjclose"`
		} `json:"adjclose"`
	} `json:"indicators"`
}

type rawMeta struct {
	Symbol               string   `json:"symbol"`
	Currency             string   `json:"currency"`
	ExchangeName         string   `json:"exchangeName"`
	FullExchangeName     string   `json:"fullExchangeName"`
	InstrumentType       string   `json:"instrumentType"`
	ExchangeTimezoneName string   `json:"exchangeTimezoneName"`
	LongName             string   `json:"longName"`
	ShortName            string   `json:"shortName"`
	RegularMarketPrice   *float64 `json:"regularMarketPrice"`
	RegularMarketTime    *float64 `json:"regularMarketTime"`
	PreviousClose        *float64 `json:"previousClose"`
	FiftyTwoWeekHigh     *float64 `json:"fiftyTwoWeekHigh"`
	FiftyTwoWeekLow      *float64 `json:"fiftyTwoWeekLow"`
	ChartPreviousClose   *float64 `json:"chartPreviousClose"`
	RegularMarketDayHigh *float64 `json:"regularMarketDayHigh"`
	RegularMarketDayLow  *float64 `json:"regularMarketDayLow"`
	RegularMarketVolume  *float64 `json:"regularMarketVolume"`
	FirstTradeDate       *float64 `json:"firstTradeDate"`
	CurrentTradingPeriod *struct {
		Regular *struct {
			Start *float64 `json:"start"`
			End   *float64 `json:"end"`
		} `json:"regular"`
	} `json:"currentTradingPeriod"`
}

func parseChart(sym, rng, interval string, status int, body []byte) (Chart, error) {
	var d chartDoc
	if err := json.Unmarshal(body, &d); err != nil {
		if status == http.StatusNotFound {
			return Chart{}, notFound(sym)
		}
		return Chart{}, unavailable(sym, "unreadable Yahoo answer (HTTP %d)", status)
	}
	if e := d.Chart.Error; status == http.StatusNotFound || e != nil && e.Code == "Not Found" {
		return Chart{}, notFound(sym)
	}
	switch {
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return Chart{}, unavailable(sym, "Yahoo rejected range %s / interval %s", rng, interval)
	case status != http.StatusOK:
		return Chart{}, unavailable(sym, "Yahoo answered HTTP %d", status)
	case d.Chart.Error != nil || len(d.Chart.Result) == 0:
		return Chart{}, unavailable(sym, "Yahoo sent no chart")
	}
	r := d.Chart.Result[0]
	m := r.Meta.meta(sym)
	return Chart{Meta: m, Range: rng, Interval: interval, Bars: r.bars(m.Location(), interval, m.InstrumentType == "EQUITY")}, nil
}

// parseSpark reads a v7 spark document; false when body is not one.
func parseSpark(body []byte) (sparkDoc, bool) {
	var d sparkDoc
	if json.Unmarshal(body, &d) != nil || d.Spark == nil {
		return d, false
	}
	return d, true
}

func (r rawMeta) meta(sym string) Meta {
	m := Meta{Symbol: cmp.Or(r.Symbol, sym), Currency: r.Currency, ExchangeName: r.ExchangeName,
		FullExchangeName: r.FullExchangeName, InstrumentType: r.InstrumentType, Timezone: r.ExchangeTimezoneName,
		LongName: r.LongName, ShortName: r.ShortName,
		Price: positive(r.RegularMarketPrice), PriceTime: unix(r.RegularMarketTime), PreviousClose: positive(r.PreviousClose),
		FiftyTwoWeekHigh: positive(r.FiftyTwoWeekHigh), FiftyTwoWeekLow: positive(r.FiftyTwoWeekLow),
		ChartPreviousClose: positive(r.ChartPreviousClose), DayHigh: positive(r.RegularMarketDayHigh),
		DayLow: positive(r.RegularMarketDayLow), DayVolume: positive(r.RegularMarketVolume),
		FirstTradeDate: unix(r.FirstTradeDate)}
	if p := r.CurrentTradingPeriod; p != nil && p.Regular != nil {
		m.RegularStart, m.RegularEnd = unix(p.Regular.Start), unix(p.Regular.End)
	}
	return m
}

// bars zips the series, skipping a bar with a null, non-finite or non-positive
// O/H/L/C. Timestamps end up strictly ascending (a repeat keeps the later
// bar), and daily bars one per exchange-local date: Yahoo can append the live
// session as a second bar of the same day. A stock's daily bar with no volume
// and O = H = L = C at the previous kept close is a holiday filler (SET's
// PTT.BK on 2026-07-28 and 07-29, Bursa's 1155.KL on 08-31): dropped, so it
// adds no flat candle and no 0% day to volatility or RSI. Indexes, FX and
// weekly bars keep theirs.
func (r chartResult) bars(loc *time.Location, interval string, equity bool) []Bar {
	if len(r.Indicators.Quote) == 0 {
		return nil
	}
	q := r.Indicators.Quote[0]
	var adj []*float64
	if len(r.Indicators.Adjclose) > 0 {
		adj = r.Indicators.Adjclose[0].Adjclose
	}
	out := make([]Bar, 0, len(r.Timestamp))
	for i, ts := range r.Timestamp {
		t := unix(ts)
		o, h, l, c := at(q.Open, i), at(q.High, i), at(q.Low, i), at(q.Close, i)
		if t == nil || !(o > 0 && h > 0 && l > 0 && c > 0) {
			continue
		}
		out = append(out, Bar{T: *t, O: o, H: h, L: l, C: c, V: max(orZero(at(q.Volume, i)), 0), Adj: max(orZero(at(adj, i)), 0)})
	}
	slices.SortStableFunc(out, func(a, b Bar) int { return a.T.Compare(b.T) })
	same := func(a, b Bar) bool { return a.T.Equal(b.T) }
	if interval == "1d" {
		same = func(a, b Bar) bool {
			ay, am, ad := a.T.In(loc).Date()
			by, bm, bd := b.T.In(loc).Date()
			return ay == by && am == bm && ad == bd
		}
	}
	kept := out[:0]
	for _, b := range out {
		if n := len(kept); n > 0 && same(kept[n-1], b) {
			kept[n-1] = b
			continue
		}
		kept = append(kept, b)
	}
	if interval != "1d" || !equity {
		return kept
	}
	traded := kept[:0]
	for _, b := range kept {
		if n := len(traded); n > 0 && b.V == 0 && b.O == b.C && b.H == b.C && b.L == b.C && b.C == traded[n-1].C {
			continue
		}
		traded = append(traded, b)
	}
	return traded
}

// at is v[i], NaN when missing, null or non-finite.
func at(v []*float64, i int) float64 {
	if i >= len(v) || v[i] == nil || math.IsInf(*v[i], 0) {
		return math.NaN()
	}
	return *v[i]
}

func orZero(x float64) float64 {
	if math.IsNaN(x) {
		return 0
	}
	return x
}

func positive(p *float64) *float64 {
	if p == nil || !(*p > 0) || math.IsInf(*p, 0) {
		return nil
	}
	v := *p
	return &v
}

func unix(p *float64) *time.Time {
	if p == nil || *p == 0 || math.IsNaN(*p) || math.IsInf(*p, 0) {
		return nil
	}
	t := time.Unix(int64(*p), 0).UTC()
	return &t
}
