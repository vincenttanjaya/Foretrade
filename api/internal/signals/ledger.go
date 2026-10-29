package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The pick ledger and the track record (rule steady-v1). Every shown board
// enters signal_picks once (syncLedger): its new picks, the exits its refresh
// resolved and, for every pick picked 72 h or more before it, the score
// against a random pick from the same list, all from the prices the boards
// stored in _ledger. GET /api/signals reads only these rows: no candles.

const (
	costPct         = 0.10 // assumed round-trip cost (crypto and stock perps), for the record's net line
	recordMinPicks  = 200
	recordMinBlocks = 30
	// recordZ is 99% two-sided: the record is re-read every 4 h indefinitely
	// (repeated looks), and at 95% a rule with no edge would flash "better"
	// or "worse" by chance far too often.
	recordZ   = 2.576
	blockDays = 3
	horizon   = 72 * time.Hour // a pick is scored at the first board this long after it
)

// ledger is a steady board's private record part (_ledger, never public):
// every analyzed asset's price this board (s, p) and whether it was eligible
// (e), and the previous board's picks that left (exits).
type ledger struct {
	Version  int           `json:"version"`
	Rule     string        `json:"rule"`
	Universe []ledgerAsset `json:"universe"`
	Exits    []ledgerExit  `json:"exits"`
}

type ledgerAsset struct {
	S string  `json:"s"`
	P float64 `json:"p"`
	E bool    `json:"e"`
}

type ledgerExit struct {
	Symbol    string    `json:"symbol"`
	Side      string    `json:"side"`
	PickedAt  time.Time `json:"pickedAt"`
	Reason    string    `json:"reason"` // target_hit | invalidated | expired
	ExitAt    time.Time `json:"exitAt"`
	ExitPrice float64   `json:"exitPrice"`
}

// pricedAsset is one entry of a ledger row's universe: the pick board's eligible assets.
type pricedAsset struct {
	S string  `json:"s"`
	P float64 `json:"p"`
}

// syncLedger walks every retained board created after the last one synced,
// oldest first, and records each that has a ledger part and validates (only
// shown boards enter the record): its new picks, its exits and the scores
// it can give. Every step is idempotent, so a crash between storing a board
// and recording it is repaired by the next call. Callers hold the refresh's
// advisory lock.
func syncLedger(ctx context.Context, db *pgxpool.Pool) error {
	var last time.Time
	err := db.QueryRow(ctx, "select last_board_at from signal_ledger_sync").Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("signals: ledger sync state: %w", err)
	}
	rows, _ := db.Query(ctx, selectBoards+" where created_at > $1 order by created_at, id", last)
	boards, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Board, error) { return scanBoard(row) })
	if err != nil {
		return fmt.Errorf("signals: ledger boards: %w", err)
	}
	for i := range boards {
		b := &boards[i]
		record := false
		if b.ledger != nil {
			if record, err = validBoard(ctx, db, b); err != nil {
				return err
			}
		}
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			if record {
				if err := syncBoard(ctx, tx, b); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `insert into signal_ledger_sync (last_board_at) values ($1)
				on conflict (id) do update set last_board_at = excluded.last_board_at`, b.CreatedAt)
			return err
		}); err != nil {
			return fmt.Errorf("signals: ledger sync of the board at %s: %w", b.CreatedAt.Format(time.RFC3339), err)
		}
	}
	return nil
}

// syncBoard records one shown steady board: its new picks (pickedAt is its
// createdAt), the exits its refresh resolved, then the score of every pick
// picked at least 72 h and less than 7 days before it, at its prices.
func syncBoard(ctx context.Context, tx pgx.Tx, b *Board) error {
	var universe []pricedAsset
	px := map[string]float64{}
	for _, u := range b.ledger.Universe {
		if u.E {
			universe = append(universe, pricedAsset{u.S, u.P})
		}
		px[u.S] = u.P
	}
	if universe == nil {
		universe = []pricedAsset{}
	}
	for _, p := range append(append([]Pick{}, b.Bullish...), b.Bearish...) {
		if !p.PickedAt.Equal(b.CreatedAt) {
			continue
		}
		if _, err := tx.Exec(ctx, `insert into signal_picks (rule, symbol, side, picked_at, price_at_pick, target, invalidation, valid_until, score_at_pick, universe)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) on conflict (symbol, side, picked_at) do nothing`,
			b.ledger.Rule, p.Symbol, p.Side, p.PickedAt, p.PriceAtPick, p.Target, p.Invalidation, p.ValidUntil, p.Score, universe); err != nil {
			return err
		}
	}
	for _, x := range b.ledger.Exits {
		if _, err := tx.Exec(ctx, `update signal_picks set exit_reason = $4, exit_at = $5, exit_price = $6, removed_at = $7,
			return_pct = round(((case when side = 'bullish' then 1 else -1 end) * ($6 / price_at_pick - 1) * 100)::numeric, 2)::double precision
			where symbol = $1 and side = $2 and picked_at = $3 and exit_reason is null`,
			x.Symbol, x.Side, x.PickedAt, x.Reason, x.ExitAt, x.ExitPrice, b.CreatedAt); err != nil {
			return err
		}
	}
	rows, _ := tx.Query(ctx, `select id, symbol, side, picked_at, price_at_pick, universe from signal_picks
		where scored_at is null and picked_at <= $1 and picked_at > $2`, b.CreatedAt.Add(-horizon), b.CreatedAt.Add(-keep))
	type due struct {
		id, symbol, side string
		pickedAt         time.Time
		price            float64
		universe         []pricedAsset
	}
	pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var r due
		var raw []byte
		if err := row.Scan(&r.id, &r.symbol, &r.side, &r.pickedAt, &r.price, &raw); err != nil {
			return r, err
		}
		return r, json.Unmarshal(raw, &r.universe)
	})
	if err != nil {
		return err
	}
	for _, r := range pending {
		s, ok := scoreAt(r.side, r.symbol, r.price, r.universe, px)
		if !ok {
			continue // a later board scores it
		}
		if _, err := tx.Exec(ctx, `update signal_picks set scored_at = $2, horizon_h = $3, result_pct = $4, random_pct = $5,
			random_up_share = $6, universe_n = $7 where id = $1 and scored_at is null`,
			r.id, b.CreatedAt, b.CreatedAt.Sub(r.pickedAt).Hours(), s.result, s.random, s.upShare, s.n); err != nil {
			return err
		}
	}
	return nil
}

type scored struct {
	result, random, upShare float64
	n                       int
}

// scoreAt is a pick's move to the scoring board's prices (px), in the pick's
// direction, against the mean move of the pick board's eligible universe
// over the same span (a random pick from the same list), and the share of
// that universe that moved the pick's way. ok is false when the scoring board
// has no price for the symbol or for any of the universe.
func scoreAt(side, symbol string, price float64, universe []pricedAsset, px map[string]float64) (s scored, ok bool) {
	d := dir(side)
	if px[symbol] <= 0 || price <= 0 {
		return s, false
	}
	s.result = d * (px[symbol]/price - 1) * 100
	sum, up := 0.0, 0
	for _, u := range universe {
		if px[u.S] > 0 && u.P > 0 {
			m := (px[u.S]/u.P - 1) * 100
			sum += m
			if d*m > 0 {
				up++
			}
			s.n++
		}
	}
	if s.n == 0 {
		return s, false
	}
	s.random, s.upShare = d*sum/float64(s.n), float64(up)/float64(s.n)
	return s, true
}

// exits is the ledger's newest exits, newest first (contract RemovedPick).
func exits(ctx context.Context, db *pgxpool.Pool) ([]RemovedPick, error) {
	rows, _ := db.Query(ctx, `select symbol, side, picked_at, removed_at, price_at_pick, target, invalidation, exit_at, exit_price,
		return_pct, exit_reason, result_pct, random_pct, scored_at from signal_picks
		where removed_at is not null order by removed_at desc, picked_at desc limit $1`, maxHistory)
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RemovedPick, error) {
		var r RemovedPick
		err := row.Scan(&r.Symbol, &r.Side, &r.PickedAt, &r.RemovedAt, &r.PriceAtPick, &r.Target, &r.Invalidation, &r.ExitAt, &r.ExitPrice,
			&r.ReturnPct, &r.Reason, &r.Result72Pct, &r.Random72Pct, &r.ScoredAt)
		r.Result72Pct, r.Random72Pct = round2p(r.Result72Pct), round2p(r.Random72Pct)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("signals: exits: %w", err)
	}
	if out == nil {
		out = []RemovedPick{}
	}
	return out, nil
}

// RecordSide is the track record of one side (or both: all) over the scored picks.
type RecordSide struct {
	Scored               int      `json:"scored"`
	Blocks               int      `json:"blocks"` // non-empty 3-day blocks
	MovedItsWayPct       *float64 `json:"movedItsWayPct"`
	RandomMovedItsWayPct *float64 `json:"randomMovedItsWayPct"`
	AvgResultPct         *float64 `json:"avgResultPct"`
	AvgResultNetPct      *float64 `json:"avgResultNetPct"` // after costPct
	AvgRandomPct         *float64 `json:"avgRandomPct"`
	ExcessPct            *float64 `json:"excessPct"` // result − random
	ExcessLo99           *float64 `json:"excessLo99"`
	ExcessHi99           *float64 `json:"excessHi99"`
	TargetHit            int      `json:"targetHit"`
	Invalidated          int      `json:"invalidated"`
	Expired              int      `json:"expired"`
	Verdict              string   `json:"verdict"` // insufficient | no_difference | better | worse
}

// ReplaySide is one side of the out-of-sample replay: shares of picks whose
// first level touched within 72 h was the target or the invalidation, or
// neither (expired), next to a random pick's, and the 72 h excess with its 95% CI.
type ReplaySide struct {
	Picks                     int     `json:"picks"`
	TargetFirstPct            float64 `json:"targetFirstPct"`
	InvalidatedFirstPct       float64 `json:"invalidatedFirstPct"`
	ExpiredPct                float64 `json:"expiredPct"`
	RandomTargetFirstPct      float64 `json:"randomTargetFirstPct"`
	RandomInvalidatedFirstPct float64 `json:"randomInvalidatedFirstPct"`
	ExcessPct                 float64 `json:"excessPct"`
	ExcessLo95                float64 `json:"excessLo95"`
	ExcessHi95                float64 `json:"excessHi95"`
}

// Replay is the rule's replay on data it was not tuned on (constants).
type Replay struct {
	From                 string     `json:"from"`
	To                   string     `json:"to"`
	Picks                int        `json:"picks"`
	Days                 int        `json:"days"`
	Blocks               int        `json:"blocks"`
	MovedItsWayPct       float64    `json:"movedItsWayPct"`
	RandomMovedItsWayPct float64    `json:"randomMovedItsWayPct"`
	AvgResultPct         float64    `json:"avgResultPct"`
	AvgResultNetPct      float64    `json:"avgResultNetPct"`
	AvgRandomPct         float64    `json:"avgRandomPct"`
	ExcessPct            float64    `json:"excessPct"`
	ExcessLo95           float64    `json:"excessLo95"`
	ExcessHi95           float64    `json:"excessHi95"`
	Bullish              ReplaySide `json:"bullish"`
	Bearish              ReplaySide `json:"bearish"`
}

// replayRef is the steady board's out-of-sample replay (main universe,
// 2025-12-24 to 2026-10-10, holdout of the sig2 study): no measurable edge.
var replayRef = Replay{From: "2025-12-24", To: "2026-10-10", Picks: 864, Days: 281, Blocks: 97,
	MovedItsWayPct: 49.3, RandomMovedItsWayPct: 49.3, AvgResultPct: -0.12, AvgResultNetPct: -0.22, AvgRandomPct: -0.09,
	ExcessPct: -0.03, ExcessLo95: -0.97, ExcessHi95: 0.72,
	Bullish: ReplaySide{Picks: 449, TargetFirstPct: 20.3, InvalidatedFirstPct: 11.8, ExpiredPct: 67.9,
		RandomTargetFirstPct: 12.1, RandomInvalidatedFirstPct: 8.1, ExcessPct: 0.53, ExcessLo95: -0.65, ExcessHi95: 1.38},
	Bearish: ReplaySide{Picks: 415, TargetFirstPct: 7.2, InvalidatedFirstPct: 10.1, ExpiredPct: 82.7,
		RandomTargetFirstPct: 7.5, RandomInvalidatedFirstPct: 12.4, ExcessPct: -0.64, ExcessLo95: -1.77, ExcessHi95: 0.39}}

// Record is the board's track record (contract SignalRecord): every pick
// scored at 72 h against a random pick from the same list, per side, with
// the replay's figures next to it.
type Record struct {
	Rule         string     `json:"rule"`
	Since        *time.Time `json:"since"` // the first ledger pick
	HorizonHours int        `json:"horizonHours"`
	CostPct      float64    `json:"costPct"`
	MinPicks     int        `json:"minPicks"`
	MinBlocks    int        `json:"minBlocks"`
	Confidence   float64    `json:"confidence"`
	Open         int        `json:"open"`     // unscored, picked within the last 7 days
	Unscored     int        `json:"unscored"` // unscored and older: no board scored them in time
	All          RecordSide `json:"all"`
	Bullish      RecordSide `json:"bullish"`
	Bearish      RecordSide `json:"bearish"`
	Replay       Replay     `json:"replay"`
}

// ledgerRow is what the record reads of one signal_picks row.
type ledgerRow struct {
	side     string
	pickedAt time.Time
	reason   *string
	result   *float64
	random   *float64
	upShare  *float64
}

// LoadRecord computes the track record from the ledger (for the agent's get_signals too).
func LoadRecord(ctx context.Context, db *pgxpool.Pool) (Record, error) {
	rows, _ := db.Query(ctx, `select side, picked_at, exit_reason, result_pct, random_pct, random_up_share from signal_picks where rule = $1`, rule)
	ledgerRows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ledgerRow, error) {
		var r ledgerRow
		return r, row.Scan(&r.side, &r.pickedAt, &r.reason, &r.result, &r.random, &r.upShare)
	})
	if err != nil {
		return Record{}, fmt.Errorf("signals: record: %w", err)
	}
	return record(ledgerRows, time.Now()), nil
}

// record is the track record of rows at now.
func record(rows []ledgerRow, now time.Time) Record {
	r := Record{Rule: rule, HorizonHours: int(horizon / time.Hour), CostPct: costPct, MinPicks: recordMinPicks, MinBlocks: recordMinBlocks,
		Confidence: 0.99, Replay: replayRef}
	for _, x := range rows {
		if r.Since == nil || x.pickedAt.Before(*r.Since) {
			t := x.pickedAt.UTC()
			r.Since = &t
		}
		if x.result == nil {
			if now.Sub(x.pickedAt) < keep {
				r.Open++
			} else {
				r.Unscored++
			}
		}
	}
	r.All, r.Bullish, r.Bearish = side(rows, r.Since, ""), side(rows, r.Since, "bullish"), side(rows, r.Since, "bearish")
	return r
}

// stats is one side's figures unrounded.
type stats struct {
	scored, blocks, nb             int
	moved, randomMoved             float64
	result, random                 float64
	excess, se                     float64
	targetHit, invalidated, expire int
}

// sideStats computes one side's figures ("" for both) over the scored rows:
// the excess's standard error is over non-overlapping 3-day blocks of pick
// days counted from since, each block's excess sum weighted by its count.
func sideStats(rows []ledgerRow, since *time.Time, which string) stats {
	var s stats
	type block struct {
		sum float64
		n   int
	}
	blocks := map[int64]*block{}
	for _, x := range rows {
		if x.result == nil || x.random == nil || which != "" && x.side != which {
			continue
		}
		s.scored++
		if *x.result > 0 {
			s.moved++
		}
		if x.upShare != nil {
			s.randomMoved += *x.upShare
		}
		s.result += *x.result
		s.random += *x.random
		if x.reason != nil {
			switch *x.reason {
			case "target_hit":
				s.targetHit++
			case "invalidated":
				s.invalidated++
			case "expired":
				s.expire++
			}
		}
		k := (utcDay(x.pickedAt) - utcDay(*since)) / blockDays
		if blocks[k] == nil {
			blocks[k] = &block{}
		}
		blocks[k].sum += *x.result - *x.random
		blocks[k].n++
	}
	s.blocks, s.nb = len(blocks), len(blocks)
	if s.scored == 0 {
		return s
	}
	n := float64(s.scored)
	s.moved, s.randomMoved, s.result, s.random = s.moved/n, s.randomMoved/n, s.result/n, s.random/n
	s.excess = s.result - s.random
	if s.nb >= 2 {
		ss := 0.0
		for _, b := range blocks {
			ss += math.Pow(b.sum-s.excess*float64(b.n), 2)
		}
		nb := float64(s.nb)
		s.se = math.Sqrt(ss/(nb-1)*nb) / n
	}
	return s
}

func utcDay(t time.Time) int64 { return int64(math.Floor(float64(t.Unix()) / 86400)) }

// side is one side's RecordSide, percentages to 2 dp.
func side(rows []ledgerRow, since *time.Time, which string) RecordSide {
	s := sideStats(rows, since, which)
	out := RecordSide{Scored: s.scored, Blocks: s.blocks, TargetHit: s.targetHit, Invalidated: s.invalidated, Expired: s.expire, Verdict: "insufficient"}
	if s.scored == 0 {
		return out
	}
	out.MovedItsWayPct, out.RandomMovedItsWayPct = round2v(100*s.moved), round2v(100*s.randomMoved)
	out.AvgResultPct, out.AvgResultNetPct, out.AvgRandomPct = round2v(s.result), round2v(s.result-costPct), round2v(s.random)
	if s.nb < 2 {
		return out
	}
	lo, hi := s.excess-recordZ*s.se, s.excess+recordZ*s.se
	out.ExcessPct, out.ExcessLo99, out.ExcessHi99 = round2v(s.excess), round2v(lo), round2v(hi)
	switch {
	case s.scored < recordMinPicks || s.blocks < recordMinBlocks:
	case lo > 0:
		out.Verdict = "better"
	case hi < 0:
		out.Verdict = "worse"
	default:
		out.Verdict = "no_difference"
	}
	return out
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func round2v(x float64) *float64 {
	r := round2(x)
	return &r
}

func round2p(x *float64) *float64 {
	if x == nil {
		return nil
	}
	return round2v(*x)
}
