package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

const (
	referencePlan = `{"angles":[
		{"kind":"comparables","label":"Past BTC moves over 30 days","queries":[{"q":"bitcoin 30 day price moves history","type":"web","recency":"year"}]},
		{"kind":"catalysts","label":"Scheduled crypto catalysts","queries":[{"q":"bitcoin ETF decision date","type":"news","recency":"month"}]}]}`
	referenceOpinion = `{"probabilities":[{"outcome":"Yes","p":0.5},{"outcome":"No","p":0.5}],"reasoning":"Even.","keyFactors":["k"],"confidence":"medium"}`
	// The aggregator's hedge to 50% against an R1 well above it; with a note citing F1 or without one.
	hedgeCited   = `{"probabilities":[{"outcome":"Yes","p":0.5},{"outcome":"No","p":0.5}],"summary":"We call it even.","confidence":"medium","referenceNote":{"reason":"The ETF decision (F1) lands before the date and cuts either way.","ids":["F1"]}}`
	hedgeUncited = `{"probabilities":[{"outcome":"Yes","p":0.5},{"outcome":"No","p":0.5}],"summary":"We call it even.","confidence":"medium"}`
)

// referenceRun runs a deep-research BTC price-rule forecast whose panel and
// aggregator call it even, the aggregator answering aggregate, and returns
// the completed forecast, R1's P(Yes) as GET shows it and the row's anchor_p.
// Every panel member and the aggregator must see the R1 line.
func referenceRun(t *testing.T, aggregate string) (f any, r1, anchor float64) {
	t.Helper()
	// The desk methods on, as in production: a binary price rule stays an event frame, so R1 and the guard are unchanged.
	s := boot(t, options{brave: true, deep: true, methods: true, model: func(r llmtest.Request) llmtest.Reply {
		switch {
		case strings.HasPrefix(r.System, queryWriter):
			return noMarketQueries
		case strings.HasPrefix(r.System, "Plan the research"):
			return llmtest.Reply{Text: referencePlan}
		case strings.HasPrefix(r.System, "Screen search results"):
			var scores []string
			for _, m := range candidateLine.FindAllStringSubmatch(r.Prompt, -1) {
				scores = append(scores, fmt.Sprintf(`{"id":%q,"score":3}`, m[1]))
			}
			return llmtest.Reply{Text: `{"scores":[` + strings.Join(scores, ",") + `]}`}
		case strings.HasPrefix(r.System, "Extract findings"):
			id := firstSource.FindStringSubmatch(r.Prompt)
			if id == nil {
				t.Errorf("extract prompt without sources: %.300s", r.Prompt)
				return llmtest.Reply{Status: 400}
			}
			return llmtest.Reply{Text: fmt.Sprintf(`{"findings":[{"claim":"The SEC decides on a spot ETF application before the resolution date, per %[1]s.","sourceIds":["%[1]s"],
				"asOf":"","angle":"A2","direction":"context","strength":"moderate"}]}`, id[1])}
		case strings.HasPrefix(r.System, "Review a forecasting desk's research ledger"):
			return llmtest.Reply{Text: `{"queries":[]}`}
		case strings.HasPrefix(r.System, "You are the aggregator"), strings.HasPrefix(r.System, "You are the Base-rate statistician"),
			strings.HasPrefix(r.System, "You are the Market & quant analyst"), strings.HasPrefix(r.System, "You are the Narrative & macro skeptic"):
			if !strings.Contains(r.Prompt, "\nR1 · Volatility-only reference (server-computed from Binance daily closes, zero-drift lognormal; tool data, safe to cite as R1): P(Yes) ") ||
				!strings.Contains(r.Prompt, "that the BTC daily close on ") {
				t.Errorf("%.40s did not see R1: %.600s", r.System, r.Prompt)
			}
			if before := r.Prompt[:max(strings.Index(r.Prompt, "R1 ·"), 0)]; strings.Count(before, "<untrusted_web_content>") != strings.Count(before, "</untrusted_web_content>") {
				t.Errorf("%.40s: R1 must sit in the trusted data, outside the web text", r.System)
			}
			if strings.HasPrefix(r.System, "You are the aggregator") {
				if !strings.Contains(r.System, "Start from R1 (the volatility-only reference)") || !strings.Contains(r.Prompt, `"id":"F1"`) {
					t.Errorf("the aggregator without the R1 rules or the ledger: %.300s", r.Prompt)
				}
				return llmtest.Reply{Text: aggregate}
			}
			return llmtest.Reply{Text: referenceOpinion}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)
	date := time.Now().UTC().AddDate(0, 0, 30).Format(time.DateOnly)
	id := str(t, c.call("POST", "/api/forecasts", map[string]any{"shape": "binary",
		"question": "Will the Binance BTC/USDT daily close on " + date + " be above $97,000?", "outcomes": []string{"Yes", "No"},
		"resolutionDate": date, "category": "crypto", "priceRule": map[string]any{"symbol": "BTC", "op": "above", "threshold": 97000, "mode": "on_date"}}, 201), "forecast.id")
	eventually(t, 60*time.Second, "the forecast run", func() bool {
		f = c.call("GET", "/api/forecasts/"+id, nil, 200)
		return str(t, f, "forecast.status") != "running"
	})
	if str(t, f, "forecast.status") != "completed" {
		t.Fatalf("run: %v", f)
	}
	shape(t, f, "forecast.result.reference", "kind pYes spot volAnnPct horizonDays asOf")
	isTime(t, f, "forecast.result.reference.asOf")
	r1 = num(t, f, "forecast.result.reference.pYes")
	if str(t, f, "forecast.result.reference.kind") != "volatility" || num(t, f, "forecast.result.reference.horizonDays") != 30 ||
		num(t, f, "forecast.result.reference.spot") <= 0 || num(t, f, "forecast.result.reference.volAnnPct") <= 0 || r1 < 0.6 || r1 > 0.99 {
		t.Fatalf("the fixture's R1 must lean Yes, well past the guard's slack: %v", at(t, f, "forecast.result.reference"))
	}
	if err := s.db.QueryRow(context.Background(), `select anchor_p from forecasts where id = $1`, id).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	return f, r1, anchor
}

// The shrink guard end to end: the aggregator pulls a live BTC price
// question to 50% from R1 without a referenceNote, so the stored result is
// R1's probability with guard "shrink" and the server's flag, and the row's
// anchor_p is R1 for the track record.
func TestIntegrationForecastShrinkGuard(t *testing.T) {
	f, r1, anchor := referenceRun(t, hedgeUncited)
	r := "forecast.result."
	shape(t, f, "forecast.result", resultKeys+" guard")
	flagged := false
	for _, fl := range list(t, f, r+"flags") {
		flagged = flagged || str(t, fl, "type") == "low_information" &&
			str(t, fl, "note") == "Server check: the desk moved toward 50% from the volatility-only reference without citing evidence, so the final probability is the reference's."
	}
	if str(t, f, r+"guard") != "shrink" || num(t, f, r+"probabilities.0.p") != r1 || !near(num(t, f, r+"probabilities.1.p"), 1-r1) || anchor != r1 || !flagged {
		t.Fatalf("guarded result: %v (anchor_p %v)", at(t, f, "forecast.result"), anchor)
	}
}

// The same hedge with a referenceNote citing the run's finding F1 is kept:
// no guard, the note on the result, and anchor_p still R1.
func TestIntegrationForecastReferenceNoteKept(t *testing.T) {
	f, r1, anchor := referenceRun(t, hedgeCited)
	r := "forecast.result."
	shape(t, f, "forecast.result", resultKeys+" referenceNote")
	shape(t, f, r+"referenceNote", "reason ids")
	if num(t, f, r+"probabilities.0.p") != 0.5 || anchor != r1 || len(list(t, f, r+"referenceNote.ids")) != 1 || str(t, f, r+"referenceNote.ids.0") != "F1" ||
		str(t, f, r+"referenceNote.reason") != "The ETF decision (F1) lands before the date and cuts either way." || str(t, f, r+"findings.0.id") != "F1" {
		t.Fatalf("cited hedge: %v (anchor_p %v)", at(t, f, "forecast.result"), anchor)
	}
	for _, fl := range list(t, f, r+"flags") {
		if strings.HasPrefix(str(t, fl, "note"), "Server check:") {
			t.Fatalf("a kept hedge carries the guard's flag: %v", fl)
		}
	}
}
