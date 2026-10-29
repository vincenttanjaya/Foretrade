package market

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
)

// Register mounts the market endpoints behind require (the session check).
func (c *Client) Register(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("GET /api/market/assets", require(http.HandlerFunc(c.handleAssets)))
	mux.Handle("GET /api/market/instruments", require(http.HandlerFunc(c.handleInstruments)))
	mux.Handle("GET /api/market/candles", require(http.HandlerFunc(c.handleCandles)))
	mux.Handle("GET /api/market/analysis", require(http.HandlerFunc(c.handleAnalysis)))
	mux.Handle("GET /api/market/derivatives", require(http.HandlerFunc(c.handleDerivatives)))
	mux.Handle("GET /api/market/quote-sources", require(http.HandlerFunc(c.handleQuoteSources)))
}

// maxWith is how many listed symbols one assets read may add with with=.
const maxWith = 20

// withFresh is how old a with= row's read may be before the list reads it
// again (within feedWait).
const withFresh = 2 * time.Minute

// handleAssets lists the instruments that trade: a halted one's frozen quote
// would read as live, and nothing new can be opened in it. With no scope it
// answers the named rows, as before the listed rows existed (old bundles);
// scope=named adds the listed rows named in with= (each Touched, and read
// first when unread or over withFresh old); scope=listed is the listed rows.
func (c *Client) handleAssets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := q.Get("scope")
	var with []string
	if raw := q.Get("with"); raw != "" {
		for s := range strings.SplitSeq(raw, ",") {
			if a, ok := Lookup(s); ok && a.IsListed() && !slices.Contains(with, a.Symbol) {
				with = append(with, a.Symbol)
			}
		}
		if n := len(strings.Split(raw, ",")); n > maxWith {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "with takes at most 20 symbols.")
			return
		}
	}
	switch {
	case scope != "" && scope != "named" && scope != "listed":
		httpx.Error(w, http.StatusBadRequest, "bad_request", "scope must be named or listed.")
		return
	case q.Has("with") && scope != "named":
		httpx.Error(w, http.StatusBadRequest, "bad_request", "with needs scope=named.")
		return
	}
	if len(with) > 0 {
		c.Touch(with...)
	}
	assets, err := c.Assets(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	keep := func(a Asset) bool { return !a.IsListed() } // no scope, or named
	if scope == "listed" {
		keep = Asset.IsListed
	}
	assets = slices.DeleteFunc(assets, func(a Asset) bool { return a.Halted || !keep(a) })
	if len(with) > 0 {
		fresh, _ := c.freshAssets(r.Context(), with, withFresh)
		for _, sym := range with {
			if a, ok := fresh[sym]; ok && !a.Halted {
				assets = append(assets, a)
			}
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"assets": assets})
}

// Instrument is one row of GET /api/market/instruments: an instrument as
// configured, priced or not, halted or not. The Listing fields are null for
// crypto; yahoo is null without a Yahoo listing; index is a listed row's
// index name ("" once it left every list) and null for a named row.
type Instrument struct {
	Symbol        string  `json:"symbol"`
	Pair          string  `json:"pair"`
	Name          string  `json:"name"`
	Class         string  `json:"class"`
	Venue         string  `json:"venue"`
	Region        *string `json:"region"`
	SessionVenue  *string `json:"sessionVenue"`
	LocalCurrency *string `json:"localCurrency"`
	Underlying    *string `json:"underlying"`
	Yahoo         *string `json:"yahoo"`
	Listed        bool    `json:"listed"`
	Index         *string `json:"index"`
}

// InstrumentRows is every instrument (Instruments order) as the API lists it.
func InstrumentRows() []Instrument {
	all := Instruments()
	out := make([]Instrument, len(all))
	for i, a := range all {
		row := Instrument{Symbol: a.Symbol, Pair: a.Pair, Name: a.Name, Class: a.Class, Venue: a.Venue, Listed: a.IsListed()}
		if l := a.Listing; l != nil {
			row.Region, row.SessionVenue, row.LocalCurrency, row.Underlying = &l.Region, &l.SessionVenue, &l.LocalCurrency, &l.Underlying
			if l.Yahoo != "" {
				row.Yahoo = &l.Yahoo
			}
		}
		if idx, ok := ListedIndex(a.Symbol); ok && row.Listed {
			row.Index = &idx
		}
		out[i] = row
	}
	return out
}

// handleInstruments lists every instrument, named then listed, for the
// pages that need one the assets list does not carry (unpriced, halted,
// held). It changes only with a deploy: an hour's private cache, and an ETag
// of the body's hash and the count.
func (c *Client) handleInstruments(w http.ResponseWriter, r *http.Request) {
	rows := InstrumentRows()
	body, err := json.Marshal(map[string]any{"instruments": rows})
	if err != nil {
		httpx.ServerError(w, r, err)
		return
	}
	sum := sha256.Sum256(body)
	etag := fmt.Sprintf(`"%x-%d"`, sum[:8], len(rows))
	h := w.Header()
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && slices.Contains(strings.Split(strings.ReplaceAll(match, " ", ""), ","), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(append(body, '\n'))
}

func (c *Client) handleCandles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	info, ok := Lookup(q.Get("symbol"))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "Unknown symbol.")
		return
	}
	interval := q.Get("interval")
	if !validChartInterval(interval) {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "interval must be one of 15m, 1h, 4h, 1d, 1w, 1M.")
		return
	}
	currency := strings.ToLower(strings.TrimSpace(q.Get("currency")))
	if currency != "" && currency != "usd" && currency != "original" {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "currency must be USD or original.")
		return
	}
	if currency == "original" && !info.QuoteFeed() {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "Original currency candles are only available for quote-feed listings.")
		return
	}
	limit := 500
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "limit must be 1..1000.")
			return
		}
		limit = n
	}
	var before time.Time
	if q.Has("before") {
		n, err := strconv.ParseInt(q.Get("before"), 10, 64)
		if err != nil || n <= 0 || n > math.MaxInt64/1000 {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "before must be a positive Unix timestamp in seconds.")
			return
		}
		before = time.Unix(n, 0)
	}
	if info.QuoteFeed() {
		c.Touch(info.Symbol) // the chart on screen: its quote is read every minute
	}
	var candles []Candle
	var err error
	if before.IsZero() {
		candles, err = c.candlesInCurrency(r.Context(), info.Symbol, interval, limit, currency == "original")
	} else {
		if info.Perp() && !info.QuoteFeed() {
			if now := c.now(); before.After(now) {
				// Perps request a window ending at the cursor. Keep a future
				// cursor's window current and include a bar opening at now.
				before = now.Add(time.Millisecond)
			}
		}
		candles, err = c.candlesBeforeInCurrency(r.Context(), info.Symbol, interval, before, limit, currency == "original")
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if candles == nil {
		candles = []Candle{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"candles": candles})
}

func (c *Client) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	info, ok := Lookup(q.Get("symbol"))
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "Unknown symbol.")
		return
	}
	interval := q.Get("interval")
	if interval == "" {
		interval = "4h"
	}
	if !ValidInterval(interval) {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "interval must be one of 15m, 1h, 4h, 1d.")
		return
	}
	if info.QuoteFeed() {
		c.Touch(info.Symbol)
	}
	a, err := c.Analyze(r.Context(), info.Symbol, interval)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"analysis": a})
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrUpstream) {
		slog.Warn("market upstream", "path", r.URL.Path, "err", err)
		httpx.Error(w, http.StatusBadGateway, "upstream", "Market data is unavailable right now. Try again shortly.")
		return
	}
	httpx.ServerError(w, r, err)
}
