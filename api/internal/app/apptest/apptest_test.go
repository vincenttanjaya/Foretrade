package apptest_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

// The env's session must pass the real auth.Require, bodies must arrive as
// JSON, and every fake must be wired into the deps.
func TestEnv(t *testing.T) {
	e := apptest.New(t, func(mux *http.ServeMux, d *app.Deps) {
		mux.Handle("POST /echo", d.Auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := httpx.Decode(w, r, &body); err != nil {
				app.WriteError(w, &app.BadRequest{Msg: err.Error()})
				return
			}
			text, err := d.LLM.Complete(r.Context(), "", "ping", llm.Low)
			if err != nil {
				app.WriteError(w, err)
				return
			}
			price, _ := d.Market.Price(r.Context(), "BTC")
			httpx.JSON(w, http.StatusOK, map[string]any{"user": httpx.UserID(r.Context()), "body": body, "ai": text,
				"price": price, "search": d.Research.Enabled()})
		})))
	}, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: "pong"}), Brave: func(w http.ResponseWriter, r *http.Request) {}})

	w := e.Do("POST", "/echo", map[string]int{"n": 1})
	got := apptest.JSON[struct {
		User   string
		Body   map[string]int
		AI     string
		Price  float64
		Search bool
	}](t, w)
	if w.Code != 200 || got.User != e.User || got.Body["n"] != 1 || got.AI != "pong" || got.Price != 100_000 || !got.Search {
		t.Fatalf("echo: %d %+v", w.Code, got)
	}
	if w := e.DoAs(nil, "POST", "/echo", "{}"); w.Code != 401 || apptest.ErrorCode(t, w) != "unauthorized" {
		t.Fatalf("anonymous: %d %s", w.Code, w.Body)
	}
	other := storetest.User(t, e.DB)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if w := e.DoCtx(ctx, e.Session(other), "POST", "/echo", "not json"); w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" {
		t.Fatalf("second session, bad body: %d %s", w.Code, w.Body)
	}
	if n := len(e.Bedrock.Requests()); n != 1 {
		t.Fatalf("bedrock calls: %d", n)
	}
}
