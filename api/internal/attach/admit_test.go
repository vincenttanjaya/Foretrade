package attach

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// uploadServer reads each upload body after Admit, as chat and forecasts do,
// for the user named in the X-User header: 200 when the body arrived, 400
// when its read failed (the read deadline cut it), 429 when it was not
// admitted. done counts finished requests.
func uploadServer(t *testing.T) (*httptest.Server, *sync.WaitGroup) {
	t.Helper()
	var done sync.WaitGroup
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer done.Done()
		release, err := Admit(w, r, r.Header.Get("X-User"))
		if err != nil {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		defer release()
		if _, err := io.ReadAll(http.MaxBytesReader(w, r.Body, BodyLimit)); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		release()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &done
}

// trickle opens a connection that declares a cap-sized body for user and
// sends a byte every 50 ms until the test ends.
func trickle(t *testing.T, srv *httptest.Server, user string, done *sync.WaitGroup) {
	t.Helper()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	done.Add(1)
	fmt.Fprintf(c, "POST / HTTP/1.1\r\nHost: x\r\nX-User: %s\r\nContent-Length: %d\r\n\r\n{", user, BodyLimit)
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for range tick.C {
			if _, err := c.Write([]byte(" ")); err != nil {
				return
			}
		}
	}()
}

func upload(t *testing.T, srv *httptest.Server, user string, size int, done *sync.WaitGroup) int {
	t.Helper()
	done.Add(1)
	req, _ := http.NewRequest("POST", srv.URL, bytes.NewReader(bytes.Repeat([]byte(" "), size)))
	req.Header.Set("X-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func restoreReads(t *testing.T) {
	g, r, m, w := readGrace, readRate, readMax, wait
	t.Cleanup(func() { readGrace, readRate, readMax, wait = g, r, m, w })
}

// Four trickling bodies (gap round G23) that declare the cap each held the
// whole upload budget, so a normal 5.6 MB upload waited 30 s for a 429. Now
// each body has a read deadline: the trickles are cut, their shares come
// back, and the normal upload gets 200.
func TestAdmitReadDeadline(t *testing.T) {
	restoreReads(t)
	readGrace, readRate, wait = 300*time.Millisecond, 1<<40, 10*time.Second
	srv, done := uploadServer(t)
	for i := range 4 {
		trickle(t, srv, fmt.Sprintf("slow-%d", i), done)
	}
	time.Sleep(100 * time.Millisecond) // all four admitted: the budget is full
	if budget.TryAcquire(1) {
		budget.Release(1)
		t.Fatal("the four trickles should hold the whole budget")
	}
	start := time.Now()
	if code := upload(t, srv, "ok", 5_600_000, done); code != http.StatusOK {
		t.Fatalf("a normal upload got %d after %v", code, time.Since(start))
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the normal upload waited %v", took)
	}
	done.Wait()
	free, ok := Saturate(time.Second)
	if !ok {
		t.Fatal("a cut body kept its share")
	}
	free()
	if d := min(readGrace+time.Duration(int64(BodyLimit)/readRate)*time.Second, readMax); d != readGrace {
		t.Fatal(d)
	}
	readGrace, readRate = 20*time.Second, 128<<10
	if d := min(readGrace+time.Duration(int64(5_600_000)/readRate)*time.Second, readMax); d != 62*time.Second {
		t.Errorf("a 5.6 MB body gets %v", d)
	}
	if d := min(readGrace+time.Duration(int64(BodyLimit)/readRate)*time.Second, readMax); d != 276*time.Second {
		t.Errorf("a cap-sized body gets %v", d)
	}
}

// One user holds at most two upload-sized admissions (gap round G23): a third
// waits like a full budget, then gets 429, while another user's upload goes
// through.
func TestAdmitPerUser(t *testing.T) {
	restoreReads(t)
	readGrace, wait = time.Minute, 300*time.Millisecond
	srv, done := uploadServer(t)
	trickle(t, srv, "alice", done)
	trickle(t, srv, "alice", done)
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	if code := upload(t, srv, "alice", 5_000_000, done); code != http.StatusTooManyRequests || time.Since(start) < wait {
		t.Fatalf("alice's third upload: %d after %v", code, time.Since(start))
	}
	if code := upload(t, srv, "bob", 5_000_000, done); code != http.StatusOK {
		t.Fatalf("bob's upload: %d", code)
	}
	users.Lock()
	n := users.m["alice"].refs
	_, bob := users.m["bob"]
	users.Unlock()
	if n != 2 || bob { // alice's two trickles hold her slots; bob's were let go
		t.Fatalf("slots: alice %d, bob kept %v", n, bob)
	}
}
