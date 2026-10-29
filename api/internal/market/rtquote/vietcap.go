package rtquote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Vietcap is HOSE's real-time source: the price board of Vietcap Securities
// (VCI), the feed its own web board reads. One POST carries every symbol:
// /api/price/symbols/getList on trading.vietcap.com.vn with the body
// {"symbols":["ACB","BID",...]} (all 400 HOSE symbols came back in one
// answer of 985 KB, research cov_vci_all400; our 30 are 76 KB of JSON, served
// gzipped). It needs no key, cookie, Referer or crumb: the research sent
// Content-Type alone (19 reads, no refusal). robots.txt on the host reads
// "User-agent: *", "Allow: /", "Crawl-delay: 10" (research
// robots_trading.vietcap.com.vn), so Every and Floor are both
// vietcapCrawlDelay: the market layer never sends two requests, fill
// re-reads included, closer than 10 s.
//
// Our reads, through the market layer's own request (UserAgent, Accept and
// Content-Type, nothing else), on 2026-10-14 with HOSE closed: 12:13:34,
// 12:14:22 and 12:15:09 UTC, HTTP 200 in 2.0-2.4 s each. They asked the feed
// rows and canaries (26), then all 30 of our HOSE tickers with the screener's
// watch rows (TCX.VN included: testdata/vietcap_live_after_close.json), then
// the 26 and a symbol that does not exist, which comes back as a placeholder
// row with no listing and an empty symbol (testdata/vietcap_live_unknown.json).
// Every row read ENDED with the closing auction's 07:45:00.023 UTC, so each
// answer was Closed. The answers keep the order asked, but rows are matched by
// symbol here.
//
// Fields read, per row: listingInfo.symbol, .board (HSX is HOSE) and
// .tradingStatus; matchPrice.symbol, .matchPrice (the last match in VND, the
// unit of Yahoo's .VN quotes: VIC 218,400 on both after the 2026-10-14
// close), .accumulatedVolume (shares matched today), .time (ISO 8601 with
// milliseconds), .session (LO_MORNING, LO_AFTERNOON and ENDED on ours; PCA and
// EXTEND_HOUR on others') and .floorPrice/.ceilingPrice (the day's band,
// ±7%). No currency is stated: the listing's (VND).
//
// Times are TimesNone although each row has a time, because matchPrice.time
// is the time of the symbol's newest board message and not every message is
// a trade. In the full-board read at 06:02:15 UTC on 2026-10-14, 76 of 400
// symbols had matched nothing that day (price and volume 0) yet carried
// times: 25 at 06:00:00.6-06:00:05.9 (the afternoon session's start), 2 at
// 02:15:00.024 (the opening auction's match), 4 at 00:30:11 (the board's
// 07:30 local reset), the rest at scattered moments; 83 symbols that had
// traded showed 06:00:00.4-06:00:02.9 as well. For a symbol that trades it
// was the last trade's time in every check (VIC in research rounds r1-r5
// against Vietcap's own tick tape, LEData: the same second and the same
// cumulative volume, 5 of 5), and all 29 of ours in that read were -1.4 to
// 61.5 s old. Since a message without a trade can still move it, the market
// layer dates each row by the first read that showed its current cumulative
// volume (the volume clock): a price that stops trading never looks fresh,
// and the closing auction, which matches nothing until 14:45, ages honestly.
// The answer's Stamp is the newest message time among the rows accepted, the
// board's own clock (HOSE transaction times, -2 to +4 s from ours over 40
// research reads), so a board that falls behind shows how far behind instead
// of our read time.
//
// HOSE has no verified calendar in the market layer, so Hours gives its
// weekday sessions in Asia/Ho_Chi_Minh: 09:15-11:30 and 13:00-14:45, from the
// opening auction's match (ATO 09:00-09:15, one match at 09:15:00) to the
// closing auction's (ATC 14:30-14:45, the Quiet window, one match at
// 14:45:00); the lunch break is not polled. The settled read comes 5 minutes
// after 14:45: at 08:18 UTC every row's session was ENDED and its price the
// closing auction's, stamped 07:45:00.023 (testdata/vietcap_after_close.json).
// An answer whose asked rows all read ENDED is Closed, so a holiday (no
// calendar) is polled every 5 minutes only; no holiday answer was read.
//
// A row is rejected when it was not asked for, its two symbols disagree, it
// is not on HSX, its trading is suspended or halted, it matched nothing today
// (price or volume missing or 0: such rows still carry message times), its
// price is outside the day's band, or its time is unreadable; an asked symbol
// with no row (the board does not know it) is listed as rejected too.
//
// Canaries: the three largest of our HOSE tickers by traded value over the
// whole of 2026-10-14 (accumulatedValue in million VND, our 12:14:22 UTC read
// after the close): Vinhomes 708,011, Vingroup 683,681, Techcombank 626,114
// (FPT 402,875 next). Health needs only the newest of the three to be fresh,
// so two of one group going quiet together does not degrade the source. At
// 06:02:15 UTC the order had been TCB, FPT, HDB.
//
// Not measured: the closing-auction minutes (whether the board shows an
// indicative auction price then: under the volume clock a price that moves
// without volume keeps the earlier trade time), the 09:00-09:15 opening
// auction, and a pre-open or holiday answer (if the 09:15:00 read still shows
// the last session's ENDED rows, the venue waits 5 minutes).
//
// Terms: apart from downloading and printing for personal use, users may not
// copy, distribute, trade or publish the site's content
// (www.vietcap.com.vn/dieu-khoan-su-dung-chinh-sach-bao-mat); robots.txt
// allows crawling at the delay above. The owner accepted that risk on
// 2026-10-14.
type Vietcap struct{}

// vietcapCrawlDelay is the Crawl-delay of trading.vietcap.com.vn/robots.txt:
// the least spacing of any two requests.
const vietcapCrawlDelay = 10 * time.Second

// HOSE share codes are three characters, a letter first (VCB, HT1, C32).
var vietcapTicker = regexp.MustCompile(`^([A-Z][A-Z0-9]{2})\.VN$`)

func (Vietcap) Info() Info {
	return Info{Name: "vietcap", Venue: "HOSE", Switch: "hose", Host: "https://trading.vietcap.com.vn",
		Class: ClassRealtime, Times: TimesNone, Every: vietcapCrawlDelay, Floor: vietcapCrawlDelay,
		Settle: 5 * time.Minute, MaxLag: 90 * time.Second,
		Canaries: []string{"VHM.VN", "VIC.VN", "TCB.VN"},
		Quiet:    []Window{{"14:30", "14:45"}}, // the closing auction: one match at 14:45
		Hours:    &Hours{Zone: "Asia/Ho_Chi_Minh", Sessions: []Window{{"09:15", "11:30"}, {"13:00", "14:45"}}}}
}

func (Vietcap) Symbol(ticker string) (string, bool) {
	m := vietcapTicker.FindStringSubmatch(ticker)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func (Vietcap) Request(ctx context.Context, host string, want Want) (*http.Request, error) {
	if len(want) == 0 {
		return nil, fmt.Errorf("vietcap: nothing to ask for")
	}
	body, err := json.Marshal(struct {
		Symbols []string `json:"symbols"`
	}{want.Symbols()})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/price/symbols/getList", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// vietcapRow is the part of a getList row Vietcap reads.
type vietcapRow struct {
	ListingInfo *struct {
		Symbol        string `json:"symbol"`
		Board         string `json:"board"`
		TradingStatus string `json:"tradingStatus"`
	} `json:"listingInfo"`
	MatchPrice *struct {
		Symbol            string          `json:"symbol"`
		MatchPrice        *float64        `json:"matchPrice"`
		AccumulatedVolume *float64        `json:"accumulatedVolume"`
		Time              json.RawMessage `json:"time"`
		Session           string          `json:"session"`
		CeilingPrice      float64         `json:"ceilingPrice"`
		FloorPrice        float64         `json:"floorPrice"`
	} `json:"matchPrice"`
}

func (Vietcap) Parse(body []byte, want Want) (Batch, error) {
	if page := bytes.TrimSpace(body); len(page) > 0 && page[0] == '<' {
		if why, ok := vietcapBlock(page); ok {
			return Batch{}, Refused(why)
		}
		return Batch{}, fmt.Errorf("vietcap: an HTML page, not data")
	}
	var rows []vietcapRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return Batch{}, fmt.Errorf("vietcap: not a JSON list: %w", err)
	}
	if len(rows) == 0 {
		return Batch{}, fmt.Errorf("vietcap: no rows")
	}
	var b Batch
	answered := map[string]bool{}
	asked, ended := 0, 0
	for _, r := range rows {
		mp, li := r.MatchPrice, r.ListingInfo
		sym := ""
		switch {
		case mp != nil && mp.Symbol != "":
			sym = mp.Symbol
		case li != nil:
			sym = li.Symbol
		}
		if sym == "" {
			continue // the placeholder for a symbol the board does not know: listed below
		}
		answered[sym] = true
		w, ok := want[sym]
		reject := func(why string) {
			b.Rejected = append(b.Rejected, Reject{Ticker: w.Ticker, Symbol: sym, Reason: why})
		}
		if !ok {
			reject("not asked for")
			continue
		}
		asked++
		if mp != nil && strings.EqualFold(mp.Session, "ENDED") {
			ended++
		}
		var at time.Time
		var terr error
		if mp != nil {
			at, terr = vietcapTime(mp.Time)
		}
		switch {
		case mp == nil:
			reject("no match data")
		case li == nil:
			reject("no listing data")
		case li.Symbol != sym:
			reject(fmt.Sprintf("symbols disagree: listing %q, match %q", li.Symbol, sym))
		case li.Board != "HSX":
			reject(fmt.Sprintf("not a HOSE listing (board %q)", li.Board))
		case vietcapStopped(li.TradingStatus):
			reject("trading suspended (" + li.TradingStatus + ")")
		case !vietcapPositive(mp.MatchPrice) || !vietcapPositive(mp.AccumulatedVolume):
			reject("no trade today (price or volume 0)")
		case mp.FloorPrice > 0 && mp.CeilingPrice > 0 && (*mp.MatchPrice < mp.FloorPrice || *mp.MatchPrice > mp.CeilingPrice):
			reject(fmt.Sprintf("price %g outside the day's band %g-%g", *mp.MatchPrice, mp.FloorPrice, mp.CeilingPrice))
		case terr != nil:
			reject(terr.Error())
		default:
			volume := *mp.AccumulatedVolume
			b.Rows = append(b.Rows, Row{Ticker: w.Ticker, Price: *mp.MatchPrice, Volume: &volume})
			if at.After(b.Stamp) {
				b.Stamp = at
			}
		}
	}
	for _, sym := range want.Symbols() {
		if !answered[sym] {
			b.Rejected = append(b.Rejected, Reject{Ticker: want[sym].Ticker, Symbol: sym,
				Reason: "no row in the answer (the board does not know it)"})
		}
	}
	b.Closed = asked > 0 && ended == asked
	return b, nil
}

// vietcapTime is a row's message time: ISO 8601 as served
// ("2026-10-14T06:01:33.809Z"), or epoch milliseconds.
func vietcapTime(raw json.RawMessage) (time.Time, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("time %q unreadable", s)
		}
		return t.UTC(), nil
	}
	var ms float64
	if err := json.Unmarshal(raw, &ms); err == nil && ms > 1e12 && ms < 1e14 {
		return time.UnixMilli(int64(ms)).UTC(), nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, fmt.Errorf("time missing")
	}
	return time.Time{}, fmt.Errorf("time %s unreadable", raw)
}

func vietcapPositive(p *float64) bool { return p != nil && *p > 0 && !math.IsInf(*p, 0) }

// vietcapStopped reports a trading status that stops matching
// (TRADING_SUSPENSION was seen; the ACTIVATED ones trade, warnings included).
func vietcapStopped(status string) bool {
	s := strings.ToUpper(status)
	return strings.Contains(s, "SUSPEN") || strings.Contains(s, "HALT")
}

// vietcapBlock recognises a block or challenge page served instead of data.
func vietcapBlock(page []byte) (string, bool) {
	p := strings.ToLower(string(page))
	for _, mark := range []string{"captcha", "just a moment", "challenge", "access denied", "attention required",
		"request rejected", "blocked", "incapsula", "forbidden"} {
		if strings.Contains(p, mark) {
			return "a block or challenge page instead of data (" + mark + ")", true
		}
	}
	return "", false
}
