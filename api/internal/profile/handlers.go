package profile

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
)

func Register(mux *http.ServeMux, db *pgxpool.Pool, require func(http.Handler) http.Handler) {
	h := handler{db}
	mux.Handle("GET /api/me/profile", require(http.HandlerFunc(h.get)))
	mux.Handle("PUT /api/me/profile", require(http.HandlerFunc(h.put)))
	mux.Handle("POST /api/me/profile/memories", require(http.HandlerFunc(h.confirm)))
	mux.Handle("DELETE /api/me/profile/memories/{id}", require(http.HandlerFunc(h.delete)))
}

type handler struct{ db *pgxpool.Pool }

func replyError(w http.ResponseWriter, r *http.Request, err error) {
	var bad *BadRequest
	switch {
	case errors.As(err, &bad):
		httpx.Error(w, 400, "bad_request", bad.Message)
	case errors.Is(err, ErrConflict):
		httpx.Error(w, 409, "conflict", ErrConflict.Error())
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, 404, "not_found", ErrNotFound.Error())
	default:
		httpx.ServerError(w, r, err)
	}
}
func (h handler) get(w http.ResponseWriter, r *http.Request) {
	p, err := Load(r.Context(), h.db, httpx.UserID(r.Context()))
	if err != nil {
		replyError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"profile": p})
}

// decodeObject checks required fields and types before decoding the typed DTO.
// In particular, JSON null cannot silently become a false/zero/empty value.
func decodeObject(w http.ResponseWriter, r *http.Request, dest any, fields []string, nullable map[string]bool) error {
	var raw json.RawMessage
	if err := httpx.Decode(w, r, &raw); err != nil {
		return &BadRequest{err.Error()}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return &BadRequest{"Body must be a JSON object."}
	}
	if len(obj) != len(fields) {
		return &BadRequest{"Body must contain exactly the documented fields."}
	}
	for _, key := range fields {
		value, ok := obj[key]
		if !ok || (!nullable[key] && strings.TrimSpace(string(value)) == "null") {
			return &BadRequest{"Missing or null field: " + key + "."}
		}
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return &BadRequest{"Invalid field type."}
	}
	return nil
}
func (h handler) put(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Preferences
		Revision int64 `json:"revision"`
	}
	err := decodeObject(w, r, &req, []string{"enabled", "riskProfile", "style", "focusSymbols", "customPrompt", "revision"}, map[string]bool{"riskProfile": true, "style": true})
	if err == nil && req.Revision < 0 {
		err = &BadRequest{"revision must be a nonnegative integer."}
	}
	if err != nil {
		replyError(w, r, err)
		return
	}
	p, err := Update(r.Context(), h.db, httpx.UserID(r.Context()), req.Preferences, req.Revision)
	if err != nil {
		replyError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"profile": p})
}
func (h handler) confirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProposalID string `json:"proposalId"`
		Text       string `json:"text"`
	}
	err := decodeObject(w, r, &req, []string{"proposalId", "text"}, nil)
	if err == nil && !validID(req.ProposalID) {
		err = &BadRequest{"proposalId must be a UUID."}
	}
	if err == nil {
		err = ValidateMemory(req.Text)
	}
	if err != nil {
		replyError(w, r, err)
		return
	}
	m, p, err := Confirm(r.Context(), h.db, httpx.UserID(r.Context()), req.ProposalID, req.Text)
	if err != nil {
		replyError(w, r, err)
		return
	}
	httpx.JSON(w, 201, map[string]any{"memory": m, "profile": p})
}
func (h handler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		replyError(w, r, ErrNotFound)
		return
	}
	p, err := Delete(r.Context(), h.db, httpx.UserID(r.Context()), id)
	if err != nil {
		replyError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"profile": p})
}
func validID(id string) bool { _, err := uuid.Parse(id); return len(id) == 36 && err == nil }
