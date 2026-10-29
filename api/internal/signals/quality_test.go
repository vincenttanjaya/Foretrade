package signals

import (
	"context"
	"encoding/json"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
	"github.com/vincenttanjaya/Foretrade/api/internal/research/researchtest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

func TestUnverifiableBoardSuppressed(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	raw, _ := json.Marshal(Board{Bullish: []Pick{{Symbol: "BTC", Score: 99, Target: 999, Rationale: "Betting-derived", Headlines: []Headline{{"Bitcoin rallies", "https://reuters.com/btc", "Reuters"}}}}, Bearish: []Pick{}})
	if _, err := e.DB.Exec(context.Background(), `insert into signal_boards (board) values ($1)`, raw); err != nil {
		t.Fatal(err)
	}
	b, err := Latest(context.Background(), e.DB)
	if err != nil || b != nil {
		t.Fatalf("legacy result exposed: %+v %v", b, err)
	}
	for range 2 {
		w := e.Do("GET", "/api/signals", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"board":null`) || !strings.Contains(w.Body.String(), `"history":[]`) {
			t.Fatalf("legacy response: %s", w.Body)
		}
	}
	if len(e.Bedrock.Requests()) != 0 {
		t.Fatal("read invoked model")
	}
}

func TestCompleteSignalInputsIncludeUncitedThird(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"picks":[]}`})})
	addNews(t, e, "BTC", "Bitcoin ETF inflows rise", time.Hour)
	addNews(t, e, "BTC", "Bitcoin miners sell less", 2*time.Hour)
	addNews(t, e, "BTC", "Bitcoin validators grow", 3*time.Hour)
	b := build([]asset{mk("BTC", 40, "uptrend")}, nil, nil, nil, now)
	annotate(context.Background(), e.Deps, &b)
	// The public board keeps two citations, but the third input must be retained.
	if len(b.Bullish[0].Headlines) != 2 || b.quality == nil || len(b.quality.Inputs) != 3 {
		t.Fatalf("incomplete model inputs: %+v", b)
	}
	for _, change := range []string{"valid", "changed-third", "incomplete"} {
		t.Run(change, func(t *testing.T) {
			q := *b.quality
			q.Inputs = append([]research.NewsItem{}, q.Inputs...)
			if change == "incomplete" {
				q.Inputs = q.Inputs[:2]
			}
			if change == "changed-third" {
				if _, err := e.DB.Exec(context.Background(), `update news_items set source='Betting News' where id=$1`, q.Inputs[2].ID); err != nil {
					t.Fatal(err)
				}
				defer e.DB.Exec(context.Background(), `update news_items set source=$2 where id=$1`, q.Inputs[2].ID, q.Inputs[2].Source)
			}
			copy := b
			copy.quality = &q
			valid, err := validBoard(context.Background(), e.DB, &copy)
			if err != nil || valid != (change == "valid") {
				t.Fatalf("quality %s: %v %v", change, valid, err)
			}
		})
	}
}

func TestStoredSignalInputsAndSentimentDependency(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"picks":[]}`})})
	uncached(t)
	ctx := context.Background()
	addNews(t, e, "BTC", "Bitcoin ETF demand grows", time.Hour)
	addNews(t, e, "BTC", "Bitcoin miners sell less", 2*time.Hour)
	addNews(t, e, "BTC", "Bitcoin wallets grow", 3*time.Hour)
	if _, err := e.DB.Exec(ctx, `insert into market_briefs (brief) values ('{"sentiment":[{"symbol":"BTC","score":0.4,"reason":"ETF demand"}]}')`); err != nil {
		t.Fatal(err)
	}
	researchtest.AcceptBriefs(t, e.DB)
	brief, err := e.Research.LatestBrief(ctx)
	if err != nil || brief == nil {
		t.Fatalf("accepted brief: %+v %v", brief, err)
	}
	b := build([]asset{mk("BTC", 40, "uptrend")}, sentiment(brief, time.Now()), nil, nil, now)
	b.quality = &quality{Version: 1, Briefs: []research.MarketBrief{*brief}}
	annotate(ctx, e.Deps, &b)
	raw, err := json.Marshal(struct {
		Bullish []Pick   `json:"bullish"`
		Bearish []Pick   `json:"bearish"`
		Quality *quality `json:"_quality"`
	}{b.Bullish, b.Bearish, b.quality})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.DB.Exec(ctx, `insert into signal_boards (board) values ($1)`, raw); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"valid", "changed-brief", "missing-brief", "changed-uncited", "deleted-uncited", "incomplete"} {
		t.Run(change, func(t *testing.T) {
			var original []byte
			e.DB.QueryRow(ctx, `select brief from market_briefs where id=$1`, brief.ID).Scan(&original)
			switch change {
			case "changed-brief":
				e.DB.Exec(ctx, `update market_briefs set brief=jsonb_set(brief,'{sentiment,0,reason}','"Gambling reason"') where id=$1`, brief.ID)
				defer e.DB.Exec(ctx, `update market_briefs set brief=$2 where id=$1`, brief.ID, original)
			case "missing-brief":
				e.DB.Exec(ctx, `delete from market_briefs where id=$1`, brief.ID)
				defer e.DB.Exec(ctx, `insert into market_briefs (id,brief) values ($1,$2)`, brief.ID, original)
			case "changed-uncited":
				e.DB.Exec(ctx, `update news_items set source='Bookmaker Daily' where id=$1`, b.quality.Inputs[2].ID)
				defer e.DB.Exec(ctx, `update news_items set source=$2 where id=$1`, b.quality.Inputs[2].ID, b.quality.Inputs[2].Source)
			case "deleted-uncited":
				n := b.quality.Inputs[2]
				e.DB.Exec(ctx, `delete from news_items where id=$1`, n.ID)
				defer e.DB.Exec(ctx, `insert into news_items (id,title,url,source,description,published_at,symbols,topic) values ($1,$2,$3,$4,$5,$6,$7,$8)`, n.ID, n.Title, n.URL, n.Source, n.Description, n.PublishedAt, n.Symbols, n.Topic)
			case "incomplete":
				e.DB.Exec(ctx, `update signal_boards set board=jsonb_set(board,'{_quality,inputs}',(board->'_quality'->'inputs')-2)`)
				defer e.DB.Exec(ctx, `update signal_boards set board=$1`, raw)
			}
			for range 2 {
				got, err := Latest(ctx, e.DB)
				if err != nil || (got != nil) != (change == "valid") {
					t.Fatalf("stored %s: %+v %v", change, got, err)
				}
			}
			w := e.Do("GET", "/api/signals", nil)
			if w.Code != 200 {
				t.Fatalf("status %d %s", w.Code, w.Body)
			}
			if change != "valid" && (!strings.Contains(w.Body.String(), `"board":null`) || !strings.Contains(w.Body.String(), `"history":[]`)) {
				t.Fatalf("derived data exposed: %s", w.Body)
			}
			if len(e.Bedrock.Requests()) != 1 {
				t.Fatal("read repaired with a model")
			}
		})
	}
}

// storeAnnotated builds and annotates a BTC board on three stored headlines
// with sentiment from brief (nil: none), stores it now and returns it.
func storeAnnotated(t *testing.T, e *apptest.Env, brief *research.MarketBrief) Board {
	t.Helper()
	ctx := context.Background()
	addNews(t, e, "BTC", "Bitcoin ETF demand grows", time.Hour)
	addNews(t, e, "BTC", "Bitcoin miners sell less", 2*time.Hour)
	addNews(t, e, "BTC", "Bitcoin wallets grow", 3*time.Hour)
	b := build([]asset{mk("BTC", 40, "uptrend")}, sentiment(brief, time.Now()), nil, nil, now)
	b.quality = &quality{Version: 1, Inputs: []research.NewsItem{}, Briefs: []research.MarketBrief{}}
	if brief != nil {
		b.quality.Briefs = append(b.quality.Briefs, *brief)
	}
	annotate(ctx, e.Deps, &b)
	carry(&b, nil, now)
	raw, err := json.Marshal(struct {
		Bullish []Pick   `json:"bullish"`
		Bearish []Pick   `json:"bearish"`
		Quality *quality `json:"_quality"`
	}{b.Bullish, b.Bearish, b.quality})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.DB.Exec(ctx, `insert into signal_boards (board) values ($1)`, raw); err != nil {
		t.Fatal(err)
	}
	if got, err := Latest(ctx, e.DB); err != nil || got == nil {
		t.Fatalf("the stored board: %+v %v", got, err)
	}
	return b
}

// Root causes of boards blanking without a new one written: ingest adds tags
// and regions to a stored headline whenever a later search finds it again
// (a deploy that adds universe names tags old titles anew). Labels growing
// keep the board; the text a model read changing, or a tag going, does not.
func TestBoardSurvivesNewsLabels(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"picks":[]}`})})
	ctx := context.Background()
	b := storeAnnotated(t, e, nil)
	id := b.quality.Inputs[0].ID
	for _, tc := range []struct {
		name, update string
		valid        bool
	}{
		{"retagged", `update news_items set symbols = symbols || '{ETH,SOL}' where id = $1`, true},
		{"relabelled", `update news_items set regions = '{crypto,us}' where id = $1`, true},
		{"untagged", `update news_items set symbols = '{}' where id = $1`, false},
		{"redated", `update news_items set published_at = published_at - interval '1 day' where id = $1`, false},
		{"retitled", `update news_items set title = 'Bitcoin ETF demand shrinks' where id = $1`, false},
		{"rewritten", `update news_items set description = 'Bitcoin slides' where id = $1`, false},
		{"pruned", `delete from news_items where id = $1`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var saved struct {
				title, url, source, desc, topic string
				published                       *time.Time
				symbols, regions                []string
			}
			if err := e.DB.QueryRow(ctx, `select title, url, source, description, topic, published_at, symbols, regions from news_items where id = $1`, id).
				Scan(&saved.title, &saved.url, &saved.source, &saved.desc, &saved.topic, &saved.published, &saved.symbols, &saved.regions); err != nil {
				t.Fatal(err)
			}
			defer e.DB.Exec(ctx, `insert into news_items (id, title, url, source, description, topic, published_at, symbols, regions) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				on conflict (id) do update set title = excluded.title, description = excluded.description, published_at = excluded.published_at,
				symbols = excluded.symbols, regions = excluded.regions`, id, saved.title, saved.url, saved.source, saved.desc, saved.topic, saved.published, saved.symbols, saved.regions)
			if _, err := e.DB.Exec(ctx, tc.update, id); err != nil {
				t.Fatal(err)
			}
			if got, err := Latest(ctx, e.DB); err != nil || (got != nil) != tc.valid {
				t.Fatalf("%s: %+v %v", tc.name, got, err)
			}
		})
	}
}

// A brief snapshot is compared as canonical JSON: a window in a zone other
// than the process's, a field the row's build wrote that this one does not
// know, and empty fields a build serialised differently all keep the board;
// a changed score or reason (TestStoredSignalInputsAndSentimentDependency) does not.
func TestBriefSnapshotAcrossDeploys(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, llmtest.Reply{Text: `{"picks":[]}`})})
	ctx := context.Background()
	if _, err := e.DB.Exec(ctx, `insert into market_briefs (brief) values ('{"sentiment":[{"symbol":"BTC","score":0.4,"reason":"ETF demand"}],
		"window":{"from":"2026-10-06T14:45:00+05:45","to":"2026-10-07T14:45:00.123456+05:45"},"outlook":"a field from a newer build"}')`); err != nil {
		t.Fatal(err)
	}
	researchtest.AcceptBriefs(t, e.DB)
	brief, err := e.Research.LatestBrief(ctx)
	if err != nil || brief == nil {
		t.Fatalf("accepted brief: %+v %v", brief, err)
	}
	storeAnnotated(t, e, brief)

	// A build whose struct serialised empty fields as [] (no omitempty) wrote
	// the snapshot; the row has null or nothing there.
	if _, err := e.DB.Exec(ctx, `update signal_boards set board = jsonb_set(jsonb_set(board, '{_quality,briefs,0,regions}', '[]'),
		'{_quality,briefs,0,bullets,0,symbols}', '[]')`); err != nil {
		t.Fatal(err)
	}
	if got, err := Latest(ctx, e.DB); err != nil || got == nil {
		t.Fatalf("an unchanged brief read as changed: %+v %v", got, err)
	}
	// The struct comparison this replaced told them apart.
	var raw, board []byte
	e.DB.QueryRow(ctx, `select brief from market_briefs where id = $1`, brief.ID).Scan(&raw)
	e.DB.QueryRow(ctx, `select board->'_quality'->'briefs'->0 from signal_boards`).Scan(&board)
	var row, snapshot research.MarketBrief
	json.Unmarshal(raw, &row)
	json.Unmarshal(board, &snapshot)
	row.ID, row.CreatedAt = snapshot.ID, snapshot.CreatedAt
	if reflect.DeepEqual(row, snapshot) {
		t.Fatal("the fixture does not reproduce the struct comparison's failure")
	}
	if _, err := e.DB.Exec(ctx, `update market_briefs set brief = jsonb_set(brief, '{sentiment,0,score}', '0.1')`); err != nil {
		t.Fatal(err)
	}
	if got, err := Latest(ctx, e.DB); err != nil || got != nil {
		t.Fatalf("a changed sentiment kept the board: %+v %v", got, err)
	}
	// A row this build cannot read is a changed brief, not an error that
	// would fail every read and Run's check.
	if _, err := e.DB.Exec(ctx, `update market_briefs set brief = jsonb_set(brief, '{sentiment}', '"mixed"')`); err != nil {
		t.Fatal(err)
	}
	if got, err := Latest(ctx, e.DB); err != nil || got != nil {
		t.Fatalf("an unreadable brief: %+v %v", got, err)
	}
}

// A carried pick brings only its picking board's inputs and briefs along,
// so a board's inputs end with the last pick that needs them instead of
// chaining back into the 14-day news retention; a board stored before
// origins hands all of its inputs to the picks it carries.
func TestCarryBoundsInputs(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	ctx := context.Background()
	addNews(t, e, "BTC", "Bitcoin ETF demand grows", time.Hour)
	addNews(t, e, "ETH", "Ethereum staking grows", time.Hour)
	addNews(t, e, "SOL", "Solana DEX volumes hit a record", time.Hour)
	news := map[string]research.NewsItem{}
	for _, sym := range []string{"BTC", "ETH", "SOL"} {
		items, err := e.Research.Headlines(ctx, sym, 48*time.Hour, 1)
		if err != nil || len(items) != 1 {
			t.Fatalf("%s news: %v %v", sym, items, err)
		}
		news[sym] = items[0]
	}
	t0, t1, t2 := now, now.Add(4*time.Hour), now.Add(8*time.Hour)
	pick := func(sym string, at time.Time, fresh bool) Pick {
		return Pick{Symbol: sym, Side: "bullish", PickedAt: at, ValidUntil: at.Add(valid), Headlines: []Headline{}, fresh: fresh}
	}
	next := func(prev *Board, at time.Time, own []string, picks ...Pick) *Board {
		b := &Board{CreatedAt: at, Bullish: picks, Bearish: []Pick{}, quality: &quality{Version: 1, Inputs: []research.NewsItem{}, Briefs: []research.MarketBrief{}}}
		for _, sym := range own {
			addInputs(b.quality, []research.NewsItem{news[sym]})
		}
		carry(b, prev, at)
		return b
	}
	symbolsOf := func(b *Board) (out []string) {
		for _, n := range b.quality.Inputs {
			out = append(out, n.Symbols[0])
		}
		slices.Sort(out)
		return out
	}
	b0 := next(nil, t0, []string{"BTC"}, pick("BTC", t0, true))
	b1 := next(b0, t1, []string{"ETH"}, pick("BTC", t0, false), pick("ETH", t1, true))
	b2 := next(b1, t2, []string{"SOL"}, pick("ETH", t1, false), pick("SOL", t2, true))
	if got := symbolsOf(b1); !slices.Equal(got, []string{"BTC", "ETH"}) {
		t.Fatalf("b1 inputs %v", got)
	}
	if got := symbolsOf(b2); !slices.Equal(got, []string{"ETH", "SOL"}) || len(b2.quality.Origins) != 2 {
		t.Fatalf("b2 inputs %v, origins %+v", got, b2.quality.Origins)
	}
	// BTC's headline ages out of the news retention: b2 never depended on it.
	if _, err := e.DB.Exec(ctx, `delete from news_items where id = $1`, news["BTC"].ID); err != nil {
		t.Fatal(err)
	}
	for b, want := range map[*Board]bool{b1: false, b2: true} {
		if got, err := validBoard(ctx, e.DB, b); err != nil || got != want {
			t.Fatalf("board at %s: valid %v %v", b.CreatedAt, got, err)
		}
	}

	// An origin naming an input the board does not hold is not provenance.
	b2.quality.Origins[0].Inputs = append(b2.quality.Origins[0].Inputs, news["BTC"].ID)
	if got, _ := validBoard(ctx, e.DB, b2); got {
		t.Fatal("an origin outside the inputs validated")
	}

	// A board stored before origins: its carried picks keep all of its inputs.
	legacy := &Board{CreatedAt: t1, Bullish: []Pick{pick("ETH", t1, false)}, quality: &quality{Version: 1,
		Inputs: []research.NewsItem{news["ETH"], news["SOL"]}, InputCount: 2, Briefs: []research.MarketBrief{}}}
	b3 := next(legacy, t2, nil, pick("ETH", t1, false))
	if got := symbolsOf(b3); !slices.Equal(got, []string{"ETH", "SOL"}) || len(b3.quality.Origins) != 1 || len(b3.quality.Origins[0].Inputs) != 2 {
		t.Fatalf("from a legacy board: %v %+v", got, b3.quality.Origins)
	}
}

// A kept pick whose asset this board could not analyze keeps its previous
// factors, sentiment included, so the briefs they came from come along;
// a board with its own brief, or no sentiment, takes none.
func TestCarryKeepsBriefsForKeptSentiment(t *testing.T) {
	brief := research.MarketBrief{ID: "b1"}
	prev := &Board{quality: &quality{Version: 1, Inputs: []research.NewsItem{}, Briefs: []research.MarketBrief{brief},
		Origins: []origin{{PickedAt: now.Add(-4 * time.Hour), Inputs: []string{}, Briefs: []string{}}}}} // picked without a brief
	next := func(sentiment *float64, own []research.MarketBrief) *Board {
		k := Pick{Symbol: "BTC", Side: "bullish", PickedAt: now.Add(-4 * time.Hour), Factors: Factors{Sentiment: sentiment}, Headlines: []Headline{}}
		b := &Board{Bullish: []Pick{k}, Bearish: []Pick{}, quality: &quality{Version: 1, Inputs: []research.NewsItem{}, Briefs: own}}
		carry(b, prev, now)
		return b
	}
	if b := next(ptr(0.3), []research.MarketBrief{}); len(b.quality.Briefs) != 1 || b.quality.Briefs[0].ID != "b1" {
		t.Fatalf("kept sentiment: %+v", b.quality.Briefs)
	}
	if b := next(nil, []research.MarketBrief{}); len(b.quality.Briefs) != 0 {
		t.Fatalf("no sentiment: %+v", b.quality.Briefs)
	}
	if b := next(ptr(0.3), []research.MarketBrief{{ID: "b2"}}); len(b.quality.Briefs) != 1 || b.quality.Briefs[0].ID != "b2" {
		t.Fatalf("own brief: %+v", b.quality.Briefs)
	}
}
