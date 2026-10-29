package chat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/attach"
	"github.com/vincenttanjaya/Foretrade/api/internal/llm/llmtest"
)

func imageFixture(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	var b bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, width, height)), nil)
	} else {
		err = png.Encode(&b, image.NewGray(image.Rect(0, 0, width, height)))
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func imageInput(data []byte, media, name string) map[string]any {
	return map[string]any{"kind": "image", "mediaType": media, "name": name, "data": base64.StdEncoding.EncodeToString(data)}
}
func imageBody(images ...map[string]any) map[string]any {
	return map[string]any{"message": "Read this chart", "attachments": images}
}
func imageCount(r llmtest.Request) int {
	n := 0
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if b["image"] != nil {
				n++
			}
		}
	}
	return n
}
func imageChatID(t *testing.T, evs []sseEvent) string {
	t.Helper()
	var c struct{ ChatID string }
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	if err := json.Unmarshal(evs[0].Data, &c); err != nil {
		t.Fatal(err)
	}
	return c.ChatID
}

// Missing admission checks must reject before either account usage or chat rows change.
func TestImageAdmission(t *testing.T) {
	small := imageFixture(t, "png", 1, 1)
	large := append(slices.Clone(small), make([]byte, 1_500_001-len(small))...)
	pngInput := func(b []byte) map[string]any { return imageInput(b, "image/png", "chart.png") }
	cases := map[string]map[string]any{
		"sixth image":       imageBody(pngInput(small), pngInput(small), pngInput(small), pngInput(small), pngInput(small), pngInput(small)),
		"byte overflow":     imageBody(pngInput(large)),
		"empty bytes":       imageBody(pngInput(nil)),
		"encoding mismatch": imageBody(imageInput(small, "image/jpeg", "chart.jpg")),
		"truncated GIF":     imageBody(imageInput([]byte("GIF89a"), "image/gif", "chart.gif")),
		"unsupported BMP":   imageBody(imageInput([]byte("BM"), "image/bmp", "chart.bmp")),
		"invalid header":    imageBody(pngInput([]byte("not an image"))),
		"side overflow":     imageBody(pngInput(imageFixture(t, "png", 8001, 1))),
		"pixel overflow":    imageBody(pngInput(imageFixture(t, "png", 4001, 4000))),
		"name overflow":     imageBody(imageInput(small, "image/png", strings.Repeat("é", 121))),
	}
	bad := pngInput(small)
	bad["data"] = "%%%%"
	cases["malformed base64"] = imageBody(bad)
	bad = pngInput(small)
	bad["kind"] = "document"
	cases["wrong kind"] = imageBody(bad)
	bad = pngInput(small)
	bad["data"] = strings.Repeat("A", 2_000_001)
	cases["encoded overflow"] = imageBody(bad)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{Reply: func(llmtest.Request) llmtest.Reply { return llmtest.Reply{Text: "Unexpected model call"} }})
			w := e.Do("POST", "/api/chat/stream", body)
			var chats int
			if err := e.DB.QueryRow(context.Background(), "select count(*) from chats where user_id=$1", e.User).Scan(&chats); err != nil {
				t.Fatal(err)
			}
			if w.Code != 400 || apptest.ErrorCode(t, w) != "bad_request" || spent(t, e) != 0 || chats != 0 || len(e.Bedrock.Requests()) != 0 {
				t.Fatalf("admission: status %d, credits %d, chats %d, model calls %d", w.Code, spent(t, e), chats, len(e.Bedrock.Requests()))
			}
		})
	}
}

func TestImageBodyAndByteBoundaries(t *testing.T) {
	small := imageFixture(t, "png", 1, 1)
	for _, size := range []int{1_499_999, 1_500_000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			b := append(slices.Clone(small), make([]byte, size-len(small))...)
			e := apptest.New(t, Register, apptest.Options{Reply: func(llmtest.Request) llmtest.Reply { return llmtest.Reply{Text: "Chart read."} }})
			body := imageBody(imageInput(b, "image/png", strings.Repeat("é", 120)), imageInput(b, "image/png", strings.Repeat("é", 120)))
			body["message"] = strings.Repeat("😀", 4000)
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) > attach.BodyLimit {
				t.Fatalf("two maxima do not fit: %d", len(raw))
			}
			raw = append(raw, bytes.Repeat([]byte(" "), attach.BodyLimit-len(raw))...)
			w := e.Do("POST", "/api/chat/stream", string(raw))
			if w.Code != 200 || spent(t, e) != 3 || imageCount(e.Bedrock.Requests()[0]) != 2 {
				t.Fatalf("valid max: status %d, credits %d, calls %d", w.Code, spent(t, e), len(e.Bedrock.Requests()))
			}
		})
	}
	e := apptest.New(t, Register, apptest.Options{})
	raw := `{"message":"Read this chart"}`
	raw += strings.Repeat(" ", attach.BodyLimit+1-len(raw))
	if w := e.Do("POST", "/api/chat/stream", raw); w.Code != 400 || spent(t, e) != 0 {
		t.Fatalf("trailing padding accepted: %d", w.Code)
	}
}

// This catches image loss/duplication through tool, final-budget and recovery turns, and byte retention in history.
func TestImageReplayAndMetadata(t *testing.T) {
	data := imageFixture(t, "jpeg", 2, 1)
	var replies []llmtest.Reply
	for i := 0; i < 4; i++ {
		replies = append(replies, llmtest.Reply{Tools: []llmtest.ToolUse{tool("p", "get_portfolio", "")}})
	}
	replies = append(replies, llmtest.Reply{Text: "I've already written the answer above.", Tools: []llmtest.ToolUse{tool("over", "get_portfolio", "")}}, llmtest.Reply{Text: "The chart suggests a range. **View:** neutral."}, llmtest.Reply{Text: "Refreshed. **View:** neutral."})
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, replies...)})
	body := imageBody(imageInput(data, "image/jpeg", "C:\\private\\chart\n.jpg"))
	body["mode"] = "fast"
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", body).Body.String())
	id := imageChatID(t, evs)
	if len(find(evs, "done")) != 1 || spent(t, e) != 3 {
		t.Fatalf("answer/charge: %v %d", names(evs), spent(t, e))
	}
	for i, r := range e.Bedrock.Requests() {
		if imageCount(r) != 1 {
			t.Fatalf("request %d has %d images", i, imageCount(r))
		}
		m := r.Messages[len(r.Messages)-1]
		var encoded string
		for _, b := range m.Content {
			if img, ok := b["image"].(map[string]any); ok {
				encoded = img["source"].(map[string]any)["bytes"].(string)
				if img["format"] != "jpeg" {
					t.Fatal(img)
				}
			}
		}
		if encoded != base64.StdEncoding.EncodeToString(data) {
			t.Fatalf("request %d changed bytes", i)
		}
	}
	if len(e.Bedrock.Requests()) != 6 {
		t.Fatalf("tool/final/nudge requests %d", len(e.Bedrock.Requests()))
	}
	w := e.Do("GET", "/api/chats/"+id, nil)
	var got struct {
		Chat struct {
			Title    string
			Messages []struct {
				Content     string
				Attachments []map[string]any
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Chat.Title != "Read this chart" || got.Chat.Messages[0].Content != "Read this chart\n\n[image: chart.jpg]" || len(got.Chat.Messages[0].Attachments) != 1 || got.Chat.Messages[1].Attachments == nil {
		t.Fatalf("safe metadata: %s", w.Body)
	}
	a := got.Chat.Messages[0].Attachments[0]
	if len(a) != 4 || a["kind"] != "image" || a["mediaType"] != "image/jpeg" || a["name"] != "chart.jpg" || a["size"] != float64(len(data)) {
		t.Fatalf("metadata: %+v", a)
	}
	var saved string
	if err := e.DB.QueryRow(context.Background(), "select row_to_json(m)::text from chat_messages m where chat_id=$1 and role='user'", id).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved, base64.StdEncoding.EncodeToString(data)) || strings.Contains(w.Body.String(), "\"data\"") {
		t.Fatal("saved image bytes")
	}
	e.Do("POST", "/api/chat/stream", map[string]any{"chatId": id, "message": "Refresh the live setup"})
	if r := e.Bedrock.Requests()[6]; imageCount(r) != 0 || !strings.Contains(string(r.Body), "[image: chart.jpg]") {
		t.Fatalf("historical bytes or marker lost: images %d", imageCount(r))
	}
}

func TestImageChargeAndRawEventRefund(t *testing.T) {
	data := imageFixture(t, "png", 1, 1)
	for name, c := range map[string]struct {
		replies []llmtest.Reply
		want    int
	}{
		"before events":                  {[]llmtest.Reply{{Status: 500}}, 0},
		"raw start before error":         {[]llmtest.Reply{{StreamErr: "bad stream"}}, 3},
		"private reasoning before error": {[]llmtest.Reply{{Reasoning: "private", Tools: []llmtest.ToolUse{tool("p", "get_portfolio", "")}}, {Status: 500}}, 3},
		"tool before error":              {[]llmtest.Reply{{Tools: []llmtest.ToolUse{tool("p", "get_portfolio", "")}}, {Status: 500}}, 3},
		"partial before error":           {[]llmtest.Reply{{Text: "Partial.", Tools: []llmtest.ToolUse{tool("p", "get_portfolio", "")}}, {Status: 500}}, 3},
		"empty successful recovery":      {[]llmtest.Reply{{}, {}}, 3},
	} {
		t.Run(name, func(t *testing.T) {
			e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t, c.replies...)})
			e.Do("POST", "/api/chat/stream", imageBody(imageInput(data, "image/png", "chart.png")))
			if spent(t, e) != c.want {
				t.Fatalf("refund: got %d want %d", spent(t, e), c.want)
			}
		})
	}
}

func TestImageQuickBypassAndForcedForecast(t *testing.T) {
	data := imageFixture(t, "png", 1, 1)
	e := apptest.New(t, Register, apptest.Options{Reply: llmtest.Queue(t,
		llmtest.Reply{Tools: []llmtest.ToolUse{tool("f", "draft_forecast", `{"idea":"BTC tomorrow"}`)}},
		llmtest.Reply{Text: "Use the ordinary Forecasts workflow. **View:** neutral."},
	)})
	body := imageBody(imageInput(data, "image/png", "chart.png"))
	body["message"] = "buy $500 of BTC"
	evs := parseSSE(t, e.Do("POST", "/api/chat/stream", body).Body.String())
	reqs := e.Bedrock.Requests()
	if len(reqs) != 2 || spent(t, e) != 3 || slices.Contains(reqs[0].Tools, "draft_forecast") || len(find(evs, "card")) != 0 {
		t.Fatalf("quick/extra spend: calls %d credits %d cards %d", len(reqs), spent(t, e), len(find(evs, "card")))
	}
	if !strings.Contains(reqs[1].Prompt, `unknown tool`) || !strings.Contains(reqs[0].System, "untrusted") || !strings.Contains(reqs[0].System, "image") {
		t.Fatal("forced paid tool or image trust rule missing")
	}
	var trades int
	if err := e.DB.QueryRow(context.Background(), "select count(*) from trades where user_id=$1", e.User).Scan(&trades); err != nil {
		t.Fatal(err)
	}
	if trades != 0 {
		t.Fatal("image executed an order")
	}
}
