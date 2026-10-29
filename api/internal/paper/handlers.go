package paper

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
)

// Register mounts the paper-trading endpoints behind require (the session check).
func (e *Engine) Register(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	mux.Handle("GET /api/portfolio", require(http.HandlerFunc(e.handlePortfolio)))
	mux.Handle("GET /api/portfolio/trades", require(http.HandlerFunc(e.handleTrades)))
	mux.Handle("POST /api/orders", require(http.HandlerFunc(e.handleOrder)))
	mux.Handle("POST /api/portfolio/close", require(http.HandlerFunc(e.handleClose)))
	mux.Handle("POST /api/portfolio/reset", require(http.HandlerFunc(e.handleReset)))
	mux.Handle("PATCH /api/portfolio/positions/{symbol}", require(http.HandlerFunc(e.handleProtect)))
}

func (e *Engine) handlePortfolio(w http.ResponseWriter, r *http.Request) {
	p, err := e.Portfolio(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"portfolio": p})
}

func (e *Engine) handleTrades(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 200 {
			httpx.Error(w, http.StatusBadRequest, "bad_request", "limit must be 1..200.")
			return
		}
		limit = n
	}
	trades, err := e.Trades(r.Context(), httpx.UserID(r.Context()), limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"trades": trades})
}

func (e *Engine) handleOrder(w http.ResponseWriter, r *http.Request) {
	var req OrderRequest
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	t, p, err := e.Order(r.Context(), httpx.UserID(r.Context()), req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"trade": t, "portfolio": p})
}

func (e *Engine) handleClose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Symbol string `json:"symbol"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	t, p, err := e.Close(r.Context(), httpx.UserID(r.Context()), req.Symbol, "manual")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"trade": t, "portfolio": p})
}

func (e *Engine) handleReset(w http.ResponseWriter, r *http.Request) {
	p, err := e.Reset(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"portfolio": p})
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ve *ValidationError
	var halted *HaltedError
	switch {
	case errors.As(err, &ve):
		httpx.Error(w, http.StatusBadRequest, "bad_request", ve.Msg)
	case errors.As(err, &halted):
		httpx.Error(w, http.StatusConflict, "conflict", halted.Error())
	case errors.Is(err, ErrInsufficient):
		httpx.Error(w, http.StatusBadRequest, "insufficient_buying_power",
			"Not enough buying power: gross exposure may not exceed 2× equity.")
	case errors.Is(err, ErrNoPosition):
		httpx.Error(w, http.StatusNotFound, "not_found", "No open position in that symbol.")
	case errors.Is(err, ErrProposalFilled):
		httpx.Error(w, http.StatusConflict, "conflict", "This proposal was already filled.")
	case errors.Is(err, ErrLevelsChanged):
		httpx.Error(w, http.StatusConflict, "conflict",
			"This position's stop or target changed since the card was drafted. Ask the agent for fresh levels.")
	case errors.Is(err, market.ErrUpstream):
		slog.Warn("paper upstream", "path", r.URL.Path, "err", err)
		httpx.Error(w, http.StatusBadGateway, "upstream", "Market data is unavailable right now. Try again shortly.")
	default:
		httpx.ServerError(w, r, err)
	}
}
