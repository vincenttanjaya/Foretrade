// Package rtquote holds the real-time quote adapters: for each source, its
// static description (Info), the mapping from our Yahoo tickers to its
// symbols, the builder of the one request a poll sends and the parser of its
// answer. An adapter does no I/O of its own: the market layer
// (internal/market/realtime.go) sends the request with the honest
// User-Agent, guards every row, derives trade times, schedules the polls by
// the venue's session and falls back to Yahoo. Every venue is off by default
// and has its own switch (QUOTES_REALTIME): these sources' terms may forbid
// automated reading or redistribution, so the operator decides.
package rtquote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// UserAgent is the only User-Agent any real-time request sends: it names the
// app. The market layer sets it; an adapter never sets one.
const UserAgent = "Foretrade/1.0 (+https://github.com/vincenttanjaya/Foretrade)"

// Classes: how fresh a source is by design.
const (
	ClassRealtime = "realtime"      // the exchange's trades within seconds
	ClassNear     = "near-realtime" // a snapshot a minute or two behind (SGX)
)

// Times: what a row's time is.
const (
	TimesTrade    = "trade"    // each row carries the source's own last-trade time
	TimesSnapshot = "snapshot" // the answer carries one snapshot time (Batch.Stamp) for every row
	TimesNone     = "none"     // no trade time: the market layer derives it from volume changes
)

// Info is an adapter's static description.
type Info struct {
	Name   string // the API's source name and the /healthz name: "naver"
	Venue  string // the session venue it prices: KRX, HKEX, HOSE, SGX or TWSE
	Switch string // its QUOTES_REALTIME token: krx, hkex, hose, sgx or twse
	Gate   string // "" or an extra switch that must be on too: "twse-yahoo" (QUOTES_REALTIME_TWSE_YAHOO)
	Host   string // scheme and host, "https://polling.finance.naver.com" (QUOTES_REALTIME_BASE replaces it in tests)
	Class  string // ClassRealtime or ClassNear
	Times  string // TimesTrade, TimesSnapshot or TimesNone
	// Every is the poll interval while the session is open; Floor the least
	// time between two requests (fill re-reads included), at least MinFloor.
	Every, Floor time.Duration
	// Settle is how long after the final close the one settled read comes.
	Settle time.Duration
	// MaxLag: the canaries' newest trade older than this while the venue
	// trades makes the source degraded.
	MaxLag time.Duration
	// Canaries are three of our tickers on the venue, its top three by traded
	// value: the health check reads their trade times.
	Canaries []string
	// Quiet are local windows inside the session without continuous trades
	// (a closing call): no health check runs in them.
	Quiet []Window
	// Hours is the venue's weekday sessions, only for a venue with no
	// verified calendar in the market layer (HOSE).
	Hours *Hours
}

// Window is a local time span, "15:20" to "15:30".
type Window struct{ From, To string }

// Hours is a venue's weekday regular sessions in its zone.
type Hours struct {
	Zone     string // "Asia/Ho_Chi_Minh"
	Sessions []Window
}

// Row is one listing's quote in an answer.
type Row struct {
	Ticker   string    // our Yahoo ticker, as Want named it
	Price    float64   // the last trade, local currency
	Currency string    // as the source states it, else the listing's (Want)
	TradeAt  time.Time // TimesTrade: the source's own last-trade time; else zero
	Volume   *float64  // the venue's cumulative session volume, when the source gives it
}

// Reject is a row the parser could not use, and why.
type Reject struct{ Ticker, Symbol, Reason string }

// Batch is one parsed answer.
type Batch struct {
	Rows     []Row
	Rejected []Reject
	Stamp    time.Time // the source's own clock for the answer (server time, snapshot time); zero when none
	Closed   bool      // the source itself says the market is not trading now
}

// Want is what a poll asks for: the source's symbol → our listing.
type Want map[string]Wanted

// Wanted is one asked-for listing: our Yahoo ticker and its currency.
type Wanted struct{ Ticker, Currency string }

// Symbols is want's source symbols, sorted (a stable request).
func (w Want) Symbols() []string {
	out := make([]string, 0, len(w))
	for s := range w {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// Adapter is one real-time source.
type Adapter interface {
	Info() Info
	// Symbol is the source's symbol for our Yahoo ticker; false when the
	// source cannot serve it.
	Symbol(ticker string) (string, bool)
	// Request is a poll's one request for every symbol in want, against host
	// (Info.Host, or the test base). It sets only Accept and Content-Type.
	Request(ctx context.Context, host string, want Want) (*http.Request, error)
	// Parse reads one answer. A row it cannot use goes to Rejected with the
	// reason; an answer it cannot read at all is an error, Refused for a block
	// or challenge page served instead of data.
	Parse(body []byte, want Want) (Batch, error)
}

// Blocker is the optional side of an Adapter that recognises a refusal in a
// non-2xx answer (TWSE's firewall serves its block page with HTTP 502); any
// other non-2xx answer is a failure. 401, 403, 407, 429 and 451 are
// refusals whatever the body.
type Blocker interface {
	Blocked(status int, body []byte) (reason string, ok bool)
}

// MinFloor is the least Floor any adapter may declare.
const MinFloor = 5 * time.Second

// ErrRefused marks an answer that refused our request (a block or challenge
// page): the source is not used for a while, and the refusal is recorded.
var ErrRefused = errors.New("refused")

// Refused is an ErrRefused with its reason.
func Refused(reason string) error { return fmt.Errorf("%w: %s", ErrRefused, reason) }

// Venues are the venues with a real-time source, in the order /healthz lists
// them, by QUOTES_REALTIME token.
var Venues = []struct{ Switch, Venue string }{{"krx", "KRX"}, {"hkex", "HKEX"}, {"hose", "HOSE"}, {"sgx", "SGX"}, {"twse", "TWSE"}}

// DefaultSwitches is QUOTES_REALTIME when it is unset.
const DefaultSwitches = "krx,hkex,hose,sgx,twse"

// GateTWSEYahoo is the Gate of TWSE's second source (QUOTES_REALTIME_TWSE_YAHOO).
const GateTWSEYahoo = "twse-yahoo"

// Enabled is All's adapters whose venue switch is in venues (lower-case
// tokens) and whose Gate, if any, is on: the chains the market layer polls.
func Enabled(venues []string, twseYahoo bool) []Adapter {
	var out []Adapter
	for _, a := range All {
		in := a.Info()
		if !slices.Contains(venues, in.Switch) || in.Gate == GateTWSEYahoo && !twseYahoo {
			continue
		}
		out = append(out, a)
	}
	return out
}

// ParseSwitches reads QUOTES_REALTIME: the known venue tokens it names (lower
// case, deduplicated, in Venues order) and the unknown ones.
func ParseSwitches(s string) (venues, unknown []string) {
	for tok := range strings.SplitSeq(s, ",") {
		tok = strings.ToLower(strings.TrimSpace(tok))
		switch {
		case tok == "":
		case slices.ContainsFunc(Venues, func(v struct{ Switch, Venue string }) bool { return v.Switch == tok }):
			if !slices.Contains(venues, tok) {
				venues = append(venues, tok)
			}
		default:
			unknown = append(unknown, tok)
		}
	}
	slices.SortFunc(venues, func(a, b string) int { return switchIndex(a) - switchIndex(b) })
	return venues, unknown
}

func switchIndex(tok string) int {
	return slices.IndexFunc(Venues, func(v struct{ Switch, Venue string }) bool { return v.Switch == tok })
}
