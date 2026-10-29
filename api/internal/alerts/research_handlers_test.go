package alerts

import (
	"fmt"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResearchHistoryAndResumeOwnership(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`)
	w := e.Do("GET", "/api/alerts/"+a.ID+"/runs", nil)
	if w.Code != 200 {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}
	if got := e.Do("GET", "/api/alerts/"+a.ID+"/runs?limit=51", nil); got.Code != 400 {
		t.Fatalf("history limit: %d", got.Code)
	}
	if got := e.Do("GET", "/api/alerts/"+a.ID+"/runs?before=bad", nil); got.Code != 400 {
		t.Fatalf("history cursor: %d", got.Code)
	}
	if _, err := e.DB.Exec(t.Context(), "update alerts set status='paused',quota_streak=3 where id=$1", a.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.Do("POST", "/api/alerts/"+a.ID+"/resume", nil); got.Code != 200 {
		t.Fatalf("resume: %d %s", got.Code, got.Body)
	}
	if load(t, e, a.ID).Status != "active" {
		t.Fatal("not resumed")
	}
	if got := e.Do("POST", "/api/alerts/"+a.ID+"/resume", nil); got.Code != 409 {
		t.Fatalf("active resume: %d", got.Code)
	}
	if got := e.DoAs(nil, "GET", "/api/alerts/"+a.ID+"/runs", nil); got.Code != 401 {
		t.Fatalf("anonymous history: %d", got.Code)
	}
	var charged int
	if err := e.DB.QueryRow(t.Context(), "select coalesce(sum(credits),0) from ai_usage where user_id=$1", e.User).Scan(&charged); err != nil || charged != 0 {
		t.Fatalf("resume charged %d %v", charged, err)
	}
}

func TestResearchConcurrentCreateAndResumeCaps(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	research := `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`
	create(t, e, research)
	create(t, e, research)
	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make(chan int, 4)
	for range 4 {
		wg.Go(func() { <-start; codes <- e.Do("POST", "/api/alerts", research).Code })
	}
	close(start)
	wg.Wait()
	close(codes)
	win := 0
	for c := range codes {
		if c == 201 {
			win++
		} else if c != 409 {
			t.Fatalf("cap response %d", c)
		}
	}
	if win != 1 {
		t.Fatalf("3-cap winners %d", win)
	}
	var paused string
	if err := e.DB.QueryRow(t.Context(), `update alerts set status='paused' where id=(select id from alerts where user_id=$1 order by id limit 1) returning id::text`, e.User).Scan(&paused); err != nil {
		t.Fatal(err)
	}
	ordinary := `{"symbol":"BTC","trigger":"schedule","every":"day","at":"08:00"}`
	for range 17 {
		create(t, e, ordinary)
	}
	start = make(chan struct{})
	codes = make(chan int, 3)
	for _, job := range []struct{ path, body string }{{"/api/alerts", research}, {"/api/alerts", ordinary}, {"/api/alerts/" + paused + "/resume", "{}"}} {
		wg.Go(func() { <-start; codes <- e.Do("POST", job.path, job.body).Code })
	}
	close(start)
	wg.Wait()
	close(codes)
	win = 0
	for c := range codes {
		if c == 201 || c == 200 {
			win++
		} else if c != 409 {
			t.Fatalf("shared cap response %d", c)
		}
	}
	if win != 1 {
		t.Fatalf("20-cap winners %d", win)
	}
	var active, paid int
	if err := e.DB.QueryRow(t.Context(), `select count(*),count(*) filter(where action='research') from alerts where user_id=$1 and status='active'`, e.User).Scan(&active, &paid); err != nil || active != 20 || paid > 3 {
		t.Fatalf("caps %d/%d %v", active, paid, err)
	}
}

func TestResearchHistoryCursorOwnerBeforeLimitAndChat(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	a := create(t, e, `{"action":"research","trigger":"schedule","every":"day","at":"08:00","prompt":"Review portfolio risks"}`)
	foreign := storetest.User(t, e.DB)
	cookie := e.Session(foreign)
	for _, method := range []string{"GET", "POST"} {
		path := "/api/alerts/" + a.ID + "/runs"
		if method == "POST" {
			path = "/api/alerts/" + a.ID + "/resume"
		}
		if got := e.DoAs(cookie, method, path, nil); got.Code != 404 {
			t.Fatalf("foreign %s %d", method, got.Code)
		}
	}
	var foreignChat string
	if err := e.DB.QueryRow(t.Context(), `insert into chats(user_id,title) values($1,'Foreign') returning id::text`, foreign).Scan(&foreignChat); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for i := range 23 {
		uid, body := e.User, fmt.Sprintf("own report %d", i)
		if i >= 21 {
			uid, body = foreign, "FOREIGN SECRET"
		}
		if _, err := e.DB.Exec(t.Context(), `insert into alert_runs(alert_id,user_id,ran_at,status,body,report_chat_id) values($1,$2,$3,'ok',$4,$5)`, a.ID, uid, at, body, foreignChat); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.DB.Exec(t.Context(), `insert into alert_runs(alert_id,user_id,ran_at,status,body) values($1,$2,$3,'ok','OLD SECRET')`, a.ID, e.User, at.AddDate(0, 0, -31)); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	before := ""
	pages := 0
	for {
		w := e.Do("GET", "/api/alerts/"+a.ID+"/runs?limit=20&before="+before, nil)
		if w.Code != 200 {
			t.Fatalf("history %d %s", w.Code, w.Body)
		}
		page := apptest.JSON[RunPage](t, w)
		pages++
		for _, run := range page.Runs {
			if seen[run.ID] || strings.Contains(run.Body, "SECRET") || run.ReportChatID != nil {
				t.Fatalf("history leaked/repeated %+v", run)
			}
			seen[run.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		before = *page.NextCursor
	}
	if len(seen) != 21 || pages != 2 {
		t.Fatalf("exclusive equal-time pages %d/%d", len(seen), pages)
	}
}
