package forecast

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

var frameDay = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

// TestFrameOf: which questions get the numeric methods, about what, and on
// which plan; everything else stays an event.
func TestFrameOf(t *testing.T) {
	tsm := tsmDraft()
	fr := frameOf(tsm, frameDay)
	if fr.Kind != "price_long" || fr.Subject == nil || fr.Subject.Symbol != "TSM" || fr.Subject.Yahoo != "TSM" || fr.Subject.Class != market.Stock ||
		fr.Measure != "price" || fr.Scale != "log" || fr.Plan != "long_stock" || fr.HorizonDays != 1825 || len(fr.Bounds) != 6 || !fr.numeric() {
		t.Fatalf("TSM: %+v %+v", fr, fr.Subject)
	}
	if fr.Subject.Underlying != "NYSE: TSM ADS = 5 TWSE 2330 shares" {
		t.Fatalf("listing note %q", fr.Subject.Underlying)
	}
	btc := Draft{Shape: "ordered", Question: "Where will the Binance BTC/USDT daily close be on 2026-10-14?", ResolutionDate: "2026-10-14",
		Outcomes: []string{"< $60,000", "$60,000 – $65,000", "$65,000 – $70,000", "≥ $70,000"}}
	if fr := frameOf(btc, frameDay); fr.Kind != "price_short" || fr.Subject.Symbol != "BTC" || fr.Subject.Class != market.Crypto || fr.Plan != "short" || fr.Subject.Yahoo != "" {
		t.Fatalf("BTC: %+v", fr)
	}
	btc.ResolutionDate = "2028-10-14"
	if fr := frameOf(btc, frameDay); fr.Kind != "price_long" || fr.Plan != "long_crypto" {
		t.Fatalf("BTC two years out: %+v", fr)
	}
	nvda := Draft{Shape: "ordered", Question: "What will Nvidia's market capitalization be on 2027-12-31?", ResolutionDate: "2027-12-31",
		Outcomes: []string{"< $3T", "$3T – $4T", "$4T – $5T", "≥ $5T"}}
	if fr := frameOf(nvda, frameDay); fr.Kind != "price_long" || fr.Measure != "market_cap" || fr.Subject.Symbol != "NVDA" || fr.Bounds[1][1] != 4e12 {
		t.Fatalf("NVDA market cap: %+v", fr)
	}
	cpi := Draft{Shape: "ordered", Question: "What will US CPI inflation (YoY) be for February 2027?", ResolutionDate: "2027-03-31",
		Outcomes: []string{"< 0%", "0% – 2%", "2% – 3%", "≥ 3%"}}
	if fr := frameOf(cpi, frameDay); fr.Kind != "level" || fr.Subject != nil || fr.Measure != "level" || fr.Scale != "linear" || fr.Plan != "short" {
		t.Fatalf("CPI: %+v", fr)
	}
	cpi.ResolutionDate = "2028-04-15"
	if fr := frameOf(cpi, frameDay); fr.Plan != "long_level" {
		t.Fatalf("CPI past six months: %+v", fr)
	}
	pair := Draft{Shape: "ordered", Question: "How far above AMD's market cap will NVDA's market cap be on 2027-12-31?", ResolutionDate: "2027-12-31",
		Outcomes: []string{"< $1T", "$1T – $2T", "≥ $2T"}}
	if fr := frameOf(pair, frameDay); fr.Kind != "level" || fr.Subject != nil {
		t.Fatalf("two instruments: %+v", fr)
	}
	margin := Draft{Shape: "ordered", Question: "What will TSMC's gross margin be in Q4 2027?", ResolutionDate: "2028-01-31",
		Outcomes: []string{"< 50%", "50% – 55%", "55% – 60%", "≥ 60%"}}
	if fr := frameOf(margin, frameDay); fr.Kind != "level" || fr.Subject != nil {
		t.Fatalf("a percentage about a stock is a level: %+v", fr)
	}
	revenue := Draft{Shape: "ordered", Question: "What will TSMC report as its 2027 revenue?", ResolutionDate: "2028-01-31",
		Outcomes: []string{"< $100B", "$100B – $150B", "≥ $150B"}}
	if fr := frameOf(revenue, frameDay); fr.Kind != "level" || fr.Subject != nil || fr.Measure != "level" {
		t.Fatalf("a company's revenue is a level, not its price: %+v", fr)
	}
	revenue.Question = "Where will TSM's closing price be after its 2027 earnings, on 2028-01-31?"
	if fr := frameOf(revenue, frameDay); fr.Kind != "price_long" || fr.Measure != "price" {
		t.Fatalf("a price question that names earnings: %+v", fr)
	}
	// A valuation ratio is not the price, even beside "trade": TSM's $485 would be read against P/E ranges.
	for _, q := range []string{"What will NVDA's P/E ratio be on 2027-12-31?", "At what price-to-earnings multiple will TSM trade on 2027-12-31?", "What PE will TSMC shares trade at on 2027-12-31?"} {
		pe := Draft{Shape: "ordered", Question: q, ResolutionDate: "2027-12-31", Outcomes: []string{"< 20", "20 – 30", "30 – 40", "≥ 40"}}
		if fr := frameOf(pe, frameDay); fr.Kind != "level" || fr.Subject != nil || fr.Measure != "level" {
			t.Errorf("%q is a level: %+v", q, fr)
		}
	}
	sox := Draft{Shape: "ordered", Question: "Where will the PHLX Semiconductor index close on 2027-12-31?", ResolutionDate: "2027-12-31",
		Outcomes: []string{"< 4,000", "4,000 – 6,000", "≥ 6,000"}}
	if fr := frameOf(sox, frameDay); fr.Kind != "price_long" || fr.Subject.Symbol != "SOX" || fr.Subject.Yahoo != "^SOX" || fr.Subject.Class != market.Index {
		t.Fatalf("SOX: %+v %+v", fr, fr.Subject)
	}
	for name, d := range map[string]Draft{
		"binary":      {Shape: "binary", Question: "Will TSM close above $600 on 2031-10-06?", Outcomes: yesNo, ResolutionDate: "2031-10-06"},
		"categorical": {Shape: "categorical", Question: "Which will be larger by market cap on 2027-12-31?", Outcomes: []string{"Nvidia", "Apple"}, ResolutionDate: "2027-12-31"},
		"unparseable": {Shape: "ordered", Question: "How will TSM trade in 2031?", Outcomes: []string{"Down a lot", "Flat", "Up a lot"}, ResolutionDate: "2031-10-06"},
	} {
		if fr := frameOf(d, frameDay); fr.Kind != "event" || fr.numeric() {
			t.Errorf("%s: %+v", name, fr)
		}
	}
	on, off := &handler{&app.Deps{ForecastMethods: true}}, &handler{&app.Deps{}}
	if on.frameFor(tsm, frameDay, false).Kind != "price_long" || off.frameFor(tsm, frameDay, false).Kind != "event" || on.frameFor(tsm, frameDay, true).Kind != "event" {
		t.Fatal("the knob off and a replay are event frames")
	}
	if weightsFor(fr)["quant"] != 0.45 || weightsFor(frameOf(cpi, frameDay))["base_rate"] != 0.45 || weightsFor(frameOf(btc, frameDay))["base_rate"] != 0.40 {
		t.Fatal("prior weights per plan")
	}
}

// measureEnv is a DB-free handler with the methods on, Binance fixtures and
// a Yahoo fake.
func measureEnv(t *testing.T) (*handler, *yahootest.Fake) {
	yh := yahootest.New()
	mc := testMarketClient(markettest.New(t).URL)
	mc.Yahoo = yh
	return &handler{&app.Deps{Market: mc, Yahoo: yh, ForecastMethods: true}}, yh
}

// TestMeasureAndH1: the subject's measured price and history become the H1
// block (from a synthetic series), its ranges blend the empirical windows
// with the capped-drift lognormal at w = min(0.5, independent/10), a short
// history has no empirical column, and a Yahoo failure leaves no H1 and
// says so.
func TestMeasureAndH1(t *testing.T) {
	h, yh := measureEnv(t)
	yh.Set(yahootest.Daily("TSM", "USD", synthCloses(1, 29, 0.10, 0.38, 485.12)...))
	yh.Set(yahootest.Daily("^TWII", "TWD", synthCloses(3, 30, 0.05, 0.22, 22000)...))
	yh.Set(yahootest.Daily("TWDUSD=X", "USD", flatCloses(30*261, 0.031)...))
	d := tsmDraft()
	fr := frameOf(d, frameDay)
	m := h.measure(context.Background(), fr, d, frameDay)
	if !m.h1 || m.spot() != 485.12 || m.ref.Source != "Yahoo Finance (delayed)" || m.ref.Label != "TSM, NYSE regular-market price" || m.vol < 0.3 || m.vol > 0.45 {
		t.Fatalf("measurement %+v", m)
	}
	if indep := m.stats.Window.Independent; m.weight != min(0.5, float64(indep)/10) || indep != 5 || len(m.blend) != 6 || len(m.empirical) != 6 {
		t.Fatalf("blend weight %v (independent %d)", m.weight, indep)
	}
	sum := 0.0
	for i, p := range m.blend {
		sum += p
		if want := m.weight*m.empirical[i] + (1-m.weight)*m.lognormal[i]; p < epsilon || (m.empirical[i] > 0.01 && !within(p, want, 0.01)) {
			t.Errorf("blend %d: %v, want about %v", i, p, want)
		}
	}
	if !within(sum, 1, 1e-9) || m.cap.Benchmark != "TWII" || !strings.HasSuffix(m.cap.Source, usdClause) || m.mu > m.cap.CapPct/100 || m.capNote != "" {
		t.Fatalf("blend sum %v, cap %+v mu %v", sum, m.cap, m.mu)
	}
	// The survivor's windows (median ×2.12 in five years) are brought to the capped drift on the ranges: the
	// empirical column's centre is the lognormal's, never the history's own return.
	years := float64(fr.HorizonDays) / 365
	if w := m.stats.Window; m.scale >= 1 || !within(w.Q[3]*m.scale, math.Exp(m.mu*years), 1e-9) {
		t.Fatalf("window scale %v (median ×%v, capped ×%v)", m.scale, w.Q[3], math.Exp(m.mu*years))
	}
	bounds, _ := bucketBounds(d.Outcomes)
	emp, _ := newDist(bounds, m.empirical)
	logn, _ := newDist(bounds, m.lognormal)
	if e, l := emp.quantile(0.5).Value, logn.quantile(0.5).Value; math.Abs(math.Log(e/l)) > 0.1 {
		t.Fatalf("the empirical column's median $%.0f is not the capped drift's $%.0f", e, l)
	}
	for _, want := range []string{
		"\nH1 · Measured history (tool data, safe to cite as H1): TSM (NYSE: TSM ADS = 5 TWSE 2330 shares). Yahoo Finance daily closes (NYSE: TSM), split-adjusted, dividends excluded (the question resolves on the price), 1997-",
		"- Today's level: $485.12 (NYSE regular-market price via Yahoo Finance (delayed), as of ",
		"- Realized volatility, annualized: 1y ", "- 1,825-day windows: ", "about 5 independent", "- Largest drawdown -",
		`- On this question's ranges (share of past windows, each multiple ×0.74 so their median grows at that drift / lognormal at full-history volatility, drift +`, `"< $300" `,
		"- Notes: windows overlap (about 5 of", "the series starts at the listing (1997-",
		"the windows' own median multiple (×2.12) grew faster than the capped drift (×1.57 over this horizon), so on the ranges every window is scaled to it",
	} {
		if !strings.Contains(m.text, want) {
			t.Errorf("H1 lacks %q:\n%s", want, m.text)
		}
	}
	if line := subjectLine(m, d); !strings.HasPrefix(line, "- TSM (TSMC (Taiwan Semiconductor)): $485.12, NYSE regular-market price via Yahoo Finance (delayed), as of ") ||
		!strings.HasSuffix(line, " (tool data, safe to cite)\n") {
		t.Fatalf("subject line %q", line)
	}
	if r := m.result(); r == nil || r.URL == nil || *r.URL != "https://finance.yahoo.com/quote/TSM/history" || r.Window.Independent != 5 || r.Currency != "USD" {
		t.Fatalf("measured %+v", r)
	}
	if n := withHistory("Model knowledge (training data) as of 2026-10-07 09:00 UTC; no live news feed.", m); n != "Model knowledge (training data) + Yahoo daily history (TSM since 1997) as of 2026-10-07 09:00 UTC; no live news feed." {
		t.Fatalf("knowledge note %q", n)
	}

	// Seven years of history: one independent five-year window, so no empirical column.
	yh.Set(yahootest.Daily("TSM", "USD", synthCloses(4, 7, 0.10, 0.38, 485.12)...))
	m = h.measure(context.Background(), fr, d, frameDay)
	if m.empirical != nil || m.lognormal == nil || !strings.Contains(m.text, "Fewer than 2 independent windows of this length: no empirical share; the lognormal alone.") {
		t.Fatalf("short history: %v\n%s", m.empirical, m.text)
	}

	// Yahoo down: no price, no H1; the briefs say so.
	yh.SetErr("TSM", &yahoo.UnavailableError{Symbol: "TSM", Reason: "HTTP 503"})
	m = h.measure(context.Background(), fr, d, frameDay)
	if m.h1 || m.ref != nil || m.result() != nil || !strings.Contains(m.text, "Measured history for TSM is unavailable this run: use peers or the sector index via history_stats, or say so.") {
		t.Fatalf("Yahoo down: %+v", m)
	}
	if b := briefFor(roles[0], fr, false, m); !strings.Contains(b, "The subject's own history is unavailable this run") {
		t.Fatalf("base-rate brief without history: %s", b)
	}
	if h.measure(context.Background(), frame{Kind: "event"}, d, frameDay) != nil {
		t.Fatal("an event frame measures nothing")
	}
}

// TestH1Currencies: a ₩ or S$ question reads its listing's own numbers and
// H1 says nothing of a USD conversion; a US$ question on a ₩ listing says its
// closes and today's level were converted; scenario_model writes its levels
// in the question's currency (the live check's "Bear: $21,510" was ₩21,510).
func TestH1Currencies(t *testing.T) {
	h, yh := measureEnv(t)
	samsung := synthCloses(6, 20, 0.10, 0.36, 262_000)
	yh.Set(yahootest.Daily("005930.KS", "KRW", samsung...))
	fx := make([]float64, len(samsung))
	for i := range fx {
		fx[i] = 0.000744
	}
	yh.Set(yahootest.Daily("KRWUSD=X", "USD", fx...))
	yh.Set(yahootest.Daily("D05.SI", "SGD", synthCloses(7, 20, 0.04, 0.24, 73.85)...))
	date := frameDay.AddDate(3, 0, 0).Format(time.DateOnly)
	ask := func(q string, outcomes ...string) (Draft, frame, *measurement) {
		d := Draft{Shape: "ordered", Question: q + " on " + date + "?", Outcomes: outcomes, ResolutionDate: date, Category: "markets"}
		fr := frameOf(d, frameDay)
		return d, fr, h.measure(context.Background(), fr, d, frameDay)
	}

	d, fr, m := ask("What will be the KRX closing price of Samsung Electronics (SMSN)", "< ₩200,000", "₩200,000 – ₩300,000", "₩300,000 – ₩400,000", "≥ ₩400,000")
	if m == nil || !m.h1 || m.spot() != 262_000 || m.from != "" || m.ref.Label != "SMSN, NYSE regular-market price" ||
		!strings.Contains(m.text, "): SMSN (KRX 005930 common share). Yahoo Finance daily closes") || strings.Contains(m.text, "→USD") ||
		!strings.Contains(m.text, "- Today's level: ₩262,000 (NYSE regular-market price via ") {
		t.Fatalf("₩ question: %+v\n%s", m, m.text)
	}
	k := &deskKit{h: h, d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
	level := func(name, kind string, p, x float64) map[string]any {
		return map[string]any{"name": name, "kind": kind, "p": p, "level": x, "level_basis": "assumption"}
	}
	in, _ := json.Marshal(map[string]any{"scenarios": []map[string]any{level("Bear", "bear", 0.3, 21_510), level("Base", "base", 0.5, 300_000), level("Bull", "bull", 0.2, 420_000)}})
	out, err := run(t, k.scenarioTool(newToolLog()), string(in))
	if w := strings.Join(toStrings(out["warnings"]), " | "); err != nil || w != "Bear: ₩21,510 is under 0.1× today's price" {
		t.Fatalf("₩ warnings %q %v", w, err)
	}
	in, _ = json.Marshal(map[string]any{"scenarios": []map[string]any{level("Bear", "bear", 0.5, 1_000), level("Bull", "bull", 0.5, 420_000)}})
	if _, err := run(t, k.scenarioTool(newToolLog()), string(in)); err == nil || err.Error() != `scenario "Bear": level must be between ₩5,240 and ₩52.4M` {
		t.Fatalf("₩ level error: %v", err)
	}

	_, _, m = ask("What will be the price of Samsung Electronics (SMSN) in US dollars", "< $150", "$150 – $200", "$200 – $250", "≥ $250")
	if m == nil || !m.h1 || !within(m.spot(), 262_000*0.000744, 1e-9) || m.from != "KRW" || m.ref.Label != "SMSN, NYSE regular-market price, converted from ₩ at Yahoo FX" ||
		!strings.Contains(m.text, "): SMSN (KRX 005930 common share, converted from ₩ at Yahoo FX). Yahoo Finance daily closes") ||
		!strings.Contains(m.text, "- Today's level: $194.93 (NYSE regular-market price, converted from ₩ at Yahoo FX via ") {
		t.Fatalf("US$ question on a ₩ listing: %+v\n%s", m, m.text)
	}
	if r := m.result(); r == nil || r.Source != "Yahoo Finance daily closes (NYSE: 005930.KS), converted from ₩ at Yahoo FX" || r.Currency != "USD" {
		t.Fatalf("measured source: %+v", r)
	}

	d, fr, m = ask("What will be the SGX closing price of DBS Group (DBS)", "< S$60", "S$60 – S$80", "S$80 – S$100", "≥ S$100")
	if m == nil || !m.h1 || m.spot() != 73.85 || !strings.Contains(m.text, "): DBS (SGX: D05 ordinary share). Yahoo Finance daily closes") ||
		strings.Contains(m.text, "→USD") || !strings.Contains(m.text, "- Today's level: S$73.85 (") {
		t.Fatalf("S$ question: %+v\n%s", m, m.text)
	}
	k = &deskKit{h: h, d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
	in, _ = json.Marshal(map[string]any{"scenarios": []map[string]any{level("Bear", "bear", 0.3, 6.06), level("Base", "base", 0.7, 80)}})
	if out, err := run(t, k.scenarioTool(newToolLog()), string(in)); err != nil || strings.Join(toStrings(out["warnings"]), " | ") != "Bear: S$6.06 is under 0.1× today's price" {
		t.Fatalf("S$ warnings %v %v", out["warnings"], err)
	}
	if got := listingNote(subject{Name: "Bitcoin"}, ""); got != "Bitcoin" {
		t.Fatalf("no listing note: %q", got)
	}
	if got := (&measurement{ccy: "IDR"}).mark(Draft{Outcomes: []string{"< 9,000", "≥ 9,000"}}); got != "IDR " {
		t.Fatalf("a question without a mark on an IDR listing: %q", got)
	}
}

// TestH1DollarSeries: a question in US dollars on a ₩ listing measures σ, the
// drift and the windows on the closes converted at the daily FX (the won here
// falls about 5% a year with its own swings, so the won series would
// overstate the dollar drift and understate σ) and caps that drift by KOSPI
// in dollars; a ₩ question keeps the won series and KOSPI in won.
// history_stats reads a peer's ₩ listing in the question's currency too, and
// with the FX history down a dollar question gets an error, never won closes.
func TestH1DollarSeries(t *testing.T) {
	h, yh := measureEnv(t)
	samsung := synthCloses(6, 20, 0.05, 0.36, 262_000)
	yh.Set(yahootest.Daily("005930.KS", "KRW", samsung...))
	yh.Set(yahootest.Daily("000660.KS", "KRW", synthCloses(9, 20, 0.12, 0.45, 180_000)...))
	yh.Set(yahootest.Daily("^KS11", "KRW", synthCloses(8, 20, -0.025, 0.20, 2_600)...))
	fx := make([]float64, len(samsung))
	for i := range fx { // USD per won: 5% lower a year, with swings of its own
		fx[i] = 0.000744 * math.Exp(0.05*float64(len(fx)-1-i)/261+0.05*math.Sin(float64(i)/4))
	}
	yh.Set(yahootest.Daily("KRWUSD=X", "USD", fx...))
	date := frameDay.AddDate(3, 0, 0).Format(time.DateOnly)
	ask := func(q string, outcomes ...string) (Draft, frame, *measurement) {
		d := Draft{Shape: "ordered", Question: q + " on " + date + "?", Outcomes: outcomes, ResolutionDate: date, Category: "markets"}
		fr := frameOf(d, frameDay)
		return d, fr, h.measure(context.Background(), fr, d, frameDay)
	}
	ctx := context.Background()
	usd, err1 := h.d.Market.DailyHistoryUSD(ctx, "SMSN")
	won, err2 := h.d.Market.DailyHistory(ctx, "SMSN")
	kospiUSD, err3 := h.d.Market.TickerHistoryUSD(ctx, "KOSPI")
	kospiKRW, err4 := h.d.Market.TickerHistory(ctx, "KOSPI")
	if err := errors.Join(err1, err2, err3, err4); err != nil || !usd.Converted() || won.Currency != "KRW" || !kospiUSD.Converted() || kospiKRW.Converted() {
		t.Fatalf("fixtures: %v", err)
	}
	want := func(s, bench market.DailySeries) (vol, mu float64, c market.DriftCap) {
		mu, _ = market.HalvedDrift(s)
		mu, c = market.CapDrift(mu, "KOSPI", &bench)
		return *market.ComputeHistoryStats(s, 1096).VolAnnPct["all"] / 100, mu, c
	}
	usdVol, usdMu, usdCap := want(usd, kospiUSD)
	wonVol, wonMu, wonCap := want(won, kospiKRW)
	if math.Abs(usdVol-wonVol) < 0.005 || math.Abs(usdMu-wonMu) < 0.01 || usdCap.Source == wonCap.Source {
		t.Fatalf("the fixture must tell the currencies apart: σ %v vs %v, drift %v vs %v", usdVol, wonVol, usdMu, wonMu)
	}

	d, fr, m := ask("What will be the price of Samsung Electronics (SMSN) in US dollars", "< $150", "$150 – $200", "$200 – $250", "≥ $250")
	if m == nil || !m.h1 || m.series.Currency != "USD" || !m.series.Converted() || m.from != "KRW" || !within(m.vol, usdVol, 1e-12) ||
		!within(m.mu, usdMu, 1e-12) || m.cap.Source != kospiUSD.Source || !strings.HasSuffix(m.cap.Source, usdClause) || m.lognormal == nil {
		t.Fatalf("US$ question on a ₩ listing: σ %v (USD %v, KRW %v), drift %v (USD %v, KRW %v), cap %+v", m.vol, usdVol, wonVol, m.mu, usdMu, wonMu, m.cap)
	}
	if strings.Contains(m.text, usdClause) || !strings.Contains(m.text, "): SMSN (KRX 005930 common share, converted from ₩ at Yahoo FX). Yahoo Finance daily closes (NYSE: 005930.KS), split-adjusted") ||
		m.result().Source != "Yahoo Finance daily closes (NYSE: 005930.KS), converted from ₩ at Yahoo FX" {
		t.Fatalf("H1 says the conversion once: %q\n%s", m.result().Source, m.text)
	}
	k := &deskKit{h: h, d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
	for _, sym := range []string{"SMSN", "SKHX", "KOSPI"} {
		out, err := run(t, k.historyTool(newToolLog()), `{"symbol":"`+sym+`","horizon_days":1096,"thresholds":[150,200,250]}`)
		if err != nil || out["history"].(map[string]any)["currency"] != "USD" || !strings.HasSuffix(out["history"].(map[string]any)["source"].(string), usdClause) {
			t.Fatalf("history_stats %s in a US$ question: %v %v", sym, out["history"], err)
		}
	}
	// The index's own windows and drift cap, in dollars like the level they map onto.
	out, _ := run(t, k.historyTool(newToolLog()), `{"symbol":"KOSPI","horizon_days":1096,"thresholds":[150,200,250]}`)
	kospiMu, _ := market.HalvedDrift(kospiUSD)
	_, kospiCap := market.CapDrift(kospiMu, "KOSPI", &kospiUSD)
	if lg := out["lognormal"].(map[string]any); !within(lg["driftCapPct"].(float64), kospiCap.CapPct, 1e-9) || out["driftCapNote"] != nil {
		t.Fatalf("history_stats KOSPI's cap in a US$ question: %v (want %v)", lg, kospiCap.CapPct)
	}

	d, fr, m = ask("What will be the KRX closing price of Samsung Electronics (SMSN)", "< ₩200,000", "₩200,000 – ₩300,000", "₩300,000 – ₩400,000", "≥ ₩400,000")
	if m == nil || !m.h1 || m.series.Currency != "KRW" || m.series.Converted() || m.from != "" || !within(m.vol, wonVol, 1e-12) ||
		!within(m.mu, wonMu, 1e-12) || m.cap.Source != kospiKRW.Source {
		t.Fatalf("₩ question: σ %v, drift %v, cap %+v", m.vol, m.mu, m.cap)
	}
	k = &deskKit{h: h, d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
	for _, sym := range []string{"SKHX", "KOSPI"} {
		if out, err := run(t, k.historyTool(newToolLog()), `{"symbol":"`+sym+`","horizon_days":1096,"thresholds":[200000,300000,400000]}`); err != nil ||
			out["history"].(map[string]any)["currency"] != "KRW" || strings.Contains(out["history"].(map[string]any)["source"].(string), usdClause) {
			t.Fatalf("history_stats %s in a ₩ question: %v %v", sym, out, err)
		}
	}
	// A dollar listing has no won series: its statistics, never its windows on a won level.
	yh.Set(yahootest.Daily("TSM", "USD", synthCloses(1, 29, 0.10, 0.38, 485.12)...))
	if _, err := run(t, k.historyTool(newToolLog()), `{"symbol":"TSM","horizon_days":1096,"thresholds":[200000,300000,400000]}`); err == nil ||
		err.Error() != "TSM's history is in USD, not this question's KRW: its windows cannot map onto the question's level (call it without thresholds for its statistics alone)" {
		t.Fatalf("history_stats TSM's windows on a ₩ level: %v", err)
	}
	if out, err := run(t, k.historyTool(newToolLog()), `{"symbol":"TSM","horizon_days":1096}`); err != nil || out["history"].(map[string]any)["currency"] != "USD" {
		t.Fatalf("history_stats TSM's statistics in a ₩ question: %v %v", out, err)
	}

	// The FX history down: no dollar level, no H1, and history_stats refuses rather than read the won closes.
	yh.SetErr("KRWUSD=X", &yahoo.UnavailableError{Symbol: "KRWUSD=X", Reason: "HTTP 503"})
	d, fr, m = ask("What will be the price of Samsung Electronics (SMSN) in US dollars", "< $150", "$150 – $200", "$200 – $250", "≥ $250")
	if m == nil || m.h1 || m.ref != nil {
		t.Fatalf("FX down: %+v", m)
	}
	k = &deskKit{h: h, d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
	for _, sym := range []string{"SMSN", "SKHX", "KOSPI"} {
		if _, err := run(t, k.historyTool(newToolLog()), `{"symbol":"`+sym+`","horizon_days":1096}`); err == nil || !strings.Contains(err.Error(), "FX history unavailable") {
			t.Fatalf("history_stats %s with the FX history down: %v", sym, err)
		}
	}
}

// TestCapInDriftCurrency: a dollar listing of an Asian company (TSM) has its
// drift capped by its home index in dollars, as the gap round's outlook card
// (the Taiwan dollar here falls 4% a year, so TWII in dollars returned less
// than in TWD and its cap binds lower), on H1 and in history_stats; TWII read
// in a $ question is in dollars too. With the FX history down the ceiling
// alone caps the drift and both say so; history_stats on TWII refuses.
func TestCapInDriftCurrency(t *testing.T) {
	h, yh := measureEnv(t)
	yh.Set(yahootest.Daily("TSM", "USD", synthCloses(1, 29, 0.10, 0.38, 485.12)...))
	twii := synthCloses(3, 30, 0.05, 0.22, 22000)
	yh.Set(yahootest.Daily("^TWII", "TWD", twii...))
	fx := make([]float64, len(twii))
	for i := range fx { // USD per Taiwan dollar: 4% lower a year
		fx[i] = 0.031 * math.Exp(0.04*float64(len(fx)-1-i)/261)
	}
	yh.Set(yahootest.Daily("TWDUSD=X", "USD", fx...))
	ctx := context.Background()
	tsm, err1 := h.d.Market.DailyHistory(ctx, "TSM")
	twiiUSD, err2 := h.d.Market.TickerHistoryUSD(ctx, "TWII")
	twiiTWD, err3 := h.d.Market.TickerHistory(ctx, "TWII")
	if err := errors.Join(err1, err2, err3); err != nil || !twiiUSD.Converted() || twiiTWD.Converted() {
		t.Fatalf("fixtures: %v", err)
	}
	mu0, _ := market.HalvedDrift(tsm)
	usdMu, usdCap := market.CapDrift(mu0, "TWII", &twiiUSD)
	twdMu, _ := market.CapDrift(mu0, "TWII", &twiiTWD)
	if !usdCap.Applied || math.Abs(usdMu-twdMu) < 0.005 {
		t.Fatalf("the fixture must tell the caps apart: %v vs %v", usdMu, twdMu)
	}

	d := tsmDraft()
	fr := frameOf(d, frameDay)
	m := h.measure(ctx, fr, d, frameDay)
	if m == nil || !m.h1 || m.series.Converted() || m.from != "" || !within(m.mu, usdMu, 1e-12) || m.cap.Source != twiiUSD.Source || m.capNote != "" ||
		!strings.Contains(m.text, "capped at the TWII's long-run return + 2 pts") {
		t.Fatalf("TSM's cap: drift %v (USD TWII %v, TWD TWII %v), cap %+v\n%s", m.mu, usdMu, twdMu, m.cap, m.text)
	}
	k := &deskKit{h: h, d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
	out, err := run(t, k.historyTool(newToolLog()), `{"symbol":"TSM","horizon_days":1825,"thresholds":[300,450,600,800,1100]}`)
	if err != nil || !within(out["lognormal"].(map[string]any)["driftCapPct"].(float64), usdCap.CapPct, 1e-9) || out["driftCapNote"] != nil {
		t.Fatalf("history_stats TSM's cap: %v %v (want %v)", out["lognormal"], err, usdCap.CapPct)
	}
	if out, err := run(t, k.historyTool(newToolLog()), `{"symbol":"TWII","horizon_days":1825}`); err != nil ||
		out["history"].(map[string]any)["currency"] != "USD" || out["history"].(map[string]any)["source"] != twiiUSD.Source {
		t.Fatalf("history_stats TWII in a US$ question: %v %v", out, err)
	}

	yh.SetErr("TWDUSD=X", &yahoo.UnavailableError{Symbol: "TWDUSD=X", Reason: "HTTP 503"})
	const note = "the TWII benchmark's USD history is unavailable (no FX history): the drift is capped at the 10%/yr ceiling alone"
	m = h.measure(ctx, fr, d, frameDay)
	if m == nil || !m.h1 || m.cap.Benchmark != "" || m.cap.CapPct != 10 || m.capNote != note || !strings.Contains(m.text, "; "+note) && !strings.Contains(m.text, "Notes: "+note) {
		t.Fatalf("TWD FX down: cap %+v, note %q\n%s", m.cap, m.capNote, m.text)
	}
	k = &deskKit{h: h, d: d, fr: fr, m: m, logs: map[string]*toolLog{}}
	if out, err := run(t, k.historyTool(newToolLog()), `{"symbol":"TSM","horizon_days":1825,"thresholds":[300,450,600,800,1100]}`); err != nil || out["driftCapNote"] != note {
		t.Fatalf("history_stats TSM with the TWD FX down: %v %v", out, err)
	}
	if _, err := run(t, k.historyTool(newToolLog()), `{"symbol":"TWII","horizon_days":1825}`); err == nil || !strings.Contains(err.Error(), "FX history unavailable") {
		t.Fatalf("history_stats TWII with the TWD FX down: %v", err)
	}
}

// TestWindowScale: windows that grew faster than the capped drift are scaled
// down to it; slower ones stand, and no windows means no scaling.
func TestWindowScale(t *testing.T) {
	mu := math.Log(1.094) // TSM's capped drift in the H1 fixture, +9.4%/yr
	for _, c := range []struct {
		median, years, want float64
	}{{2.12, 5, math.Pow(1.094, 5) / 2.12}, {1.2, 5, 1}, {1.094, 1, 1}, {1.5, 1, 1.094 / 1.5}} {
		if got := windowScale(&market.Windows{Q: [7]float64{0, 0, 0, c.median}}, mu, c.years); !within(got, c.want, 1e-12) {
			t.Errorf("median ×%v over %v years: %v, want %v", c.median, c.years, got, c.want)
		}
	}
	if windowScale(nil, mu, 5) != 1 || windowScale(&market.Windows{}, mu, 5) != 1 {
		t.Error("no windows: no scaling")
	}
}

// TestPrompts: event frames keep round 8b's prompts byte for byte (the
// goldens are main's); numeric frames carry their methods; the plan hint
// goes only to the PLAN prompt.
func TestPrompts(t *testing.T) {
	event := frame{Kind: "event"}
	for name, got := range map[string]string{
		"specialist_base_rate":    specialistPrompt(roles[0], event, false, nil),
		"specialist_base_rate_r1": specialistPrompt(roles[0], event, true, nil),
		"specialist_quant":        specialistPrompt(roles[1], event, false, nil),
		"specialist_skeptic":      specialistPrompt(roles[2], event, false, nil),
		"aggregator_binary":       aggregatorPrompt(Draft{Shape: "binary"}, event, false),
		"aggregator_binary_r1":    aggregatorPrompt(Draft{Shape: "binary"}, event, true),
		"aggregator_categorical":  aggregatorPrompt(Draft{Shape: "categorical"}, event, false),
		"aggregator_ordered":      aggregatorPrompt(Draft{Shape: "ordered"}, event, false),
		"plan_system":             planSystem,
	} {
		want, err := os.ReadFile("testdata/prompts/" + name + ".golden")
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s differs from round 8b's prompt", name)
		}
	}
	h, _ := measureEnv(t)
	d := tsmDraft()
	fr := frameOf(d, frameDay)
	m := &measurement{sub: *fr.Subject, h1: true}
	quant, base, skeptic := specialistPrompt(roles[1], fr, false, m), specialistPrompt(roles[0], fr, false, m), specialistPrompt(roles[2], fr, false, m)
	agg := aggregatorPrompt(d, fr, false)
	for _, c := range []struct{ prompt, want string }{
		{quant, "Your job is the fundamentals view: valuation scenarios (EPS × multiple)."},
		{quant, `"median": <the level equally likely to be beaten or missed, in the question's unit>, "reasoning"`},
		{quant, "A near-equal spread across the outcomes is a claim too (that every range is about equally likely)"},
		{quant, "- This question asks for a number, split into ordered ranges."},
		{base, "Method for this question: historical simulation, the outside view from measured history."},
		{base, "Adjust for that as far as the evidence supports and say by how many points."},
		{skeptic, "Method for this question: regime and tail risk."},
		{skeptic, "Your value is pricing the regimes, catalysts and tails others ignore, in both directions"},
		{agg, "How to resolve the views (this question asks for a number in ordered ranges):"},
		{agg, `"weights": [{"role": "base_rate" | "quant" | "skeptic", "weight": <number>, "why": "<one line>"}],`},
		{agg, "Never describe the process (the ensemble, the pool, the panel, averaging, specialists, methods, 'we stay close to')"},
		{agg, "On past price questions this desk lost to a volatility-only formula by pulling its numbers toward 50%; do not repeat that."},
	} {
		if !strings.Contains(c.prompt, c.want) {
			t.Errorf("numeric prompt lacks %q", c.want)
		}
	}
	for _, p := range []string{quant, base, skeptic, agg} {
		for _, gone := range []string{"these tools cannot price it", "Moving toward 50% is a claim too", "%!", "only modestly", "asset class's typical volatility"} {
			if strings.Contains(p, gone) {
				t.Errorf("numeric prompt says %q", gone)
			}
		}
	}
	if !strings.Contains(planHint(fr), `The "drivers" angle must find: consensus EPS`) || planHint(event) != "" || planHint(frameOf(btcLong(), frameDay)) != "" ||
		strings.Contains(planSystem, "consensus EPS") {
		t.Fatal("the plan hint: long-horizon listed stocks only, never in planSystem")
	}
	cpi := Draft{Shape: "ordered", Question: "What will US CPI inflation (YoY) be for March 2027?", ResolutionDate: "2027-04-15",
		Outcomes: []string{"< 0%", "0% – 2%", "2% – 3%", "≥ 3%"}}
	level := frameOf(cpi, frameDay)
	if q := specialistPrompt(roles[1], level, false, nil); !strings.Contains(q, "Anchor on market-implied levels in the research") || strings.Contains(q, "Binance data tools") {
		t.Fatal("a level frame's quant anchors on market-implied levels, without tools it does not have")
	}
	k := &deskKit{h: h, d: cpi, fr: level, logs: map[string]*toolLog{}}
	for _, r := range roles {
		if st := k.seat(r, nil, false); st.tools != nil {
			t.Errorf("a level frame's %s has no tools", r.ID)
		}
	}
}

func btcLong() Draft {
	return Draft{Shape: "ordered", Question: "Where will the Binance BTC/USDT daily close be on 2028-10-14?", ResolutionDate: "2028-10-14",
		Outcomes: []string{"< $60,000", "$60,000 – $65,000", "$65,000 – $70,000", "≥ $70,000"}}
}
