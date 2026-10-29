package forecast

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/app"
	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
	"github.com/vincenttanjaya/Foretrade/api/internal/market"
	"github.com/vincenttanjaya/Foretrade/api/internal/market/markettest"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

// barsBack is how many daily bars before markettest's newest one the bar opening on date is.
func barsBack(date time.Time) int { return int(markettest.End.Sub(date).Hours() / 24) }

func TestReplayQuestions(t *testing.T) {
	m := testMarketClient(markettest.New(t).URL)
	ctx := context.Background()
	qs, err := ReplayQuestions(ctx, m, day("2026-06-30"), time.Now())
	if err != nil || len(qs) != 24 {
		t.Fatalf("%d questions: %v", len(qs), err)
	}
	keys := map[string]bool{}
	for i, q := range qs {
		keys[q.Key] = true
		sym, k := replayAssets[i/3], replayK[i%4]
		res := q.AsOf.AddDate(0, 0, 14)
		x := roundSig(markettest.Close(sym+"USDT", barsBack(q.AsOf.AddDate(0, 0, -1)))*k, 3)
		want := "No"
		if markettest.Close(sym+"USDT", barsBack(res)) > x {
			want = "Yes"
		}
		if q.AsOf.Format(time.DateOnly) != replayDates[i%3] || q.Key != fmt.Sprintf("%s-%s-%v", sym, replayDates[i%3], k) ||
			*q.Draft.PriceRule != (PriceRule{sym, "above", x, "on_date"}) || q.Draft.ResolutionDate != res.Format(time.DateOnly) ||
			q.Draft.Question != fmt.Sprintf("Will the Binance %s/USDT daily close on %s be above $%s?", sym, res.Format(time.DateOnly), money(x)) ||
			q.Outcome == nil || *q.Outcome != want || !(q.AnchorP > 0 && q.AnchorP < 1) || replayIndex(sym, replayDates[i%3]) != i {
			t.Errorf("question %d: %+v (want threshold %v, outcome %s)", i, q, x, want)
		}
	}
	if len(keys) != 24 || qs[0].Key != "BTC-2026-07-15-0.94" || qs[5].Key != "ETH-2026-09-09-0.98" {
		t.Fatalf("keys: %v", keys)
	}
	if !strings.HasPrefix(qs[0].Draft.ResolutionCriteria, "Resolves Yes if the Binance BTCUSDT 1d candle that opens at 00:00 UTC on 2026-07-29 closes above $") {
		t.Fatalf("criteria: %s", qs[0].Draft.ResolutionCriteria)
	}
	// Every question and its criteria name their asset (LINK by its pair), so each brief carries the contracted spot line.
	for _, q := range qs {
		sym := q.Draft.PriceRule.Symbol
		if !slices.Equal(research.QuestionSymbols(q.Draft.Question), []string{sym}) || !slices.Equal(research.QuestionSymbols(q.Draft.ResolutionCriteria), []string{sym}) {
			t.Errorf("%s names %v / %v", q.Key, research.QuestionSymbols(q.Draft.Question), research.QuestionSymbols(q.Draft.ResolutionCriteria))
		}
	}
	link := qs[21] // LINK as of 2026-07-15
	hist := &history{m: m, asOf: link.AsOf, bars: map[string][]market.Candle{}}
	last := strconv.FormatFloat(markettest.Close("LINKUSDT", barsBack(link.AsOf.AddDate(0, 0, -1))), 'f', -1, 64)
	if line := hist.spotLines(ctx, spotSymbols(link.Draft)); link.Key != "LINK-2026-07-15-0.98" || !strings.Contains(line, "\n- LINK (Chainlink): $"+last+"\n") {
		t.Fatalf("LINK replay spot line: %s %q", link.Key, line)
	}
	if _, err := ReplayQuestions(ctx, m, day("2026-07-10"), time.Now()); err == nil {
		t.Fatal("a replay date within 14 days of the cutoff must be refused")
	}
	early, err := llm.NewFromSettings(llm.Settings{Provider: "openai", Model: "m", APIKey: "k", KnowledgeCutoff: day("2026-07-10")}) // the cutoff applies only with a provider
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(ctx, &app.Deps{LLM: early}, qs[0]); err == nil || !strings.Contains(err.Error(), "knowledge cutoff") {
		t.Fatalf("Replay before cutoff+14d: %v", err)
	}
	for x, want := range map[float64]string{118000: "118,000", 3720: "3,720", 152: "152", 0.215: "0.215", 20.6: "20.6"} {
		if got := money(x); got != want {
			t.Errorf("money(%v) = %s", x, got)
		}
	}
	for _, c := range [][3]float64{{118234.5, 3, 118000}, {0.21467, 3, 0.215}, {3.7249, 3, 3.72}, {99.96, 3, 100}} {
		if got := roundSig(c[0], int(c[1])); got != c[2] {
			t.Errorf("roundSig(%v) = %v", c[0], got)
		}
	}
}

func TestReplayQuantToolsReadHistory(t *testing.T) {
	m := testMarketClient(markettest.New(t).URL)
	asOf := day("2026-07-15")
	hist := &history{m: m, asOf: asOf, bars: map[string][]market.Candle{}}
	tools := (&handler{&app.Deps{Market: m}}).quantTools(hist)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "price_outlook,get_asset_analysis" || !strings.Contains(tools[0].Description, "simulated date") {
		t.Fatalf("replay tools: %v", names)
	}
	spot := markettest.Close("SOLUSDT", barsBack(asOf.AddDate(0, 0, -1))) // the 2026-07-14 bar, closed at asOf 00:00
	out, err := tools[0].Run(context.Background(), json.RawMessage(`{"symbol":"sol","horizon_days":14,"thresholds":[150]}`))
	var view struct {
		Outlook market.PriceOutlook
		Levels  []struct{ Price, PBelow float64 }
	}
	if err != nil || json.Unmarshal([]byte(out), &view) != nil || view.Outlook.Spot != spot || view.Outlook.HorizonDays != 14 || len(view.Levels) != 1 {
		t.Fatalf("price_outlook as of %s: %v %s (want spot %v)", asOf, err, out, spot)
	}
	out, err = tools[1].Run(context.Background(), json.RawMessage(`{"symbol":"SOL","interval":"1d"}`))
	var a market.Analysis
	if err != nil || json.Unmarshal([]byte(out), &a) != nil || a.Price != spot || a.Sma200 == nil || a.Interval != "1d" {
		t.Fatalf("get_asset_analysis as of %s: %v %s", asOf, err, out)
	}
	if line := hist.spotLines(context.Background(), []string{"SOL"}); !strings.HasPrefix(line, "Binance daily close on 2026-07-14 (the last close before simulated today, 2026-07-15") ||
		!strings.Contains(line, "SOL (Solana): $"+strconv.FormatFloat(spot, 'f', -1, 64)) {
		t.Fatalf("replay spot line: %q", line)
	}
}

// fakeNews is a Brave that records every call and answers news searches
// with items dated before, at and after 2026-07-15, one undated, and an
// evergreen page whose old date hides text from October.
func fakeNews(mu *sync.Mutex, seen *[]url.URL) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, *r.URL)
		mu.Unlock()
		q := url.PathEscape(r.URL.Query().Get("q"))
		fmt.Fprintf(w, `{"results":[
			{"title":"SOL ETF filing (%[1]s)","url":"https://news.example/a-%[1]s","description":"Filed","page_age":"2026-07-10T09:00:00","profile":{"name":"Desk"}},
			{"title":"Undated (%[1]s)","url":"https://news.example/b-%[1]s","description":"x"},
			{"title":"At midnight (%[1]s)","url":"https://news.example/c-%[1]s","description":"x","page_age":"2026-07-15T00:00:00"},
			{"title":"From the future (%[1]s)","url":"https://news.example/d-%[1]s","description":"x","page_age":"2026-07-20T09:00:00"},
			{"title":"Why BTC Is Up – 02 October 2026 (%[1]s)","url":"https://news.example/e-%[1]s","description":"Evergreen","page_age":"2026-07-02T09:00:00"}]}`, q)
	}
}

// A replay runs the whole panel as of its date: the clock, spot, tools and
// news all predate it, and no web search or page read happens. Its R1 is the
// question's AnchorP exactly, and the shrink guard applies: the desk's 33%
// against R1's 97%, with no referenceNote, becomes R1.
func TestReplayRun(t *testing.T) {
	var mu sync.Mutex
	var seen []url.URL
	e := apptest.New(t, Register, apptest.Options{Brave: fakeNews(&mu, &seen), Reply: panel(t, map[string]llmtest.Reply{
		"queries":    {Text: `{"queries":["bitcoin ETF flows July 2026","bitcoin price outlook"]}`},
		"base_rate":  {Text: opinion(0.4, "Reference class.")},
		"quant":      {Text: opinion(0.3, "Lognormal anchor.")},
		"skeptic":    {Text: opinion(0.35, "Catalysts.")},
		"aggregator": {Text: `{"probabilities":[{"outcome":"Yes","p":0.33}],"summary":"We stay close to the ensemble. We put it at a third.","confidence":"medium"}`},
	}, true)})
	var searches func() []string
	e.PolymarketBase, searches = gamma(t, nil)
	e.ForecastDeepResearch = true // round 8: a replay never runs deep research (panel fails any plan call)
	e.ForecastMethods = true      // round 9b: a replay keeps round 8b's frame, prompts and output
	ctx := context.Background()
	qs, err := ReplayQuestions(ctx, e.Market, e.LLM.KnowledgeCutoff(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	res, err := Replay(ctx, e.Deps, qs[0]) // BTC as of 2026-07-15
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "We stay close to the ensemble. We put it at a third." || res.View != nil || res.Methods != nil { // round 9b: no lint, no view
		t.Fatalf("a replay's output is round 8b's: %q %+v", res.Summary, res.View)
	}
	if res.Market != nil || len(searches()) != 0 { // an as-of run must not see today's market
		t.Fatalf("replay with market odds: %+v, searches %q", res.Market, searches())
	}
	if slices.ContainsFunc(res.Evidence, func(e Evidence) bool { return e.MaterialID != "" }) || strings.Contains(res.KnowledgeNote, "your materials") {
		t.Fatalf("a replay never has materials: %+v", res.Evidence) // round 9
	}
	if res.Findings == nil || len(res.Findings) != 0 || res.Research.Depth != "" || res.Research.Searches != 0 || res.Research.Angles != nil {
		t.Fatalf("a replay has no ledger and no depth: %+v %+v", res.Findings, res.Research)
	}
	if ref := res.Reference; ref == nil || ref.PYes != qs[0].AnchorP || ref.HorizonDays != 14 || !ref.AsOf.Equal(qs[0].AsOf) || ref.Kind != "volatility" ||
		res.Probabilities[0].P != qs[0].AnchorP || res.Guard != "shrink" || res.Flags[len(res.Flags)-1].Note != guardNote {
		t.Fatalf("replay reference and guard: %+v %+v", res.Reference, res)
	}
	if len(res.Specialists) != 3 || len(res.Sources) != 2 || readCount(res.Sources) != 0 ||
		res.KnowledgeNote != "Historical replay as of 2026-07-15: Binance daily data and 2 dated news headlines published before that date; no pages read." {
		t.Fatalf("replay result: %+v", res)
	}
	for _, s := range res.Sources {
		if !strings.HasPrefix(s.Title, "SOL ETF filing") || !s.PublishedAt.Before(qs[0].AsOf) {
			t.Errorf("source not dated before asOf: %+v", s)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("want one Brave News call per query, got %d", len(seen))
	}
	for _, u := range seen {
		if q := u.Query(); u.Path != "/news/search" || q.Get("freshness") != "2026-07-01to2026-07-14" || q.Get("count") != "6" {
			t.Errorf("replay Brave call %s %v", u.Path, q)
		}
	}
	spot := strconv.FormatFloat(markettest.Close("BTCUSDT", barsBack(day("2026-07-14"))), 'f', -1, 64)
	quant := 0
	for _, r := range e.Bedrock.Requests() {
		if strings.HasPrefix(r.System, "You are the research assistant") && !strings.Contains(r.Prompt, "Today: 2026-07-15 (UTC)") {
			t.Errorf("query writer clock: %s", r.Prompt)
		}
		if strings.HasPrefix(r.System, "You are the aggregator") && !strings.Contains(r.System, `in the desk's voice ("we"), never "I"`) {
			t.Error("aggregator voice")
		}
		if strings.Contains(r.Prompt, "M1 ·") {
			t.Errorf("a replay never sees a market: %.300s", r.Prompt)
		}
		if panelist := !strings.HasPrefix(r.System, "You are the research assistant"); panelist != strings.Contains(string(r.Body), "R1 · Volatility-only reference") ||
			panelist && !strings.Contains(string(r.Body), "as of 2026-07-15 00:00 UTC.") {
			t.Errorf("R1 reaches the panel and the aggregator only: %.80s", r.System)
		}
		if strings.HasPrefix(r.System, "You are the Base-rate") && !strings.Contains(r.System, baseRatePriceR1) ||
			strings.HasPrefix(r.System, "You are the aggregator") && !strings.Contains(r.System, combineR1) {
			t.Errorf("the R1 variant of the prompt: %.80s", r.System)
		}
		if !strings.HasPrefix(r.System, "You are the Market & quant analyst") {
			continue
		}
		if quant++; strings.Join(r.Tools, ",") != "price_outlook,get_asset_analysis" {
			t.Errorf("replay quant tools: %v", r.Tools)
		}
		body := string(r.Body)
		if !strings.Contains(body, "Today: 2026-07-15 (UTC)") || !strings.Contains(body, "Binance daily close on 2026-07-14") ||
			!strings.Contains(body, "Resolution date: 2026-07-29 (14 days from today)") {
			t.Errorf("replay brief: %.600s", body)
		}
		if quant == 2 && !strings.Contains(body, `\"spot\":`+spot) {
			t.Errorf("price_outlook must read the close before asOf (%s): %.400s", spot, body)
		}
	}
	if quant != 2 {
		t.Fatalf("quant calls: %d", quant)
	}
}

func TestDatedAfter(t *testing.T) {
	asOf := day("2026-09-09")
	for text, want := range map[string]bool{
		"Bitcoin (BTC) News Today: Why BTC Is Up – 02 October 2026": true,
		"Stock market news for Sep. 9, 2026":                        true,
		"Fed decision on 2026-09-17 looms":                          true,
		"LINK hits an 8-month high on September 8th, 2026":          false,
		"XRP Price Prediction: XRP Will Hit $27 by October 2026":    false,
		"Solana (SOL) News Today: Why SOL Is Down – 08 Sept 2026":   false,
		"No dates here, just 2026 and 9 lives":                      false,
	} {
		if got := datedAfter(text, asOf); got != want {
			t.Errorf("datedAfter(%q) = %v", text, got)
		}
	}
}
