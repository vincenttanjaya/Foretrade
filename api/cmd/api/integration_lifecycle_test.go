package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"

	"github.com/vincenttanjaya/Foretrade/api/internal/config"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo/yahootest"
)

// Cancellation is a request to stop, not a completion receipt. The actual
// paper watcher returns on cancellation, then its owner is held at return.
func TestIntegrationLifecycleDrain(t *testing.T) {
	t.Run("image_client_disconnect_still_persists", func(t *testing.T) {
		entered, release, exited, saved := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		open := sync.OnceFunc(func() { close(release) })
		o := runOptions{event: func(e string) {
			if e == "task_done:chat" {
				close(saved)
			}
		}}
		s := boot(t, options{runtime: &o, model: func(llmtest.Request) llmtest.Reply {
			close(entered)
			defer close(exited)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
			return llmtest.Reply{Text: "Image answer after departure. **View:** neutral."}
		}})
		t.Cleanup(func() {
			open()
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Error("image fake model driver did not exit")
			}
		})
		c := s.signIn(t)
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"message": "Read this chart", "attachments": []any{map[string]any{"kind": "image", "mediaType": "image/png", "name": "chart.png", "data": base64.StdEncoding.EncodeToString(b.Bytes())}}}
		ctx, cancelClient := context.WithCancel(t.Context())
		defer cancelClient()
		resp, err := c.http.Do(c.request("POST", "/api/chat/stream", body).WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		id := ""
		for id == "" && sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var d struct{ ChatID string }
				json.Unmarshal([]byte(v), &d)
				id = d.ChatID
			}
		}
		if id == "" {
			t.Fatal("no real image chat event")
		}
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("actual image model not entered")
		}
		cancelClient()
		resp.Body.Close()
		open()
		select {
		case <-saved:
		case <-time.After(10 * time.Second):
			t.Fatal("normal disappearing image client canceled accepted persistence")
		}
		if n := s.count(t, "select count(*) from chat_messages where chat_id=$1 and role='assistant' and content='Image answer after departure. **View:** neutral.'", id); n != 1 {
			t.Fatal("image answer was not persisted after ordinary client departure")
		}
		if n := s.count(t, "select coalesce(sum(credits),0) from ai_usage where user_id=$1", c.id); n != 3 {
			t.Fatalf("ordinary image client departure changed credits: %d", n)
		}
		if n := s.count(t, "select count(*) from trades where user_id=$1", c.id); n != 0 {
			t.Fatal("image inference executed an unconfirmed paper order")
		}
	})
	for _, withImage := range []bool{false, true} {
		name := "sse_forced_close_joins_locked_save"
		cost := 2
		if withImage {
			name = "image_sse_forced_close_joins_locked_save"
			cost = 3
		}
		t.Run(name, func(t *testing.T) {
			entered, release, exited, deltaSeen, deadline, closed, forced := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			open := sync.OnceFunc(func() { close(release) })
			o := runOptions{shutdownTimeout: 200 * time.Millisecond, event: func(e string) {
				switch e {
				case "drain_timeout":
					close(deadline)
				case "pool_close_begin":
					close(closed)
				case "http_close_begin":
					close(forced)
				}
			}}
			s := boot(t, options{runtime: &o, model: func(llmtest.Request) llmtest.Reply {
				return llmtest.Reply{Deltas: []string{"Saved before stop."}, AfterFirst: func() {
					close(entered)
					defer close(exited)
					select {
					case <-release:
					case <-time.After(10 * time.Second):
					}
				}}
			}})
			t.Cleanup(func() {
				open()
				select {
				case <-exited:
				case <-time.After(10 * time.Second):
					t.Error("fake native stream driver did not exit")
				}
			})
			c := s.signIn(t)
			body := map[string]any{"message": "Stop while storing my answer"}
			if withImage {
				var b bytes.Buffer
				if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
					t.Fatal(err)
				}
				body["attachments"] = []any{map[string]any{"kind": "image", "mediaType": "image/png", "name": "chart.png", "data": base64.StdEncoding.EncodeToString(b.Bytes())}}
			}
			resp, err := c.http.Do(c.request("POST", "/api/chat/stream", body))
			if err != nil {
				t.Fatal(err)
			}
			clientDone, chatID := make(chan struct{}), make(chan string, 1)
			go func() {
				defer close(clientDone)
				defer resp.Body.Close()
				sc := bufio.NewScanner(resp.Body)
				name := ""
				for sc.Scan() {
					line := sc.Text()
					if v, ok := strings.CutPrefix(line, "event: "); ok {
						name = v
					}
					if v, ok := strings.CutPrefix(line, "data: "); ok {
						if name == "chat" {
							var data struct{ ChatID string }
							json.Unmarshal([]byte(v), &data)
							chatID <- data.ChatID
						}
						if name == "delta" {
							close(deltaSeen)
						}
					}
				}
			}()
			t.Cleanup(func() {
				resp.Body.Close()
				select {
				case <-clientDone:
				case <-time.After(10 * time.Second):
					t.Error("SSE HTTP client driver did not exit")
				}
			})
			var id string
			select {
			case id = <-chatID:
			case <-time.After(10 * time.Second):
				t.Fatal("no actual chat ID")
			}
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("native fake did not enter")
			}
			select {
			case <-deltaSeen:
			case <-time.After(10 * time.Second):
				t.Fatal("actual SDK delta was not delivered")
			}
			tx, err := s.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			var pid int
			if err := tx.QueryRow(t.Context(), "select pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(t.Context(), "update chats set updated_at=updated_at where id=$1", id); err != nil {
				t.Fatal(err)
			}
			s.cancel()
			waitForLifecycleLock(t, s, pid)
			select {
			case <-deadline:
			case <-closed:
				t.Fatal("pool closed while accepted assistant save held real row lock")
			case <-time.After(5 * time.Second):
				t.Fatal("locked save did not qualify drain timeout")
			}
			select {
			case <-forced:
			default:
				t.Fatal("HTTP deadline did not force connection Close")
			}
			select {
			case <-closed:
				t.Fatal("forced HTTP Close abandoned actual handler cleanup")
			case <-s.done:
				t.Fatal("run returned while accepted cleanup still held DB")
			default:
			}
			tx.Rollback(context.Background())
			select {
			case err := <-s.done:
				s.done <- nil
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lost real forced-close timeout: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("run did not join released save")
			}
			open()
			<-exited
			if n := s.count(t, "select count(*) from chat_messages where chat_id=$1 and role='assistant' and content='Saved before stop.'", id); n != 1 {
				t.Fatal("accepted partial answer was not persisted before pool close")
			}
			if n := s.count(t, "select coalesce(sum(credits),0) from ai_usage where user_id=$1", c.id); n != cost {
				t.Fatalf("native event credit contract changed: %d want %d", n, cost)
			}
		})
	}
	t.Run("detached_arena_cancellation_and_finalization", func(t *testing.T) {
		entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
		open := sync.OnceFunc(func() { close(release) })
		s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
			close(entered)
			defer close(exited)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
			return llmtest.Reply{Text: "Holding cash."}
		}})
		t.Cleanup(func() {
			open()
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Error("fake historical arena driver did not exit")
			}
		})
		c := s.signIn(t)
		created := c.call("POST", "/api/arena/runs", map[string]any{"cadence": "1d", "start": "2026-09-01", "end": "2026-09-30", "agents": []map[string]string{{"personaId": "momentum"}}}, 201)
		id := str(t, created, "run.id")
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("actual detached historical arena did not enter model")
		}
		s.cancel()
		select {
		case err := <-s.done:
			s.done <- nil
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("detached arena did not cancel with process")
		}
		if n := s.count(t, "select count(*) from arena_runs where id=$1 and status='failed' and credits=0", id); n != 1 {
			t.Fatal("detached arena was not finalized before pool close")
		}
		if n := s.count(t, "select coalesce(sum(credits),0) from ai_usage where user_id=$1", c.id); n != 0 {
			t.Fatalf("arena unrun steps not refunded: %d", n)
		}
	})
	t.Run("detached_forecast_cancellation_and_finalization", func(t *testing.T) {
		entered, release := make(chan struct{}, 3), make(chan struct{})
		open := sync.OnceFunc(func() { close(release) })
		var upstream sync.WaitGroup
		s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
			if strings.HasPrefix(r.System, queryWriter) {
				return noMarketQueries
			}
			if strings.Contains(r.System, "statistician") || strings.Contains(r.System, "quant analyst") || strings.Contains(r.System, "macro skeptic") {
				upstream.Add(1)
				defer upstream.Done()
				entered <- struct{}{}
				select {
				case <-release:
				case <-time.After(10 * time.Second):
				}
				return llmtest.Reply{Status: 400}
			}
			return llmtest.Reply{Status: 400}
		}})
		t.Cleanup(func() { open(); upstream.Wait() })
		c := s.signIn(t)
		created := c.call("POST", "/api/forecasts", map[string]any{"shape": "binary", "question": "Will BTC reach $200000?", "outcomes": []string{"Yes", "No"}, "resolutionCriteria": "BTC close reaches $200000", "resolutionDate": "2099-12-31", "category": "markets"}, 201)
		id := str(t, created, "forecast.id")
		for range 3 {
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("actual detached forecast did not enter all specialists")
			}
		}
		s.cancel()
		select {
		case err := <-s.done:
			s.done <- nil
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("detached forecast did not observe process cancellation")
		}
		if n := s.count(t, "select count(*) from forecasts where id=$1 and status='failed'", id); n != 1 {
			t.Fatal("run returned and closed pool before detached forecast finalized")
		}
		if n := s.count(t, "select coalesce(sum(credits),0) from ai_usage where user_id=$1", c.id); n != 0 {
			t.Fatalf("failed forecast was not refunded: %d", n)
		}
	})
	t.Run("sse_process_cancellation", func(t *testing.T) {
		entered, release, exited, timeout := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		open := sync.OnceFunc(func() { close(release) })
		// The budget is generous (the handler has logged 175 ms after a stop on a loaded runner); the fake
		// still holds the stream 10 s, so a drain that strips cancellation still times out.
		o := runOptions{shutdownTimeout: 2 * time.Second, event: func(e string) {
			if e == "drain_timeout" {
				close(timeout)
			}
		}}
		s := boot(t, options{runtime: &o, model: func(r llmtest.Request) llmtest.Reply {
			return llmtest.Reply{Text: "Accepted answer. **View:** neutral.", AfterFirst: func() {
				close(entered)
				defer close(exited)
				select {
				case <-release:
				case <-time.After(10 * time.Second):
				}
			}}
		}})
		t.Cleanup(func() {
			open()
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Error("fake stream did not exit")
			}
		})
		c := s.signIn(t)
		resp, err := c.http.Do(c.request("POST", "/api/chat/stream", map[string]string{"message": "Stop during my answer"}))
		if err != nil {
			t.Fatal(err)
		}
		clientDone, deltaSeen := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(clientDone)
			defer resp.Body.Close()
			sc := bufio.NewScanner(resp.Body)
			seen := false
			for sc.Scan() {
				if !seen && sc.Text() == "event: delta" {
					seen = true
					close(deltaSeen)
				}
			}
		}()
		t.Cleanup(func() {
			resp.Body.Close()
			select {
			case <-clientDone:
			case <-time.After(10 * time.Second):
				t.Error("SSE client driver did not exit")
			}
		})
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("fake model did not enter actual SSE")
		}
		select {
		case <-deltaSeen:
		case <-time.After(10 * time.Second):
			t.Fatal("real SDK did not deliver native model text before stop")
		}
		s.cancel()
		select {
		case err := <-s.done:
			s.done <- nil
			if err != nil {
				t.Fatalf("cooperative SSE did not drain: %v", err)
			}
		case <-timeout:
			t.Fatal("accepted SSE stripped process cancellation")
		case <-time.After(10 * time.Second):
			t.Fatal("process stop did not finish or qualify")
		}
		open()
		<-exited
		if n := s.count(t, "select coalesce(sum(credits),0) from ai_usage where user_id=$1", c.id); n != 2 {
			t.Fatalf("produced native event must keep charge: %d", n)
		}
	})
	for _, exceptional := range []bool{false, true} {
		name := "actual_worker_return_before_pool_close"
		if exceptional {
			name = "unexpected_serve_error_cancels_and_joins"
		}
		t.Run(name, func(t *testing.T) {
			dsn := os.Getenv("INTEGRATION_DATABASE_URL")
			if dsn == "" {
				t.Skip("INTEGRATION_DATABASE_URL not set")
			}
			dsn, _ = freshSchema(t, dsn)
			_, ai := llmtest.New(t, func(r llmtest.Request) llmtest.Reply { return llmtest.Reply{Text: `{"picks":[]}`} })
			isolateAWS(t, *ai.Config().BaseEndpoint)
			market := markettest.New(t)
			for k, v := range map[string]string{"DATABASE_URL": dsn, "BINANCE_BASE": market.URL, "HYPERLIQUID_BASE": market.URL,
				"YAHOO_BASE": yahootest.New().Serve(t), "SCREENER_MAX_TICKERS": "2", "AI_PROVIDER": "bedrock", "AI_MODEL": llmtest.Model, "BRAVE_API_KEY": "", "CRAWL_TOKEN": "", "LIVE_ARENA": "off", "QUOTES_REALTIME": "", "DEMO_EMAIL": "", "DEMO_PASSWORD": "", "ADMIN_EMAIL": "", "ADMIN_PASSWORD": ""} {
				t.Setenv(k, v)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			acknowledged, release, closed, deadline := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			ready := make(chan *app.Tasks, 1)
			var eventMu sync.Mutex
			var events []string
			var releaseOnce sync.Once
			releaseWork := func() { releaseOnce.Do(func() { close(release) }) }
			done := make(chan error, 1)
			go func() {
				done <- runWithOptions(ctx, cfg, ln, runOptions{shutdownTimeout: 10 * time.Millisecond, deps: func(d *app.Deps) { ready <- d.Tasks }, worker: func(name string, work func()) {
					work()
					if name == "paper" {
						close(acknowledged)
						<-release
					}
				}, event: func(e string) {
					eventMu.Lock()
					events = append(events, e)
					eventMu.Unlock()
					if e == "pool_close_begin" {
						close(closed)
					}
					if e == "drain_timeout" {
						close(deadline)
					}
				}})
			}()
			t.Cleanup(func() {
				cancel()
				releaseWork()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("run driver did not return")
				}
			})
			resp, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + ln.Addr().String() + "/healthz")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			owner := <-ready
			if exceptional {
				ln.Close()
			} else {
				cancel()
			}
			select {
			case <-acknowledged:
			case <-closed:
				t.Fatal("Serve error escaped without canceling and joining actual watcher")
			case <-time.After(10 * time.Second):
				t.Fatal("actual paper watcher did not acknowledge cancellation")
			}
			select {
			case <-closed:
				t.Fatal("pool.Close began before the actual canceled worker returned")
			case <-deadline:
			case <-time.After(10 * time.Second):
				t.Fatal("shutdown did not report its drain timeout")
			}
			select {
			case <-closed:
				t.Fatal("pool closed after timeout while worker still owned it")
			case <-done:
				t.Fatal("run returned before worker completed")
			default:
			}
			if _, complete, accepted := owner.Begin("late", context.Background()); accepted {
				complete()
				t.Fatal("process admitted new work after stop")
			}
			releaseWork()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("held worker must qualify shutdown timeout: %v", err)
				}
				if exceptional && !errors.Is(err, net.ErrClosed) {
					t.Fatalf("lost unexpected Serve error: %v", err)
				}
				done <- nil
			case <-time.After(10 * time.Second):
				t.Fatal("run did not join released worker")
			}
			eventMu.Lock()
			defer eventMu.Unlock()
			positions := map[string]int{}
			for i, e := range events {
				positions[e] = i + 1
			}
			for _, name := range []string{"paper", "alerts", "research", "scheduled_research", "signals", "live_arena", "autopilot", "screener", "quote_feeds", "forecast_recovery", "arena_recovery"} {
				if positions["task_start:"+name] == 0 || positions["task_done:"+name] == 0 || positions["task_done:"+name] > positions["pool_close_begin"] {
					t.Fatalf("missing actual completion before pool close for %s: %v", name, events)
				}
			}
			if positions["admission_closed"] >= positions["drain_complete"] || positions["drain_complete"] >= positions["pool_close_begin"] || positions["pool_close_begin"] >= positions["pool_close_done"] {
				t.Fatalf("shutdown order: %v", events)
			}
			t.Logf("actual owned completion order: %v", events)
		})
	}
}

func waitForLifecycleLock(t *testing.T, s *server, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := s.db.QueryRow(ctx, "select exists(select 1 from pg_stat_activity where $1=any(pg_blocking_pids(pid)) and wait_event_type='Lock')", pid).Scan(&waiting); err != nil {
			t.Fatalf("observe accepted cleanup's PG waiter: %v", err)
		}
		if waiting {
			return
		}
	}
}
