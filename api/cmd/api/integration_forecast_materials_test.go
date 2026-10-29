package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

// Forecast materials end to end: a PDF, a CSV and a PNG reach Bedrock once
// each as one document or image block; the PNG's digest fails, so it is
// flagged and refunded; the panel sees D1..Dn and the report carries
// material findings and evidence linked to the material, never a URL; the
// row never holds the bytes; an update with {} keeps the read digests at 5
// credits without reading anything again.
func TestIntegrationForecastMaterials(t *testing.T) {
	var chart bytes.Buffer
	if err := png.Encode(&chart, image.NewGray(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Type /Pages /Kids [3 0 R] /Count 3 >> endobj\n3 0 obj << /Type /Page /Parent 2 0 R >> endobj\n%%EOF\n")
	csv := []byte("date,rate\n2026-09-17,4.25\n")
	enc := base64.StdEncoding.EncodeToString
	var digests, panel atomic.Int32
	var updating atomic.Bool // the update keeps the two read files only
	s := boot(t, options{model: func(r llmtest.Request) llmtest.Reply {
		var blocks []string
		for _, b := range r.Messages[len(r.Messages)-1].Content {
			if d, ok := b["document"].(map[string]any); ok {
				blocks = append(blocks, d["format"].(string)+"|"+d["name"].(string)+"|"+d["source"].(map[string]any)["bytes"].(string))
			}
			if img, ok := b["image"].(map[string]any); ok {
				blocks = append(blocks, img["format"].(string)+"|"+img["source"].(map[string]any)["bytes"].(string))
			}
		}
		switch {
		case strings.HasPrefix(r.System, "Read the user's material"):
			digests.Add(1)
			want := map[string]string{"Material D1:": "pdf|Document 1 (Fed note)|" + enc(pdf), "Material D2:": "csv|Document 1 (rates)|" + enc(csv), "Material D3:": "png|" + enc(chart.Bytes())}
			for marker, block := range want {
				if strings.Contains(r.Prompt, marker) && !slices.Equal(blocks, []string{block}) {
					t.Errorf("%s blocks %v", marker, blocks)
				}
			}
			if strings.Contains(r.Prompt, "Material D3:") {
				return llmtest.Reply{Status: 500}
			}
			return llmtest.Reply{Text: `{"summary":"A note on the policy path.","relevance":"direct","asOf":"2026-09-30","findings":[{"claim":"The note expects a 25 bp cut in December.","ref":"p. 2","asOf":"2026-09","direction":"yes","strength":"moderate"}],"excerpt":"We expect a cut."}`}
		case strings.HasPrefix(r.System, queryWriter):
			return noMarketQueries
		case len(blocks) > 0:
			t.Errorf("a file reached a panel call: %.60s", r.System)
		case strings.HasPrefix(r.System, "You are the aggregator"):
			panel.Add(1)
			if !strings.Contains(r.Prompt, "D1..Dn · Your materials") || !strings.Contains(r.Prompt, `"material":"D1"`) {
				t.Errorf("aggregator without materials: %.300s", r.Prompt)
			}
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.4},{"outcome":"No","p":0.6}],"summary":"We lean No.","confidence":"medium",
				"evidence":[{"claim":"The user's note expects a cut","sourceId":"D1","source":"User material","stance":"supports","asOf":""},
				{"claim":"Its finding","sourceId":"F1","source":"User material","stance":"supports","asOf":""}]}`}
		case strings.HasPrefix(r.System, "You are the research assistant"):
			if strings.Contains(r.Prompt, "Fed note") || strings.Contains(r.Prompt, "policy path") {
				t.Errorf("material content reached the research queries")
			}
			return llmtest.Reply{Text: `{"queries":["fed december cut odds"]}`}
		case strings.HasPrefix(r.System, "You are the"):
			panel.Add(1)
			if !strings.Contains(r.Prompt, `{"id":"D1","name":"Fed note.pdf","kind":"PDF","pages":3`) ||
				strings.Contains(r.Prompt, `{"id":"D3","name":"chart.png","kind":"image","status":"unreadable"}`) == updating.Load() {
				t.Errorf("specialist without materials: %.300s", r.Prompt)
			}
			return llmtest.Reply{Text: `{"probabilities":[{"outcome":"Yes","p":0.4},{"outcome":"No","p":0.6}],"reasoning":"D1 and F1.","keyFactors":["k"],"confidence":"medium"}`}
		}
		t.Errorf("unexpected model call: %.60q", r.System)
		return llmtest.Reply{Status: 400}
	}})
	c := s.signIn(t)
	used := func() int { return s.count(t, "select coalesce(sum(credits),0) from ai_usage where user_id=$1", c.id) }
	done := func(id string) any {
		var f any
		eventually(t, 30*time.Second, "the forecast run", func() bool {
			f = c.call("GET", "/api/forecasts/"+id, nil, 200)
			return str(t, f, "forecast.status") != "running"
		})
		return f
	}
	doc := func(media, name string, data []byte) map[string]any {
		return map[string]any{"kind": "document", "mediaType": media, "name": name, "data": enc(data)}
	}
	draft := map[string]any{"shape": "binary", "question": "Will the Fed cut rates at its December 2099 meeting?", "outcomes": []string{"Yes", "No"},
		"resolutionDate": "2099-12-31", "category": "macro"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range draft {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	c.fail("POST", "/api/forecasts", with(map[string]any{"attachments": []any{doc("application/pdf", "fake.pdf", csv)}}), 400, "bad_request")
	c.fail("POST", "/api/forecasts", with(map[string]any{"attachments": slices.Repeat([]any{doc("text/csv", "r.csv", csv)}, 6)}), 400, "bad_request")
	if s.count(t, "select count(*) from forecasts where user_id=$1", c.id) != 0 || used() != 0 || digests.Load() != 0 {
		t.Fatal("rejected files created a forecast, charged or reached the model")
	}

	created := c.call("POST", "/api/forecasts", with(map[string]any{"attachments": []any{doc("application/pdf", "Fed note.pdf", pdf), doc("text/csv", "rates.csv", csv),
		map[string]any{"kind": "image", "mediaType": "image/png", "name": "chart.png", "data": enc(chart.Bytes())}}}), 201)
	shape(t, created, "forecast", forecastKeys)
	shape(t, created, "forecast.materials.0", "id kind mediaType name size pages status summary asOf findings excerpt readAt")
	shape(t, created, "forecast.materials.1", "id kind mediaType name size status summary asOf findings excerpt readAt")
	if num(t, created, "credits.used") != 8 || str(t, created, "forecast.materials.0.status") != "pending" || num(t, created, "forecast.materials.0.pages") != 3 {
		t.Fatalf("create: %v", created)
	}
	id := str(t, created, "forecast.id")
	f := done(id)
	if str(t, f, "forecast.status") != "completed" || digests.Load() != 3 || used() != 7 {
		t.Fatalf("run: %v, %d digests, %d credits", str(t, f, "forecast.status"), digests.Load(), used())
	}
	shape(t, f, "forecast", forecastKeys)
	for _, i := range []string{"0", "1"} {
		shape(t, f, "forecast.materials."+i, "id kind mediaType name size status summary relevance asOf findings excerpt readAt"+map[string]string{"0": " pages", "1": ""}[i])
		shapeAll(t, f, "forecast.materials."+i+".findings", "claim ref asOf direction strength")
		isTime(t, f, "forecast.materials."+i+".readAt")
	}
	shape(t, f, "forecast.materials.2", "id kind mediaType name size status error summary asOf findings excerpt readAt")
	if str(t, f, "forecast.materials.2.error") != "unreadable" || str(t, f, "forecast.materials.0.relevance") != "direct" || str(t, f, "forecast.materials.1.name") != "rates.csv" {
		t.Fatalf("materials: %v", at(t, f, "forecast.materials"))
	}
	if p := at(t, f, "forecast.progress.materials"); p == nil {
		t.Fatal("no materials step")
	}
	shape(t, f, "forecast.progress.materials", "status total reused read failed")
	if num(t, f, "forecast.progress.materials.read") != 2 || num(t, f, "forecast.progress.materials.failed") != 1 {
		t.Fatalf("progress %v", at(t, f, "forecast.progress.materials"))
	}
	var material []any
	for _, x := range list(t, f, "forecast.result.findings") {
		if m := x.(map[string]any); m["origin"] == "material" {
			material = append(material, x)
		}
	}
	if len(material) != 2 || str(t, material[0], "sourceIds.0") != "D1" || str(t, material[1], "sourceIds.0") != "D2" || str(t, material[0], "ref") != "p. 2" {
		t.Fatalf("material findings %v", list(t, f, "forecast.result.findings"))
	}
	ev := list(t, f, "forecast.result.evidence")
	if str(t, ev[0], "materialId") != "D1" || !isNull(t, ev[0], "url") || str(t, ev[0], "source") != "Your material · Fed note.pdf" || str(t, ev[0], "asOf") != "2026-09-30" ||
		str(t, ev[1], "materialId") != str(t, material[0], "sourceIds.0") || str(t, ev[1], "asOf") != "2026-09" {
		t.Fatalf("evidence %v", ev)
	}
	flagged := false
	for _, fl := range list(t, f, "forecast.result.flags") {
		flagged = flagged || str(t, fl, "type") == "low_information" && strings.Contains(str(t, fl, "note"), "chart.png")
	}
	if !flagged || !strings.Contains(str(t, f, "forecast.result.knowledgeNote"), "2 of your materials (user-supplied, unverified)") {
		t.Fatalf("flags %v note %q", list(t, f, "forecast.result.flags"), str(t, f, "forecast.result.knowledgeNote"))
	}
	for _, b := range [][]byte{pdf, csv, chart.Bytes()} {
		if n := s.count(t, "select count(*) from forecasts f where id=$1 and strpos(row_to_json(f)::text, $2) > 0", id, enc(b)[:min(64, len(enc(b)))]); n != 0 {
			t.Fatal("the row holds file bytes")
		}
	}
	if s.count(t, "select count(*) from forecasts f where id=$1 and strpos(row_to_json(f)::text, 'Document 1 (') > 0", id) != 0 {
		t.Fatal("the row holds a model-facing document name")
	}
	if strings.Contains(s.logs.String(), "Fed note") || strings.Contains(s.logs.String(), enc(pdf)[:32]) {
		t.Fatal("a log line names the file or holds its bytes")
	}

	before := digests.Load()
	updating.Store(true)
	up := c.call("POST", "/api/forecasts/"+id+"/update", map[string]any{}, 201)
	shape(t, up, "forecast", forecastKeys)
	if num(t, up, "credits.used") != 12 || len(list(t, up, "forecast.materials")) != 2 || str(t, up, "forecast.materials.1.reusedFrom") != id ||
		str(t, up, "forecast.materials.1.status") != "read" {
		t.Fatalf("update: %v", up)
	}
	u := done(str(t, up, "forecast.id"))
	if str(t, u, "forecast.status") != "completed" || digests.Load() != before || panel.Load() != 8 {
		t.Fatalf("update run %v: %d digests, %d panel calls", str(t, u, "forecast.status"), digests.Load(), panel.Load())
	}
}
