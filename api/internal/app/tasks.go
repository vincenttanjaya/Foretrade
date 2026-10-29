package app

import (
	"context"
	"net/http"
	"sync"

	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
)

// Tasks owns process work. Stop closes admission before Wait can observe the
// group, and completion means the function (including cleanup) actually returned.
type Tasks struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	stopping bool
	wg       sync.WaitGroup
	done     chan struct{}
	observe  func(string)
}

func NewTasks(ctx context.Context, observe func(string)) *Tasks {
	ctx, cancel := context.WithCancel(ctx)
	return &Tasks{ctx: ctx, cancel: cancel, done: make(chan struct{}), observe: observe}
}

func (p *Tasks) emit(e string) {
	if p.observe != nil {
		p.observe(e)
	}
}
func (p *Tasks) Context() context.Context { return taskContext{Context: p.ctx, values: p.ctx} }

// taskContext retains request values while cancellation follows the process,
// so an ordinary disconnected client does not orphan accepted durable work.
type taskContext struct {
	context.Context
	values context.Context
}
type processKey struct{}

func (c taskContext) Value(key any) any {
	if _, ok := key.(processKey); ok {
		return c.Context
	}
	return c.values.Value(key)
}

// Detached drops an operation deadline but retains an accepted task's process
// lifetime. This is for shared bounded subwork, not cancellation-shielded writes.
func Detached(ctx context.Context) context.Context {
	if process, ok := ctx.Value(processKey{}).(context.Context); ok {
		return taskContext{Context: process, values: ctx}
	}
	return context.WithoutCancel(ctx)
}

// Begin registers before durable effects or spawning. The caller must invoke
// done on every path, or hand it to the function which owns the actual work.
func (p *Tasks) Begin(name string, values context.Context) (context.Context, func(), bool) {
	if p == nil {
		return context.WithoutCancel(values), func() {}, true
	}
	p.mu.Lock()
	if p.stopping || p.ctx.Err() != nil {
		p.mu.Unlock()
		return nil, nil, false
	}
	p.wg.Add(1)
	p.mu.Unlock()
	p.emit("task_start:" + name)
	var once sync.Once
	return taskContext{Context: p.ctx, values: values}, func() { once.Do(func() { p.emit("task_done:" + name); p.wg.Done() }) }, true
}

func (p *Tasks) Go(name string, work func(context.Context)) bool {
	ctx, done, ok := p.Begin(name, p.ctx)
	if !ok {
		return false
	}
	go func() { defer done(); work(ctx) }()
	return true
}

func (p *Tasks) Stop() {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return
	}
	p.stopping = true
	p.mu.Unlock()
	p.emit("admission_closed")
	p.cancel()
	p.emit("process_cancel")
	go func() { p.wg.Wait(); close(p.done) }()
}

// Wait requires Stop first. A deadline reports failure; owners must still join
// before closing resources, even when cancellation-hostile work needs release.
func (p *Tasks) Wait(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Tasks) Handler(next http.Handler) http.Handler {
	if p == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, done, ok := p.Begin("http", r.Context())
		if !ok {
			httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "Server is stopping.")
			return
		}
		defer done()
		next.ServeHTTP(w, r)
	})
}
