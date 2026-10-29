package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// WriteError maps every package error to the contract's status and code,
// also when wrapped, and never leaks an internal error's text.
func TestWriteError(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("handler: %w", err) }
	for _, tc := range []struct {
		err    error
		status int
		code   string
		msg    string
	}{
		{&BadRequest{Msg: "symbol is required"}, 400, "bad_request", "symbol is required"},
		{wrap(&Conflict{Msg: "This run is not running."}), 409, "conflict", "This run is not running."},
		{wrap(&paper.ValidationError{Msg: "side must be buy or sell"}), 400, "bad_request", "side must be buy or sell"},
		{wrap(&paper.HaltedError{Symbol: "TON"}), 409, "conflict", "TON is halted on Binance, so new orders are paused"},
		{wrap(usage.ErrQuota), 429, "ai_quota", "AI credits"},
		{wrap(usage.ErrOwnerOnly), 403, "ai_owner_only", "owner-only mode"},
		{wrap(paper.ErrInsufficient), 400, "insufficient_buying_power", "buying power"},
		{ErrNotFound, 404, "not_found", "Not found."},
		{wrap(ErrForbidden), 403, "forbidden", "Admins only."},
		{wrap(pgx.ErrNoRows), 404, "not_found", "Not found."},
		{wrap(paper.ErrNoPosition), 404, "not_found", "Not found."},
		{fmt.Errorf("%w: binance 500", market.ErrUpstream), 502, "upstream", "Market data"},
		{fmt.Errorf("%w: bedrock throttled", llm.ErrUpstream), 502, "upstream", "AI model"},
		{wrap(&yahoo.UnavailableError{Symbol: "D05.SI", Reason: "rate limited by Yahoo"}), 502, "upstream", "Market data"},
		{wrap(&yahoo.UnavailableError{Symbol: "SM.PS", Reason: "not found on Yahoo", NotFound: true}), 404, "not_found", "Not found."},
		{errors.New("pq: password=hunter2 rejected"), 500, "internal", "Something went wrong."},
	} {
		w := httptest.NewRecorder()
		WriteError(w, tc.err)
		var body struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: body %q", tc.err, w.Body)
		}
		if w.Code != tc.status || body.Error.Code != tc.code || !strings.Contains(body.Error.Message, tc.msg) ||
			strings.Contains(w.Body.String(), "hunter2") {
			t.Errorf("%v → %d %s, want %d %s containing %q", tc.err, w.Code, w.Body, tc.status, tc.code, tc.msg)
		}
	}

	w := httptest.NewRecorder()
	WriteError(w, fmt.Errorf("query: %w", context.Canceled))
	if w.Code != 499 || w.Body.Len() != 0 {
		t.Fatalf("canceled request: %d %q", w.Code, w.Body)
	}
	if (&BadRequest{Msg: "x"}).Error() != "x" || (&Conflict{Msg: "y"}).Error() != "y" {
		t.Fatal("BadRequest.Error / Conflict.Error")
	}
}

func TestYahooSourceIsNilSafe(t *testing.T) {
	var nilDeps *Deps
	for _, d := range []*Deps{nilDeps, {}} {
		if _, err := d.YahooSource().Quote(context.Background(), "D05.SI"); !errors.Is(err, yahoo.ErrUnavailable) {
			t.Fatalf("%v", err)
		}
	}
	c := yahoo.New(yahoo.Config{})
	if (&Deps{Yahoo: c}).YahooSource() != c {
		t.Fatal("a wired client is used")
	}
}

// AIOff is true only for a wired client without a provider.
func TestAIOff(t *testing.T) {
	off, err := llm.NewFromSettings(llm.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	var nilDeps *Deps
	if nilDeps.AIOff() || (&Deps{}).AIOff() || !(&Deps{LLM: off}).AIOff() {
		t.Fatal("AIOff: nil deps, no client, unconfigured client")
	}
}
