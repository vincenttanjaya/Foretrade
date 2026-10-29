package rtquote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Naver is KRX's real-time source: Naver Finance's polling feed v2, the one
// its own quote pages poll (they declare pollingInterval 7000 ms). One GET
// carries every code: /api/realtime/domestic/stock/005930,000660,... on
// polling.finance.naver.com (200 codes, about 51 KB, measured 2026-10-14).
// It needs no key, cookie, Referer or browser User-Agent: a plain Go client
// was served (research h1_naver_pollnew_goua), and so were two reads with
// UserAgent alone on 2026-10-14 at 10:45 UTC (171 of our codes, HTTP 200,
// 1.5 s; KRX closed: the close, testdata/naver_after_close.json).
//
// Fields read, per data row: itemCode (our ticker without .KS),
// closePriceRaw (the KRX last trade; the formatted closePrice when the raw
// one is missing), accumulatedTradingVolumeRaw (KRX's own cumulative volume;
// overMarketPriceInfo is the Nextrade ATS and integratedPriceInfo both
// venues, neither used), currencyType.code, stockExchangeType.code (KS for
// KOSPI) and delayTime (0: real time), tradeStopType.name (TRADING),
// marketStatus with marketSessionType (OPEN and regularMarket while the
// regular session trades; at 19:45 KST on 2026-10-14 every row read OPEN and
// afterMarket, and KRX volumes still moved) and localTradedAt. An answer
// with no row in the regular session is Closed.
//
// There is no trade time: localTradedAt and the top-level time are Naver's
// server clock at answer time (-0.7 to -1.4 s from our request start over
// 35 reads), so they are the answer's Stamp and the market layer dates each
// row by the first read that showed its current volume (TimesNone). Evidence
// that it is real time anyway: its price fell inside Yahoo's 1-minute bar
// for the read's own minute in 90 of 90 reads (20 minutes ahead of Yahoo),
// and its KRX volume rose in every 7.7 s interval of a burst
// (scratchpad/rt/results/kr.md).
//
// Canaries: the three largest of our KRX tickers by traded value in Naver's
// own answer at 13:03 KST on 2026-10-14 (b3_naver_pollnew_200,
// accumulatedTradingValueRaw): SK hynix 2.79 trillion won, Samsung
// Electronics 2.07 trillion, Samsung Electro-Mechanics 0.44 trillion.
//
// Terms: Naver bans automated use without its permission
// (policy.naver.com/rules/service.html); the polling host has no robots.txt.
// The owner accepted that risk on 2026-10-14.
type Naver struct{}

var naverTicker = regexp.MustCompile(`^([0-9A-Z]{6})\.KS$`)

func (Naver) Info() Info {
	return Info{Name: "naver", Venue: "KRX", Switch: "krx", Host: "https://polling.finance.naver.com",
		Class: ClassRealtime, Times: TimesNone, Every: 10 * time.Second, Floor: 5 * time.Second,
		Settle: 2 * time.Minute, MaxLag: 90 * time.Second,
		Canaries: []string{"000660.KS", "005930.KS", "009150.KS"},
		Quiet:    []Window{{"15:20", "15:30"}}} // the closing call: one print at 15:30
}

func (Naver) Symbol(ticker string) (string, bool) {
	m := naverTicker.FindStringSubmatch(ticker)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func (Naver) Request(ctx context.Context, host string, want Want) (*http.Request, error) {
	if len(want) == 0 {
		return nil, fmt.Errorf("naver: nothing to ask for")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		host+"/api/realtime/domestic/stock/"+strings.Join(want.Symbols(), ","), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// naverRow is the part of a data row Naver read.
type naverRow struct {
	ItemCode          string `json:"itemCode"`
	StockExchangeType *struct {
		Code      string `json:"code"`
		DelayTime *int   `json:"delayTime"`
	} `json:"stockExchangeType"`
	ClosePrice     string                 `json:"closePrice"`
	ClosePriceRaw  string                 `json:"closePriceRaw"`
	Volume         string                 `json:"accumulatedTradingVolume"`
	VolumeRaw      string                 `json:"accumulatedTradingVolumeRaw"`
	MarketStatus   string                 `json:"marketStatus"`
	SessionType    string                 `json:"marketSessionType"`
	LocalTradedAt  string                 `json:"localTradedAt"`
	TradeStopType  *struct{ Name string } `json:"tradeStopType"`
	CurrencyType   *struct{ Code string } `json:"currencyType"`
	OverMarketInfo json.RawMessage        `json:"overMarketPriceInfo"` // Nextrade: never read
}

func (Naver) Parse(body []byte, want Want) (Batch, error) {
	var doc struct {
		Datas *[]naverRow `json:"datas"`
		Time  string      `json:"time"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Batch{}, fmt.Errorf("naver: not JSON: %w", err)
	}
	if doc.Datas == nil {
		return Batch{}, fmt.Errorf("naver: no datas")
	}
	var b Batch
	open := 0
	for _, r := range *doc.Datas {
		w, ok := want[r.ItemCode]
		reject := func(why string) {
			b.Rejected = append(b.Rejected, Reject{Ticker: w.Ticker, Symbol: r.ItemCode, Reason: why})
		}
		price, perr := naverNumber(r.ClosePriceRaw, r.ClosePrice)
		volume, verr := naverNumber(r.VolumeRaw, r.Volume)
		switch {
		case !ok:
			reject("not asked for")
		case r.StockExchangeType == nil || r.StockExchangeType.Code != "KS":
			reject("not a KOSPI listing")
		case r.StockExchangeType.DelayTime == nil || *r.StockExchangeType.DelayTime != 0:
			reject("not real time (delayTime)")
		case r.TradeStopType == nil || r.TradeStopType.Name != "TRADING":
			reject("trading stopped")
		case perr != nil:
			reject("price: " + perr.Error())
		case verr != nil:
			reject("volume: " + verr.Error())
		default:
			ccy := w.Currency
			if r.CurrencyType != nil && r.CurrencyType.Code != "" {
				ccy = r.CurrencyType.Code
			}
			b.Rows = append(b.Rows, Row{Ticker: w.Ticker, Price: price, Currency: ccy, Volume: &volume})
			if b.Stamp.IsZero() {
				b.Stamp, _ = time.Parse(time.RFC3339Nano, r.LocalTradedAt)
			}
			if r.MarketStatus == "OPEN" && (r.SessionType == "" || r.SessionType == "regularMarket") {
				open++
			}
		}
	}
	if b.Stamp.IsZero() && doc.Time != "" { // the top-level server clock, KST to the second
		if t, err := time.ParseInLocation("20060102150405", doc.Time, seoul); err == nil {
			b.Stamp = t
		}
	}
	if !b.Stamp.IsZero() {
		b.Stamp = b.Stamp.UTC()
	}
	b.Closed = len(b.Rows) > 0 && open == 0
	return b, nil
}

var seoul = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		return time.FixedZone("KST", 9*3600) // Korea keeps no daylight saving
	}
	return loc
}()

// naverNumber is a raw field ("131000"), else its formatted twin with
// thousands separators ("131,000"): digits only, positive.
func naverNumber(raw, formatted string) (float64, error) {
	s := raw
	if s == "" {
		s = strings.ReplaceAll(formatted, ",", "")
	}
	if s == "" {
		return 0, fmt.Errorf("missing")
	}
	if strings.Trim(s, "0123456789") != "" {
		return 0, fmt.Errorf("%q is not a whole number", s)
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", s, err)
	}
	return n, nil
}
