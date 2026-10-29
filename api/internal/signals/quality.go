package signals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

// quality covers the whole board: every new pick shares one model prompt,
// and kept picks retain the dependencies of the board that picked them.
type quality struct {
	InputCount int                    `json:"inputCount"`
	Version    int                    `json:"version"`
	Inputs     []research.NewsItem    `json:"inputs"`
	Briefs     []research.MarketBrief `json:"briefs"`
	// Origins are, per pickedAt on the board, the inputs and briefs (ids) of
	// the board that picked it, whose model call wrote the rationale a kept
	// pick keeps. Only those come along with a kept pick, so the inputs
	// end with the last pick that needs them (72 h) instead of chaining back
	// board after board into the news retention. Absent on boards stored
	// before them: a pick carried from one depends on all of its inputs.
	Origins []origin `json:"origins,omitempty"`
}

type origin struct {
	PickedAt time.Time `json:"pickedAt"`
	Inputs   []string  `json:"inputs"`
	Briefs   []string  `json:"briefs"`
}

func validBoard(ctx context.Context, db *pgxpool.Pool, b *Board) (bool, error) {
	validations.Add(1)
	if b.quality == nil || b.quality.Version != 1 || len(b.quality.Inputs) != b.quality.InputCount {
		return false, nil
	}
	for _, o := range b.quality.Origins {
		if slices.ContainsFunc(o.Inputs, func(id string) bool { return !slices.ContainsFunc(b.quality.Inputs, byID(id)) }) ||
			slices.ContainsFunc(o.Briefs, func(id string) bool { return !slices.ContainsFunc(b.quality.Briefs, briefID(id)) }) {
			return false, nil
		}
	}
	valid, err := research.ValidateNewsInputs(ctx, db, b.quality.Inputs)
	if !valid || err != nil {
		return valid, err
	}
	for _, p := range append(append([]Pick{}, b.Bullish...), b.Bearish...) {
		if p.Factors.Sentiment != nil && len(b.quality.Briefs) == 0 {
			return false, nil
		}
		for _, h := range p.Headlines {
			found := false
			for _, n := range b.quality.Inputs {
				found = found || h.Title == n.Title && h.URL == n.URL && h.Source == n.Source && research.Relevant(n.Title, n.Description, n.URL, n.Source, p.Symbol)
			}
			if !found {
				return false, nil
			}
		}
	}
	s := research.New(research.Config{}, db, nil)
	for _, snapshot := range b.quality.Briefs {
		// Use the exact brief ID; the newest brief may legitimately be newer.
		var raw []byte
		if err := db.QueryRow(ctx, `select brief from market_briefs where id=$1`, snapshot.ID).Scan(&raw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		same, err := sameBrief(raw, snapshot)
		if err != nil || !same {
			return false, err
		}
		inputs, err := s.BriefInputs(ctx, snapshot.ID)
		if err != nil {
			return false, err
		}
		if len(inputs) == 0 {
			return false, nil
		}
	}
	return true, nil
}

// sameBrief reports whether the stored brief row still says what the board's
// snapshot of it says. Both are compared as canonical JSON (canonicalBrief),
// not as Go structs: reflect.DeepEqual told a nil slice from an empty one, so
// a snapshot written by a build that serialised an empty field differently
// (a tag or field added or changed in a deploy) turned an unchanged brief
// into a changed one. A row this build cannot read is a changed brief, not
// an error.
func sameBrief(raw []byte, snapshot research.MarketBrief) (bool, error) {
	var row research.MarketBrief
	if json.Unmarshal(raw, &row) != nil {
		return false, nil
	}
	have, err := canonicalBrief(row)
	if err != nil {
		return false, err
	}
	want, err := canonicalBrief(snapshot)
	return err == nil && bytes.Equal(have, want), err
}

// canonicalBrief is b's JSON as this build reads it, without its id and time
// (the row's columns) and without zero values, so a field added to or dropped
// from MarketBrief reads the same on both sides; object keys sorted.
func canonicalBrief(b research.MarketBrief) ([]byte, error) {
	b.ID, b.CreatedAt = "", time.Time{}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(dropZero(v))
}

// dropZero removes the zero values (zero) from v's objects, recursively.
func dropZero(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if e = dropZero(e); zero(e) {
				delete(x, k)
			} else {
				x[k] = e
			}
		}
	case []any:
		for i := range x {
			x[i] = dropZero(x[i])
		}
	}
	return v
}

// zero is a decoded JSON zero value: null, "", 0, false, [] or {}.
func zero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case float64:
		return x == 0
	case bool:
		return !x
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func addInputs(q *quality, items []research.NewsItem) {
	for _, n := range items {
		found := false
		for _, old := range q.Inputs {
			if old.ID == n.ID {
				found = true
				break
			}
		}
		if !found {
			q.Inputs = append(q.Inputs, n)
			q.InputCount = len(q.Inputs)
		}
	}
}

// carry records the origin of every pick on b, which refresh annotated at
// now: the board's own inputs and briefs for a new pick, and for a kept one
// its origin on prev, whose inputs and briefs (its headlines' rows and the
// model call that wrote its rationale) come along.
func carry(b *Board, prev *Board, now time.Time) {
	q := b.quality
	own := origin{PickedAt: now, Inputs: newsIDs(q.Inputs), Briefs: briefIDs(q.Briefs)}
	for _, p := range slices.Concat(b.Bullish, b.Bearish) {
		if slices.ContainsFunc(q.Origins, func(o origin) bool { return o.PickedAt.Equal(p.PickedAt) }) {
			continue
		}
		if p.fresh || prev == nil || prev.quality == nil {
			q.Origins = append(q.Origins, own)
			continue
		}
		pq := prev.quality
		k := slices.IndexFunc(pq.Origins, func(o origin) bool { return o.PickedAt.Equal(p.PickedAt) })
		o := origin{PickedAt: p.PickedAt, Inputs: newsIDs(pq.Inputs), Briefs: briefIDs(pq.Briefs)} // prev predates origins
		if k >= 0 {
			o = pq.Origins[k]
		}
		q.Origins = append(q.Origins, o)
		for _, id := range o.Inputs {
			if i := slices.IndexFunc(pq.Inputs, byID(id)); i >= 0 {
				addInputs(q, pq.Inputs[i:i+1])
			}
		}
		for _, id := range o.Briefs {
			if i := slices.IndexFunc(pq.Briefs, briefID(id)); i >= 0 && !slices.ContainsFunc(q.Briefs, briefID(id)) {
				q.Briefs = append(q.Briefs, pq.Briefs[i])
			}
		}
	}
	// A kept pick whose asset this board could not analyze keeps its previous
	// factors, sentiment included: the briefs they came from come along.
	if len(q.Briefs) == 0 && prev != nil && prev.quality != nil &&
		slices.ContainsFunc(slices.Concat(b.Bullish, b.Bearish), func(p Pick) bool { return !p.fresh && p.Factors.Sentiment != nil }) {
		q.Briefs = append(q.Briefs, prev.quality.Briefs...)
	}
}

func byID(id string) func(research.NewsItem) bool {
	return func(n research.NewsItem) bool { return n.ID == id }
}

func briefID(id string) func(research.MarketBrief) bool {
	return func(b research.MarketBrief) bool { return b.ID == id }
}

func newsIDs(items []research.NewsItem) []string {
	out := make([]string, len(items))
	for i, n := range items {
		out[i] = n.ID
	}
	return out
}

func briefIDs(briefs []research.MarketBrief) []string {
	out := make([]string, len(briefs))
	for i, b := range briefs {
		out[i] = b.ID
	}
	return out
}
