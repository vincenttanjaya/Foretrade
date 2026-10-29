package forecast

// The track record: forecasts resolve (automatically from Binance closes for
// price rules, by hand otherwise) and resolved ones are scored (Brier, log
// loss, hit rate, reliability bins), the shared replay set separately.

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
)

// PriceRule resolves a binary question from the Binance <Symbol>USDT daily
// closes; its date is the forecast's resolution date. on_date: Yes iff the
// candle opening that day closes strictly above (below) Threshold. by_date:
// Yes as soon as any daily close from the creation day through that date
// does; No once that day's candle has closed without it.
type PriceRule struct {
	Symbol    string  `json:"symbol"`
	Op        string  `json:"op"` // above | below
	Threshold float64 `json:"threshold"`
	Mode      string  `json:"mode"` // on_date | by_date
}

// cleanRule is r with a canonical symbol, op and mode when it is a valid rule
// for a question of this shape, else nil.
func cleanRule(r *PriceRule, shape string) *PriceRule {
	if r == nil || shape != "binary" {
		return nil
	}
	info, ok := market.LookupCrypto(r.Symbol)
	c := PriceRule{Symbol: info.Symbol, Op: oneOf(r.Op, "", "above", "below"), Threshold: r.Threshold, Mode: oneOf(r.Mode, "", "on_date", "by_date")}
	if !ok || c.Op == "" || c.Mode == "" || !(c.Threshold > 0) || math.IsInf(c.Threshold, 1) {
		return nil
	}
	return &c
}

// decide resolves rule from daily candles (any order) as of now: the outcome
// and the deciding candle's close time, or ok false while a candle it needs
// is missing or still open. from is the first day a by_date rule counts.
func decide(rule PriceRule, candles []market.Candle, from, resolutionDate, now time.Time) (string, time.Time, bool) {
	if cleanRule(&rule, "binary") == nil {
		return "", time.Time{}, false
	}
	closes := make(map[int64]float64, len(candles))
	for _, c := range candles {
		closes[c.T] = c.C
	}
	day := from.UTC().Truncate(24 * time.Hour)
	if rule.Mode == "on_date" || day.After(resolutionDate) {
		day = resolutionDate
	}
	for ; !day.After(resolutionDate); day = day.AddDate(0, 0, 1) {
		c, ok := closes[day.Unix()]
		if !ok || now.Before(day.AddDate(0, 0, 1)) {
			return "", time.Time{}, false
		}
		if rule.Op == "above" && c > rule.Threshold || rule.Op == "below" && c < rule.Threshold {
			return "Yes", day.Add(24*time.Hour - time.Millisecond), true
		}
	}
	return "No", resolutionDate.Add(24*time.Hour - time.Millisecond), true
}

// resolveDue resolves the users' completed price-rule forecasts that can be
// decided now (on_date: the resolution date has passed; by_date: any time),
// settling the paper positions on each (and the runs it updates) in the same
// transaction. A forecast whose candles are not in yet waits for the next call.
func (h *handler) resolveDue(ctx context.Context, now time.Time) (int, error) {
	rows, err := h.d.DB.Query(ctx, `
		select id, price_rule, created_at, resolution_date from forecasts
		where status = 'completed' and outcome is null and price_rule is not null and user_id is not null
		  and (price_rule->>'mode' = 'by_date' or resolution_date < $1)`, now.UTC().Truncate(24*time.Hour))
	if err != nil {
		return 0, err
	}
	type due struct {
		id        string
		rule      PriceRule
		from, res time.Time
	}
	var dues []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.rule, &d.from, &d.res); err != nil {
			rows.Close()
			return 0, err
		}
		dues = append(dues, d)
	}
	if rows.Close(); rows.Err() != nil {
		return 0, rows.Err()
	}
	n := 0
	for _, d := range dues {
		if cleanRule(&d.rule, "binary") == nil {
			continue
		} // stored JSON can predate validation or be forged
		days := int(now.Sub(d.from).Hours()/24) + 2
		candles, err := h.d.Market.Candles(ctx, d.rule.Symbol, "1d", min(max(days, 2), 1000)) // cached: one Binance call per symbol
		if err != nil {
			slog.Warn("forecast resolver: no candles", "symbol", d.rule.Symbol, "err", err)
			continue
		}
		outcome, at, ok := decide(d.rule, candles, d.from, d.res, now)
		if !ok {
			continue
		}
		resolved := false
		if err := pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `update forecasts set outcome = $2, resolved_at = $3, resolved_by = 'auto'
				where id = $1 and outcome is null and status = 'completed'`, d.id, outcome, at)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			resolved = true
			return settle(ctx, tx, d.id, outcome)
		}); err != nil {
			return n, err
		}
		if resolved {
			n++
		}
	}
	return n, nil
}

// settle settles the open paper positions on id and the runs it updates at outcome.
func settle(ctx context.Context, tx pgx.Tx, id, outcome string) error {
	ids, err := ancestry(ctx, tx, id)
	if err != nil {
		return err
	}
	return paper.SettleTx(ctx, tx, ids, outcome)
}

func (h *handler) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Outcome *string `json:"outcome"` // null clears a manual resolution
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	ctx, user, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	if err := h.resolve(ctx, user, id, req.Outcome); err != nil {
		app.WriteError(w, err)
		return
	}
	h.handleGet(w, r)
}

// resolve records (or, with nil, clears) the outcome of the user's own
// completed forecast that has no price rule, and settles the paper positions
// on it and the runs it updates, in one transaction. Early resolution is
// allowed; once positions on that lineage have settled the outcome can be
// re-confirmed but not cleared or changed.
func (h *handler) resolve(ctx context.Context, user, id string, outcome *string) error {
	if !validID(id) {
		return app.ErrNotFound
	}
	return pgx.BeginFunc(ctx, h.d.DB, func(tx pgx.Tx) error {
		var status string
		var outcomes []string
		var auto bool
		var current *string
		if err := tx.QueryRow(ctx, `select status, outcomes, price_rule is not null, outcome from forecasts
			where id = $1 and user_id = $2 and status <> 'archived' for update`, id, user).Scan(&status, &outcomes, &auto, &current); err != nil {
			return err
		}
		next := ""
		if outcome != nil {
			next = strings.TrimSpace(*outcome)
		}
		switch {
		case status != "completed":
			return &app.Conflict{Msg: "Only a completed forecast can be resolved."}
		case auto:
			return &app.Conflict{Msg: "This forecast resolves automatically from Binance data."}
		case outcome != nil && !slices.Contains(outcomes, next):
			return &app.BadRequest{Msg: "outcome must be one of the forecast's outcomes."}
		}
		ids, err := ancestry(ctx, tx, id)
		if err != nil {
			return err
		}
		if current != nil && (outcome == nil || next != *current) {
			var settled bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from market_positions where forecast_id = any($1::uuid[]) and status <> 'open')`,
				ids).Scan(&settled); err != nil {
				return err
			}
			if settled {
				return &app.Conflict{Msg: "Paper positions on this forecast have settled, so its resolution can't change."}
			}
		}
		var tag pgconn.CommandTag
		if outcome == nil {
			tag, err = tx.Exec(ctx, `update forecasts set outcome = null, resolved_at = null, resolved_by = null
				where id = $1 and user_id = $2 and price_rule is null and status = 'completed'`, id, user)
		} else {
			tag, err = tx.Exec(ctx, `update forecasts set outcome = $3, resolved_at = now(), resolved_by = 'manual'
				where id = $1 and user_id = $2 and price_rule is null and status = 'completed'`, id, user, next)
		}
		switch {
		case err != nil:
			return err
		case tag.RowsAffected() == 0:
			return &app.Conflict{Msg: "Only a completed forecast can be resolved."}
		case outcome == nil:
			return nil
		}
		return paper.SettleTx(ctx, tx, ids, next)
	})
}

// Scoring.

type TrackRecord struct {
	N             int      `json:"n"`
	Brier         *float64 `json:"brier"`
	BaselineBrier *float64 `json:"baselineBrier"` // the uniform forecast's
	AnchorBrier   *float64 `json:"anchorBrier"`   // the volatility-only anchor's over the rows with one; null when anchorN = 0
	LogLoss       *float64 `json:"logLoss"`
	HitRate       *float64 `json:"hitRate"` // the top outcome happened
	Bins          []Bin    `json:"bins"`

	Skill          *float64    `json:"skill"`          // 1 − brier/baselineBrier; null when n = 0
	BrierCI90      *[2]float64 `json:"brierCi90"`      // bootstrap 90% interval of the mean Brier; null when n < 5
	AnchorDiffCI90 *[2]float64 `json:"anchorDiffCi90"` // paired bootstrap 90% interval of mean(Brier − anchor Brier) over the anchored rows; null when anchorN < 5

	AnchorN     int      `json:"anchorN"`     // round 8b: the binary rows with an anchor (replay seed rows; live runs with R1)
	AnchorSkill *float64 `json:"anchorSkill"` // round 8b: 1 − (the desk's mean Brier on those rows)/anchorBrier; null when anchorN = 0

	Claim Claim `json:"claim"` // round 8b: what the product may say about this record; the fields above stay for compatibility and admin use
}

// claimMinN is the resolved questions a score claim needs: fewer cannot
// separate skill from luck at these effect sizes (the replay's 24 gave a 90%
// range of 0.15–0.23).
const claimMinN = 50

// Claim is the record's verdict, symmetric so no direction is claimed early.
// basis: "formula" with ≥ minN anchored rows (desk vs the volatility-only
// anchor, paired); "uniform" with ≥ minN rows and none anchored (vs the
// uniform forecast); null otherwise, and status is then "too_few". status
// "ahead" or "behind" when the basis's whole 90% range is on one side,
// "no_edge" when it spans the line. n counts toward the basis: anchorN when
// any row is anchored, else n. skill only when ahead.
type Claim struct {
	Status string   `json:"status"` // too_few | ahead | no_edge | behind
	Basis  *string  `json:"basis"`  // formula | uniform | null
	MinN   int      `json:"minN"`
	N      int      `json:"n"`
	Skill  *float64 `json:"skill"` // anchorSkill (formula) or skill (uniform); null unless ahead
}

// Bin is one reliability bucket of forecast probabilities.
type Bin struct {
	Lo       float64  `json:"lo"`
	Hi       float64  `json:"hi"`
	N        int      `json:"n"`
	MeanP    *float64 `json:"meanP"`
	Observed *float64 `json:"observed"` // share of the pairs whose outcome happened
}

// resolved is one scored forecast.
type resolved struct {
	shape    string
	outcomes []string
	probs    []OutcomeP
	outcome  string   // the one that happened
	anchorP  *float64 // P(Yes) of the volatility-only anchor: the replay's, or a live run's R1
}

var binEdges = [...]float64{0, 0.2, 0.4, 0.6, 0.8, 1}

// score: per forecast with K outcomes, brier = (1/K)·Σ(p_k − o_k)² (the
// classic (p − o)² for binary), baseline = (K−1)/K², log loss = −ln max(p_hit,
// 0.01), hit = the top outcome happened. Reliability pairs (p, happened): one
// per binary forecast (P(Yes)), one per outcome otherwise. skill = 1 −
// brier/baseline; the intervals are ci90 of the per-forecast Brier scores and
// of the paired (Brier − anchor Brier) gaps. The anchor fields cover only the
// binary rows with an anchorP: anchorBrier = mean (anchorP − o_Yes)²,
// anchorSkill = 1 − (the desk's mean Brier on the same rows)/anchorBrier.
func score(rows []resolved) TrackRecord {
	tr := TrackRecord{Bins: make([]Bin, len(binEdges)-1)}
	var brier, base, logLoss, hits, anchorSum, pairedSum float64
	var briers, gaps []float64
	var binP, binO [len(binEdges) - 1]float64
	pair := func(p float64, happened bool) {
		i := 0
		for i < len(tr.Bins)-1 && p >= binEdges[i+1] {
			i++
		}
		tr.Bins[i].N++
		binP[i] += p
		if happened {
			binO[i]++
		}
	}
	for _, r := range rows {
		hit := slices.Index(r.outcomes, r.outcome)
		if hit < 0 {
			continue
		}
		p := make([]float64, len(r.outcomes))
		for k, o := range r.outcomes {
			if i := slices.IndexFunc(r.probs, func(x OutcomeP) bool { return x.Outcome == o }); i >= 0 {
				p[k] = r.probs[i].P
			}
		}
		k := float64(len(p))
		b := 0.0
		for i := range p {
			b += math.Pow(p[i]-b2f(i == hit), 2)
		}
		brier += b / k
		briers = append(briers, b/k)
		base += (k - 1) / (k * k)
		logLoss -= math.Log(max(p[hit], 0.01))
		if top := slices.Index(p, slices.Max(p)); top == hit {
			hits++
		}
		if r.shape == "binary" {
			pair(p[0], hit == 0)
		} else {
			for i := range p {
				pair(p[i], i == hit)
			}
		}
		if r.anchorP != nil && r.shape == "binary" {
			a := math.Pow(*r.anchorP-b2f(hit == 0), 2)
			anchorSum += a
			pairedSum += b / k
			tr.AnchorN++
			gaps = append(gaps, b/k-a)
		}
		tr.N++
	}
	for i := range tr.Bins {
		tr.Bins[i].Lo, tr.Bins[i].Hi = binEdges[i], binEdges[i+1]
		if n := float64(tr.Bins[i].N); n > 0 {
			tr.Bins[i].MeanP, tr.Bins[i].Observed = ptr(binP[i]/n), ptr(binO[i]/n)
		}
	}
	if n := float64(tr.N); n > 0 {
		tr.Brier, tr.BaselineBrier, tr.LogLoss, tr.HitRate = ptr(brier/n), ptr(base/n), ptr(logLoss/n), ptr(hits/n)
		tr.Skill = ptr(1 - brier/base)
	}
	if tr.AnchorN > 0 {
		tr.AnchorBrier = ptr(anchorSum / float64(tr.AnchorN))
		if anchorSum > 0 {
			tr.AnchorSkill = ptr(1 - pairedSum/anchorSum)
		}
	}
	tr.BrierCI90, tr.AnchorDiffCI90 = ci90(briers), ci90(gaps)
	tr.Claim = claim(tr)
	return tr
}

// claim is tr's Claim (see Claim).
func claim(tr TrackRecord) Claim {
	c := Claim{Status: "too_few", MinN: claimMinN, N: tr.N}
	if tr.AnchorN > 0 {
		c.N = tr.AnchorN
	}
	var lo, hi, line float64 // the basis's 90% range of the desk's Brier, and the bar it is measured against
	var skill *float64
	switch {
	case tr.AnchorN >= claimMinN && tr.AnchorDiffCI90 != nil:
		c.Basis, skill = ptr("formula"), tr.AnchorSkill
		lo, hi = tr.AnchorDiffCI90[0], tr.AnchorDiffCI90[1] // desk − formula
	case tr.AnchorN == 0 && tr.N >= claimMinN && tr.BrierCI90 != nil:
		c.Basis, skill = ptr("uniform"), tr.Skill
		lo, hi, line = tr.BrierCI90[0], tr.BrierCI90[1], *tr.BaselineBrier
	default:
		return c
	}
	switch {
	case hi < line:
		c.Status, c.Skill = "ahead", skill
	case lo > line:
		c.Status = "behind"
	default:
		c.Status = "no_edge"
	}
	return c
}

// ci90 is the bootstrap 90% interval of the mean of xs (the 5th and 95th
// percentiles of 1,000 resample means), seeded with 1 so a response is
// stable between calls; nil under 5 values.
func ci90(xs []float64) *[2]float64 {
	if len(xs) < 5 {
		return nil
	}
	r := rand.New(rand.NewPCG(1, 1))
	means := make([]float64, 1000)
	for i := range means {
		sum := 0.0
		for range xs {
			sum += xs[r.IntN(len(xs))]
		}
		means[i] = sum / float64(len(xs))
	}
	slices.Sort(means)
	return &[2]float64{means[50], means[949]}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func ptr[T any](v T) *T { return &v }

func (h *handler) handleTrackRecord(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.DB.Query(r.Context(), `
		select user_id is null, shape, outcomes, coalesce(result->'probabilities', '[]'), outcome, anchor_p from forecasts f
		where status = 'completed' and outcome is not null and (user_id = $1 or user_id is null)
		  and not exists (select 1 from forecasts c where c.parent_id = f.id and c.status = 'completed')`, httpx.UserID(r.Context())) // a lineage counts once: its head
	if err != nil {
		app.WriteError(w, err)
		return
	}
	defer rows.Close()
	var replay, mine []resolved
	for rows.Next() {
		var isReplay bool
		var f resolved
		if err := rows.Scan(&isReplay, &f.shape, &f.outcomes, &f.probs, &f.outcome, &f.anchorP); err != nil {
			app.WriteError(w, err)
			return
		}
		if isReplay {
			replay = append(replay, f)
		} else {
			mine = append(mine, f)
		}
	}
	if err := rows.Err(); err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]TrackRecord{"replay": score(replay), "mine": score(mine)})
}

// The replay seed: rows cmd/forecast-replay generated, inserted at boot.

//go:embed replay_seed.json
var replaySeed []byte

// SeedRow is one replay_seed.json entry.
type SeedRow struct {
	Key        string     `json:"key"`
	AsOf       string     `json:"asOf"` // YYYY-MM-DD
	Draft      Draft      `json:"draft"`
	PriceRule  PriceRule  `json:"priceRule"`
	Result     Result     `json:"result"`
	Outcome    *string    `json:"outcome"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	AnchorP    float64    `json:"anchorP"`
}

// SeedSummary is cmd/forecast-replay's closing report on rows: the desk's
// Brier against the volatility-only formula (the seed's anchorP) and the coin
// flip, skill against each, the paired 90% range of desk − formula, and how
// many answers the shrink guard moved to the formula.
func SeedSummary(rows []SeedRow) string {
	var rs []resolved
	moved := 0
	for _, r := range rows {
		if r.Result.Guard == "shrink" {
			moved++
		}
		if r.Outcome != nil {
			rs = append(rs, resolved{shape: r.Draft.Shape, outcomes: r.Draft.Outcomes, probs: r.Result.Probabilities, outcome: *r.Outcome, anchorP: ptr(r.AnchorP)})
		}
	}
	tr := score(rs)
	guard := fmt.Sprintf("The shrink guard moved %d of %d answers to the formula.", moved, len(rows))
	if tr.N == 0 {
		return "Replay score: no resolved questions. " + guard
	}
	span := func(r *[2]float64, f string) string {
		if r == nil {
			return "n < 5"
		}
		return fmt.Sprintf(f+" to "+f, r[0], r[1])
	}
	out := fmt.Sprintf("Replay score over %d resolved questions: desk Brier %.4f (90%% range %s), coin flip %.4f, skill vs the coin flip %+.1f%%.",
		tr.N, *tr.Brier, span(tr.BrierCI90, "%.3f"), *tr.BaselineBrier, *tr.Skill*100)
	if tr.AnchorBrier != nil && tr.AnchorSkill != nil {
		out += fmt.Sprintf(" Volatility-only formula Brier %.4f over %d paired questions, skill vs the formula %+.1f%% (paired 90%% range of desk − formula %s).",
			*tr.AnchorBrier, tr.AnchorN, *tr.AnchorSkill*100, span(tr.AnchorDiffCI90, "%+.4f"))
	}
	return out + " " + guard
}

// insertSeed adds the seed rows missing by replay_key, created and completed
// at asOf 00:00 UTC + i minutes. It never updates a row.
// ponytail: to regenerate, delete the replay rows (user_id is null) by hand first.
func insertSeed(ctx context.Context, db *pgxpool.Pool, seed []byte) (int, error) {
	var rows []SeedRow
	if err := json.Unmarshal(seed, &rows); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		asOf, err := time.Parse(time.DateOnly, r.AsOf)
		if err != nil {
			return n, err
		}
		at := asOf.Add(time.Duration(replayIndex(r.PriceRule.Symbol, r.AsOf)) * time.Minute)
		var by *string
		if r.Outcome != nil {
			by = ptr("auto")
		}
		tag, err := db.Exec(ctx, `
			insert into forecasts (shape, question, outcomes, resolution_criteria, resolution_date, category, status, result,
				created_at, completed_at, price_rule, outcome, resolved_at, resolved_by, replay_key, as_of, anchor_p)
			values ($1, $2, $3, $4, $5, $6, 'completed', $7, $8, $8, $9, $10, $11, $12, $13, $14, $15)
			on conflict (replay_key) do nothing`,
			r.Draft.Shape, r.Draft.Question, r.Draft.Outcomes, r.Draft.ResolutionCriteria, r.Draft.ResolutionDate, r.Draft.Category,
			r.Result, at, r.PriceRule, r.Outcome, r.ResolvedAt, by, r.Key, r.AsOf, r.AnchorP)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
