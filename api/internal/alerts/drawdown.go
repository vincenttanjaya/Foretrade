package alerts

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// Drawdown triggers (contract "All-time-high drawdown triggers"): fire once when the
// price is at or below ath × (1 − drawdown_bps / 10000), then notify, or also
// sell sell_pct of the current paper long in the claim's own transaction. The
// stored high ratchets up whenever a check sees a higher price.

// quoteFeedVenue is a stock priced from the Yahoo quote feed, whose high and
// trigger price are the listing's own, in its currency.
const quoteFeedVenue = market.QuoteFeedVenue

// lookup is market.Lookup; the quote-feed tests swap in a fake venue "yahoo" asset.
var lookup = market.Lookup

// ATH is the all-time high a drawdown trigger measures from, and where it came from.
type ATH struct {
	High     float64   `json:"ath"`         // in Currency
	At       time.Time `json:"athAt"`       // the daily bar that set it (its open, UTC), or the check that raised it
	From     time.Time `json:"coveredFrom"` // the first daily bar read: the high covers From to now
	Live     bool      `json:"athLive"`     // raised by a checked price, not read from the daily history
	Source   string    `json:"source"`      // binance | yahoo | perp
	Ticker   string    `json:"ticker"`      // whose highs: BTCUSDT, NVDA, xyz:KIOXIA, D05.SI
	Currency string    `json:"currency"`    // USD, or a quote-feed listing's currency
	Basis    string    `json:"basis"`       // mark: the USD price; listing: a quote feed's last price in Currency
	Label    string    `json:"sourceLabel"` // what the high covers, for people
}

// Drawdown is a drawdown trigger's view (Alert.Drawdown; null on the other triggers).
type Drawdown struct {
	Pct     float64 `json:"pct"`     // fire this many % below the high: drawdown_bps / 100
	SellPct *int    `json:"sellPct"` // action sell: the share of the paper long sold when it fires
	ATH
	Threshold float64 `json:"threshold"` // ath × (1 − pct / 100), in Currency
	Outcome   *string `json:"outcome"`   // once fired: notified | sold | nothing_to_sell
	// Price is the current price in Currency and FromATHPct (price / ath − 1) × 100:
	// on create and on GET /api/alerts for active triggers, null when unavailable.
	Price      *float64 `json:"price"`
	FromATHPct *float64 `json:"fromAthPct"`
}

// drawdownRow is the drawdown columns as scan reads them.
type drawdownRow struct {
	bps, sellPct                      *int
	ath                               *float64
	at, from                          *time.Time
	source, ticker, currency, outcome *string
	live                              *bool
}

func (r drawdownRow) view(symbol string) *Drawdown {
	if r.bps == nil || r.ath == nil || r.at == nil || r.from == nil || r.source == nil || r.ticker == nil || r.currency == nil || r.live == nil {
		return nil
	}
	h := ATH{High: *r.ath, At: *r.at, From: *r.from, Live: *r.live, Source: *r.source, Ticker: *r.ticker, Currency: *r.currency, Basis: "mark"}
	if info, ok := lookup(symbol); ok && info.Venue == quoteFeedVenue {
		h.Basis = "listing"
	}
	h.Label = h.label()
	return &Drawdown{Pct: float64(*r.bps) / 100, SellPct: r.sellPct, ATH: h, Threshold: threshold(h.High, *r.bps), Outcome: r.outcome}
}

// label says what the high covers: "Binance BTCUSDT daily highs since
// 2017-08-17", "Yahoo Finance NVDA lifetime daily highs since 1999-01-22
// (split-adjusted)", "perp history since 2026-06-25, not the listing's lifetime high".
func (h ATH) label() string {
	since := h.From.UTC().Format(time.DateOnly)
	switch h.Source {
	case "binance":
		return fmt.Sprintf("Binance %s daily highs since %s", h.Ticker, since)
	case "yahoo":
		s := fmt.Sprintf("Yahoo Finance %s lifetime daily highs since %s (split-adjusted)", h.Ticker, since)
		if h.Currency != "USD" {
			s += ", in " + h.Currency
		}
		return s
	}
	return "perp history since " + since + ", not the listing's lifetime high"
}

// threshold is the trigger price: ath × (1 − bps / 10000). The SQL claim
// computes the same expression in the same order, so the two agree exactly.
func threshold(ath float64, bps int) float64 { return ath * float64(10000-bps) / 10000 }

// Threshold is the trigger price of a drawdown of pct % (a validated
// Input.DrawdownPct) below high.
func Threshold(high, pct float64) float64 { return threshold(high, int(math.Round(pct*100))) }

// ResolvedATH is the high ValidateDrawdown resolved (nil before it ran).
func (in Input) ResolvedATH() *ATH { return in.ath }

// PctLabel is a drawdown level for people: "20%", "12.5%", "0.01%".
func PctLabel(pct float64) string { return strconv.FormatFloat(pct, 'f', -1, 64) + "%" }

// InCurrency formats a price in ccy: "$236.40" for USD, else "SGD 30.12".
func InCurrency(x float64, ccy string) string {
	if ccy == "" || ccy == "USD" {
		return paper.Money(x)
	}
	return ccy + " " + strings.TrimPrefix(paper.Money(x), "$")
}

// DrawdownLabel is the sale's trade note and ref label: "Drawdown alert: SOL 20% below ATH $295.83".
func DrawdownLabel(symbol string, pct, ath float64, ccy string) string {
	return fmt.Sprintf("Drawdown alert: %s %s below ATH %s", symbol, PctLabel(pct), InCurrency(ath, ccy))
}

// ValidateDrawdown normalizes and checks a drawdown trigger as POST
// /api/alerts takes it and resolves its all-time high, returning the current
// price it compares (in the high's currency). It does not refuse a price that
// is already past the threshold: that trigger fires on the next check, and the
// confirmation says so. Rule errors are *app.BadRequest; an unavailable high
// or price is market.ErrUpstream.
func ValidateDrawdown(ctx context.Context, d *app.Deps, in Input) (Input, float64, error) {
	info, ok := lookup(in.Symbol)
	if !ok {
		return in, 0, bad("unknown symbol %q", in.Symbol)
	}
	in.Symbol, in.Note, in.Trigger = info.Symbol, strings.TrimSpace(in.Note), "drawdown"
	in.Action = cmp.Or(strings.ToLower(strings.TrimSpace(in.Action)), "notify")
	in.Source = cmp.Or(in.Source, "manual")
	bps := math.Round(in.DrawdownPct * 100)
	switch {
	case strings.TrimSpace(in.Op) != "" || in.Price != 0 || strings.TrimSpace(in.Every) != "" || strings.TrimSpace(in.At) != "" || in.Weekday != nil ||
		in.Notional != nil || in.StopLoss != nil || in.TakeProfit != nil:
		return in, 0, bad("A drawdown alert takes no op, price, schedule, notional, stopLoss or takeProfit.")
	case !(bps >= 1 && bps <= 9999): // NaN fails too
		return in, 0, bad("drawdownPct must be 0.01–99.99 (%% below the all-time high).")
	case in.Action != "notify" && in.Action != "sell":
		return in, 0, bad("A drawdown alert notifies or sells: action must be notify or sell.")
	case in.Action == "notify" && in.SellPct != nil:
		return in, 0, bad("sellPct is for action sell only.")
	case in.SellPct != nil && !(*in.SellPct >= 1 && *in.SellPct <= 100 && *in.SellPct == math.Trunc(*in.SellPct)):
		return in, 0, bad("sellPct must be a whole number from 1 to 100 (%% of the paper long).")
	}
	if err := validCommon(in, in.Action == "sell"); err != nil {
		return in, 0, err
	}
	in.DrawdownPct = bps / 100
	if in.Action == "sell" && in.SellPct == nil {
		in.SellPct = new(100.0)
	}
	h, price, err := resolveATH(ctx, d, info)
	if err != nil {
		return in, 0, err
	}
	in.ath = &h
	return in, price, nil
}

// resolveATH is info's all-time high and the current price it is compared with:
//   - a quote-feed stock (venue "yahoo"): its listing's Yahoo lifetime daily
//     highs and last price, both in the listing's currency (not the USD mark);
//   - crypto: Binance daily highs since the pair listed, against the mark;
//   - a USD perp with a Yahoo listing (US stocks, ADRs, ETFs, the S&P 500):
//     the listing's Yahoo lifetime daily highs, against the USD mark;
//   - any other perp (a KRW, JPY, HKD or CNY underlying the oracle converts, or
//     no listing): the highest daily high of the perp's own history, labelled
//     as such; local-currency highs cannot be compared with USD marks.
//
// A current price above the history's high is the high (Live, now). A Yahoo
// high over maxATHMultiple × the price is refused as a rule error.
func resolveATH(ctx context.Context, d *app.Deps, info market.AssetInfo) (ATH, float64, error) {
	var h ATH
	var price float64
	var err error
	switch {
	case info.Venue == quoteFeedVenue:
		h, price, err = yahooATH(ctx, d.YahooSource(), listingTicker(info), "", true)
		h.Basis = "listing"
	case info.Class == market.Crypto:
		var bars []market.Candle
		if bars, err = sinceListing(ctx, d.Market, info.Symbol); err == nil {
			h, err = candleATH(bars, "binance", info.Pair)
		}
	case info.Listing != nil && info.Listing.LocalCurrency == "USD" && info.Listing.Yahoo != "":
		h, _, err = yahooATH(ctx, d.YahooSource(), info.Listing.Yahoo, "USD", false)
	default:
		var bars []market.Candle
		if bars, err = d.Market.Candles(ctx, info.Symbol, "1d", 3000); err == nil {
			h, err = candleATH(bars, "perp", info.Pair)
		}
	}
	if err == nil && h.Basis == "" {
		h.Basis = "mark"
		price, err = d.Market.Price(ctx, info.Symbol)
		if err != nil && !errors.Is(err, market.ErrUpstream) {
			err = fmt.Errorf("%w: %v", market.ErrUpstream, err)
		}
	}
	if err != nil {
		return ATH{}, 0, err
	}
	if h.Source == "yahoo" && h.High > maxATHMultiple*price {
		return ATH{}, 0, bad("%s has no usable all-time high: Yahoo's split-adjusted lifetime high for %s is over %d× the price, which reverse "+
			"splits produce, so a drawdown alert can't measure from it.", info.Symbol, h.Ticker, maxATHMultiple)
	}
	if price > h.High {
		h.High, h.At, h.Live = price, time.Now().UTC(), true
	}
	h.Label = h.label()
	return h, price, nil
}

// maxATHMultiple bounds a Yahoo lifetime high: one over this many times the
// price is withheld as unavailable, as get_stock_quote withholds it. Reverse
// splits scale old prices up (HSDT's split-adjusted 2017 high reads about
// $27M against a $2.66 price), so it is no high to measure a drawdown from
// and a trigger on it would fire at once.
const maxATHMultiple = 100

// listingTicker is a quote-feed stock's Yahoo ticker.
func listingTicker(info market.AssetInfo) string {
	if info.Listing != nil && info.Listing.Yahoo != "" {
		return info.Listing.Yahoo
	}
	return info.Pair
}

// yahooATH is ticker's lifetime daily high (Yahoo's "max" daily window,
// split-adjusted) in the chart's currency, which must be want unless want is
// "", and with quote its last regular-market price in that currency. Every
// Yahoo failure, a not-found ticker included, is market.ErrUpstream: the
// instrument is real, only its history is unavailable.
func yahooATH(ctx context.Context, src yahoo.Source, ticker, want string, quote bool) (ATH, float64, error) {
	unavailable := func(why string) (ATH, float64, error) {
		return ATH{}, 0, fmt.Errorf("%w: %s all-time high: %s", market.ErrUpstream, ticker, why)
	}
	chart, err := src.Chart(ctx, ticker, "max", "1d")
	if err != nil {
		return unavailable(err.Error())
	}
	hi, ok := yahoo.AllTimeHigh(chart.Bars)
	ccy := chart.Meta.Currency
	switch {
	case !ok || !(hi.High > 0):
		return unavailable("no daily bars")
	case ccy == "" || want != "" && ccy != want:
		return unavailable("chart currency " + ccy)
	}
	h := ATH{High: hi.High, At: hi.At.UTC(), From: hi.From.UTC(), Source: "yahoo", Ticker: ticker, Currency: ccy}
	if !quote {
		return h, 0, nil
	}
	q, err := src.Quote(ctx, ticker)
	switch {
	case err != nil:
		return unavailable(err.Error())
	case q.Currency != ccy || !(q.Price > 0):
		return unavailable("quote currency " + q.Currency)
	}
	return h, q.Price, nil
}

// candleATH is the highest daily high of bars (the earliest on a tie).
func candleATH(bars []market.Candle, source, ticker string) (ATH, error) {
	conv := make([]yahoo.Bar, len(bars))
	for i, b := range bars {
		conv[i] = yahoo.Bar{T: time.Unix(b.T, 0).UTC(), H: b.H}
	}
	hi, ok := yahoo.AllTimeHigh(conv)
	if !ok || !(hi.High > 0) {
		return ATH{}, fmt.Errorf("%w: no daily history for %s", market.ErrUpstream, ticker)
	}
	return ATH{High: hi.High, At: hi.At, From: hi.From, Source: source, Ticker: ticker, Currency: "USD"}, nil
}

// sinceListing is every daily bar of a Binance pair since it listed, oldest
// first: CandlesBefore pages back 3,000 bars at a time until a page comes up
// short (BTCUSDT, listed 2017-08-17: two pages, four klines calls).
func sinceListing(ctx context.Context, m *market.Client, symbol string) ([]market.Candle, error) {
	var out []market.Candle
	end := time.Now()
	for range 10 { // 30,000 days: more than any pair's history
		page, err := m.CandlesBefore(ctx, symbol, "1d", end, 3000)
		if err != nil {
			return nil, err
		}
		out = append(page, out...)
		if len(page) < 3000 || !time.Unix(page[0].T, 0).Before(end) {
			break
		}
		end = time.Unix(page[0].T, 0)
	}
	return out, nil
}

// ATHQuote is GET /api/alerts/ath's answer: the high a drawdown trigger on
// Symbol would measure from and the current price it compares.
type ATHQuote struct {
	Symbol string `json:"symbol"`
	ATH
	Price      float64 `json:"price"`
	FromATHPct float64 `json:"fromAthPct"` // (price / ath − 1) × 100
}

// QuoteATH resolves symbol's high and current price, as the create form and
// the chat card show them before anything is set.
func QuoteATH(ctx context.Context, d *app.Deps, symbol string) (ATHQuote, error) {
	info, ok := lookup(symbol)
	if !ok {
		return ATHQuote{}, bad("unknown symbol %q", symbol)
	}
	h, price, err := resolveATH(ctx, d, info)
	if err != nil {
		return ATHQuote{}, err
	}
	return ATHQuote{Symbol: info.Symbol, ATH: h, Price: price, FromATHPct: round2(yahoo.DrawdownPct(price, h.High))}, nil
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func (h *handler) ath(w http.ResponseWriter, r *http.Request) {
	q, err := QuoteATH(r.Context(), h.d, r.URL.Query().Get("symbol"))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ath": q})
}

// withPrice sets an active trigger's current price and distance from its high.
func (dd *Drawdown) withPrice(price float64) {
	if dd == nil || !(price > 0) {
		return
	}
	dd.Price, dd.FromATHPct = &price, new(round2(yahoo.DrawdownPct(price, dd.High)))
}

// livePrices fills the current price of the active drawdown triggers in list,
// best effort (GET /api/alerts): a market or Yahoo failure leaves them null.
func livePrices(ctx context.Context, d *app.Deps, list []Alert) {
	var symbols []string
	for _, a := range list {
		if a.Drawdown != nil && a.Status == "active" {
			symbols = append(symbols, a.Symbol)
		}
	}
	if len(symbols) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	prices, _, err := drawdownPrices(ctx, d, symbols, false)
	if err != nil {
		slog.Debug("drawdown prices", "err", err)
	}
	for i := range list {
		if a := &list[i]; a.Drawdown != nil && a.Status == "active" {
			if p, ok := prices[a.Symbol]; ok && p.ccy == a.Drawdown.Currency {
				a.Drawdown.withPrice(p.price)
			}
		}
	}
}

type pricePoint struct {
	price float64
	ccy   string
}

// drawdownPrices is the price each symbol's trigger compares: the USD mark
// from one Assets snapshot, or a quote feed's listing price from one batched
// Yahoo read. assets also returns the snapshot when withAssets asks for it
// (a sale fills from it). A failed source leaves its symbols out.
func drawdownPrices(ctx context.Context, d *app.Deps, symbols []string, withAssets bool) (map[string]pricePoint, []market.Asset, error) {
	out := map[string]pricePoint{}
	var tickers []string
	byTicker := map[string]string{}
	marks := withAssets
	for _, s := range symbols {
		info, ok := lookup(s)
		switch {
		case !ok:
		case info.Venue == quoteFeedVenue:
			t, valid := yahoo.Normalize(listingTicker(info))
			if !valid {
				continue
			}
			if _, dup := byTicker[t]; !dup {
				tickers = append(tickers, t)
			}
			byTicker[t] = info.Symbol
		default:
			marks = true
		}
	}
	var errs []error
	var assets []market.Asset
	if marks {
		var err error
		if assets, err = d.Market.Assets(ctx); err != nil {
			errs = append(errs, err)
		}
		for _, a := range assets {
			if info, ok := lookup(a.Symbol); ok && info.Venue != quoteFeedVenue {
				out[a.Symbol] = pricePoint{a.Price, "USD"}
			}
		}
	}
	if len(tickers) > 0 {
		quotes, err := d.YahooSource().Quotes(ctx, tickers)
		if err != nil {
			errs = append(errs, err)
		}
		for t, q := range quotes {
			if sym, ok := byTicker[t]; ok && q.Price > 0 {
				out[sym] = pricePoint{q.Price, q.Currency}
			}
		}
	}
	return out, assets, errors.Join(errs...)
}

// checkDrawdowns ratchets each active drawdown trigger of an enabled account
// to a higher checked price and fires the ones at or below their threshold;
// a sell trigger on a quote feed whose quote is stale (halted) waits for a
// fresh one. With none active it makes no market call.
func checkDrawdowns(ctx context.Context, d *app.Deps) error {
	type armed struct {
		id, symbol, action, ccy string
		bps                     int
		ath                     float64
	}
	rows, _ := d.DB.Query(ctx, `select a.id::text, a.symbol, a.action, a.ath_currency, a.drawdown_bps, a.ath from alerts a join users u on u.id = a.user_id
		where a.status = 'active' and a.trigger = 'drawdown' and u.disabled_at is null`)
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (armed, error) {
		var a armed
		return a, r.Scan(&a.id, &a.symbol, &a.action, &a.ccy, &a.bps, &a.ath)
	})
	if err != nil || len(list) == 0 {
		return err
	}
	symbols, sells := make([]string, len(list)), false
	for i, a := range list {
		symbols[i], sells = a.symbol, sells || a.action == "sell"
	}
	prices, assets, perr := drawdownPrices(ctx, d, symbols, sells)
	paused := map[string]bool{} // quote feeds without a fresh quote: the engine refuses every order in them
	for _, as := range assets {
		paused[as.Symbol] = as.Halted && as.QuoteFeed != nil
	}
	for _, a := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p, ok := prices[a.symbol]
		switch {
		case !ok || p.ccy != a.ccy:
		case p.price > a.ath:
			if _, err := d.DB.Exec(ctx, `update alerts set ath = $2, ath_at = now(), ath_live = true where id = $1 and status = 'active' and ath < $2`,
				a.id, p.price); err != nil {
				slog.Error("drawdown ratchet", "id", a.id, "err", err)
			}
		case p.price <= threshold(a.ath, a.bps) && a.action == "sell" && paused[a.symbol]:
			// The sale waits for a fresh quote rather than failing the trigger.
		case p.price <= threshold(a.ath, a.bps):
			if err := fireDrawdown(context.WithoutCancel(ctx), d, a.id, p.price, assets); err != nil {
				slog.Error("drawdown fire", "id", a.id, "err", err)
			}
		}
	}
	return perr
}

// fireDrawdown claims a drawdown trigger once and completes it in the same
// transaction, so concurrent checks complete it once: notify, or sell sellPct
// of the current paper long under the account lock at the snapshot's mark
// (fees and slippage as any fill). With no long it notifies "nothing to sell":
// it never opens or adds to a short. A refused sale marks it failed with why;
// any other error rolls the claim back for the next check. The claim rechecks
// the threshold against the stored high, which a concurrent ratchet may raise.
func fireDrawdown(ctx context.Context, d *app.Deps, id string, price float64, quotes []market.Asset) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return pgx.BeginFunc(ctx, d.DB, func(tx pgx.Tx) error {
		var userID, symbol, action string
		var sellPct *int
		// The owner is locked before the account and the alert, as fire does.
		err := tx.QueryRow(ctx, `select u.id::text, a.symbol, a.action, a.sell_pct from users u join alerts a on a.user_id = u.id
			where a.id = $1 and a.status = 'active' and a.trigger = 'drawdown' and u.disabled_at is null for no key update of u`, id).
			Scan(&userID, &symbol, &action, &sellPct)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		outcome, qty := "notified", 0.0
		var ledger *paper.Ledger
		if action == "sell" {
			if ledger, err = d.Paper.LockTx(ctx, tx, userID, quotes); err != nil {
				return err
			}
			for _, p := range ledger.Portfolio().Positions {
				if p.Symbol == symbol && p.Qty > 0 {
					qty = p.Qty
				}
			}
			outcome = "nothing_to_sell"
			if qty > 0 && sellPct != nil {
				outcome = "sold"
				if *sellPct < 100 {
					qty = qty * float64(*sellPct) / 100
				}
			}
		}
		a, err := scan(tx.QueryRow(ctx, `update alerts set status = 'fired', fired_at = now(), fired_price = $2, outcome = $3
			where id = $1 and status = 'active' and $2 <= ath * (10000 - drawdown_bps)::float8 / 10000
			returning `+columns, id, price, outcome))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || outcome != "sold" {
			return err
		}
		t, err := ledger.Order(ctx, paper.OrderRequest{Symbol: symbol, Side: "sell", Qty: &qty, Source: "alert",
			Note: DrawdownLabel(symbol, a.Drawdown.Pct, a.Drawdown.High, a.Drawdown.Currency), RefID: &a.ID})
		var ve *paper.ValidationError
		var halted *paper.HaltedError
		switch {
		case errors.As(err, &ve), errors.As(err, &halted), errors.Is(err, paper.ErrInsufficient), errors.Is(err, paper.ErrNoPosition):
			_, err = tx.Exec(ctx, "update alerts set status = 'failed', outcome = null, error = $2 where id = $1", id, orderError(id, err))
			return err
		case err != nil:
			return err
		}
		_, err = tx.Exec(ctx, "update alerts set trade_id = $2 where id = $1", id, t.ID)
		return err
	})
}
