package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/aipf"
	"github.com/vincenttanjaya/Foretrade/api/internal/forecast"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

// Chat actions: cards that run a forecast, build an AI portfolio or backtest a
// strategy when the user taps them. Only draft_forecast calls a model here (the
// forecast desk's draft, 1 credit); the runs and builds spend their own
// endpoint's credits on the tap.

// buildAction is the build_action card: the brief the web posts on Build.
type buildAction struct {
	Kind     string       `json:"kind"` // portfolio | strategy
	Prompt   string       `json:"prompt"`
	Symbol   *string      `json:"symbol"`
	Interval *string      `json:"interval"`
	Cost     int          `json:"cost"`
	Result   *buildResult `json:"result"` // null in the stream; GET /api/chats/{id} fills it once built
}

// buildResult is what the card shows of the portfolio or strategy it built.
type buildResult struct {
	BenchmarkSymbol    *string   `json:"benchmarkSymbol"`
	BenchmarkLabel     *string   `json:"benchmarkLabel"`
	WindowStart        *int64    `json:"windowStart"`
	WindowEnd          *int64    `json:"windowEnd"`
	Bars               *int      `json:"bars"`
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	ReturnPct          float64   `json:"returnPct"`
	BenchmarkReturnPct float64   `json:"benchmarkReturnPct"` // saved BTC/SP500 for a portfolio; buy & hold for a strategy
	Sharpe             *float64  `json:"sharpe"`             // strategies only
	MaxDrawdownPct     float64   `json:"maxDrawdownPct"`
	Holdings           []holding `json:"holdings"` // portfolios only; [] for a strategy
}

type holding struct {
	Symbol string  `json:"symbol"`
	Weight float64 `json:"weight"`
}

// draftRun is the forecast_draft card's forecast: the run started from it.
// A completed ordered run adds its view brief and, when the view leads, its
// reading (forecast.ViewHeadline: "median $611, 62% chance at or above
// today's $485.12"), which the card states in place of the top range.
type draftRun struct {
	ID           string              `json:"id"`
	Status       string              `json:"status"`
	Headline     *outcomeP           `json:"headline"` // null until completed
	View         *forecast.ViewBrief `json:"view,omitempty"`
	ViewHeadline string              `json:"viewHeadline,omitempty"`
}

func (a *agentRun) draftForecast(ctx context.Context, input json.RawMessage) (string, error) {
	if a.withAttach {
		return "", errors.New("forecast drafting is unavailable during a message with attachments; use the ordinary Forecasts workflow")
	}
	var in struct{ Idea string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	idea := strings.Join(strings.Fields(in.Idea), " ")
	switch n := utf8.RuneCountInString(idea); {
	case n < 3 || n > 400:
		return "", errors.New("idea must be 3..400 characters")
	case a.drafted:
		return "", errors.New("one forecast draft per message; the card is already shown")
	}
	a.drafted = true
	credits, err := a.d.Usage.Spend(ctx, a.userID, usage.CostDraft)
	switch {
	case errors.Is(err, usage.ErrOwnerOnly):
		return "", errors.New("AI is in owner-only mode on this server: only admins can draft forecasts.")
	case errors.Is(err, usage.ErrQuota):
		return "", errors.New("Out of AI credits for today: the forecast desk needs 1 to draft and 5 to run.")
	case err != nil:
		return "", toolErr(err)
	}
	a.credits = &credits
	d, err := forecast.DraftIdea(ctx, a.d, idea)
	if err != nil {
		if ctx.Err() == nil { // as POST /api/forecasts/draft: a client that hung up still pays
			a.d.Usage.Refund(ctx, a.userID, usage.CostDraft)
			a.credits = nil
		}
		slog.Warn("chat draft_forecast", "err", err)
		return "", errors.New("the forecast desk couldn't draft this right now; suggest the Forecasts page")
	}
	a.card = &Card{Type: "forecast_draft", Data: map[string]any{"draft": d, "forecast": nil}}
	a.summary = "Drafted: " + clip(d.Question, 60)
	return toJSON(map[string]any{"draft": d,
		"status": "Card shown. The desk runs only when the user taps Run (5 credits, usually four to eight minutes). Do not describe results."})
}

func (a *agentRun) buildPortfolio(_ context.Context, input json.RawMessage) (string, error) {
	var in struct{ Prompt string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if n := utf8.RuneCountInString(prompt); n < 3 || n > 500 {
		return "", errors.New("prompt must be 3..500 characters")
	}
	a.card = &Card{Type: "build_action", Data: buildAction{Kind: "portfolio", Prompt: prompt, Cost: usage.CostPortfolio}}
	a.summary = "Portfolio card ready"
	return "Card shown; it builds (2 credits) when the user taps Build. Do not invent holdings or returns.", nil
}

func (a *agentRun) backtestStrategy(_ context.Context, input json.RawMessage) (string, error) {
	var in struct{ Prompt, Symbol, Interval string }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}
	card := buildAction{Kind: "strategy", Prompt: strings.TrimSpace(in.Prompt), Cost: usage.CostStrategy}
	if n := utf8.RuneCountInString(card.Prompt); n < 5 || n > 1000 {
		return "", errors.New("prompt must be 5..1000 characters")
	}
	if in.Symbol != "" {
		symbol, err := lookup(in.Symbol)
		if err != nil {
			return "", err
		}
		card.Symbol = &symbol
	}
	if in.Interval != "" {
		if !market.ValidInterval(in.Interval) {
			return "", errors.New("interval must be one of 15m, 1h, 4h, 1d")
		}
		card.Interval = &in.Interval
	}
	a.card, a.summary = &Card{Type: "build_action", Data: card}, "Strategy card ready"
	return "Card shown; it generates and backtests (2 credits) when the user taps it. Do not invent rules, returns or metrics.", nil
}

// actionStatus fills in what became of each stored action card, as
// proposalStatus does for trade cards: a forecast_draft card gets the newest
// forecast run from its question, a build_action card the first portfolio or
// strategy built from its prompt, each created since the card could be tapped.
// ponytail: matched by text, one query per card; an identical later prompt
// links to the first build. Upgrade = store an actionId on the entity.
func (h handler) actionStatus(ctx context.Context, user string, msgs []message) error {
	for i, m := range msgs {
		if !bytes.Contains(m.Cards, []byte(`"forecast_draft"`)) && !bytes.Contains(m.Cards, []byte(`"build_action"`)) {
			continue
		}
		var cards []map[string]any
		if json.Unmarshal(m.Cards, &cards) != nil {
			continue
		}
		// The card shows mid-answer, but the assistant message is stored when the
		// answer ends: count from the user's message before it.
		since := m.CreatedAt
		if i > 0 {
			since = msgs[i-1].CreatedAt
		}
		for _, c := range cards {
			data, _ := c["data"].(map[string]any)
			var err error
			switch {
			case data == nil:
			case c["type"] == "forecast_draft":
				draft, _ := data["draft"].(map[string]any)
				question, _ := draft["question"].(string)
				data["forecast"], err = h.draftRun(ctx, user, question, since)
			case c["type"] == "build_action":
				kind, _ := data["kind"].(string)
				prompt, _ := data["prompt"].(string)
				data["result"], err = h.buildResult(ctx, user, kind, prompt, since)
			}
			if err != nil {
				return err
			}
		}
		var err error
		if msgs[i].Cards, err = json.Marshal(cards); err != nil {
			return err
		}
	}
	return nil
}

func (h handler) draftRun(ctx context.Context, user, question string, since time.Time) (*draftRun, error) {
	var r draftRun
	var shape string
	var probs []outcomeP
	var outcomes []string
	var view json.RawMessage
	var date time.Time
	var completed *time.Time
	err := h.d.DB.QueryRow(ctx, `
		select id, status, shape, coalesce(result->'probabilities', '[]'), outcomes, result->'view', resolution_date, completed_at from forecasts
		where user_id = $1 and question = $2 and created_at >= $3 and status <> 'archived'
		order by created_at desc limit 1`, user, question, since).Scan(&r.ID, &r.Status, &shape, &probs, &outcomes, &view, &date, &completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(probs) > 0 {
		hl := headline(shape, probs)
		r.Headline = &hl
		ref := newRef(r.ID, question, shape, probs, nil, deskOf(shape, outcomes, probs, view, date.Format(time.DateOnly), completed))
		r.View, r.ViewHeadline = ref.View, ref.read
	}
	return &r, nil
}

func (h handler) buildResult(ctx context.Context, user, kind, prompt string, since time.Time) (*buildResult, error) {
	var r buildResult
	var err error
	switch kind {
	case "portfolio":
		var bt aipf.Backtest
		err = h.d.DB.QueryRow(ctx, `
			select id, name, holdings, backtest from ai_portfolios
			where user_id = $1 and prompt = $2 and created_at >= $3 order by created_at limit 1`, user, prompt, since).Scan(
			&r.ID, &r.Name, &r.Holdings, &bt)
		r.ReturnPct, r.BenchmarkReturnPct, r.MaxDrawdownPct = bt.ReturnPct, bt.BenchmarkReturnPct, bt.MaxDrawdownPct
		if bt.BenchmarkSymbol == "" {
			bt.BenchmarkSymbol = "BTC"
		}
		if bt.BenchmarkLabel == "" {
			if bt.BenchmarkSymbol == "BTC" {
				bt.BenchmarkLabel = "BTC"
			} else if bt.BenchmarkSymbol == "SP500" {
				bt.BenchmarkLabel = "S&P 500 perp"
			}
		}
		r.BenchmarkSymbol, r.BenchmarkLabel = &bt.BenchmarkSymbol, &bt.BenchmarkLabel
		if bt.Bars == 0 && len(bt.Series) > 0 {
			bt.Bars = len(bt.Series)
			bt.WindowStart = bt.Series[0].T
			bt.WindowEnd = bt.Series[len(bt.Series)-1].T
		}
		if bt.Bars > 0 {
			r.WindowStart, r.WindowEnd, r.Bars = &bt.WindowStart, &bt.WindowEnd, &bt.Bars
		}
	case "strategy":
		var m struct{ TotalReturnPct, BuyHoldReturnPct, Sharpe, MaxDrawdownPct float64 }
		err = h.d.DB.QueryRow(ctx, `
			select id, name, metrics from strategies
			where user_id = $1 and prompt = $2 and created_at >= $3 order by created_at limit 1`, user, prompt, since).Scan(
			&r.ID, &r.Name, &m)
		r.ReturnPct, r.BenchmarkReturnPct, r.Sharpe, r.MaxDrawdownPct, r.Holdings = m.TotalReturnPct, m.BuyHoldReturnPct, &m.Sharpe, m.MaxDrawdownPct, []holding{}
	default:
		return nil, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
