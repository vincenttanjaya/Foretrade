package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

// The actual pool batch waits for the real crawler client's connection. It
// then executes the actual overview query against this test's renamed table.
type probeBatchTrace struct{ probe <-chan struct{} }

func (*probeBatchTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (*probeBatchTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (t *probeBatchTrace) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	select {
	case <-t.probe:
	case <-time.After(5 * time.Second):
		panic("test probe did not enter before actual batch")
	}
	return ctx
}
func (*probeBatchTrace) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (*probeBatchTrace) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}

type probeErrorWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	panic   bool
	once    sync.Once
}

func (w *probeErrorWriter) WriteHeader(status int) {
	if status == http.StatusInternalServerError {
		w.once.Do(func() {
			close(w.entered)
			if w.panic {
				panic("controlled overview error-write panic")
			}
		})
	}
	w.ResponseRecorder.WriteHeader(status)
}

func TestOverviewErrorAndPanicJoinActiveProbe(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "early_batch_error"
		if panics {
			name = "panic_after_batch_error"
		}
		t.Run(name, func(t *testing.T) {
			e := setup(t, apptest.Options{})
			// args uses cached quotes: the request trace below belongs only to
			// the actual crawler health connection, not to unrelated market I/O.
			if _, err := e.Market.Assets(t.Context()); err != nil {
				t.Fatal(err)
			}
			probeEntered, probeRelease, probeExited, errorEntered, handlerDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			open := sync.OnceFunc(func() { close(probeRelease) })
			probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
			t.Cleanup(probe.Close) // the library joins its own local handler drivers
			e.Research = research.New(research.Config{CrawlBase: probe.URL, CrawlToken: "test"}, e.DB, nil)
			cfg := e.DB.Config()
			cfg.ConnConfig.Tracer = &probeBatchTrace{probe: probeEntered}
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			e.DB = pool
			if _, err := pool.Exec(t.Context(), "alter table live_attempts rename to held_overview_attempts"); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var events []string
			e.Tasks = app.NewTasks(t.Context(), func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() })
			requestCtx := httptrace.WithClientTrace(e.Tasks.Context(), &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) {
				close(probeEntered) // synchronous inside the real healthy→HTTP Do
				defer close(probeExited)
				select {
				case <-probeRelease:
				case <-time.After(5 * time.Second):
				}
			}})
			r := httptest.NewRequestWithContext(requestCtx, "GET", "/api/admin/overview", nil)
			r.AddCookie(e.Cookie)
			w := &probeErrorWriter{ResponseRecorder: httptest.NewRecorder(), entered: errorEntered, panic: panics}
			go func() { defer close(handlerDone); e.Tasks.Handler(httpx.Recover(e.Mux)).ServeHTTP(w, r) }()
			t.Cleanup(func() {
				open()
				e.Tasks.Stop()
				for name, done := range map[string]<-chan struct{}{"overview HTTP": handlerDone, "local probe client": probeExited} {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Errorf("%s driver did not exit", name)
					}
				}
			})
			select {
			case <-errorEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("actual overview batch did not take its early error/panic path")
			}
			e.Tasks.Stop()
			bound, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			if err := e.Tasks.Wait(bound); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("process drained before early overview error/panic joined actual probe: %v", err)
			}
			select {
			case <-handlerDone:
				t.Fatal("overview published HTTP completion while active probe held")
			default:
			}
			open()
			join, stopJoin := context.WithTimeout(t.Context(), 5*time.Second)
			defer stopJoin()
			if err := e.Tasks.Wait(join); err != nil {
				t.Fatal(err)
			}
			<-handlerDone
			<-probeExited
			if w.Code != 500 {
				t.Fatalf("actual batch failure status: %d", w.Code)
			}
			mu.Lock()
			defer mu.Unlock()
			t.Logf("actual overview/probe owner completion: %v", events)
		})
	}
}
