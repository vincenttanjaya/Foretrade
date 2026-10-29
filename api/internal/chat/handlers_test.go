package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/store/storetest"
)

type sseEvent struct {
	Name string
	Data json.RawMessage
}

// parseSSE splits a stream into its events, skipping keepalive comments.
func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		name, data, ok := strings.Cut(frame, "\ndata: ")
		if strings.HasPrefix(frame, ":") {
			continue
		}
		if !ok || !strings.HasPrefix(name, "event: ") {
			t.Fatalf("bad frame %q in\n%s", frame, body)
		}
		out = append(out, sseEvent{strings.TrimPrefix(name, "event: "), json.RawMessage(data)})
	}
	return out
}

// names lists the event names, collapsing runs of deltas into one.
func names(evs []sseEvent) []string {
	var out []string
	for _, e := range evs {
		if e.Name == "delta" && len(out) > 0 && out[len(out)-1] == "delta" {
			continue
		}
		out = append(out, e.Name)
	}
	return out
}

func find(evs []sseEvent, name string) []sseEvent {
	var out []sseEvent
	for _, e := range evs {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

func tool(id, name, input string) llmtest.ToolUse {
	return llmtest.ToolUse{ID: id, Name: name, Input: input}
}

type storedChat struct {
	Chat struct {
		ID, Title string
		Messages  []struct {
			Role, Content string
			Cards         []Card
		}
	}
}

func TestChatEndpoints(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: "Hello."})})
	for _, r := range [][2]string{{"GET", "/api/chats"}, {"GET", "/api/chats/x"}, {"DELETE", "/api/chats/x"}, {"POST", "/api/chat/stream"}} {
		if w := e.DoAs(nil, r[0], r[1], "{}"); w.Code != 401 {
			t.Errorf("%s %s without a session: %d", r[0], r[1], w.Code)
		}
	}
	if w := e.Do("GET", "/api/chats", nil); w.Code != 200 || w.Body.String() != "{\"chats\":[]}\n" {
		t.Fatalf("empty list: %d %s", w.Code, w.Body)
	}
	unknown := "6f1c2c1e-0000-4000-8000-000000000000"
	for _, r := range [][2]string{{"GET", "/api/chats/not-a-uuid"}, {"GET", "/api/chats/" + unknown},
		{"DELETE", "/api/chats/not-a-uuid"}, {"DELETE", "/api/chats/" + unknown}} {
		if w := e.Do(r[0], r[1], nil); w.Code != 404 || apptest.ErrorCode(t, w) != "not_found" {
			t.Errorf("%s %s: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}

	w := e.Do("POST", "/api/chat/stream", map[string]any{"message": "  Hi   there  "})
	evs := parseSSE(t, w.Body.String())
	var first struct{ ChatID, Title string }
	json.Unmarshal(evs[0].Data, &first)
	if w.Code != 200 || !slices.Equal(names(evs), []string{"chat", "delta", "done"}) || first.Title != "Hi there" || !isUUID(first.ChatID) {
		t.Fatalf("stream: %d %v %s", w.Code, names(evs), w.Body)
	}

	w = e.Do("GET", "/api/chats", nil)
	list := apptest.JSON[struct{ Chats []struct{ ID, Title string } }](t, w)
	if len(list.Chats) != 1 || list.Chats[0].ID != first.ChatID || list.Chats[0].Title != "Hi there" {
		t.Fatalf("list: %s", w.Body)
	}
	got := apptest.JSON[storedChat](t, e.Do("GET", "/api/chats/"+first.ChatID, nil))
	if m := got.Chat.Messages; len(m) != 2 || m[0].Role != "user" || m[0].Content != "Hi   there" || m[1].Content != "Hello." ||
		m[1].Cards == nil || len(m[1].Cards) != 0 {
		t.Fatalf("stored chat: %+v", got)
	}

	// Another user can neither read nor delete it.
	other := e.Session(storetest.User(t, e.DB))
	for _, method := range []string{"GET", "DELETE"} {
		if w := e.DoAs(other, method, "/api/chats/"+first.ChatID, nil); w.Code != 404 {
			t.Errorf("%s another user's chat: %d", method, w.Code)
		}
	}
	if w := e.Do("DELETE", "/api/chats/"+first.ChatID, nil); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/chats/"+first.ChatID, nil); w.Code != 404 {
		t.Fatalf("deleted chat: %d", w.Code)
	}
}

// Requests the agent must never see: each fails before credits are spent
// or Bedrock is called (the empty queue fails the test on any model call).
func TestStreamRejects(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	other := storetest.User(t, e.DB)
	var foreign string
	if err := e.DB.QueryRow(context.Background(), "insert into chats (user_id, title) values ($1, 'theirs') returning id", other).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]int{
		`{"message":`:       400,
		`{"message":"   "}`: 400,
		`{"message":"` + strings.Repeat("é", 4001) + `"}`: 400,
		`{"message":"hi","context":{"symbol":"PEPE"}}`:    400,
		`{"message":"hi","chatId":"nope"}`:                404,
		`{"message":"hi","chatId":"` + foreign + `"}`:     404,
	} {
		if w := e.Do("POST", "/api/chat/stream", body); w.Code != want {
			t.Errorf("%.50s: %d %s, want %d", body, w.Code, w.Body, want)
		}
	}

	poor := apptest.New(t, Register, apptest.Options{Credits: 1}) // a message costs 2
	if w := poor.Do("POST", "/api/chat/stream", `{"message":"hi"}`); w.Code != 429 || apptest.ErrorCode(t, w) != "ai_quota" {
		t.Fatalf("over quota: %d %s", w.Code, w.Body)
	}
	if w := poor.Do("GET", "/api/chats", nil); !strings.Contains(w.Body.String(), `"chats":[]`) {
		t.Fatalf("a refused message must not create a chat: %s", w.Body)
	}
}

// One message through every tool: the stream keeps the contract's event
// order, cards follow their tool results, the sources card lists every web
// source in [n] order right before done, and the whole answer is stored.
func TestStreamAgent(t *testing.T) {
	now := time.Now().UTC()
	brave := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"results":[{"title":"Bitcoin ETF inflows top $1bn","url":"https://news.example/btc-etf","description":"Flows",
			"page_age":%q,"profile":{"name":"News"}}]}`, now.Add(-2*time.Hour).Format("2006-01-02T15:04:05"))
	}
	trade := `{"symbol":"BTC","side":"buy","notional_usd":2000,"stop_loss":90000,"take_profit":120000,"rationale":"Trend up."}`
	e := apptest.New(t, Register, apptest.Options{Brave: brave, Reply: llmtest.Queue(t,
		llmtest.Reply{Reasoning: "plan", Text: "Let me look.", Tools: []llmtest.ToolUse{
			tool("a1", "get_asset_analysis", `{"symbol":"btc","interval":"1d"}`), tool("a2", "price_outlook", `{"symbol":"BTC","horizon_days":7,"horizon_reason":"a swing trade: one week"}`),
			tool("a3", "get_portfolio", ""), tool("a4", "get_market_overview", "")}},
		llmtest.Reply{Tools: []llmtest.ToolUse{
			tool("b1", "get_market_wire", `{"symbol":"BTC"}`), tool("b2", "search_news", `{"query":"bitcoin etf","freshness":"day"}`),
			tool("b3", "read_page", `{"url":"https://news.example/btc-etf"}`), tool("b4", "propose_trade", trade)}},
		llmtest.Reply{Text: "ETF demand is strong [2]. **View:** bullish."},
		llmtest.Reply{Text: "ETH is fine."},
	)})
	ctx := context.Background()
	article := "Full article: inflows topped $1bn. " + strings.Repeat("Spot ETF demand held up through the week as allocators added exposure. ", 6) // past the stub rule
	if _, err := e.DB.Exec(ctx, `insert into news_items (url, title, source, published_at, symbols, content) values
		('https://wire.example/fed', 'Fed holds rates; bitcoin steadies', 'Wire', now() - interval '1 hour', '{BTC}', null),
		('https://news.example/btc-etf', 'Bitcoin ETF inflows top $1bn', 'Desk', now() - interval '2 hours', '{}', $1)`, article); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `insert into market_briefs (brief) values ($1)`,
		`{"headline":"Risk-on","summary":"Flows return.","bullets":[],"sentiment":[{"symbol":"BTC","score":0.5,"label":"bullish","reason":"ETF"}],"articleCount":9}`); err != nil {
		t.Fatal(err)
	}
	researchtest.AcceptBriefs(t, e.DB)

	w := e.Do("POST", "/api/chat/stream", map[string]any{"message": "Should I buy BTC?"})
	evs := parseSSE(t, w.Body.String())
	want := []string{"chat", "delta",
		"tool_call", "tool_result", "tool_call", "tool_result", "card", "tool_call", "tool_result", "tool_call", "tool_result",
		"tool_call", "tool_result", "tool_call", "tool_result", "tool_call", "tool_result", "tool_call", "tool_result", "card",
		"delta", "card", "done"}
	if !slices.Equal(names(evs), want) {
		t.Fatalf("events %v\n%s", names(evs), w.Body)
	}

	type call struct{ ID, Name, Label string }
	type result struct {
		ID, Name, Summary string
		OK                bool
	}
	var labels, summaries []string
	for _, ev := range find(evs, "tool_call") {
		var c call
		json.Unmarshal(ev.Data, &c)
		labels = append(labels, c.Label)
	}
	for _, ev := range find(evs, "tool_result") {
		var r result
		json.Unmarshal(ev.Data, &r)
		if !r.OK {
			t.Errorf("tool %s failed: %s", r.Name, r.Summary)
		}
		summaries = append(summaries, r.Summary)
	}
	if !slices.Equal(labels, []string{"Analyzing BTC (1d)", "Modeling BTC 7-day price range", "Checking your paper portfolio",
		"Scanning the market", "Checking the market wire for BTC", "Searching news: bitcoin etf", "Reading news.example",
		"Drafting trade card: BUY $2,000 of BTC"}) {
		t.Errorf("labels %q", labels)
	}
	for i, prefix := range []string{"BTC $100,000 · ", "BTC in 7d: median $", "Equity $100,000 · 0 open positions",
		"107 assets · best ", "Brief · 1 headline", "1 article", "Read ", "BUY $2,000 of BTC at $100,000"} {
		if !strings.HasPrefix(summaries[i], prefix) {
			t.Errorf("summary %d = %q, want prefix %q", i, summaries[i], prefix)
		}
	}

	var cards []Card
	for _, ev := range find(evs, "card") {
		var c struct {
			Type string
			Data json.RawMessage
		}
		json.Unmarshal(ev.Data, &c)
		cards = append(cards, Card{Type: c.Type, Data: c.Data})
	}
	if len(cards) != 3 || cards[0].Type != "price_outlook" || cards[1].Type != "trade_proposal" || cards[2].Type != "sources" {
		t.Fatalf("cards %+v", cards)
	}
	var tc tradeProposal
	json.Unmarshal(cards[1].Data.(json.RawMessage), &tc)
	if d := string(cards[1].Data.(json.RawMessage)); !isUUID(tc.ProposalID) || tc.ProposalID[14] != '4' ||
		d != `{"proposalId":"`+tc.ProposalID+`","symbol":"BTC","side":"buy","notional":2000,"price":100000,"stopLoss":90000,"takeProfit":120000,"rationale":"Trend up.","forecast":null,"status":"open","trade":null}` {
		t.Errorf("trade card %s", d)
	}
	src := string(cards[2].Data.(json.RawMessage))
	if !strings.HasPrefix(src, `{"items":[{"n":1,"title":"Fed holds rates; bitcoin steadies","url":"https://wire.example/fed","source":"Wire"`) ||
		!strings.Contains(src, `{"n":2,"title":"Bitcoin ETF inflows top $1bn","url":"https://news.example/btc-etf","source":"News"`) ||
		!strings.Contains(src, `"kind":"wire"},{"n":2`) || !strings.HasSuffix(src, `"kind":"news"}]}`) ||
		strings.Count(src, `"n":`) != 2 {
		t.Errorf("sources card %s", src)
	}

	var deltas strings.Builder
	for _, ev := range find(evs, "delta") {
		var d struct{ Text string }
		json.Unmarshal(ev.Data, &d)
		deltas.WriteString(d.Text)
	}
	answer := "Let me look.\n\nETF demand is strong [2]. **View:** bullish."
	var done struct {
		MessageID string
		Credits   struct{ Used, Limit int }
	}
	json.Unmarshal(evs[len(evs)-1].Data, &done)
	if deltas.String() != answer || !isUUID(done.MessageID) || done.Credits.Used != 2 || done.Credits.Limit != 500 {
		t.Fatalf("answer %q, done %+v", deltas.String(), done)
	}

	var chat struct{ ChatID string }
	json.Unmarshal(evs[0].Data, &chat)
	stored := apptest.JSON[storedChat](t, e.Do("GET", "/api/chats/"+chat.ChatID, nil))
	if m := stored.Chat.Messages; len(m) != 2 || m[1].Role != "assistant" || m[1].Content != answer || len(m[1].Cards) != 3 ||
		m[1].Cards[2].Type != "sources" {
		t.Fatalf("stored: %+v", stored)
	}

	reqs := e.Bedrock.Requests()
	if len(reqs) != 3 || reqs[0].Effort != "medium" || len(reqs[0].Tools) != len((&agentRun{}).tools()) || !strings.Contains(reqs[0].System, "Today is "+now.Format("Monday, 2 January 2006")) ||
		len(reqs[0].Messages) != 1 || reqs[0].Prompt != "Should I buy BTC?" || strings.Contains(reqs[0].System, "looking at") {
		t.Fatalf("first model call: effort %q tools %v prompt %q", reqs[0].Effort, reqs[0].Tools, reqs[0].Prompt)
	}
	third := string(reqs[2].Body)
	for _, want := range []string{"untrusted_web_content", `\"n\":1`, "Full article: inflows topped $1bn.", "NOT executed"} {
		if !strings.Contains(third, want) {
			t.Errorf("tool results sent back lack %q", want)
		}
	}
	// Flattened: after a tool round the user's message carries the run as text.
	for _, r := range reqs[1:] {
		if b := string(r.Body); strings.Contains(b, `"toolUse"`) || strings.Contains(b, `"toolResult"`) || len(r.Messages) != 1 ||
			!strings.HasPrefix(r.Prompt, "Should I buy BTC?\n<run_transcript>\n") ||
			!strings.Contains(r.Prompt, "<shown_to_user>\nLet me look.\n</shown_to_user>\n\n<tool_call name=\"get_asset_analysis\" id=\"a1\">") ||
			!strings.HasSuffix(r.Prompt, "Visible answer so far: the shown_to_user text above, in order. Continue from it without repeating it.") {
			t.Fatalf("request after tools: %s", b)
		}
	}

	// A follow-up in the same chat replays the text history and the focus.
	w = e.Do("POST", "/api/chat/stream", map[string]any{"chatId": chat.ChatID, "message": "And ETH?", "context": map[string]string{"symbol": "eth"}})
	if evs := parseSSE(t, w.Body.String()); !slices.Equal(names(evs), []string{"chat", "delta", "done"}) || !strings.Contains(string(evs[0].Data), `"title":"Should I buy BTC?"`) {
		t.Fatalf("follow-up: %s", w.Body)
	}
	follow := e.Bedrock.Requests()[3]
	if len(follow.Messages) != 3 || follow.Messages[1].Content[0]["text"] != answer || follow.Prompt != "And ETH?" ||
		!strings.Contains(follow.System, "The user is looking at ETH right now") {
		t.Fatalf("follow-up request: %s", follow.Body)
	}
}

// Failures: a model that fails before saying anything refunds and reports
// upstream; a failure after text keeps the partial answer and the charge;
// tool failures go back to the model and show as failed chips.
func TestStreamFailures(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Status: 500},
		llmtest.Reply{Text: "Partial view.", Tools: []llmtest.ToolUse{tool("p", "get_portfolio", "")}},
		llmtest.Reply{Status: 500},
		llmtest.Reply{Tools: []llmtest.ToolUse{tool("x", "get_asset_analysis", `{"symbol":"BTC"}`), tool("y", "search_news", `{"query":"btc"}`),
			tool("z", "read_page", `{"url":"https://evil.example/x"}`), tool("w", "get_market_wire", "")}},
		llmtest.Reply{Text: "No data, sorry."},
	)})
	credits := func() int {
		var used int
		e.DB.QueryRow(context.Background(), "select coalesce(sum(credits), 0) from ai_usage where user_id = $1", e.User).Scan(&used)
		return used
	}
	stored := func(chatID string) storedChat {
		return apptest.JSON[storedChat](t, e.Do("GET", "/api/chats/"+chatID, nil))
	}
	chatID := func(evs []sseEvent) string {
		var c struct{ ChatID string }
		json.Unmarshal(evs[0].Data, &c)
		return c.ChatID
	}

	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"one"}`).Body.String())
	if !slices.Equal(names(evs), []string{"chat", "error"}) || !strings.Contains(string(evs[1].Data), `"code":"upstream"`) || credits() != 0 {
		t.Fatalf("silent failure: %v %s, credits %d", names(evs), evs[len(evs)-1].Data, credits())
	}
	if m := stored(chatID(evs)).Chat.Messages; len(m) != 1 || m[0].Role != "user" {
		t.Fatalf("only the user message is kept: %+v", m)
	}

	evs = parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"two"}`).Body.String())
	if !slices.Equal(names(evs), []string{"chat", "delta", "tool_call", "tool_result", "error"}) || credits() != 2 {
		t.Fatalf("failure after text: %v, credits %d", names(evs), credits())
	}
	if m := stored(chatID(evs)).Chat.Messages; len(m) != 2 || m[1].Content != "Partial view." {
		t.Fatalf("partial answer must be stored: %+v", m)
	}

	e.Binance.Down("", true)
	evs = parseSSE(t, e.Do("POST", "/api/chat/stream", `{"message":"three"}`).Body.String())
	if !slices.Equal(names(evs), []string{"chat", "tool_call", "tool_result", "tool_call", "tool_result", "tool_call", "tool_result",
		"tool_call", "tool_result", "delta", "done"}) {
		t.Fatalf("tool failures: %v", names(evs))
	}
	var chips []string
	for _, ev := range find(evs, "tool_result") {
		var r struct {
			OK      bool
			Summary string
		}
		json.Unmarshal(ev.Data, &r)
		chips = append(chips, fmt.Sprint(r.OK, " ", r.Summary))
	}
	if chips[0] != "false live market data is temporarily unavailable; try again or answer without it" || chips[1] != "false Search unavailable" ||
		!strings.HasPrefix(chips[2], "false read_page only opens URLs") || chips[3] != "true 0 headlines" {
		t.Fatalf("chips %q", chips)
	}
	// The market client's error names the Binance path and status; neither may reach the model.
	if b := string(e.Bedrock.Requests()[4].Body); strings.Contains(b, "status 500") || strings.Contains(b, "/api/v3") {
		t.Fatalf("internal errors must not reach the model: %s", b)
	}
}

// A client that leaves mid-answer gets nothing more, but what it saw is stored.
func TestStreamClientGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var turn atomic.Int32
	e := apptest.New(t, Register, apptest.Options{Reply: func(llmtest.Request) llmtest.Reply {
		if turn.Add(1) == 1 {
			return llmtest.Reply{Text: "Working on it.", Tools: []llmtest.ToolUse{tool("p", "get_portfolio", "")}}
		}
		cancel() // the browser tab closes while the model thinks
		return llmtest.Reply{Text: " More."}
	}})
	w := e.DoCtx(ctx, e.Cookie, "POST", "/api/chat/stream", `{"message":"hi"}`)
	evs := parseSSE(t, w.Body.String())
	if n := names(evs); slices.Contains(n, "done") || slices.Contains(n, "error") || n[0] != "chat" {
		t.Fatalf("events after the client left: %v", n)
	}
	var c struct{ ChatID string }
	json.Unmarshal(evs[0].Data, &c)
	m := apptest.JSON[storedChat](t, e.Do("GET", "/api/chats/"+c.ChatID, nil)).Chat.Messages
	if len(m) != 2 || !strings.HasPrefix(m[1].Content, "Working on it.") {
		t.Fatalf("stored after disconnect: %+v", m)
	}
}

// GET /api/chats/{id} links stored action cards to what the user built from
// them: the newest forecast with the draft's question and the first portfolio
// or strategy with the card's prompt, created since the user's message (the
// card can be tapped before the answer is stored). Older, archived and other
// users' rows are ignored; a card nobody tapped reads null.
func TestActionStatus(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	const q, basket, rsi = "Will the Binance SOL/USDT daily close on 2099-11-28 be above $150?", "a defensive crypto basket", "Buy SOL when RSI < 30"
	cards := `[{"type":"forecast_draft","data":{"draft":{"shape":"binary","question":"` + q + `","outcomes":["Yes","No"]},"forecast":null}},
		{"type":"build_action","data":{"kind":"portfolio","prompt":"` + basket + `","symbol":null,"interval":null,"cost":2,"result":null}},
		{"type":"build_action","data":{"kind":"strategy","prompt":"` + rsi + `","symbol":"SOL","interval":"1h","cost":2,"result":null}},
		{"type":"build_action","data":{"kind":"portfolio","prompt":"never built","symbol":null,"interval":null,"cost":2,"result":null}}]`
	var chatID string
	if err := e.DB.QueryRow(ctx, `
		with c as (insert into chats (user_id, title) values ($1, 'Actions') returning id),
		u as (insert into chat_messages (chat_id, role, content, created_at) select id, 'user', 'go', now() - interval '10 minutes' from c)
		insert into chat_messages (chat_id, role, content, cards, created_at) select id, 'assistant', 'Tap.', $2, now() - interval '5 minutes' from c
		returning chat_id`, e.User, cards).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	other := storetest.User(t, e.DB)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	forecast := func(user, status, result, ago string) {
		exec(`insert into forecasts (user_id, shape, question, outcomes, resolution_date, status, result, created_at)
			values ($1, 'binary', $2, '["Yes","No"]', '2099-11-28', $3, $4, now() - $5::interval)`, user, q, status, result, ago)
	}
	done := `{"probabilities":[{"outcome":"Yes","p":0.46},{"outcome":"No","p":0.54}]}`
	forecast(e.User, "completed", done, "1 hour")    // before the message
	forecast(e.User, "running", "null", "8 minutes") // tapped mid-answer
	forecast(e.User, "completed", done, "2 minutes") // the newest: wins
	forecast(e.User, "archived", done, "1 minute")
	forecast(other, "completed", done, "30 seconds")
	portfolio := func(user, name, ago string) {
		exec(`insert into ai_portfolios (user_id, name, thesis, prompt, holdings, backtest, created_at)
			values ($1, $2, '', $3, '[{"symbol":"BTC","weight":0.6,"rationale":"x"},{"symbol":"PAXG","weight":0.4,"rationale":"y"}]',
			'{"returnPct":12.4,"benchmarkReturnPct":8.1,"maxDrawdownPct":6,"volatilityAnnPct":30}', now() - $4::interval)`, user, name, basket, ago)
	}
	portfolio(e.User, "Old basket", "1 hour")
	portfolio(e.User, "Defensive core", "4 minutes") // the first since the message: wins
	portfolio(e.User, "Second build", "3 minutes")
	portfolio(other, "Theirs", "6 minutes")
	exec(`insert into strategies (user_id, name, description, prompt, spec, metrics, equity, benchmark, trades, created_at)
		values ($1, 'SOL RSI dip', '', $2, '{}', '{"totalReturnPct":8.2,"buyHoldReturnPct":5.1,"sharpe":1.32,"maxDrawdownPct":6.4}', '[]', '[]', '[]', now() - interval '1 minute')`,
		e.User, rsi)

	got := apptest.JSON[struct {
		Chat struct {
			Messages []struct{ Cards []map[string]any }
		}
	}](t, e.Do("GET", "/api/chats/"+chatID, nil)).Chat.Messages[1].Cards
	b, _ := json.Marshal([]any{got[0]["data"].(map[string]any)["forecast"], got[1]["data"].(map[string]any)["result"],
		got[2]["data"].(map[string]any)["result"], got[3]["data"].(map[string]any)["result"]})
	var f struct{ ID string }
	e.DB.QueryRow(ctx, "select id from forecasts where user_id = $1 and created_at > now() - interval '3 minutes' and status = 'completed'", e.User).Scan(&f.ID)
	var pf, st string
	e.DB.QueryRow(ctx, "select id from ai_portfolios where name = 'Defensive core'").Scan(&pf)
	e.DB.QueryRow(ctx, "select id from strategies where user_id = $1", e.User).Scan(&st)
	want := `[{"headline":{"outcome":"Yes","p":0.46},"id":"` + f.ID + `","status":"completed"},` +
		`{"bars":null,"benchmarkLabel":"BTC","benchmarkReturnPct":8.1,"benchmarkSymbol":"BTC","holdings":[{"symbol":"BTC","weight":0.6},{"symbol":"PAXG","weight":0.4}],"id":"` + pf + `","maxDrawdownPct":6,"name":"Defensive core","returnPct":12.4,"sharpe":null,"windowEnd":null,"windowStart":null},` +
		`{"bars":null,"benchmarkLabel":null,"benchmarkReturnPct":5.1,"benchmarkSymbol":null,"holdings":[],"id":"` + st + `","maxDrawdownPct":6.4,"name":"SOL RSI dip","returnPct":8.2,"sharpe":1.32,"windowEnd":null,"windowStart":null},null]`
	if string(b) != want {
		t.Fatalf("resolved\n%s\nwant\n%s", b, want)
	}
}

// goneWriter is a client that disconnects right after the chat event: every
// later write fails.
type goneWriter struct {
	*httptest.ResponseRecorder
	gone bool
}

func (w *goneWriter) Write(b []byte) (int, error) {
	if w.gone {
		return 0, errors.New("write: broken pipe")
	}
	w.gone = bytes.Contains(b, []byte("event: chat\n"))
	return w.ResponseRecorder.Write(b)
}

// streamGone posts a chat message from a client that leaves after the chat event.
func streamGone(t *testing.T, e *apptest.Env, body string) []sseEvent {
	req := httptest.NewRequest("POST", "/api/chat/stream", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(e.Cookie)
	w := &goneWriter{ResponseRecorder: httptest.NewRecorder()}
	e.Mux.ServeHTTP(w, req)
	return parseSSE(t, w.Body.String())
}

// A client that disconnects mid-answer still gets the complete answer stored,
// and the message is charged.
func TestStreamSurvivesClientGone(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Text: "Working on it.", Tools: []llmtest.ToolUse{tool("p", "get_portfolio", "")}},
		llmtest.Reply{Text: "Your book is all cash. **View:** neutral."},
	)})
	evs := streamGone(t, e, `{"message":"How is my book?"}`)
	if !slices.Equal(names(evs), []string{"chat"}) {
		t.Fatalf("events written: %v", names(evs))
	}
	var c struct{ ChatID string }
	json.Unmarshal(evs[0].Data, &c)
	m := apptest.JSON[storedChat](t, e.Do("GET", "/api/chats/"+c.ChatID, nil)).Chat.Messages
	if len(m) != 2 || m[1].Content != "Working on it.\n\nYour book is all cash. **View:** neutral." || spent(t, e) != 2 {
		t.Fatalf("stored %+v, credits %d", m, spent(t, e))
	}
}

// GET /api/chats/{id} says pending while the answer is being generated.
func TestStreamPendingWhileRunning(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) }) // never leave the fake Bedrock blocked
	e := apptest.New(t, Register, apptest.Options{Reply: func(llmtest.Request) llmtest.Reply {
		close(entered)
		<-release
		return llmtest.Reply{Text: "All cash. **View:** neutral."}
	}})
	done := make(chan struct{})
	go func() { e.Do("POST", "/api/chat/stream", `{"message":"How is my book?"}`); close(done) }()
	<-entered
	var chatID string
	if err := e.DB.QueryRow(context.Background(), "select id from chats where user_id = $1", e.User).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	type got struct {
		Chat struct {
			Pending  bool
			Messages []struct{ Role, Content string }
		}
	}
	if g := apptest.JSON[got](t, e.Do("GET", "/api/chats/"+chatID, nil)); !g.Chat.Pending || len(g.Chat.Messages) != 1 {
		t.Fatalf("during the run: %+v", g)
	}
	once.Do(func() { close(release) })
	<-done
	if g := apptest.JSON[got](t, e.Do("GET", "/api/chats/"+chatID, nil)); g.Chat.Pending || len(g.Chat.Messages) != 2 || g.Chat.Messages[1].Content != "All cash. **View:** neutral." {
		t.Fatalf("after the run: %+v", g)
	}
}

// A model that fails before producing anything refunds the message even when
// the client is already gone.
func TestStreamRefundsWhenNothingProduced(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Status: 500})})
	evs := streamGone(t, e, `{"message":"hi"}`)
	var c struct{ ChatID string }
	json.Unmarshal(evs[0].Data, &c)
	if m := apptest.JSON[storedChat](t, e.Do("GET", "/api/chats/"+c.ChatID, nil)).Chat.Messages; len(m) != 1 || spent(t, e) != 0 {
		t.Fatalf("stored %+v, credits %d", m, spent(t, e))
	}
}
