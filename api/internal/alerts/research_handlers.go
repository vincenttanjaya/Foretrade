package alerts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
	"net/http"
	"strconv"
	"time"
)

type runCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}
type RunPage struct {
	Runs       []RunLog `json:"runs"`
	NextCursor *string  `json:"nextCursor"`
}

func (h *handler) runs(w http.ResponseWriter, r *http.Request) {
	id, uid := r.PathValue("id"), httpx.UserID(r.Context())
	if !canonical(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	limit := 20
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err != nil || limit < 1 || limit > 50 {
		app.WriteError(w, bad("limit must be 1–50."))
		return
	}
	page, err := ResearchHistory(r.Context(), h.d, uid, id, limit, r.URL.Query().Get("before"))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, 200, page)
}

func ResearchHistory(ctx context.Context, d *app.Deps, uid, id string, limit int, before string) (RunPage, error) {
	page := RunPage{Runs: []RunLog{}}
	var own bool
	if err := d.DB.QueryRow(ctx, `select exists(select 1 from alerts where id=$1 and user_id=$2)`, id, uid).Scan(&own); err != nil {
		return page, err
	}
	if !own {
		return page, app.ErrNotFound
	}
	if limit < 1 || limit > 50 {
		return page, bad("limit must be 1–50.")
	}
	var cur *runCursor
	if before != "" {
		if len(before) > 256 {
			return page, bad("Invalid history cursor.")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(before)
		if err != nil {
			return page, bad("Invalid history cursor.")
		}
		var c runCursor
		if json.Unmarshal(decoded, &c) != nil || c.At.IsZero() || !canonical(c.ID) {
			return page, bad("Invalid history cursor.")
		}
		cur = &c
	}
	var at *time.Time
	var cursorID *string
	if cur != nil {
		at, cursorID = &cur.At, &cur.ID
	}
	rows, err := d.DB.Query(ctx, `select r.id::text,r.ran_at,r.status,left(r.body,300),r.error,
	(select c.id::text from chats c where c.id=r.report_chat_id and c.user_id=$2),r.excerpt,r.verdict
	from alert_runs r where r.alert_id=$1 and r.user_id=$2 and r.ran_at>=clock_timestamp()-interval '30 days'
	and ($3::timestamptz is null or (r.ran_at,r.id)<($3,$4::uuid)) order by r.ran_at desc,r.id desc limit $5`, id, uid, at, cursorID, limit+1)
	if err != nil {
		return page, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (RunLog, error) {
		var run RunLog
		err := r.Scan(&run.ID, &run.At, &run.Status, &run.Body, &run.Error, &run.ReportChatID, &run.Excerpt, &run.Verdict)
		return run, err
	})
	if err != nil {
		return page, err
	}
	if len(list) > limit {
		list = list[:limit]
		last := list[len(list)-1]
		b, _ := json.Marshal(runCursor{last.At, last.ID})
		page.NextCursor = new(base64.RawURLEncoding.EncodeToString(b))
	}
	if list != nil {
		page.Runs = list
	}
	return page, nil
}

func (h *handler) resume(w http.ResponseWriter, r *http.Request) {
	if !canonical(r.PathValue("id")) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	a, err := ResumeResearch(r.Context(), h.d, httpx.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"alert": a})
}

func ResumeResearch(ctx context.Context, d *app.Deps, uid, id string) (Alert, error) {
	var a Alert
	err := pgx.BeginFunc(ctx, d.DB, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, `select disabled_at is null from users where id=$1 for no key update`, uid).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return app.ErrNotFound
		}
		var err error
		a, err = scan(tx.QueryRow(ctx, `select `+columns+` from alerts where id=$1 and user_id=$2 for update`, id, uid))
		if err != nil {
			return err
		}
		if a.Action != "research" || a.Status != "paused" {
			return &app.Conflict{Msg: "Only paused research can be resumed."}
		}
		var active, research int
		if err = tx.QueryRow(ctx, `select count(*),count(*) filter(where action='research') from alerts where user_id=$1 and status='active'`, uid).Scan(&active, &research); err != nil {
			return err
		}
		if active >= 20 || research >= 3 {
			return &app.Conflict{Msg: "Cancel an active automation before resuming."}
		}
		var used, limit int
		day := time.Now().UTC().Truncate(24 * time.Hour)
		if err = tx.QueryRow(ctx, `select coalesce((select credits from ai_usage where user_id=$1 and day=$2),0),coalesce(credit_limit,$3) from users where id=$1`, uid, day, d.Usage.DefaultLimit()).Scan(&used, &limit); err != nil {
			return err
		}
		if limit-used < 2 {
			return usage.ErrQuota
		}
		wd := 0
		if a.Weekday != nil {
			wd = *a.Weekday
		}
		a, err = scan(tx.QueryRow(ctx, `update alerts set status='active',quota_streak=0,next_run_at=$2 where id=$1 returning `+columns, id, nextRun(*a.Every, atMinute(*a.At), wd, time.Now())))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = app.ErrNotFound
	}
	return a, err
}
