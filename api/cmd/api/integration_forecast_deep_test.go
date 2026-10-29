package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

const (
	deepPlanReply = `{"angles":[
		{"kind":"comparables","label":"Recent SGX debuts: opening vs offer","queries":[{"q":"SGX mainboard IPO debut opening price","type":"web","recency":"year"},
			{"q":"JustCo SGX debut opening price","type":"web","recency":"year"}]},
		{"kind":"primary","label":"Prospectus terms","queries":[{"q":"Orchid Materials prospectus offer price","type":"web","recency":"any"},
			{"q":"Orchid Materials cornerstone investors","type":"web","recency":"any"}]},
		{"kind":"catalysts","label":"Subscription and listing dates","queries":[{"q":"Orchid Materials subscription close","type":"news","recency":"month"}]},
		{"kind":"sentiment","label":"Singapore market mood","queries":[{"q":"Singapore IPO market sentiment","type":"news","recency":"week"},
			{"q":"Orchid Materials 新交所 上市","type":"news","recency":"month","country":"","lang":"zh-hans"}]}]}`
	deepGapReply = `{"queries":[{"angle":"A1","q":"Toku SGX debut opening price","type":"web","recency":"any"},
		{"angle":"A3","q":"Orchid Materials subscription results","type":"news","recency":"week"}]}`
	deepOpinion = `{"probabilities":[{"outcome":"< S$0.50","p":0.1},{"outcome":"S$0.50 – S$0.60","p":0.2},{"outcome":"S$0.60 – S$0.70","p":0.4},
		{"outcome":"S$0.70 – S$0.80","p":0.2},{"outcome":"≥ S$0.80","p":0.1}],"reasoning":"F1 and S1 anchor the class.","keyFactors":["comparables"],"confidence":"medium"}`
	deepAggregate = `{"probabilities":[{"outcome":"< S$0.50","p":0.1},{"outcome":"S$0.50 – S$0.60","p":0.25},{"outcome":"S$0.60 – S$0.70","p":0.35},
		{"outcome":"S$0.70 – S$0.80","p":0.2},{"outcome":"≥ S$0.80","p":0.1}],"summary":"We expect a debut near the offer.","confidence":"medium",
		"evidence":[{"claim":"Recent comparables opened above their offers.","sourceId":"F1","source":"","stance":"supports","asOf":""}],
		"watch":[{"signal":"Final offer price","detail":"Set when the book closes on 10 December.","branches":[{"if":"Priced at the top of the range","then":"Higher buckets gain","direction":"higher"}]}]}`
)

var (
	candidateLine = regexp.MustCompile(`\{"id":"(C\d+)","angle":"A\d+","title":"([^"]*)"`)
	firstSource   = regexp.MustCompile(`"id":"(S\d+)"`)
)

// Round 8 deep research end to end: the plan's typed queries reach Brave
// (past-year freshness, an "any" web search with none, a zh-hans search whose
// Chinese result is kept), the screen drops the candidate it scores 0, the
// best pages are read, findings citing a source outside their batch are
// dropped and URLs are cut from claims, the gap round searches again, the
// panel and the aggregator see the ledger, and the report carries
// findings[], watch[], the deep research fields and an F-cited evidence item
// whose link is its finding's first source. Mid-run the trace shows each phase.
func TestIntegrationForecastDeepResearch(t *testing.T) {
	release := make(chan struct{})
	open := sync.OnceFunc(func() { close(release) })
	defer open() // before boot's cleanups, which wait for the run: never leave the fake blocked when the test stops early
	var mu sync.Mutex
	offTopic := "" // the candidate the screen scores 0
	s := boot(t, options{brave: true, deep: true, model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, queryWriter):
			return noMarketQueries
		case strings.HasPrefix(r.System, "Plan the research"):
			return llmtest.Reply{Text: deepPlanReply}
		case strings.HasPrefix(r.System, "Screen search results"):
			var scores []string
			for _, m := range candidateLine.FindAllStringSubmatch(r.Prompt, -1) {
				score := 3
				if strings.Contains(m[2], "Orchid Materials cornerstone investors") {
					score = 0
					mu.Lock()
					offTopic = m[2]
					mu.Unlock()
				}
				scores = append(scores, fmt.Sprintf(`{"id":%q,"score":%d}`, m[1], score))
			}
			return llmtest.Reply{Text: `{"scores":[` + strings.Join(scores, ",") + `]}`}
		case strings.HasPrefix(r.System, "Extract findings"):
			id := firstSource.FindStringSubmatch(r.Prompt)
			if id == nil {
				t.Errorf("extract prompt without sources: %.300s", r.Prompt)
				return llmtest.Reply{Status: 400}
			}
			return llmtest.Reply{Text: fmt.Sprintf(`{"findings":[
				{"claim":"Comparable debut %[1]s opened 9.2%% above its offer price.","sourceIds":["%[1]s"],"asOf":"2026-09-30","angle":"A1","direction":"higher","strength":"moderate"},
				{"claim":"Analysts at %[1]s expect the opening near the offer price.","sourceIds":["%[1]s"],"asOf":"","angle":"A2","direction":"center","strength":"slight"},
				{"claim":"A forged claim pinned on a source outside this batch.","sourceIds":["S99"],"angle":"A1","direction":"lower","strength":"strong"},
				{"claim":"Order books at %[1]s were covered three times https://evil.example/fake","sourceIds":["%[1]s"],"angle":"A4","direction":"higher","strength":"slight"}]}`, id[1])}
		case strings.HasPrefix(r.System, "Review a forecasting desk's research ledger"):
			return llmtest.Reply{Text: deepGapReply}
		case strings.HasPrefix(r.System, "You are the aggregator"):
			if !strings.Contains(r.Prompt, `"id":"F1"`) || !strings.Contains(r.System, `"watch": [{"signal"`) {
				t.Errorf("the aggregator without the ledger or the watch list: %.300s", r.Prompt)
			}
			return llmtest.Reply{Text: deepAggregate}
		case strings.HasPrefix(r.System, "You are the Base-rate statistician"), strings.HasPrefix(r.System, "You are the Market & quant analyst"),
			strings.HasPrefix(r.System, "You are the Narrative & macro skeptic"):
			if !strings.Contains(r.Prompt, "Research (deep web research via Brave Search") || !strings.Contains(r.Prompt, `"id":"F1"`) {
				t.Errorf("%.40s did not see the ledger: %.400s", r.System, r.Prompt)
			}
			if strings.HasPrefix(r.System, "You are the Base-rate statistician") && !strings.Contains(r.System, "build your reference class from them") {
				t.Error("the base-rate brief must build the class from the comparables angle")
			}
			<-release
			return llmtest.Reply{Text: deepOpinion}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)
	id := str(t, c.call("POST", "/api/forecasts", map[string]any{"shape": "ordered",
		"question": "What will be the opening price of Orchid Materials Holding shares on their SGX debut on 2099-12-15?",
		"outcomes": []string{"< S$0.50", "S$0.50 – S$0.60", "S$0.60 – S$0.70", "S$0.70 – S$0.80", "≥ S$0.80"}, "resolutionDate": "2099-12-15",
		"category": "markets"}, 201), "forecast.id")

	// Mid-run: the research is done and traced phase by phase while the specialists are held.
	var f any
	p := "forecast.progress.research."
	eventually(t, 90*time.Second, "the deep research done", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return !isNull(t, f, "forecast.progress") && str(t, f, p+"status") == "done"
	})
	shape(t, f, "forecast.progress.research", "status queries found read mode depth angles searches findings steps")
	shapeAll(t, f, p+"steps", "phase round status count of")
	var phases []string
	for _, st := range list(t, f, p+"steps") {
		phases = append(phases, fmt.Sprintf("%s%v:%s", str(t, st, "phase"), num(t, st, "round"), str(t, st, "status")))
	}
	if want := "plan0:done search1:done screen1:done read1:done extract1:done gap1:done search2:done screen2:done read2:done extract2:done"; strings.Join(phases, " ") != want ||
		str(t, f, p+"depth") != "deep" || num(t, f, p+"findings") == 0 || len(list(t, f, p+"angles")) != 4 || str(t, f, p+"mode") != "live" ||
		num(t, f, p+"searches") != 9 || len(list(t, f, p+"queries")) != 9 || num(t, f, p+"found") != 8 {
		t.Fatalf("mid-run trace %v: %v", phases, at(t, f, "forecast.progress.research"))
	}

	open()
	eventually(t, 60*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	r := "forecast.result."
	if str(t, f, "forecast.status") != "completed" {
		t.Fatalf("run: %v", f)
	}
	shape(t, f, "forecast.result", resultKeys+" watch view") // round 9b: an ordered result reads as a desk view
	shapeAll(t, f, r+"findings", "id claim sourceIds asOf angle direction strength")
	shape(t, f, r+"research", "queries status depth angles rounds searches candidates")
	shapeAll(t, f, r+"research.angles", "id kind label queries findings")
	shapeAll(t, f, r+"watch", "signal detail branches")
	shapeAll(t, f, r+"watch.0.branches", "if then direction")
	if str(t, f, r+"research.depth") != "deep" || num(t, f, r+"research.rounds") != 2 || num(t, f, r+"research.searches") > 18 ||
		num(t, f, r+"research.searches") != 9 || str(t, f, r+"research.angles.0.kind") != "comparables" || str(t, f, r+"research.status") != "live" ||
		!strings.HasPrefix(str(t, f, r+"knowledgeNote"), "Deep web research via Brave Search (2 rounds, 9 searches, ") {
		t.Fatalf("research: %v note %v", at(t, f, r+"research"), at(t, f, r+"knowledgeNote"))
	}

	sources := list(t, f, r+"sources")
	urls := map[string]string{}
	zh := false
	mu.Lock()
	dropped := offTopic
	mu.Unlock()
	for _, src := range sources {
		urls[str(t, src, "id")] = str(t, src, "url")
		zh = zh || strings.HasPrefix(str(t, src, "title"), "本地报道")
		if dropped == "" || str(t, src, "title") == dropped {
			t.Errorf("the off-topic candidate %q reached the pack: %v", dropped, src)
		}
	}
	findings := list(t, f, r+"findings")
	if len(sources) != 8 || len(findings) != 9 || !zh { // one result per query, 9 queries, one screened out; 3 extract batches of 3 valid findings
		t.Fatalf("%d sources, %d findings, Chinese source kept %v: %v", len(sources), len(findings), zh, sources)
	}
	for i, fd := range findings {
		if str(t, fd, "id") != fmt.Sprintf("F%d", i+1) || strings.Contains(str(t, fd, "claim"), "http") || strings.Contains(str(t, fd, "claim"), "forged") {
			t.Errorf("finding %v", fd)
		}
		for _, sid := range list(t, fd, "sourceIds") {
			if _, ok := urls[sid.(string)]; !ok {
				t.Errorf("%s cites %v, not in the pack", str(t, fd, "id"), sid)
			}
		}
	}
	if str(t, f, r+"evidence.0.url") != urls[str(t, f, r+"findings.0.sourceIds.0")] || str(t, f, r+"evidence.0.asOf") != str(t, f, r+"findings.0.asOf") ||
		str(t, f, r+"watch.0.branches.0.direction") != "higher" {
		t.Fatalf("F-cited evidence %v / F1 %v / watch %v", at(t, f, r+"evidence.0"), at(t, f, r+"findings.0"), at(t, f, r+"watch"))
	}

	// Brave saw the typed queries; the boot's wire ingestion (freshness pd) aside.
	var sawPY, sawAny, sawZH bool
	for _, q := range s.web.queries() {
		switch {
		case q.Get("freshness") == "pd":
		case q.Get("q") == "SGX mainboard IPO debut opening price":
			sawPY = q.Get("freshness") == "py" && q.Get("count") == "8" && q.Get("search_lang") == "en"
		case q.Get("q") == "Orchid Materials prospectus offer price":
			sawAny = !q.Has("freshness")
		case q.Get("q") == "Orchid Materials 新交所 上市":
			sawZH = q.Get("search_lang") == "zh-hans" && q.Get("freshness") == "pm"
		}
	}
	if !sawPY || !sawAny || !sawZH {
		t.Fatalf("Brave params: py %v any %v zh %v", sawPY, sawAny, sawZH)
	}
	searches, _ := s.web.seen()
	if research := slices.DeleteFunc(searches, func(q search) bool { return q.freshness == "pd" }); len(research) != 9 { // the plan's 7 queries, then the gap's 2
		t.Fatalf("Brave calls %d: %v", len(research), research)
	}
	if s.count(t, "select credits from ai_usage where user_id = $1", c.id) != 5 {
		t.Fatal("deep research costs no extra credits")
	}
}
