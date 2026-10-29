package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vincenttanjaya/Foretrade/api/internal/aipf"
	"github.com/vincenttanjaya/Foretrade/api/internal/alerts"
	"github.com/vincenttanjaya/Foretrade/api/internal/autopilot"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

func (a *agentRun) accountTools() []llm.Tool {
	return []llm.Tool{
		{Name: "get_trades", Description: "Read your newest owned paper fills, entry price, quantity, fees, realized P&L and verified origin; optional symbol is filtered before the limit. No action or extra credits.", Schema: schema(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20}, "symbol": map[string]any{"type": "string", "enum": symbols()}}), Run: a.accountTrades},
		{Name: "get_pnl_attribution", Description: "Read complete since-reset FIFO P&L by source, including shared manual/copy lots and settled outcomes. Optional selected-range equity summary is separate from attribution. No action or extra credits.", Schema: schema(map[string]any{"range": map[string]any{"type": "string", "enum": []string{"1d", "7d", "30d"}}}), Run: a.accountPnl},
		{Name: "get_automations", Description: "Read up to 20 of your active price, schedule and odds automations with bounded latest run. No create, cancel, execute or extra credits.", Schema: schema(map[string]any{}), Run: a.accountAutomations},
		{Name: "get_autopilots", Description: "Read your newest 10 running or stopped paper autopilots, their own-book nullable valuation and latest five logs. No deploy, stop or extra credits.", Schema: schema(map[string]any{}), Run: a.accountAutopilots},
		{Name: "get_ai_portfolios", Description: "Read your newest 10 actually invested AI portfolios since reset, nullable live FIFO valuation and persisted BTC/SP500 benchmark comparison. No build, invest or extra credits.", Schema: schema(map[string]any{}), Run: a.accountPortfolios},
	}
}

// Parse an object, including empty objects; identity-looking unknown fields
// never enter a helper. Null/arrays/malformed inputs are not valid arguments.
func accountArgs(input json.RawMessage) (map[string]json.RawMessage, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(input, &args); err != nil || args == nil {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	return args, nil
}

func readText(s string, limit int, truncated *bool) string {
	if utf8.RuneCountInString(s) > limit {
		*truncated = true
		s = clip(s, limit)
	}
	if s == "" {
		return ""
	}
	return research.Untrusted(s)
}

func (a *agentRun) accountTrades(ctx context.Context, input json.RawMessage) (string, error) {
	args, err := accountArgs(input)
	if err != nil {
		return "", err
	}
	limit, symbol := 20, ""
	if raw, ok := args["limit"]; ok {
		if err := json.Unmarshal(raw, &limit); err != nil || strings.TrimSpace(string(raw)) == "null" || limit < 1 || limit > 50 {
			return "", fmt.Errorf("limit must be an integer from 1 to 50")
		}
	}
	if raw, ok := args["symbol"]; ok {
		if err := json.Unmarshal(raw, &symbol); err != nil || strings.TrimSpace(symbol) == "" {
			return "", fmt.Errorf("symbol must be a configured ticker")
		}
		info, ok := market.Lookup(symbol)
		if !ok {
			return "", fmt.Errorf("symbol must be a configured ticker")
		}
		symbol = info.Symbol
	}
	trades, err := a.d.Paper.TradesFiltered(ctx, a.userID, paper.TradeFilter{Limit: limit + 1, Symbol: symbol})
	if err != nil {
		return "", err
	}
	truncated := len(trades) > limit
	trades = trades[:min(len(trades), limit)]
	for i := range trades {
		trades[i].Note = readText(trades[i].Note, 200, &truncated)
		if trades[i].Ref != nil {
			r := *trades[i].Ref
			r.Label = readText(r.Label, 120, &truncated)
			trades[i].Ref = &r
		}
	}
	assets := map[string]bool{}
	for _, t := range trades {
		assets[t.Symbol] = true
	}
	a.summary = fmt.Sprintf("Last %d fills · %d symbols", len(trades), len(assets))
	if len(trades) == 0 {
		a.summary = "No paper fills"
	}
	return toJSON(struct {
		Trades    []paper.Trade `json:"trades"`
		Truncated bool          `json:"truncated"`
	}{trades, truncated})
}

type equitySummary struct {
	Scope          string   `json:"scope"`
	Range          string   `json:"range"`
	StartEquity    float64  `json:"startEquity"`
	EndEquity      float64  `json:"endEquity"`
	ReturnPct      *float64 `json:"returnPct"`
	MaxDrawdownPct float64  `json:"maxDrawdownPct"`
}

func (a *agentRun) accountPnl(ctx context.Context, input json.RawMessage) (string, error) {
	args, err := accountArgs(input)
	if err != nil {
		return "", err
	}
	rng := ""
	if raw, ok := args["range"]; ok {
		if err := json.Unmarshal(raw, &rng); err != nil || (rng != "1d" && rng != "7d" && rng != "30d") {
			return "", fmt.Errorf("range must be 1d, 7d or 30d")
		}
	}
	attr, curve, err := a.d.Paper.AttributionWithEquity(ctx, a.userID, rng, time.Now())
	if err != nil {
		return "", err
	}
	var summary *equitySummary
	if rng != "" {
		summary = &equitySummary{Scope: "selected_range", Range: rng, StartEquity: curve.Points[0].V, EndEquity: curve.Points[len(curve.Points)-1].V}
		if summary.StartEquity > 0 {
			summary.ReturnPct = new(round2((summary.EndEquity/summary.StartEquity - 1) * 100))
		}
		peak := summary.StartEquity
		for _, p := range curve.Points {
			peak = max(peak, p.V)
			if peak > 0 {
				summary.MaxDrawdownPct = max(summary.MaxDrawdownPct, (peak-p.V)/peak*100)
			}
		}
		summary.MaxDrawdownPct = round2(summary.MaxDrawdownPct)
	}
	a.summary = "Since-reset P&L by source"
	return toJSON(struct {
		Attribution struct {
			Scope string `json:"scope"`
			paper.Attribution
		} `json:"attribution"`
		EquitySummary *equitySummary `json:"equitySummary,omitempty"`
	}{Attribution: struct {
		Scope string `json:"scope"`
		paper.Attribution
	}{"since_reset", attr}, EquitySummary: summary})
}

func (a *agentRun) accountAutomations(ctx context.Context, input json.RawMessage) (string, error) {
	if _, err := accountArgs(input); err != nil {
		return "", err
	}
	rows, err := alerts.List(ctx, a.d, a.userID, "active", 21)
	if err != nil {
		return "", err
	}
	truncated := len(rows) > 20
	rows = rows[:min(len(rows), 20)]
	for i := range rows {
		r := &rows[i]
		r.Note = readText(r.Note, 200, &truncated)
		if r.Error != nil {
			r.Error = new(readText(*r.Error, 160, &truncated))
		}
		if r.LastRun != nil {
			v := *r.LastRun
			v.Body = readText(v.Body, 300, &truncated)
			if v.Error != nil {
				v.Error = new(readText(*v.Error, 160, &truncated))
			}
			r.LastRun = &v
		}
		// Rich market documents/URLs are not needed to review an automation.
		// Preserve its numeric odds and bounded question, not navigation authority.
		if r.LinkedMarket != nil {
			truncated = true
			r.LinkedMarket = nil
		}
		if r.Market != nil {
			v := *r.Market
			v.Question = readText(v.Question, 120, &truncated)
			if v.URL != "" {
				truncated = true
				v.URL = ""
			}
			if v.Outcome != nil {
				v.Outcome = new(readText(*v.Outcome, 120, &truncated))
			}
			r.Market = &v
		}
	}
	a.summary = fmt.Sprintf("%d active automations", len(rows))
	return toJSON(struct {
		Automations []alerts.Alert `json:"automations"`
		Truncated   bool           `json:"truncated"`
	}{rows, truncated})
}

func (a *agentRun) accountAutopilots(ctx context.Context, input json.RawMessage) (string, error) {
	if _, err := accountArgs(input); err != nil {
		return "", err
	}
	rows, err := autopilot.List(ctx, a.d, a.userID, 11)
	if err != nil {
		return "", err
	}
	truncated := len(rows) > 10
	rows = rows[:min(len(rows), 10)]
	for i := range rows {
		r := &rows[i]
		r.Name = readText(r.Name, 120, &truncated)
		if len(r.Log) > 5 {
			truncated = true
			r.Log = r.Log[:5]
		}
		for j := range r.Log {
			r.Log[j].Text = readText(r.Log[j].Text, 200, &truncated)
		}
	}
	a.summary = fmt.Sprintf("%d paper autopilots", len(rows))
	return toJSON(struct {
		Autopilots []autopilot.Autopilot `json:"autopilots"`
		Truncated  bool                  `json:"truncated"`
	}{rows, truncated})
}

type investedRead struct {
	Amount             float64   `json:"amount"`
	Trades             int       `json:"trades"`
	FirstAt            time.Time `json:"firstAt"`
	RealizedPnl        float64   `json:"realizedPnl"`
	UnrealizedPnl      float64   `json:"unrealizedPnl"`
	TotalPnl           float64   `json:"totalPnl"`
	ReturnPct          float64   `json:"returnPct"`
	BenchmarkSymbol    string    `json:"benchmarkSymbol"`
	BenchmarkLabel     string    `json:"benchmarkLabel"`
	BenchmarkReturnPct *float64  `json:"benchmarkReturnPct"`
	BtcReturnPct       *float64  `json:"btcReturnPct"`
}

func investedProjection(v *aipf.Invested) *investedRead {
	if v == nil {
		return nil
	}
	return &investedRead{v.Amount, v.Trades, v.FirstAt, v.RealizedPnl, v.UnrealizedPnl, v.TotalPnl, v.ReturnPct, v.BenchmarkSymbol, v.BenchmarkLabel, v.BenchmarkReturnPct, v.BtcReturnPct}
}

func (a *agentRun) accountPortfolios(ctx context.Context, input json.RawMessage) (string, error) {
	if _, err := accountArgs(input); err != nil {
		return "", err
	}
	rows, err := aipf.ListInvested(ctx, a.d, a.userID, 11)
	if err != nil {
		return "", err
	}
	truncated := len(rows) > 10
	rows = rows[:min(len(rows), 10)]
	type portfolioRead struct {
		ID              string        `json:"id"`
		Name            string        `json:"name"`
		CreatedAt       time.Time     `json:"createdAt"`
		BenchmarkSymbol string        `json:"benchmarkSymbol"`
		BenchmarkLabel  string        `json:"benchmarkLabel"`
		Invested        *investedRead `json:"invested"`
	}
	out := make([]portfolioRead, len(rows))
	for i, p := range rows {
		out[i] = portfolioRead{p.ID, readText(p.Name, 120, &truncated), p.CreatedAt, p.Backtest.BenchmarkSymbol, p.Backtest.BenchmarkLabel, investedProjection(p.Invested)}
	}
	a.summary = fmt.Sprintf("%d invested AI portfolios", len(rows))
	return toJSON(struct {
		Portfolios []portfolioRead `json:"portfolios"`
		Truncated  bool            `json:"truncated"`
	}{out, truncated})
}
