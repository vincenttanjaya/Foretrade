package forecast

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Progress is a running forecast's live trace (contract ForecastProgress):
// what the pipeline has done so far, written as each stage moves. Null for
// older runs and for replays.
type Progress struct {
	Research    ResearchStep     `json:"research"`
	Specialists []SpecialistStep `json:"specialists"`
	Aggregator  struct {
		Status string `json:"status"` // pending | running | done
	} `json:"aggregator"`
	Market    MarketStep     `json:"market"`
	Materials *MaterialsStep `json:"materials"` // null when the run has no materials (and on older traces)
	UpdatedAt time.Time      `json:"updatedAt"`
}

// MaterialsStep is the digest stage (contract MaterialsStep).
type MaterialsStep struct {
	Status string `json:"status"` // pending | running | done
	Total  int    `json:"total"`  // materials on the run, reused and new
	Reused int    `json:"reused"` // copied digests: read at once
	Read   int    `json:"read"`   // read so far, reused included
	Failed int    `json:"failed"`
}

type ResearchStep struct {
	Status  string   `json:"status"`  // pending | running | done
	Queries []string `json:"queries"` // the search queries, once written (deep: round 1's once planned, then round 2's)
	Found   int      `json:"found"`   // sources in the pack (deep: admitted so far)
	Read    int      `json:"read"`    // of them read in full
	Mode    *string  `json:"mode"`    // live | wire | carried | none once done, else null

	// Deep research.
	Depth    *string       `json:"depth"`    // deep | quick once the stage picks one; null with search off
	Angles   []string      `json:"angles"`   // the research angles' labels once planned
	Searches int           `json:"searches"` // Brave calls so far
	Findings int           `json:"findings"` // ledger size so far
	Steps    []ResearchLog `json:"steps"`    // deep phases in order, append-only
}

// ResearchLog is one deep research phase (contract ResearchStepLog).
type ResearchLog struct {
	Phase  string `json:"phase"`  // plan | search | screen | read | extract | gap
	Round  int    `json:"round"`  // 0 for plan, else 1 or 2 (gap: 1)
	Status string `json:"status"` // running | done | failed | skipped
	Count  int    `json:"count"`  // plan: angles · search: unique results · screen: admitted · read: pages read · extract: findings added · gap: follow-up queries
	Of     int    `json:"of"`     // search: Brave calls · screen: candidates · read: attempted · extract: batches; else 0
}

type SpecialistStep struct {
	Role   string    `json:"role"`
	Status string    `json:"status"` // pending | running | done | failed
	Top    *OutcomeP `json:"top"`    // done: binary Yes, else the highest outcome
}

type MarketStep struct {
	Status  string `json:"status"` // pending | done | skipped
	Matched bool   `json:"matched"`
}

// tracker writes a run's Progress as it moves. A nil tracker is a no-op
// (replays and cmd/forecast-replay have no row to trace).
type tracker struct {
	ctx context.Context
	db  *pgxpool.Pool
	id  string

	mu sync.Mutex // serializes writes, so a stale state never lands after a newer one
	p  Progress
}

// newTracker starts id's trace: every stage pending, the market skipped when
// the run never looks one up (no POLYMARKET_BASE). An ordered run's market
// stage is its strike ladder: matched = at least one row. m is the run's
// materials (nil: none); with only reused ones the stage is done at once.
func newTracker(ctx context.Context, db *pgxpool.Pool, id string, d Draft, polymarket bool, m *materialRun) *tracker {
	t := &tracker{ctx: ctx, db: db, id: id}
	t.p.Research = ResearchStep{Status: "pending", Queries: []string{}, Angles: []string{}, Steps: []ResearchLog{}}
	for _, r := range roles {
		t.p.Specialists = append(t.p.Specialists, SpecialistStep{Role: r.ID, Status: "pending"})
	}
	t.p.Aggregator.Status = "pending"
	t.p.Market.Status = "pending"
	if !polymarket {
		t.p.Market.Status = "skipped"
	}
	if m != nil && len(m.stored) > 0 {
		reused := len(m.stored) - len(m.files)
		t.p.Materials = &MaterialsStep{Status: "pending", Total: len(m.stored), Reused: reused, Read: reused}
		if len(m.files) == 0 {
			t.p.Materials.Status = "done"
		}
	}
	return t
}

// set applies f and stores the trace on the row while it is still running.
// Best effort: a failed write is logged and the run carries on.
// ponytail: one UPDATE per stage change (about 30 a run with deep research); batch on a timer if runs ever get chattier.
func (t *tracker) set(f func(*Progress)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f(&t.p)
	t.p.UpdatedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
	defer cancel()
	if _, err := t.db.Exec(ctx, `update forecasts set progress = $2 where id = $1 and status = 'running'`, t.id, t.p); err != nil {
		slog.Warn("forecast progress write failed", "id", t.id, "err", err)
	}
}
