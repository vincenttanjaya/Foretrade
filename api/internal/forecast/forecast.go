// Package forecast is the forecast desk: a rough idea
// becomes a resolvable question, then three specialist agents and an
// aggregator turn it into a probability estimate with an audit
// trail. Runs happen in a goroutine; the client polls GET /api/forecasts/{id}.
package forecast

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/attach"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

type Draft struct {
	Shape              string     `json:"shape"` // binary | ordered | categorical
	Question           string     `json:"question"`
	Outcomes           []string   `json:"outcomes"` // binary: ["Yes","No"]; ordered: low→high buckets
	ResolutionCriteria string     `json:"resolutionCriteria"`
	ResolutionDate     string     `json:"resolutionDate"` // YYYY-MM-DD
	Category           string     `json:"category"`
	PriceRule          *PriceRule `json:"priceRule"` // binary Binance price questions only; resolves automatically
	Context            string     `json:"context"`   // the user's notes for the panel (S0, unverified); the draft endpoint returns ""

	LinkedMarket *LinkedMarket `json:"linkedMarket,omitempty"` // board.go: the Polymarket market a board draft came from; a run compares against it first
}

// LinkedMarket is the immutable external predicate a manual forecast prices.
// Older rows may contain only EventSlug and Question; these are not canonical.
type LinkedMarket struct {
	MarketID         string    `json:"marketId"`
	ConditionID      string    `json:"conditionId"`
	MarketSlug       string    `json:"marketSlug"`
	EventSlug        string    `json:"eventSlug"`
	Question         string    `json:"question"`
	Description      string    `json:"description"`
	ResolutionSource *string   `json:"resolutionSource"`
	EndDate          string    `json:"endDate"`
	AsOf             time.Time `json:"asOf"`
}

type OutcomeP struct {
	Outcome string  `json:"outcome"`
	P       float64 `json:"p"`
}

type Specialist struct {
	Role          string     `json:"role"`
	Name          string     `json:"name"`
	Probabilities []OutcomeP `json:"probabilities"`
	Reasoning     string     `json:"reasoning"`
	KeyFactors    []string   `json:"keyFactors"`
	Confidence    string     `json:"confidence"`

	// Numeric frames only: the method the server saw it use (from the
	// tool calls that ran), the tool's own distribution it started from, its
	// distribution read as a view, and the quant's valuation scenarios.
	Method string          `json:"method,omitempty"` // history | fundamentals | volatility | regime | judgment | fallback
	Anchor []OutcomeP      `json:"anchor,omitempty"`
	Stats  *SpecStats      `json:"stats,omitempty"` // ordered results; derived on read for older rows
	Model  []ModelScenario `json:"model,omitempty"`

	// family is the input its anchor rests on (deskKit.read):
	// "history" for H1's blend, a regime on H1's lognormal and a volatility
	// member whose σ came from history_stats; its own for fundamentals and a
	// price_outlook volatility member. Never stored; inputFamily reads Method
	// when it is unset.
	family string
}

// ModelScenario is one scenario of the quant's last scenario_model call.
type ModelScenario struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	P        float64        `json:"p"`
	Price    float64        `json:"price"`
	VsRefPct *float64       `json:"vsRefPct"`
	Inputs   []market.Input `json:"inputs"`
}

type Driver struct {
	Factor    string `json:"factor"`
	Direction string `json:"direction"` // "up" raises the top outcome
	Weight    string `json:"weight"`
}

type Evidence struct {
	Claim    string  `json:"claim"`
	Source   string  `json:"source"`
	Stance   string  `json:"stance"`
	AsOf     string  `json:"asOf"`
	URL      *string `json:"url"`                // the cited web source's URL; null for none or an unknown id
	SourceID string  `json:"sourceId,omitempty"` // the aggregator's citation ("S3"); cleared once mapped to URL
	// "D2", the user's material this claim comes from; url stays null and the page links its card
	MaterialID string `json:"materialId,omitempty"`
}

type Flag struct {
	Type string `json:"type"` // conflict | stale | duplicate | low_information
	Note string `json:"note"`
}

// Finding is one atomic claim of the research ledger (contract Finding, round
// 8): extracted from the sources it cites, which are always ids of this
// result's own pack, so its links come from the server, never from page text.
type Finding struct {
	ID        string   `json:"id"`               // "F1".., ledger order
	Claim     string   `json:"claim"`            // one dated factual sentence, no URLs
	SourceIDs []string `json:"sourceIds"`        // 1..4 ids in result.sources
	AsOf      *string  `json:"asOf"`             // YYYY-MM-DD | YYYY-MM | null
	Angle     string   `json:"angle"`            // a research.angles id
	Direction string   `json:"direction"`        // binary yes|no|context; ordered higher|lower|center|tail|context; categorical an outcome or context
	Strength  string   `json:"strength"`         // strong | moderate | slight | minimal
	Origin    string   `json:"origin,omitempty"` // "material" when read from the user's files (sourceIds are D ids); absent for web research
	Ref       string   `json:"ref,omitempty"`    // material findings: where in the file ("p. 4")
	order     int      // extraction order: the ledger's last tie-break; not stored
}

// Watch is one pending signal of the report's "What to watch".
type Watch struct {
	Signal   string   `json:"signal"`
	Detail   string   `json:"detail"`
	Branches []Branch `json:"branches"` // 1..3
}

type Branch struct {
	If        string `json:"if"`
	Then      string `json:"then"`
	Direction string `json:"direction"` // a Finding direction
}

type Result struct {
	Probabilities   []OutcomeP    `json:"probabilities"`
	Ensemble        []OutcomeP    `json:"ensemble"`
	Specialists     []Specialist  `json:"specialists"`
	Summary         string        `json:"summary"`
	Drivers         []Driver      `json:"drivers"`
	Evidence        []Evidence    `json:"evidence"`
	Flags           []Flag        `json:"flags"`
	WhatWouldChange []string      `json:"whatWouldChange"`
	Confidence      string        `json:"confidence"`
	KnowledgeNote   string        `json:"knowledgeNote"`
	Sources         []Source      `json:"sources"`              // the evidence pack S1..S8 (deep research: S1..S24)
	Research        Research      `json:"research"`             // the search queries behind it
	Findings        []Finding     `json:"findings"`             // the research ledger F1..Fn; [] without deep research
	Market          *MarketOdds   `json:"market"`               // the matching Polymarket market; binary live runs only
	Ladder          []*MarketOdds `json:"ladder,omitempty"`     // ladder.go: ordered live runs, the Polymarket strike ladder, ascending, 2..8 rows
	ChangeNote      string        `json:"changeNote,omitempty"` // updates only: what moved the estimate since the previous run
	// ViewChange (an update of a numeric question whose previous run and this
	// one both have a view) is the change the server reads off
	// the two stored views: "Median $570 → $611 (+7.2%) · chance at or above
	// today 58% → 62%", or "80% range $356–$917 → $380–$950" when either
	// states no median. A numeric changeNote then gives the reasons only.
	ViewChange string `json:"viewChange,omitempty"`

	MarketCheckedAt *time.Time `json:"marketCheckedAt,omitempty"` // when POST /market last searched Gamma (refresh.go)
	Watch           []Watch    `json:"watch,omitempty"`           // 0..4 pending signals; absent when none

	Reference     *Reference     `json:"reference"`               // reference.go: R1, the volatility-only P(Yes); null without a price rule and for older rows
	ReferenceNote *ReferenceNote `json:"referenceNote,omitempty"` // the aggregator's cited reason for moving toward 50% from R1; only when it kept the number
	Guard         string         `json:"guard,omitempty"`         // "shrink" when the server reset P(Yes) to R1

	// The desk view (view.go, views.go, frame.go): additive, older rows lack them all.
	View      *DeskView      `json:"view,omitempty"`      // ordered results whose buckets parse; derived on read for older rows
	Scenarios []DeskScenario `json:"scenarios,omitempty"` // 2..5, low to high: the quant's scenario_model levels, odds from the final buckets
	Methods   *Methods       `json:"methods,omitempty"`   // numeric frames with the methods on: weights, pool, agreement, guard
	Measured  *Measured      `json:"measured,omitempty"`  // the subject's measured daily history (H1)
}

type Forecast struct {
	Draft
	ID                 string      `json:"id"`
	Status             string      `json:"status"`
	ArchivedFromStatus *string     `json:"archivedFromStatus"`
	Error              *string     `json:"error"`
	Result             *Result     `json:"result"`
	CreatedAt          time.Time   `json:"createdAt"`
	CompletedAt        *time.Time  `json:"completedAt"`
	Resolution         *Resolution `json:"resolution"` // null = unresolved
	Replay             *ReplayInfo `json:"replay"`     // null = the caller's own forecast
	ParentID           *string     `json:"parentId"`   // the run this one updates; null for a first run
	History            []Point     `json:"history"`    // the lineage's completed runs, oldest first; [] for a replay
	Progress           *Progress   `json:"progress"`   // the run's live trace; null before the run's first write, for older runs and replays
	Materials          []Material  `json:"materials"`  // the files the user attached and what the desk read (materials.go); [] without
}

// Point is one completed run in a forecast's probability history.
type Point struct {
	ID      string    `json:"id"`
	Outcome string    `json:"outcome"` // the run's headline outcome (binary: Yes)
	P       float64   `json:"p"`
	At      time.Time `json:"at"`               // completedAt
	Median  *float64  `json:"median,omitempty"` // the run's desk-view median (ordered runs whose buckets parse)
}

type Resolution struct {
	Outcome    string    `json:"outcome"`
	ResolvedAt time.Time `json:"resolvedAt"`
	By         string    `json:"by"` // auto | manual
}

// ReplayInfo marks a shared historical-replay row: the panel run as of AsOf.
type ReplayInfo struct {
	AsOf    string  `json:"asOf"`
	AnchorP float64 `json:"anchorP"` // P(Yes) of the as-of lognormal outlook
}

type Summary struct {
	ID                 string     `json:"id"`
	Shape              string     `json:"shape"`
	Question           string     `json:"question"`
	Category           string     `json:"category"`
	Status             string     `json:"status"`
	ArchivedFromStatus *string    `json:"archivedFromStatus"`
	ResolutionDate     string     `json:"resolutionDate"`
	CreatedAt          time.Time  `json:"createdAt"`
	CompletedAt        *time.Time `json:"completedAt"`
	Headline           *OutcomeP  `json:"headline"`
	ResolvedOutcome    *string    `json:"resolvedOutcome"`
	Replay             bool       `json:"replay"`
	ParentID           *string    `json:"parentId"`
	View               *ViewBrief `json:"view,omitempty"` // ordered results whose buckets parse
}

type handler struct{ d *app.Deps }

func Register(mux *http.ServeMux, d *app.Deps) { register(mux, d, newBoard(d)) }

// register mounts the desk's routes with b serving the prediction markets
// board (tests pass a board with their own clock).
func register(mux *http.ServeMux, d *app.Deps, b *board) {
	h := &handler{d}
	auth := func(f http.HandlerFunc) http.Handler { return d.Auth.Require(f) }
	mux.Handle("POST /api/forecasts/draft", auth(h.handleDraft))
	mux.Handle("POST /api/forecasts", auth(h.handleCreate))
	mux.Handle("GET /api/forecasts", auth(h.handleList))
	mux.Handle("GET /api/forecasts/track-record", auth(h.handleTrackRecord)) // literal: wins over {id}
	mux.Handle("POST /api/forecasts/{id}/resolve", auth(h.handleResolve))
	mux.Handle("POST /api/forecasts/{id}/update", auth(h.handleUpdate))
	mux.Handle("POST /api/forecasts/{id}/restore", auth(h.handleRestore))
	mux.Handle("GET /api/forecasts/{id}", auth(h.handleGet))
	mux.Handle("POST /api/forecasts/{id}/market", auth(h.handleRefreshMarket))         // refresh.go
	mux.Handle("POST /api/forecasts/{id}/market-position", auth(h.handleOpenPosition)) // positions.go
	mux.Handle("GET /api/market-positions", auth(h.handlePositions))
	mux.Handle("DELETE /api/forecasts/{id}", auth(h.handleArchive))
	b.register(mux) // the prediction markets board, board.go
}

func (h *handler) handleDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Idea string `json:"idea"`
	}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	idea := strings.TrimSpace(req.Idea)
	if n := utf8.RuneCountInString(idea); n < 3 || n > 400 {
		app.WriteError(w, &app.BadRequest{Msg: "idea must be 3..400 characters."})
		return
	}
	ctx, user := r.Context(), httpx.UserID(r.Context())
	credits, err := h.d.Usage.Spend(ctx, user, usage.CostDraft)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	draft, ref, err := h.draftWith(ctx, idea, todayUTC())
	if err != nil {
		if ctx.Err() == nil { // a client that hung up mid-call still pays: the provider already billed
			h.d.Usage.Refund(ctx, user, usage.CostDraft)
		}
		app.WriteError(w, err)
		return
	}
	out := map[string]any{"draft": draft, "credits": credits}
	if ref != nil { // the level the ranges are multiples of
		out["reference"] = ref
	}
	httpx.JSON(w, http.StatusOK, out)
}

// createRequest is a draft plus the user's materials: new files,
// and digests to reuse from one of the caller's forecasts on the same question.
type createRequest struct {
	Draft
	Outcomes       outcomeList `json:"outcomes"` // shadows Draft's: bounded while decoding
	Attachments    attach.List `json:"attachments"`
	ReuseMaterials *struct {
		ForecastID string    `json:"forecastId"`
		IDs        *reuseIDs `json:"ids"` // omitted: every material the desk read
	} `json:"reuseMaterials"`
}

// The upload cap lets a body hold millions of tiny array items: 32 MiB of
// empty strings decode into about 500 MiB of string headers before any check
// runs. The request's string arrays therefore stop decoding one item past
// their limit, which validation then rejects, as attach.List does.
type (
	outcomeList []string // validate allows at most maxOutcomes
	reuseIDs    []string // a forecast carries at most attach.Max materials; pick rejects more
	keepIDs     []string
)

const maxOutcomes = 8

func (l *outcomeList) UnmarshalJSON(b []byte) error {
	return shortList((*[]string)(l), b, maxOutcomes+1, "outcomes")
}

func (l *reuseIDs) UnmarshalJSON(b []byte) error {
	return shortList((*[]string)(l), b, attach.Max+1, "reuseMaterials.ids")
}

func (l *keepIDs) UnmarshalJSON(b []byte) error {
	return shortList((*[]string)(l), b, attach.Max+1, "keepMaterials")
}

// shortList decodes a JSON array of strings into out, keeping at most n
// items; its type errors name field as a plain []string's would.
func shortList(out *[]string, b []byte, n int, field string) error {
	if string(b) == "null" {
		*out = nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return &json.UnmarshalTypeError{Value: "value", Type: reflect.TypeFor[[]string](), Field: field}
	}
	l := []string{}
	for len(l) < n && dec.More() {
		var s string
		if err := dec.Decode(&s); err != nil {
			if typ, ok := err.(*json.UnmarshalTypeError); ok {
				typ.Field = field
			}
			return err
		}
		l = append(l, s)
	}
	*out = l
	return nil
}

// admit reserves an upload-sized body's share of the shared budget (429 at no
// cost when it is full) and decodes it; release frees the share once the
// files are decoded.
func admit(w http.ResponseWriter, r *http.Request, v any) (release func(), ok bool) {
	release, err := attach.Admit(w, r, httpx.UserID(r.Context()))
	if errors.Is(err, attach.ErrBusy) {
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many files are uploading right now. Try again in a moment.")
		return nil, false
	}
	if err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return nil, false
	}
	// The draft obeys its own rune limits (validate); the cap is the files'.
	if err := httpx.DecodeLimit(w, r, v, attach.BodyLimit); err != nil {
		release()
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return nil, false
	}
	return release, true
}

func (h *handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	release, ok := admit(w, r, &req)
	if !ok {
		return
	}
	defer release() // early returns; the normal path releases once decoded
	d := req.Draft
	d.Outcomes = req.Outcomes
	if err := validate(&d, todayUTC()); err != nil {
		app.WriteError(w, err)
		return
	}
	ctx, user := r.Context(), httpx.UserID(r.Context())
	if err := h.validateLinked(ctx, &d, true); err != nil {
		app.WriteError(w, err)
		return
	}
	set, err := attach.Decode(req.Attachments, "forecast")
	release()
	if err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	var reused []Material
	if ref := req.ReuseMaterials; ref != nil {
		if reused, err = h.reuse(ctx, user, d, ref.ForecastID, (*[]string)(ref.IDs)); err != nil {
			app.WriteError(w, err)
			return
		}
	}
	h.start(w, ctx, user, d, nil, nil, reused, set)
}

// start is create's and update's shared tail, once every check has passed:
// at most attach.Max materials, the charge (5 plus 1 per new file), the
// row, and the detached run. parent and p are set for an update.
func (h *handler) start(w http.ResponseWriter, ctx context.Context, user string, d Draft, parent *Forecast, p *prior, kept []Material, set attach.Set) {
	if len(kept)+len(set.Files) > attach.Max {
		app.WriteError(w, &app.BadRequest{Msg: fmt.Sprintf("A forecast can carry at most %d materials. Remove one first.", attach.Max)})
		return
	}
	jctx, done, accepted := h.d.Tasks.Begin("forecast", ctx)
	if !accepted {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "Server is stopping.")
		return
	}
	started := false
	defer func() {
		if !started {
			done()
		}
	}()
	mats, files := newMaterials(kept, set)
	charge := usage.CostForecast + len(set.Files)*usage.CostMaterial
	credits, err := h.d.Usage.Spend(ctx, user, charge)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	var parentID *string
	if parent != nil {
		parentID = &parent.ID
	}
	f, err := h.insert(ctx, user, d, parentID, mats)
	if err != nil {
		h.d.Usage.Refund(ctx, user, charge)
		if pg := (*pgconn.PgError)(nil); parent != nil && errors.As(err, &pg) && pg.Code == "23505" { // the one-running-update index: a concurrent update won
			err = errUpdateRunning
		}
		app.WriteError(w, err)
		return
	}
	var m *materialRun
	if len(mats) > 0 {
		m = &materialRun{id: f.ID, stored: slices.Clone(mats), files: files, charge: charge}
	}
	if parent != nil {
		f.History = parent.History // the same lineage; the new run joins it when it completes
	}
	started = true
	go func() { defer done(); h.run(jctx, f.ID, user, d, p, m) }()
	httpx.JSON(w, http.StatusCreated, map[string]any{"forecast": f, "credits": credits})
}

// reuse is the caller's own forecast's read digests for "Run again" (create
// only): any status, never a replay row, and only for the same question.
func (h *handler) reuse(ctx context.Context, user string, d Draft, id string, ids *[]string) ([]Material, error) {
	var src Draft
	var mats []Material
	err := pgx.ErrNoRows
	if validID(id) {
		err = h.d.DB.QueryRow(ctx, `select shape, question, outcomes, materials from forecasts where id = $1 and user_id = $2`, id, user).
			Scan(&src.Shape, &src.Question, &src.Outcomes, &mats)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &app.BadRequest{Msg: "reuseMaterials.forecastId is not one of your forecasts."}
	}
	if err != nil {
		return nil, err
	}
	same := src.Shape == d.Shape && key(src.Question) == key(d.Question) && len(src.Outcomes) == len(d.Outcomes)
	for i := 0; same && i < len(d.Outcomes); i++ {
		same = key(src.Outcomes[i]) == key(d.Outcomes[i])
	}
	if !same { // a digest is written for one question: its summary, relevance and directions are about it
		return nil, &app.BadRequest{Msg: "Materials can only be reused for the same question. Attach the files again."}
	}
	picked, err := pick(shownMaterials(mats, "done"), ids, "reuseMaterials.ids")
	for i := range picked {
		picked[i] = reusable(picked[i], id)
	}
	return picked, err
}

var errUpdateRunning = &app.Conflict{Msg: "An update is already running."}

// handleUpdate re-runs the panel on today's evidence for a completed,
// unresolved, future forecast of the caller's: a new row with parent_id = id
// that copies the question, and the parent's estimate as the aggregator's
// prior. The parent stays in the lineage's history. The body may add new
// files and choose which of the parent's read materials to keep;
// {} keeps every one and adds none.
func (h *handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Attachments   attach.List `json:"attachments"`
		KeepMaterials *keepIDs    `json:"keepMaterials"` // omitted: every parent material the desk read
	}
	release, ok := admit(w, r, &req)
	if !ok {
		return
	}
	defer release()
	ctx, user := r.Context(), httpx.UserID(r.Context())
	parent, err := h.updatable(ctx, user, r.PathValue("id"))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if err := h.validateLinked(ctx, &parent.Draft, false); err != nil {
		app.WriteError(w, err)
		return
	}
	set, err := attach.Decode(req.Attachments, "forecast")
	release()
	if err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	kept, err := pick(parent.Materials, (*[]string)(req.KeepMaterials), "keepMaterials")
	if err != nil {
		app.WriteError(w, err)
		return
	}
	for i := range kept {
		kept[i] = reusable(kept[i], parent.ID)
	}
	h.start(w, ctx, user, parent.Draft, &parent, &prior{at: *parent.CompletedAt, probs: parent.Result.Probabilities, summary: parent.Result.Summary,
		sources: parent.Result.Sources, findings: parent.Result.Findings, angles: parent.Result.Research.Angles, view: parent.Result.View}, kept, set)
}

// updatable is the caller's forecast when it can be updated: 404 for another
// user's or a replay row, 409 unless it is completed, unresolved and resolves
// after today, or when an update of it is already running.
func (h *handler) updatable(ctx context.Context, user, id string) (Forecast, error) {
	f, err := h.get(ctx, user, id)
	switch {
	case err != nil:
		return f, err
	case f.Replay != nil:
		return f, app.ErrNotFound
	case f.Status != "completed" || f.Result == nil || f.CompletedAt == nil || f.Resolution != nil || f.ResolutionDate <= todayUTC().Format(time.DateOnly):
		return f, &app.Conflict{Msg: "Only a completed, unresolved forecast can be updated."}
	}
	var running bool
	if err := h.d.DB.QueryRow(ctx, `select exists (select 1 from forecasts where parent_id = $1 and status = 'running')`, id).Scan(&running); err != nil {
		return f, err
	}
	if running {
		return f, errUpdateRunning
	}
	return f, nil
}

func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, shape, scope := q.Get("status"), q.Get("shape"), cmp.Or(q.Get("scope"), "mine")
	if status != "" && !slices.Contains([]string{"running", "completed", "failed", "archived"}, status) {
		app.WriteError(w, &app.BadRequest{Msg: "status must be running, completed, failed or archived."})
		return
	}
	if shape != "" && !slices.Contains(shapes, shape) {
		app.WriteError(w, &app.BadRequest{Msg: "shape must be binary, ordered or categorical."})
		return
	}
	if scope != "mine" && scope != "replay" {
		app.WriteError(w, &app.BadRequest{Msg: "scope must be mine or replay."})
		return
	}
	if status == "archived" && scope == "replay" {
		app.WriteError(w, &app.BadRequest{Msg: "Shared replay forecasts cannot be archived."})
		return
	}
	list, err := h.list(r.Context(), httpx.UserID(r.Context()), status, shape, scope == "replay")
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"forecasts": list})
}

func (h *handler) handleGet(w http.ResponseWriter, r *http.Request) {
	f, err := h.get(r.Context(), httpx.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"forecast": f})
}

func (h *handler) handleArchive(w http.ResponseWriter, r *http.Request) {
	if err := h.archive(r.Context(), httpx.UserID(r.Context()), r.PathValue("id")); err != nil {
		app.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleRestore(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if err := httpx.Decode(w, r, &req); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	ctx, user, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	if err := h.restore(ctx, user, id); err != nil {
		app.WriteError(w, err)
		return
	}
	h.handleGet(w, r)
}

// Storage. Every query is scoped to the owner (replay rows, user_id null, are
// readable by everyone and writable by no one). Owners can read their archives.

// insert stores a running forecast; parent is the run it updates, or nil;
// mats its materials (new ones pending, reused ones read).
func (h *handler) insert(ctx context.Context, user string, d Draft, parent *string, mats []Material) (Forecast, error) {
	if mats == nil {
		mats = []Material{}
	}
	f := Forecast{Draft: d, Status: "running", ParentID: parent, History: []Point{}, Materials: mats}
	err := h.d.DB.QueryRow(ctx, `
		insert into forecasts (user_id, shape, question, outcomes, resolution_criteria, resolution_date, category, status, price_rule, context, parent_id, linked_market, materials)
		values ($1, $2, $3, $4, $5, $6, $7, 'running', $8, $9, $10, $11, $12) returning id, created_at`,
		user, d.Shape, d.Question, d.Outcomes, d.ResolutionCriteria, d.ResolutionDate, d.Category, d.PriceRule, d.Context, parent, d.LinkedMarket, mats).Scan(&f.ID, &f.CreatedAt)
	return f, err
}

// get returns pgx.ErrNoRows (→ 404) for a missing or foreign row; replay rows are everyone's.
func (h *handler) get(ctx context.Context, user, id string) (Forecast, error) {
	if !validID(id) {
		return Forecast{}, app.ErrNotFound
	}
	var f Forecast
	var date time.Time
	var outcome, by *string
	var resolvedAt, asOf *time.Time
	var anchor *float64
	err := h.d.DB.QueryRow(ctx, `
		select id, shape, question, outcomes, resolution_criteria, resolution_date, category, status, error, result,
		       created_at, completed_at, price_rule, outcome, resolved_at, resolved_by, as_of, anchor_p, context, parent_id,
		       case when status = 'archived' then coalesce(archived_from_status,
		         case when result is not null then 'completed' else 'failed' end) end, progress, linked_market, materials
		from forecasts where id = $1 and (user_id = $2 or (user_id is null and status <> 'archived'))`, id, user).Scan(
		&f.ID, &f.Shape, &f.Question, &f.Outcomes, &f.ResolutionCriteria, &date, &f.Category, &f.Status,
		&f.Error, &f.Result, &f.CreatedAt, &f.CompletedAt, &f.PriceRule, &outcome, &resolvedAt, &by, &asOf, &anchor, &f.Context, &f.ParentID, &f.ArchivedFromStatus, &f.Progress,
		&f.LinkedMarket, &f.Materials)
	if err != nil {
		return f, err
	}
	f.Materials = shownMaterials(f.Materials, f.Status)
	f.ResolutionDate = date.Format(time.DateOnly)
	if outcome != nil && resolvedAt != nil && by != nil {
		f.Resolution = &Resolution{*outcome, *resolvedAt, *by}
	}
	if asOf != nil && anchor != nil {
		f.Replay = &ReplayInfo{asOf.Format(time.DateOnly), *anchor}
	}
	if f.Result != nil && f.Result.Sources == nil { // ponytail: results stored before the research stage; no backfill
		f.Result.Sources, f.Result.Research.Queries = []Source{}, []string{}
	}
	if f.Result != nil && f.Result.Findings == nil { // results stored before deep research have no ledger
		f.Result.Findings = []Finding{}
	}
	if f.Result != nil && f.Result.Market != nil && thin(f.Result.Market.VolumeUSD) { // older results have no thin flag: derive it, never an edge
		f.Result.Market.Thin, f.Result.Market.Signal = true, "wait"
	}
	if f.LinkedMarket != nil && f.Result != nil && f.Result.Market != nil && !linkedPredicate(f.Draft) {
		f.Result.Market.Match, f.Result.Market.LinkStatus, f.Result.Market.Signal = "close", "legacy", "wait"
	}
	derive(&f)
	f.History, err = h.lineage(ctx, user, f.ID)
	return f, err
}

// derive fills what an older ordered result stored without a view lacks, from its
// stored probabilities only (nothing is backfilled): the desk view, marked
// derived and without a reference (no measured price, so no P(above) or
// change), and each member's stats.
func derive(f *Forecast) {
	r := f.Result
	if r == nil || f.Shape != "ordered" {
		return
	}
	if r.View == nil {
		if v := deskView(f.Draft, r.Probabilities, nil, horizonOf(f.Draft, runDay(f.CreatedAt, f.CompletedAt)), noBasis); v != nil {
			v.Derived, r.View = true, v
		}
	}
	for i := range r.Specialists {
		if s := &r.Specialists[i]; s.Stats == nil {
			if s.Stats = statsOf(f.Draft, s.Probabilities, nil, nil); s.Stats != nil {
				s.Stats.derived = true
			}
		}
	}
}

// runDay is the UTC date a run's horizon counts from: its completion, else its creation.
func runDay(created time.Time, completed *time.Time) time.Time {
	if completed != nil {
		created = *completed
	}
	return created.UTC().Truncate(24 * time.Hour)
}

// lineage is the probability history of id's lineage: up through parent_id
// to the root, then every completed run below it, oldest first (the newest
// 20). A replay row (no owner) has none.
// ponytail: walks at most 20 levels each way; a longer lineage loses its oldest runs, which the cap drops anyway.
func (h *handler) lineage(ctx context.Context, user, id string) ([]Point, error) {
	rows, err := h.d.DB.Query(ctx, `
		with recursive up as (
			select id, parent_id, 0 as depth from forecasts where id = $1 and user_id = $2
			union all
			select f.id, f.parent_id, up.depth + 1 from forecasts f join up on f.id = up.parent_id where up.depth < 20
		), root as (select id from up order by depth desc limit 1),
		down as (
			select id, 0 as depth from root
			union all
			select f.id, down.depth + 1 from forecasts f join down on f.parent_id = down.id where down.depth < 20
		)
		select f.id, f.shape, coalesce(f.result->'probabilities', '[]'), f.completed_at,
		       case when f.result->'view'->'basis'->>'kind' in ('scenarios', 'capped_drift') then (f.result->'view'->'median'->>'value')::float8 end
		from forecasts f join down on f.id = down.id
		where f.status = 'completed' and f.completed_at is not null
		order by f.completed_at desc limit 20`, id, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Point{}
	for rows.Next() {
		var p Point
		var shape string
		var probs []OutcomeP
		if err := rows.Scan(&p.ID, &shape, &probs, &p.At, &p.Median); err != nil { // a median only where the run's view stated one
			return nil, err
		}
		if top := headline(shape, probs); top != nil {
			p.Outcome, p.P = top.Outcome, top.P
			out = append(out, p)
		}
	}
	slices.Reverse(out)
	return out, rows.Err()
}

// list hides a run superseded by a completed update: a lineage shows as its newest run.
// ponytail: newest 200, no pagination; add a cursor when someone has more.
func (h *handler) list(ctx context.Context, user, status, shape string, replay bool) ([]Summary, error) {
	rows, err := h.d.DB.Query(ctx, `
		select id, shape, question, category, status, resolution_date, created_at, completed_at, result->'probabilities',
		       outcome, user_id is null, parent_id, outcomes, result->'view',
		       case when status = 'archived' then coalesce(archived_from_status,
		         case when result is not null then 'completed' else 'failed' end) end
		from forecasts f
		where case when $4 then user_id is null else user_id = $1 end
		  and (case when $2 = 'archived' then status = 'archived' else status <> 'archived' end)
		  and ($2 = '' or status = $2) and ($3 = '' or shape = $3)
		  and ($2 = 'archived' or not exists (select 1 from forecasts c where c.parent_id = f.id and c.status = 'completed'))
		order by created_at desc limit 200`, user, status, shape, replay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var s Summary
		var date time.Time
		var probs []OutcomeP
		var outcomes []string
		var view *DeskView
		if err := rows.Scan(&s.ID, &s.Shape, &s.Question, &s.Category, &s.Status, &date,
			&s.CreatedAt, &s.CompletedAt, &probs, &s.ResolvedOutcome, &s.Replay, &s.ParentID, &outcomes, &view, &s.ArchivedFromStatus); err != nil {
			return nil, err
		}
		s.ResolutionDate, s.Headline = date.Format(time.DateOnly), headline(s.Shape, probs)
		if view == nil && s.Shape == "ordered" && probs != nil { // stored without a view: derived, without a reference
			d := Draft{Shape: s.Shape, Outcomes: outcomes, ResolutionDate: s.ResolutionDate}
			view = deskView(d, probs, nil, horizonOf(d, runDay(s.CreatedAt, s.CompletedAt)), noBasis)
		}
		s.View = view.brief()
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *handler) archive(ctx context.Context, user, id string) error {
	if !validID(id) {
		return app.ErrNotFound
	}
	tag, err := h.d.DB.Exec(ctx, `
		update forecasts set archived_from_status = status, status = 'archived'
		where id = $1 and user_id = $2 and status <> 'archived'`, id, user)
	if err == nil && tag.RowsAffected() == 0 {
		err = app.ErrNotFound
	}
	return err
}

// restore never revives a background run. A forecast archived while running
// returns as failed, so its old worker cannot complete it after restoration.
func (h *handler) restore(ctx context.Context, user, id string) error {
	if !validID(id) {
		return app.ErrNotFound
	}
	tag, err := h.d.DB.Exec(ctx, `
		update forecasts set
		  status = case when archived_from_status = 'running' then 'failed'
		    else coalesce(archived_from_status, case when result is not null then 'completed' else 'failed' end) end,
		  error = case when archived_from_status = 'running' then 'Run interrupted when this forecast was archived. Create a new forecast to run it again.'
		    when result is null then coalesce(error, 'No completed result was saved before this forecast was archived. Create a new forecast to run it again.')
		    else error end,
		  archived_from_status = null
		where id = $1 and user_id = $2 and status = 'archived'`, id, user)
	if err != nil || tag.RowsAffected() != 0 {
		return err
	}
	var own bool
	if err := h.d.DB.QueryRow(ctx, `select exists (select 1 from forecasts where id = $1 and user_id = $2)`, id, user).Scan(&own); err != nil {
		return err
	}
	if !own {
		return app.ErrNotFound
	}
	return &app.Conflict{Msg: "Only an archived forecast can be restored."}
}

// complete and fail only touch running rows, so a forecast archived (or
// recovered) mid-run stays that way. complete stores R1's P(Yes) as the
// row's anchor_p (null without one), so the track record pairs it.
func (h *handler) complete(ctx context.Context, id string, res Result) error {
	var anchor *float64
	if res.Reference != nil {
		anchor = &res.Reference.PYes
	}
	_, err := h.d.DB.Exec(ctx, `
		update forecasts set status = 'completed', result = $2, completed_at = now(), anchor_p = $3
		where id = $1 and status = 'running'`, id, res, anchor)
	return err
}

func (h *handler) fail(ctx context.Context, id, msg string) error {
	_, err := h.d.DB.Exec(ctx, `
		update forecasts set status = 'failed', error = $2 where id = $1 and status = 'running'`, id, msg)
	return err
}

// Recover fails runs orphaned by a restart. A run times out after 8 minutes,
// so one still running after 10 is dead. The sweep runs at boot and then every
// 5 minutes, which also catches runs that were only minutes old at boot. At
// boot it also adds the embedded replay rows that are missing; the resolver
// (which calls Binance, so it stays off the boot path) runs right after boot
// and then with every sweep.
// The boot sweep and seed are synchronous; the caller owns the returned loop.
func Recover(ctx context.Context, d *app.Deps) func() {
	sweep := func() {
		tag, err := d.DB.Exec(ctx, `
			update forecasts set status = 'failed', error = 'interrupted by restart'
			where status = 'running' and created_at < now() - interval '10 minutes'`)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("forecast recover", "err", err)
			}
			return
		}
		if n := tag.RowsAffected(); n > 0 {
			slog.Info("forecasts interrupted by restart marked failed", "count", n)
		}
	}
	resolve := func() {
		if n, err := (&handler{d}).resolveDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
			slog.Error("forecast resolver", "err", err)
		} else if n > 0 {
			slog.Info("forecasts resolved from Binance data", "count", n)
		}
	}
	sweep()
	if n, err := insertSeed(ctx, d.DB, replaySeed); err != nil {
		slog.Error("forecast replay seed", "err", err)
	} else if n > 0 {
		slog.Info("forecast replay rows added", "count", n)
	}
	return func() {
		resolve()
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
				resolve()
			}
		}
	}
}

// runError is the user-facing text stored on a failed run.
func runError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "The forecast timed out. Try again."
	case errors.Is(err, llm.ErrNotConfigured):
		return app.AIOffMsg
	case errors.Is(err, llm.ErrUpstream):
		return "The AI model is unavailable right now. Try again shortly."
	}
	return "The forecast could not be completed."
}

func validID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !strings.ContainsRune("0123456789abcdefABCDEF", c):
			return false
		}
	}
	return true
}

// todayUTC is the current UTC date at midnight.
func todayUTC() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }
