package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// Each source on the card says which path numbered it first: the wire, a news
// search, a web search, or a page the user linked. A URL already numbered
// keeps its first kind.
func TestSourceKinds(t *testing.T) {
	brave := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/news/search" {
			fmt.Fprint(w, `{"results":[{"title":"ETF flows","url":"https://news.example/flows","description":"x"},
				{"title":"Fed holds rates","url":"https://wire.example/fed","description":"again"}]}`)
			return
		}
		fmt.Fprint(w, `{"web":{"results":[{"title":"Flow tracker","url":"https://data.example/flows","description":"Daily"}]}}`)
	}
	e := apptest.New(t, Register, apptest.Options{Brave: brave, Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{tool("a", "get_market_wire", "{}"), tool("b", "search_news", `{"query":"btc etf"}`),
			tool("c", "web_search", `{"query":"etf flow data"}`), tool("d", "read_page", `{"url":"https://blog.example/post"}`)}},
		llmtest.Reply{Text: "Flows [2]. **View:** neutral."},
	)})
	if _, err := e.DB.Exec(context.Background(), `insert into news_items (url, title, source, published_at, symbols, content) values
		('https://wire.example/fed', 'Fed holds rates', 'Reuters', now() - interval '1 hour', '{BTC}', null),
		('https://blog.example/post', 'A post', 'Blog', now() - interval '3 days', '{}', 'Cached body. ' || repeat('More reporting on the flows. ', 20))`); err != nil {
		t.Fatal(err)
	}
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", map[string]any{"message": "News on BTC? See https://blog.example/post"}).Body.String())
	cards := cardsOf(evs)
	if len(cards) != 1 || cards[0].Type != "sources" {
		t.Fatalf("cards %+v", cards)
	}
	b, _ := json.Marshal(cards[0].Data["items"])
	var items []struct{ URL, Kind string }
	json.Unmarshal(b, &items)
	want := []struct{ URL, Kind string }{{"https://wire.example/fed", "wire"}, {"https://news.example/flows", "news"},
		{"https://data.example/flows", "web"}, {"https://blog.example/post", "page"}}
	if fmt.Sprint(items) != fmt.Sprint(want) {
		t.Fatalf("kinds %v, want %v", items, want)
	}
}

func TestMarketWireSuppressesEntireLegacyBrief(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	if _, err := e.DB.Exec(ctx, `insert into news_items (title,url,source,symbols) values ('Bitcoin ETF demand grows','https://reuters.com/btc','Reuters','{BTC}'); insert into market_briefs (brief) values ('{"headline":"GAMBLING_DERIVED","summary":"GAMBLING_DERIVED","bullets":[{"text":"GAMBLING_DERIVED"}],"sentiment":[{"symbol":"BTC","score":1,"reason":"GAMBLING_DERIVED"}]}')`); err != nil {
		t.Fatal(err)
	}
	a := &agentRun{d: e.Deps}
	for range 2 {
		out, err := a.marketWire(ctx, json.RawMessage(`{"symbol":"BTC"}`))
		if err != nil || strings.Contains(out, "GAMBLING_DERIVED") || !strings.Contains(out, "Bitcoin ETF demand grows") {
			t.Fatalf("wire input: %s %v", out, err)
		}
	}
	if len(e.Bedrock.Requests()) != 0 {
		t.Fatal("wire invoked repair")
	}
}
