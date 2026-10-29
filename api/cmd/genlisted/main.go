// Command genlisted writes internal/market/listed.json, the listed
// instruments: every Asian screener constituent (internal/screener's embedded
// constituents.json) that no named instrument trades already and that passes
// the rule every quote feed passed, as a paper-tradable Yahoo quote feed under
// its own Yahoo ticker. The market package embeds the file (universe_listed.go).
//
//	go run ./cmd/genlisted build [-measure file] [-min-traded-usd 1000000] [-min-history-days 365] [-drop SYMBOL] [-on YYYY-MM-DD]
//	go run ./cmd/genlisted measure -out file [-tickers new|all]
//
// build is pure (no network): it reads the constituents, the compiled named
// market.Universe, listed.json's own measurements and, over them, any
// -measure file, then rewrites listed.json. Per constituent, in list order:
// a ticker a named instrument lists, or whose US ADR is one, is skipped; a
// ticker Yahoo answered 404 for, one Yahoo serves as something other than
// an equity in the list's currency, one trading under -min-traded-usd a day
// (30-day mean), one first traded within -min-history-days, one whose last
// trade is over 4 days older than its read, or one with fewer than 10 daily
// bars with a close in the month is excluded with that reason; the rest are
// rows. The file is append-only: a row stays (positions, alerts and
// autopilots may hold it) when its ticker leaves the constituents, with index
// "" and the date it left (-on, default today), and when a new measurement
// would exclude it; -drop removes by hand a row whose ticker left the
// constituents.
//
// measure is the only networked step and is run by hand, never in tests or
// CI: one keyless chart read (range=1mo, interval=1d) per ticker through the
// shared, rate-gated Yahoo client, paced at one request a second, plus one FX
// rate per currency. It writes a measurement file for build -measure. A read
// that fails for another reason than a 404 is reported and left out, so build
// keeps that ticker's earlier measurement.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/screener"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

const (
	listedPath       = "internal/market/listed.json"
	constituentsPath = "internal/screener/constituents.json"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "genlisted:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: genlisted build|measure [flags]")
	}
	fs := flag.NewFlagSet("genlisted "+args[0], flag.ContinueOnError)
	switch args[0] {
	case "build":
		out := fs.String("out", listedPath, "listed file to rewrite (its rows and measurements are the starting point)")
		measured := fs.String("measure", "", "a measurement file from genlisted measure, over the listed file's own")
		opts := defaultOptions()
		fs.Int64Var(&opts.minTradedUSD, "min-traded-usd", opts.minTradedUSD, "30-day mean daily traded value floor, USD")
		fs.IntVar(&opts.minHistoryDays, "min-history-days", opts.minHistoryDays, "days since the first trade, at least")
		fs.StringVar(&opts.on, "on", time.Now().UTC().Format(time.DateOnly), "the date a row whose ticker left the constituents is marked with")
		fs.Func("drop", "remove this row by hand (repeatable)", func(s string) error {
			opts.drop = append(opts.drop, strings.ToUpper(strings.TrimSpace(s)))
			return nil
		})
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		prev, err := readListed(*out)
		if err != nil {
			return err
		}
		ms := prev.measurements()
		if *measured != "" {
			mf, err := readMeasurements(*measured)
			if err != nil {
				return err
			}
			for _, m := range mf.Measurements {
				ms[m.Ticker] = m
			}
		}
		f, rep, err := build(screener.Stocks(), market.Universe, screener.ADRFor, ms, prev, opts)
		if err != nil {
			return err
		}
		raw, err := f.encode()
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			return err
		}
		rep.print(stdout, f)
		return nil
	case "measure":
		out := fs.String("out", "", "measurement file to write")
		which := fs.String("tickers", "new", "new: constituents listed.json has no measurement for; all: every one not traded already")
		prevPath := fs.String("listed", listedPath, "the listed file (for -tickers new)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *out == "" {
			return errors.New("measure: -out is required")
		}
		prev, err := readListed(*prevPath)
		if err != nil {
			return err
		}
		tickers := candidates(screener.Stocks(), market.Universe, screener.ADRFor)
		if *which == "new" {
			known := prev.measurements()
			tickers = slices.DeleteFunc(tickers, func(s screener.Stock) bool { _, ok := known[s.Ticker]; return ok })
		} else if *which != "all" {
			return fmt.Errorf("measure: -tickers is new or all, not %q", *which)
		}
		mf := measure(ctx, yahoo.New(yahoo.Config{}), tickers, time.Second, time.Now, os.Stderr)
		raw, err := json.MarshalIndent(mf, "", " ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "measured %d of %d tickers into %s\n", len(mf.Measurements), len(tickers), *out)
		return nil
	}
	return fmt.Errorf("unknown command %q: build or measure", args[0])
}

// Measurement is one ticker's Yahoo read: a chart (range=1mo, interval=1d),
// its meta and the mean daily traded value in USD (close × volume over the
// month's bars, at the latest FX rate).
type Measurement struct {
	Ticker             string `json:"ticker"`
	Market             string `json:"market"`
	MeasuredAt         string `json:"measuredAt"` // RFC 3339, UTC
	Status             string `json:"status"`     // ok | not_found
	HTTP               int    `json:"http,omitempty"`
	Error              string `json:"error,omitempty"`
	InstrumentType     string `json:"instrumentType,omitempty"`
	Currency           string `json:"currency,omitempty"`
	FirstTradeDate     string `json:"firstTradeDate,omitempty"`    // YYYY-MM-DD; Yahoo floors it near 2000
	LongName           string `json:"longName,omitempty"`          // longName, else shortName
	RegularMarketTime  string `json:"regularMarketTime,omitempty"` // the freshest read's last trade, RFC 3339
	DailyBars          int    `json:"dailyBars,omitempty"`
	DailyBarsWithClose int    `json:"dailyBarsWithClose,omitempty"`
	TradedValueUSD30d  int64  `json:"tradedValueUsd30d,omitempty"`
	Flags              string `json:"flags,omitempty"` // a measurer's note; the rule ignores it
}

// MeasurementFile is what measure writes and build -measure reads.
type MeasurementFile struct {
	Source       string        `json:"source"`
	Measurements []Measurement `json:"measurements"`
}

// File is listed.json.
type File struct {
	GeneratedFrom string     `json:"generatedFrom"`
	MeasuredOn    string     `json:"measuredOn"` // the newest measurement's date
	Rule          Rule       `json:"rule"`
	Rows          []Row      `json:"rows"`
	Excluded      []Excluded `json:"excluded"`
}

// Rule is the admission rule the file was built with.
type Rule struct {
	InstrumentType       string `json:"instrumentType"`
	MinHistoryDays       int    `json:"minHistoryDays"`
	MinTradedValueUSD30d int64  `json:"minTradedValueUsd30d"`
	MaxQuoteAgeDays      int    `json:"maxQuoteAgeDays"`
	MinDailyBars         int    `json:"minDailyBars"`
}

// Row is one listed instrument: Symbol is its upper-case Yahoo ticker.
type Row struct {
	Symbol     string      `json:"symbol"`
	Name       string      `json:"name"`     // Yahoo's long name, else the list's
	ListName   string      `json:"listName"` // as the constituent list names it
	Market     string      `json:"market"`
	Code       string      `json:"code"`
	Index      string      `json:"index"` // "" once the ticker left every list
	Venue      string      `json:"venue"`
	Currency   string      `json:"currency"`
	Underlying string      `json:"underlying"`
	Left       string      `json:"left,omitempty"` // YYYY-MM-DD the ticker left the constituents
	Measured   Measurement `json:"measured"`
}

// Excluded is a constituent left research-only, with why.
type Excluded struct {
	Ticker   string       `json:"ticker"`
	Market   string       `json:"market"`
	ListName string       `json:"listName"`
	Index    string       `json:"index"`
	Reason   string       `json:"reason"`
	Measured *Measurement `json:"measured,omitempty"` // nil when never measured
}

type options struct {
	minTradedUSD   int64
	minHistoryDays int
	maxQuoteAge    int // days
	minDailyBars   int
	drop           []string
	on             string // YYYY-MM-DD
}

func defaultOptions() options {
	return options{minTradedUSD: 1_000_000, minHistoryDays: 365, maxQuoteAge: 4, minDailyBars: 10}
}

// venues is each screener market's session venue: the market's name in the
// underlying line, and the currencies its rows may quote in.
var venues = map[string]struct {
	venue, label string
	ccys         []string
}{
	"JP": {market.TSE, "TSE", []string{"JPY"}},
	"KR": {market.KRX, "KRX", []string{"KRW"}},
	"TW": {market.TWSE, "TWSE", []string{"TWD"}},
	"HK": {market.HKEX, "HKEX", []string{"HKD"}},
	"SG": {market.SGX, "SGX", []string{"SGD", "USD"}},
	"ID": {market.IDX, "IDX", []string{"IDR"}},
	"TH": {market.SET, "SET", []string{"THB"}},
	"MY": {market.BURSA, "Bursa Malaysia", []string{"MYR"}},
	"VN": {market.HOSE, "HOSE", []string{"VND"}},
}

// skip says how a named instrument already trades ticker: "listing" when one
// lists it, "adr" when one lists its US ADR; "" when none does.
func skip(ticker string, named []market.AssetInfo, adrFor func(string) string) string {
	has := func(t string) bool {
		return t != "" && slices.ContainsFunc(named, func(a market.AssetInfo) bool {
			return a.Listing != nil && strings.EqualFold(a.Listing.Yahoo, t)
		})
	}
	switch {
	case has(ticker):
		return "listing"
	case has(adrFor(ticker)):
		return "adr"
	}
	return ""
}

// candidates are the constituents no named instrument trades already.
func candidates(stocks []screener.Stock, named []market.AssetInfo, adrFor func(string) string) []screener.Stock {
	return slices.DeleteFunc(slices.Clone(stocks), func(s screener.Stock) bool { return skip(s.Ticker, named, adrFor) != "" })
}

// report is what build prints: the skips and the rows kept by append-only.
type report struct {
	skipped map[string]int // by "listing" | "adr"
	kept    []string       // rows a new measurement would exclude, kept (append-only)
	left    []string       // rows whose ticker left the constituents this build
	named   []string       // rows removed because a named instrument now trades the ticker
}

func (r report) print(w io.Writer, f File) {
	byMarket := map[string]int{}
	for _, row := range f.Rows {
		byMarket[row.Market]++
	}
	fmt.Fprintf(w, "%d rows %v; %d excluded; skipped %d (listing) + %d (adr)\n", len(f.Rows), byMarket, len(f.Excluded),
		r.skipped["listing"], r.skipped["adr"])
	for _, s := range r.kept {
		fmt.Fprintln(w, "kept (append-only):", s)
	}
	for _, s := range r.left {
		fmt.Fprintln(w, "left the constituents:", s)
	}
	for _, s := range r.named {
		fmt.Fprintln(w, "removed, a named instrument trades it now (move its positions first):", s)
	}
	for _, x := range f.Excluded {
		fmt.Fprintf(w, "research-only %s (%s): %s\n", x.Ticker, x.Market, x.Reason)
	}
}

// build is the listed file from the constituents (in list order), the named
// universe, the ADR map and the measurements by ticker, over prev (its rows
// stay, in their order; new rows follow in list order).
func build(stocks []screener.Stock, named []market.AssetInfo, adrFor func(string) string, ms map[string]Measurement, prev File, opts options) (File, report, error) {
	for _, d := range opts.drop {
		if slices.ContainsFunc(stocks, func(s screener.Stock) bool { return s.Ticker == d }) {
			return File{}, report{}, fmt.Errorf("-drop %s: still a constituent, so the build would list it again", d)
		}
	}
	f := File{GeneratedFrom: constituentsPath, Rule: Rule{InstrumentType: "EQUITY", MinHistoryDays: opts.minHistoryDays,
		MinTradedValueUSD30d: opts.minTradedUSD, MaxQuoteAgeDays: opts.maxQuoteAge, MinDailyBars: opts.minDailyBars},
		Rows: []Row{}, Excluded: []Excluded{}}
	rep := report{skipped: map[string]int{}}
	current := map[string]screener.Stock{}
	var fresh []Row
	for _, s := range stocks {
		if _, dup := current[s.Ticker]; dup {
			continue
		}
		current[s.Ticker] = s
		if via := skip(s.Ticker, named, adrFor); via != "" {
			rep.skipped[via]++
			continue
		}
		m, measured := ms[s.Ticker]
		reason := exclusion(s, m, measured, opts)
		if reason == "" {
			fresh = append(fresh, newRow(s, m))
			continue
		}
		if i := slices.IndexFunc(prev.Rows, func(r Row) bool { return r.Symbol == s.Ticker }); i >= 0 {
			// Append-only: a listed row stays. A usable read updates it; any
			// other keeps its last one.
			rep.kept = append(rep.kept, s.Ticker+": "+reason)
			if m.Status == "ok" && m.Currency == prev.Rows[i].Currency {
				fresh = append(fresh, newRow(s, m))
			} else {
				fresh = append(fresh, prev.Rows[i])
			}
			continue
		}
		x := Excluded{Ticker: s.Ticker, Market: s.Market, ListName: s.Name, Index: index(s), Reason: reason}
		if measured {
			x.Measured = &m
		}
		f.Excluded = append(f.Excluded, x)
	}
	// Earlier rows keep their place: updated while their ticker is listed, kept
	// with its last measurement and index "" once it is not.
	for _, r := range prev.Rows {
		if slices.Contains(opts.drop, r.Symbol) {
			continue
		}
		if i := slices.IndexFunc(fresh, func(n Row) bool { return n.Symbol == r.Symbol }); i >= 0 {
			f.Rows = append(f.Rows, fresh[i])
			fresh = slices.Delete(fresh, i, i+1)
			continue
		}
		if _, still := current[r.Symbol]; still {
			rep.named = append(rep.named, r.Symbol) // a named instrument trades it now
			continue
		}
		if r.Left == "" {
			r.Index, r.Left = "", opts.on
			r.Underlying = underlying(r.Market, r.Symbol, "", r.Currency)
			rep.left = append(rep.left, r.Symbol)
		}
		f.Rows = append(f.Rows, r)
	}
	f.Rows = append(f.Rows, fresh...)
	for _, r := range f.Rows {
		f.MeasuredOn = max(f.MeasuredOn, day(r.Measured.MeasuredAt))
	}
	for _, x := range f.Excluded {
		if x.Measured != nil {
			f.MeasuredOn = max(f.MeasuredOn, day(x.Measured.MeasuredAt))
		}
	}
	return f, rep, nil
}

// exclusion is why s stays research-only under opts, or "" when it is a row.
func exclusion(s screener.Stock, m Measurement, measured bool, opts options) string {
	v, ok := venues[s.Market]
	on := day(m.MeasuredAt)
	switch {
	case !measured:
		return "Not measured on Yahoo yet"
	case m.Status == "not_found":
		return fmt.Sprintf("No keyless Yahoo data for this listing (Yahoo answered 404 on %s)", on)
	case m.Status != "ok":
		return fmt.Sprintf("No usable Yahoo read (%s on %s)", m.Status, on)
	case !ok:
		return "No quote-feed venue for this market"
	case m.InstrumentType != "EQUITY":
		return fmt.Sprintf("Yahoo serves it as %s, not as an equity", cmp.Or(m.InstrumentType, "an unknown type"))
	case m.Currency != s.Currency || !slices.Contains(v.ccys, m.Currency):
		return fmt.Sprintf("Yahoo quotes it in %s, not in the list's %s", cmp.Or(m.Currency, "no currency"), s.Currency)
	case m.TradedValueUSD30d < opts.minTradedUSD:
		return fmt.Sprintf("Trades under %s a day (30-day mean %s on %s)", dollars(opts.minTradedUSD), dollars(m.TradedValueUSD30d), on)
	}
	at, err1 := time.Parse(time.RFC3339, m.MeasuredAt)
	first, err2 := time.Parse(time.DateOnly, m.FirstTradeDate)
	last, err3 := time.Parse(time.RFC3339, m.RegularMarketTime)
	switch {
	case err1 != nil || err2 != nil || err3 != nil:
		return "Yahoo's read has no first or last trade date"
	case opts.minHistoryDays > 0 && at.Sub(first) < time.Duration(opts.minHistoryDays)*24*time.Hour:
		span := fmt.Sprintf("%d days", opts.minHistoryDays)
		if opts.minHistoryDays == 365 {
			span = "a year"
		}
		return fmt.Sprintf("Listed under %s ago (first trade %s)", span, m.FirstTradeDate)
	case at.Sub(last) > time.Duration(opts.maxQuoteAge)*24*time.Hour:
		return fmt.Sprintf("No fresh Yahoo quote (last trade %s)", last.UTC().Format(time.DateOnly))
	case m.DailyBarsWithClose < opts.minDailyBars:
		bars := "bars"
		if m.DailyBarsWithClose == 1 {
			bars = "bar"
		}
		return fmt.Sprintf("Yahoo's daily history has gaps (%d daily %s in the month to %s)", m.DailyBarsWithClose, bars, on)
	}
	return ""
}

func newRow(s screener.Stock, m Measurement) Row {
	idx := index(s)
	return Row{Symbol: s.Ticker, Name: cmp.Or(strings.TrimSpace(m.LongName), s.Name), ListName: s.Name, Market: s.Market,
		Code: s.Code, Index: idx, Venue: venues[s.Market].venue, Currency: m.Currency,
		Underlying: underlying(s.Market, s.Ticker, idx, m.Currency), Measured: m}
}

// index names s's indices ("Nikkei 225"; several joined with " and ").
func index(s screener.Stock) string {
	names := make([]string, len(s.Indices))
	for i, m := range s.Indices {
		names[i] = m.Index
	}
	return strings.Join(names, " and ")
}

// underlying is the row's reference line: "TSE: 6501 (Nikkei 225), JPY→USD at
// Yahoo FX", or "…, quoted in USD (no FX)"; a Taipei Exchange (.TWO) listing
// is "TPEx: 6488 …". It states no share class.
func underlying(mkt, ticker, idx, ccy string) string {
	label := venues[mkt].label
	if strings.HasSuffix(ticker, ".TWO") {
		label = "TPEx"
	}
	s := label + ": " + strings.SplitN(ticker, ".", 2)[0]
	if idx != "" {
		s += " (" + idx + ")"
	}
	if ccy == "USD" {
		return s + ", quoted in USD (no FX)"
	}
	return s + ", " + ccy + "→USD at Yahoo FX"
}

// dollars is "$1M", "$250k", "$127k", "$1.5M".
func dollars(v int64) string {
	switch {
	case v >= 1_000_000:
		return "$" + strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", float64(v)/1e6), "0"), ".") + "M"
	case v >= 1000:
		return fmt.Sprintf("$%dk", int64(math.Round(float64(v)/1000)))
	}
	return fmt.Sprintf("$%d", v)
}

func day(rfc3339 string) string {
	if len(rfc3339) < len(time.DateOnly) {
		return ""
	}
	return rfc3339[:len(time.DateOnly)]
}

// measurements are f's own, by ticker.
func (f File) measurements() map[string]Measurement {
	out := map[string]Measurement{}
	for _, r := range f.Rows {
		out[r.Symbol] = r.Measured
	}
	for _, x := range f.Excluded {
		if x.Measured != nil {
			out[x.Ticker] = *x.Measured
		}
	}
	return out
}

// encode is f as committed: one row or exclusion per line, so a rebuild's
// diff reads row by row.
func (f File) encode() ([]byte, error) {
	var b bytes.Buffer
	line := func(v any) error {
		var one bytes.Buffer
		enc := json.NewEncoder(&one)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		b.Write(bytes.TrimSuffix(one.Bytes(), []byte("\n")))
		return nil
	}
	b.WriteString("{\n\"generatedFrom\": ")
	line(f.GeneratedFrom)
	b.WriteString(",\n\"measuredOn\": ")
	line(f.MeasuredOn)
	b.WriteString(",\n\"rule\": ")
	if err := line(f.Rule); err != nil {
		return nil, err
	}
	b.WriteString(",\n\"rows\": [")
	for i, r := range f.Rows {
		b.WriteString(map[bool]string{true: "\n", false: ",\n"}[i == 0])
		if err := line(r); err != nil {
			return nil, err
		}
	}
	b.WriteString("\n],\n\"excluded\": [")
	for i, x := range f.Excluded {
		b.WriteString(map[bool]string{true: "\n", false: ",\n"}[i == 0])
		if err := line(x); err != nil {
			return nil, err
		}
	}
	b.WriteString("\n]\n}\n")
	return b.Bytes(), nil
}

// readListed reads a listed file; a missing one is empty (the first build).
func readListed(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func readMeasurements(path string) (MeasurementFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return MeasurementFile{}, err
	}
	var mf MeasurementFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		return MeasurementFile{}, fmt.Errorf("%s: %w", path, err)
	}
	for i, m := range mf.Measurements {
		if _, ok := yahoo.Normalize(m.Ticker); !ok || m.MeasuredAt == "" || m.Status != "ok" && m.Status != "not_found" {
			return MeasurementFile{}, fmt.Errorf("%s: measurement %d (%q): ticker, measuredAt or status", path, i, m.Ticker)
		}
	}
	return mf, nil
}

// measure reads each ticker once from src, one read per pace (0: no pause),
// and the FX rate of each currency it meets. A read that fails other than
// as a not-found is reported on log and left out.
func measure(ctx context.Context, src yahoo.Source, stocks []screener.Stock, pace time.Duration, now func() time.Time, log io.Writer) MeasurementFile {
	mf := MeasurementFile{Source: "genlisted measure: Yahoo v8 chart range=1mo interval=1d, one read a second", Measurements: []Measurement{}}
	rates := map[string]float64{}
	var tick <-chan time.Time
	if pace > 0 {
		t := time.NewTicker(pace)
		defer t.Stop()
		tick = t.C
	}
	for i, s := range stocks {
		if i > 0 && tick != nil {
			select {
			case <-tick:
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			fmt.Fprintln(log, "measure: stopped:", ctx.Err())
			break
		}
		at := now().UTC().Truncate(time.Second).Format(time.RFC3339)
		ch, err := src.Chart(ctx, s.Ticker, "1mo", "1d")
		m := Measurement{Ticker: s.Ticker, Market: s.Market, MeasuredAt: at}
		switch {
		case yahoo.IsNotFound(err):
			m.Status, m.HTTP, m.Error = "not_found", 404, "not found on Yahoo"
		case err != nil:
			fmt.Fprintf(log, "measure: %s: %v (left out: the earlier measurement stands)\n", s.Ticker, err)
			continue
		default:
			meta := ch.Meta
			m.Status, m.InstrumentType, m.Currency = "ok", meta.InstrumentType, meta.Currency
			m.LongName = strings.TrimSpace(cmp.Or(meta.LongName, meta.ShortName))
			if meta.FirstTradeDate != nil {
				m.FirstTradeDate = meta.FirstTradeDate.UTC().Format(time.DateOnly)
			}
			if meta.PriceTime != nil {
				m.RegularMarketTime = meta.PriceTime.UTC().Format(time.RFC3339)
			}
			m.DailyBars, m.DailyBarsWithClose = len(ch.Bars), len(ch.Bars) // the client drops bars with no close
			rate, ok := rates[meta.Currency]
			if !ok && meta.Currency != "" {
				r, err := src.FX(ctx, meta.Currency)
				if err != nil {
					fmt.Fprintf(log, "measure: %s FX for %s: %v (left out)\n", meta.Currency, s.Ticker, err)
					continue
				}
				rate, rates[meta.Currency] = r.USD, r.USD
			}
			if len(ch.Bars) > 0 {
				var sum float64
				for _, b := range ch.Bars {
					sum += b.C * b.V
				}
				m.TradedValueUSD30d = int64(math.Round(sum / float64(len(ch.Bars)) * rate))
			}
		}
		mf.Measurements = append(mf.Measurements, m)
	}
	return mf
}
