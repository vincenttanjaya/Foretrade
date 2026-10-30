// Package chat serves the agent chat: POST /api/chat/stream and /api/chats*.
// The agent researches with Go tools (live Binance data, the paper
// portfolio) and ends in cards the user confirms; it never places orders.
package chat

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/attach"
	"github.com/vincenttanjaya/Foretrade/api/internal/httpx"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/paper"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
	"github.com/vincenttanjaya/Foretrade/api/internal/usage"
)

const (
	historyLimit = 20
	agentTimeout = 4 * time.Minute
	pingEvery    = 15 * time.Second
)

type handler struct{ d *app.Deps }

func Register(mux *http.ServeMux, d *app.Deps) {
	h := handler{d}
	mux.Handle("GET /api/chats", d.Auth.Require(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/chats/{id}", d.Auth.Require(http.HandlerFunc(h.get)))
	mux.Handle("DELETE /api/chats/{id}", d.Auth.Require(http.HandlerFunc(h.delete)))
	mux.Handle("POST /api/chat/stream", d.Auth.Require(http.HandlerFunc(h.stream)))
}

// Card is rendered by the frontend under the assistant message.
type Card struct {
	Type string `json:"type"` // "trade_proposal" | "price_outlook" | "sources" | "forecast" | "followups" | "forecast_draft" | "build_action"
	Data any    `json:"data"`
}

type message struct {
	ID          string          `json:"id"`
	Role        string          `json:"role"`
	Content     string          `json:"content"`
	Cards       json.RawMessage `json:"cards"`
	Attachments json.RawMessage `json:"attachments"`
	CreatedAt   time.Time       `json:"createdAt"`
}

func (h handler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.DB.Query(r.Context(),
		"select id, title, updated_at from chats where user_id = $1 order by updated_at desc limit 50",
		httpx.UserID(r.Context()))
	if err != nil {
		app.WriteError(w, err)
		return
	}
	defer rows.Close()
	type summary struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		UpdatedAt time.Time `json:"updatedAt"`
	}
	chats := []summary{}
	for rows.Next() {
		var s summary
		if err := rows.Scan(&s.ID, &s.Title, &s.UpdatedAt); err != nil {
			app.WriteError(w, err)
			return
		}
		chats = append(chats, s)
	}
	if err := rows.Err(); err != nil {
		app.WriteError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"chats": chats})
}

func (h handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, user, id := r.Context(), httpx.UserID(r.Context()), r.PathValue("id")
	if !isUUID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	var title string
	if err := h.d.DB.QueryRow(ctx, "select title from chats where id = $1 and user_id = $2", id, user).Scan(&title); err != nil {
		app.WriteError(w, err)
		return
	}
	rows, err := h.d.DB.Query(ctx, `
		select m.id, m.role, m.content, m.cards, m.attachments, m.created_at
		from chat_messages m join chats c on c.id = m.chat_id
		where c.id = $1 and c.user_id = $2 order by m.created_at`, id, user)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	defer rows.Close()
	msgs := []message{}
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Cards, &m.Attachments, &m.CreatedAt); err != nil {
			app.WriteError(w, err)
			return
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		app.WriteError(w, err)
		return
	}
	if err := h.proposalStatus(ctx, user, msgs); err != nil {
		app.WriteError(w, err)
		return
	}
	if err := h.actionStatus(ctx, user, msgs); err != nil {
		app.WriteError(w, err)
		return
	}
	if err := h.memoryStatus(ctx, user, msgs); err != nil {
		app.WriteError(w, err)
		return
	}
	_, pending := inflight.Load(id)
	httpx.JSON(w, http.StatusOK, map[string]any{"chat": map[string]any{"id": id, "title": title, "messages": msgs, "pending": pending}})
}

// inflight holds the chats (id → struct{}) whose answer is still being
// generated, e.g. after the client left mid-stream.
// ponytail: in-process map, correct for the single EC2 API process; a
// chat_messages.status column is the upgrade if the API runs more than one
// instance. A restart mid-run loses that answer (the user message stays and
// pending reads false).
var inflight sync.Map

// proposalStatus marks each stored trade card "filled" with its trade, or
// "open", from one trades query. Cards stored before proposal ids pass through
// untouched. A portfolio reset deletes trades, so its cards read open again.
func (h handler) proposalStatus(ctx context.Context, user string, msgs []message) error {
	parsed := make([][]map[string]any, len(msgs))
	var ids []string
	for i, m := range msgs {
		if !bytes.Contains(m.Cards, []byte(`"proposalId"`)) || json.Unmarshal(m.Cards, &parsed[i]) != nil {
			parsed[i] = nil
			continue
		}
		for _, c := range parsed[i] {
			if id := proposalID(c); id != "" {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := h.d.DB.Query(ctx, `
		select proposal_id, id, symbol, side, qty, price, notional, fee, realized_pnl, source, note, created_at
		from trades where user_id = $1 and proposal_id = any($2::uuid[])`, user, ids)
	if err != nil {
		return err
	}
	filled := map[string]paper.Trade{}
	var pid string
	var t paper.Trade
	if _, err := pgx.ForEachRow(rows, []any{&pid, &t.ID, &t.Symbol, &t.Side, &t.Qty, &t.Price, &t.Notional, &t.Fee,
		&t.RealizedPnl, &t.Source, &t.Note, &t.CreatedAt}, func() error { filled[pid] = t; return nil }); err != nil {
		return err
	}
	for i, cards := range parsed {
		if cards == nil {
			continue
		}
		for _, c := range cards {
			if id := proposalID(c); id != "" {
				data := c["data"].(map[string]any)
				if t, ok := filled[id]; ok {
					data["status"], data["trade"] = "filled", t
				} else {
					data["status"], data["trade"] = "open", nil
				}
			}
		}
		if msgs[i].Cards, err = json.Marshal(cards); err != nil {
			return err
		}
	}
	return nil
}

// proposalID is a stored trade card's proposal id, "" for any other card.
func proposalID(card map[string]any) string {
	data, _ := card["data"].(map[string]any)
	id, _ := data["proposalId"].(string)
	if card["type"] != "trade_proposal" || !isUUID(id) {
		return ""
	}
	return id
}

func (h handler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		app.WriteError(w, app.ErrNotFound)
		return
	}
	tag, err := h.d.DB.Exec(r.Context(), "delete from chats where id = $1 and user_id = $2", id, httpx.UserID(r.Context()))
	if err == nil && tag.RowsAffected() == 0 {
		err = app.ErrNotFound
	}
	if err != nil {
		app.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type streamRequest struct {
	Attachments attach.List `json:"attachments"`
	ChatID      string      `json:"chatId"`
	Message     string      `json:"message"`
	Mode        string      `json:"mode"` // empty keeps the existing quality mode
	Lens        string      `json:"lens"` // empty or "auto" infers investor, trader or hybrid from the question
	Context     struct {
		Symbol     string `json:"symbol"`
		ForecastID string `json:"forecastId"` // the forecast-desk report the chat was opened from
	} `json:"context"`
}

// stream answers one user message over SSE: chat → (tool_call, tool_result,
// delta, card)* → sources card → done | error. The user message is stored
// before the model runs; the assistant message (everything streamed, plus
// cards) after. The run is detached from the request: a client that leaves
// mid-answer gets nothing more, but the whole answer is still stored.
func (h handler) stream(w http.ResponseWriter, r *http.Request) {
	release, err := attach.Admit(w, r, httpx.UserID(r.Context()))
	if errors.Is(err, attach.ErrBusy) {
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many files are uploading right now. Try again in a moment.")
		return
	}
	if err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	defer release() // early returns; the normal path releases once decoded
	var req streamRequest
	if err := httpx.DecodeLimit(w, r, &req, attach.BodyLimit); err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	text := strings.TrimSpace(req.Message)
	if n := utf8.RuneCountInString(text); n == 0 || n > 4000 {
		app.WriteError(w, &app.BadRequest{Msg: "message must be 1..4000 characters."})
		return
	}
	if req.Mode != "" && req.Mode != "fast" && req.Mode != "quality" {
		app.WriteError(w, &app.BadRequest{Msg: "mode must be fast or quality."})
		return
	}
	if !validLens(req.Lens) {
		app.WriteError(w, &app.BadRequest{Msg: "lens must be auto, investor or trader."})
		return
	}
	att, err := attach.Decode(req.Attachments, "message")
	release()
	if err != nil {
		app.WriteError(w, &app.BadRequest{Msg: err.Error()})
		return
	}
	metadata := att.Metadata()
	attached := len(metadata) > 0
	chargedCost := usage.CostChat
	if attached {
		chargedCost = attachmentCost(len(att.Documents()))
	}
	storedText := attachmentMarkers(text, metadata)
	focus := ""
	if req.Context.Symbol != "" {
		info, ok := market.Lookup(req.Context.Symbol)
		if !ok {
			app.WriteError(w, &app.BadRequest{Msg: "Unknown symbol."})
			return
		}
		focus = info.Symbol
	}

	ctx, user := r.Context(), httpx.UserID(r.Context())
	bg, done, accepted := h.d.Tasks.Begin("chat", ctx)
	if !accepted {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "Server is stopping.")
		return
	}
	defer done()
	var desk *report
	if id := req.Context.ForecastID; id != "" {
		f, err := loadReport(ctx, h.d, user, id)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "not_found", "Unknown forecast.")
			return
		}
		if err != nil {
			app.WriteError(w, err)
			return
		}
		desk = &f
	}
	chatID, title := req.ChatID, titleFrom(text)
	var history []llm.Message
	if chatID != "" {
		if !isUUID(chatID) {
			app.WriteError(w, app.ErrNotFound)
			return
		}
		var err error
		if title, history, err = h.load(ctx, user, chatID); err != nil {
			app.WriteError(w, err)
			return
		}
	}
	if q, ok := parseQuickOrder(text); ok && desk == nil && !attached {
		if qt := h.quickTrade(ctx, user, q); qt != nil {
			h.streamQuick(ctx, w, user, chatID, title, text, qt)
			return
		}
	}
	owned, err := profile.Load(ctx, h.d.DB, user)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	credits, err := h.d.Usage.Spend(ctx, user, chargedCost)
	if err != nil {
		app.WriteError(w, err)
		return
	}
	if chatID == "" {
		chatID, err = h.create(ctx, user, title, storedText, metadata)
	} else {
		_, err = h.add(ctx, user, chatID, "user", storedText, []Card{}, metadata)
	}
	if err != nil {
		h.d.Usage.Refund(ctx, user, chargedCost)
		app.WriteError(w, err)
		return
	}
	sse, err := httpx.NewSSE(w)
	if err != nil {
		h.d.Usage.Refund(ctx, user, chargedCost)
		slog.Error("chat stream", "err", err)
		return
	}
	s := &stream{sse: sse}
	if s.send("chat", map[string]string{"chatId": chatID, "title": title}) != nil {
		h.d.Usage.Refund(ctx, user, chargedCost) // gone before the model ran
		return
	}
	inflight.Store(chatID, struct{}{})
	defer inflight.Delete(chatID) // a panic too; the normal path deletes right after the save

	run := &agentRun{d: h.d, userID: user, userText: text, withAttach: attached, forecasts: map[string]forecastRef{}, profile: owned}
	system := profileRules(systemPrompt(time.Now().UTC(), focus), owned)
	if attached {
		system += attachmentRules
	}
	if earlierAttachments(history) {
		system += earlierAttachmentRule
	}
	effort, maxTurns, maxTokens := llm.Medium, 9, 8192
	if req.Mode == "fast" {
		effort, maxTurns, maxTokens = llm.Low, 4, 4096
		system += "\n\nResponse mode: Fast. Give a concise answer focused on the user's question. " +
			"Collect the essential evidence with fewer tool steps; keep all data, citation and confirmation rules above. " +
			"Be explicit about missing evidence rather than guessing. An investment memo keeps its bottom line, scenario table and derived odds, and trims the rest."
	}
	system += lensRule(req.Lens, owned)
	cards := []Card{}
	if desk != nil {
		run.forecasts[desk.ID], run.deskID = desk.ref(), desk.ID
		system += desk.contextPrompt()
		system += desk.oddsBrief()
		if req.ChatID == "" { // the card opens the chat; later messages keep the brief, not another card
			cards = append(cards, *desk.card())
			s.send("card", desk.card())
		}
	}

	system += profile.Prompt(owned)
	actx, cancel := context.WithTimeout(bg, agentTimeout)
	defer cancel()
	stopPing := s.keepalive(actx, pingEvery)
	defer stopPing() // including panic; actual keepalive completion belongs to the handler
	var answer strings.Builder
	produced, afterTool := false, false
	emit := func(t string) { // the user sees t: it joins the answer
		answer.WriteString(t)
		run.wrote += utf8.RuneCountInString(t)
		run.viewed = run.viewed || strings.Contains(answer.String(), "**View")
		s.send("delta", map[string]string{"text": t})
	}
	g := turnGuard{chatID: chatID, emit: emit, viewed: func() bool { return run.viewed }}
	// The callback never returns a write error: a gone client must not stop the run.
	_, err = h.d.LLM.Agent(actx, llm.AgentRequest{
		System:          system,
		Messages:        append(history, llm.Message{Role: "user", Text: text, Images: att.Images(), Documents: att.Documents()}),
		Tools:           run.tools(),
		Effort:          effort,
		MaxTurns:        maxTurns,
		MaxTokens:       maxTokens,
		Flatten:         true, // Bedrock holds back the whole of a turn after toolResult blocks, and answers follow tools
		OnProviderEvent: func() { produced = true },
		Visible:         answer.String, // what the guard let through, not what the model wrote
		Nudge: func() string {
			g.end() // a held last turn may still reach the user
			if strings.TrimSpace(answer.String()) != "" {
				return ""
			}
			afterTool = true // the extra turn is held and checked like one after tools
			slog.Info("chat: no visible answer, asking again", "chat", chatID)
			return emptyAnswer
		},
	}, func(e llm.Event) error {
		produced = true
		switch e.Kind {
		case "delta":
			if run.followed { // suggest_followups closed the answer
				return nil
			}
			g.delta(e.Text, afterTool)
			afterTool = false
		case "tool_call":
			g.end()
			lbl := run.toolLabel(e.ToolName, e.ToolInput)
			if e.ToolName == "price_outlook" { // "can NVDA double?": the odds by year, from the user's words
				lbl = outlookLabel(e.ToolInput, run.userText, run.clock())
			}
			s.send("tool_call", map[string]any{"id": e.ToolID, "name": e.ToolName, "label": lbl, "input": e.ToolInput})
		case "tool_result":
			afterTool = true
			summary, card := run.summary, run.card
			run.summary, run.card = "", nil
			if e.IsError { // a tool may leave a short chip text, e.g. "Search unavailable"
				summary, card = cmp.Or(summary, clip(strings.TrimPrefix(e.Output, "Error: "), 140)), nil
			}
			s.send("tool_result", map[string]any{"id": e.ToolID, "name": e.ToolName, "ok": !e.IsError, "summary": summary})
			if card == nil || slices.ContainsFunc(cards, func(c Card) bool { return reflect.DeepEqual(c, *card) }) {
				return nil // nothing new, e.g. get_forecast on the chat's context forecast
			}
			if card.Type == "followups" { // a repeat call replaces the chips
				cards = slices.DeleteFunc(cards, func(c Card) bool { return c.Type == "followups" })
			}
			cards = append(cards, *card)
			s.send("card", card)
		}
		return nil
	})
	stopPing()
	g.end()

	// Persist the whole answer, even if the client left or the model failed midway.
	// The sources card goes last so its numbers match the [n] in the answer.
	var msgID string
	var saveErr error
	if err == nil || answer.Len() > 0 || len(cards) > 0 {
		if c := run.src.card(); c != nil {
			cards = append(cards, *c)
			s.send("card", c) // a gone client just fails the write
		}
		saveCtx, saveCancel := app.Cleanup(bg)
		msgID, saveErr = h.add(saveCtx, user, chatID, "assistant", answer.String(), cards, nil)
		saveCancel()
		if saveErr != nil {
			slog.Error("chat: save assistant message", "chat", chatID, "err", saveErr)
		}
	}
	inflight.Delete(chatID)
	upstream := errors.Is(err, llm.ErrUpstream) // tool failures go back to the model, never here
	if err != nil {
		if upstream {
			slog.Warn("chat: agent upstream", "err", err)
		} else {
			slog.Error("chat: agent", "err", err)
		}
		if !produced { // the model emitted nothing at all: connected or not, the message is free
			h.d.Usage.Refund(bg, user, chargedCost)
		}
	}
	switch {
	case ctx.Err() != nil || s.failed: // client went away; nothing left to tell it
	case err != nil:
		code, msg := "internal", "Something went wrong."
		switch {
		case errors.Is(err, llm.ErrNotConfigured):
			code, msg = "ai_off", app.AIOffMsg
		case errors.Is(err, llm.ErrUnreadableDoc):
			code, msg = "unreadable_file", "The AI on this server can't read this file. Paste the text, or attach a DOCX, CSV or screenshot instead."
		case upstream:
			code, msg = "upstream", "The AI model is unavailable right now. Try again shortly."
		}
		s.send("error", map[string]string{"code": code, "message": msg})
	case saveErr != nil:
		s.send("error", map[string]string{"code": "internal", "message": "The answer could not be saved."})
	default:
		if run.credits != nil { // draft_forecast spent after the message's charge
			credits = *run.credits
		}
		s.send("done", map[string]any{"messageId": msgID, "credits": credits})
	}
}

// load returns the chat's title and its last historyLimit messages, oldest first.
func (h handler) load(ctx context.Context, user, chatID string) (string, []llm.Message, error) {
	var title string
	if err := h.d.DB.QueryRow(ctx, "select title from chats where id = $1 and user_id = $2", chatID, user).Scan(&title); err != nil {
		return "", nil, err
	}
	rows, err := h.d.DB.Query(ctx, `
		select role, content from (
			select m.role, m.content, m.created_at
			from chat_messages m join chats c on c.id = m.chat_id
			where c.id = $1 and c.user_id = $2
			order by m.created_at desc limit $3
		) recent order by created_at`, chatID, user, historyLimit)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var history []llm.Message
	for rows.Next() {
		var m llm.Message
		if err := rows.Scan(&m.Role, &m.Text); err != nil {
			return "", nil, err
		}
		history = append(history, m)
	}
	return title, history, rows.Err()
}

// create makes a chat holding its first user message, in one statement.
func (h handler) create(ctx context.Context, user, title, text string, metadata []attachmentMetadata) (string, error) {
	var chatID string
	if metadata == nil {
		metadata = []attachmentMetadata{}
	}
	err := h.d.DB.QueryRow(ctx, `
		with c as (insert into chats (user_id, title) values ($1, $2) returning id)
		insert into chat_messages (chat_id, role, content, attachments) select id, 'user', $3, $4 from c returning chat_id`,
		user, title, text, metadata).Scan(&chatID)
	return chatID, err
}

// add appends a message and bumps the chat, only if user owns it (else pgx.ErrNoRows).
func (h handler) add(ctx context.Context, user, chatID, role, content string, cards []Card, metadata []attachmentMetadata) (string, error) {
	var id string
	if metadata == nil {
		metadata = []attachmentMetadata{}
	}
	err := h.d.DB.QueryRow(ctx, `
		with c as (update chats set updated_at = now() where id = $1 and user_id = $2 returning id)
		insert into chat_messages (chat_id, role, content, cards, attachments) select id, $3, $4, $5, $6 from c returning id`,
		chatID, user, role, content, cards, metadata).Scan(&id)
	return id, err
}

// stream serializes SSE writes: agent events and the keepalive share one writer.
type stream struct {
	mu     sync.Mutex
	sse    *httpx.SSE
	failed bool // a write failed: the client is gone
}

var errGone = errors.New("chat: client gone")

// send writes one event; after a failed write it writes nothing more.
func (s *stream) send(event string, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errGone
	}
	err := s.sse.Send(event, data)
	s.failed = s.failed || err != nil
	return err
}

// keepalive pings every interval until stop is called or ctx ends, so
// proxies keep the stream open while the model thinks or tools run.
func (s *stream) keepalive(ctx context.Context, every time.Duration) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				s.mu.Lock()
				err := s.sse.Ping()
				s.mu.Unlock()
				if err != nil {
					return
				}
			}
		}
	})
	stopOnce := sync.OnceFunc(func() { close(done) })
	return func() { stopOnce(); wg.Wait() }
}

// emptyAnswer is the Nudge for a run that would end with no visible answer,
// e.g. the model drafted it in its thinking, called suggest_followups and
// then claimed it was "above".
const emptyAnswer = "Nothing you have written is visible to the user yet: the answer they see is empty, and they never see your thinking. " +
	"Write your complete answer now (the analysis, then the **View:** line). Never say it is above or already written, and don't mention this note."

// turnGuard is the backstop for what the model writes after a tool round. A
// text turn (the text between tool rounds) that starts after tools is held
// until it is vouched for:
//   - while the answer is empty, the turn is dropped up to the next tool round,
//     **View line included, when its first sentence (through '.', '!' or '?'
//     and whitespace, or its first 300 runes) claims the answer is already
//     written or above (answerAbove); the run's Nudge then asks for it;
//   - its first sentence is dropped when it talks about tool results or
//     instructions (metaSentence); the rest of the turn goes on as usual;
//   - once the answer has text, a turn is held until its normalized text
//     reaches 120 runes, it ends, or no earlier turn of this message starts
//     with it (it can no longer become a repeat), and dropped up to the next
//     tool round when its first min(n, 120) runes (n ≥ 60) start an earlier
//     turn, keeping only its **View line if the answer has none yet.
//
// A turn that passes is sent as one delta, then streams live.
// ponytail: regexes and a prefix match; a reworded meta sentence or claim, or
// a paraphrased repeat passes. propose_trade's analysisShown, the flattened
// transcript's "Visible answer so far" and the prompt are the primary fix.
type turnGuard struct {
	chatID string
	emit   func(string) // sends text and adds it to the answer
	viewed func() bool  // the answer has its **View line

	done    []string        // normalized text of this message's finished turns
	cur     strings.Builder // this turn's text, less a dropped meta sentence
	sep     string          // "\n\n" before a held turn that follows answer text
	holding bool            // deciding
	meta    bool            // the first sentence is not checked yet
	check   bool            // the repeat check applies: the answer had text when the turn began
	repeat  bool            // decided: drop the rest of the turn
	stale   bool            // with repeat: the turn claims an answer the user never saw; keep nothing
	wrote   bool            // the answer has text
}

// metaSentence is a sentence about the tools rather than the market.
var metaSentence = regexp.MustCompile(`(?i)\b(tool['’]?s? (output|result)|didn['’]?t come from you|that instruction|prompt injection|i was (told|instructed) to)\b`)

// answerAbove claims the answer is already written: "I've already written the
// full answer above", "see above", "as shown above", "the table above". Kept
// narrow, as it drops a whole turn: "already" needs a first-person writer, and
// "above" a writing verb or an answer noun before it and no price after it
// ("BTC is above $86k", "shown above-average volume" and "SOL has already
// shown strength" pass).
var answerAbove = regexp.MustCompile(`(?i)(\bi(['’]ve| have)? already (written|wrote|shown|showed|provided|given|shared|posted|laid out|covered|answered|included)\b|` +
	`\b(see|shown|written|provided|outlined|laid out|(the|my|this) (full |complete |whole )?(answer|analysis|table|tables|breakdown|summary|response|reply)) above(\s*$|[.,:;!?)]|\s+[^\s$\d]))`)

// delta takes the next text of the answer; afterTool marks the first text after a tool round.
func (g *turnGuard) delta(text string, afterTool bool) {
	if afterTool {
		g.holding, g.meta, g.check, g.sep = true, true, g.wrote, ""
		if g.wrote {
			g.sep = "\n\n"
		}
	}
	if g.holding && g.cur.Len() == 0 { // a held turn starts at its first visible rune
		text = strings.TrimLeftFunc(text, unicode.IsSpace)
	}
	g.cur.WriteString(text)
	switch {
	case g.repeat:
	case g.holding:
		g.decide(false)
	default:
		g.out(text)
	}
}

// decide settles a held turn as far as its text allows; final (a tool round or
// the end of the run) settles it with what there is.
func (g *turnGuard) decide(final bool) {
	if g.meta {
		s := g.cur.String()
		end := sentenceEnd(s)
		if end < 0 && !final {
			return
		}
		if end < 0 {
			end = len(s)
		}
		if !g.wrote && answerAbove.MatchString(s[:end]) {
			slog.Info("chat: dropped a claim of an answer above", "chat", g.chatID, "runes", utf8.RuneCountInString(s[:end]))
			g.meta, g.holding, g.repeat, g.stale = false, false, true, true
			return
		}
		if metaSentence.MatchString(s[:end]) {
			slog.Info("chat: dropped meta sentence", "chat", g.chatID, "runes", utf8.RuneCountInString(s[:end]))
			g.cur.Reset()
			g.cur.WriteString(strings.TrimLeftFunc(s[end:], unicode.IsSpace))
		}
		g.meta = false
	}
	n := []rune(normalize(g.cur.String()))
	starts := func(p []rune) bool {
		return slices.ContainsFunc(g.done, func(d string) bool { return strings.HasPrefix(d, string(p)) })
	}
	switch {
	// The normalized text of a growing turn only grows at its end, so once no
	// earlier turn starts with it, none ever will: the turn is released then,
	// not at 120 runes.
	case !final && (g.check && len(n) < 120 && starts(n) || g.cur.Len() == 0):
		return
	case g.check && len(n) >= 60 && starts(n[:min(len(n), 120)]):
		g.holding, g.repeat = false, true
		slog.Info("chat: dropped repeated turn", "chat", g.chatID, "runes", len(n))
		return
	}
	g.holding = false
	if g.cur.Len() > 0 {
		g.out(g.sep + g.cur.String())
	}
}

// end closes the turn at a tool call or the end of the run.
func (g *turnGuard) end() {
	if g.holding {
		g.decide(true)
	}
	if g.repeat && !g.stale && !g.viewed() {
		for line := range strings.Lines(g.cur.String()) {
			if l := strings.TrimSpace(line); strings.HasPrefix(l, "**View") {
				g.out("\n\n" + l)
				break
			}
		}
	}
	if n := normalize(g.cur.String()); n != "" && !g.stale {
		g.done = append(g.done, n)
	}
	g.cur.Reset()
	g.holding, g.repeat, g.stale = false, false, false
}

func (g *turnGuard) out(s string) {
	if s != "" {
		g.wrote = g.wrote || strings.TrimSpace(s) != ""
		g.emit(s)
	}
}

// sentenceEnd is the byte length of s's first sentence: through the first
// '.', '!' or '?' that whitespace follows, or its first 300 runes; -1 while s
// has neither.
func sentenceEnd(s string) int {
	k, prev := 0, ' '
	for i, r := range s {
		if k == 300 || unicode.IsSpace(r) && strings.ContainsRune(".!?", prev) {
			return i
		}
		k, prev = k+1, r
	}
	if k == 300 {
		return len(s)
	}
	return -1
}

// normalize lower-cases s and turns every run of runes that are neither
// letters nor digits into one space, trimmed.
func normalize(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

// titleFrom is the message's first ~60 characters on one line, cut at a word
// boundary when there is one near the end.
func titleFrom(msg string) string {
	t := strings.Join(strings.Fields(msg), " ")
	if utf8.RuneCountInString(t) <= 60 {
		return t
	}
	cut := string([]rune(t)[:60])
	if i := strings.LastIndexByte(cut, ' '); i >= 40 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:-") + "…"
}

// isUUID accepts the canonical 8-4-4-4-12 form only, so a malformed id is a
// 404 instead of a Postgres cast error.
func isUUID(s string) bool {
	var u pgtype.UUID
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' && u.Scan(s) == nil
}

// clip shortens s to n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
