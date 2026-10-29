package llm

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReadSSE(t *testing.T) {
	big := strings.Repeat("x", 100<<10) // past bufio's default 64 KiB token
	in := ": keep-alive\r\n" +
		"event: message_start\r\ndata: {\"a\":1}\r\n\r\n" +
		"data: line one\ndata:line two\n\n" +
		"event: ping\n\n" + // no data: skipped
		"data: " + big + "\n\n" +
		"data: [DONE]\n\n" +
		"data: tail without blank line"
	var got []string
	err := readSSE(strings.NewReader(in), func(event string, data []byte) error {
		got = append(got, event+"|"+string(data))
		return nil
	})
	want := []string{`message_start|{"a":1}`, "|line one\nline two", "|" + big, "|tail without blank line"}
	if err != nil || len(got) != len(want) {
		t.Fatalf("%v: %d events", err, len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: %.60q, want %.60q", i, got[i], want[i])
		}
	}
}

// Every provider error shape yields a code and a readable message.
func TestErrorFrom(t *testing.T) {
	for _, tc := range []struct{ body, code, msg, param string }{
		{`{"error":{"message":"Incorrect API key","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`, "invalid_api_key", "Incorrect API key", ""},
		{`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "overloaded_error", "Overloaded", ""},
		{`{"error":"model \"llama9\" not found, try pulling it first"}`, "", `model "llama9" not found, try pulling it first`, ""},
		{`{"detail":"Unauthorized"}`, "", "Unauthorized", ""},
		{`[{"error":{"code":400,"message":"API key not valid.","status":"INVALID_ARGUMENT"}}]`, "INVALID_ARGUMENT", "API key not valid.", ""},
		{`{"type":"error","code":"invalid_value","message":"bad tools","param":"tools[0].type"}`, "invalid_value", "bad tools", "tools[0].type"},
		{`{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"x"}}`, "subscription_sharing_usage_limit_exceeded", planMessages["subscription_sharing_usage_limit_exceeded"], ""},
		{`<html>Bad Gateway</html>`, "", "<html>Bad Gateway</html>", ""},
	} {
		pe := errorFrom([]byte(tc.body))
		if pe.Code != tc.code || pe.Msg != tc.msg || pe.param != tc.param {
			t.Errorf("%s → %+v", tc.body, pe)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	h := http.Header{}
	if d, ok := retryAfter(h, 1); d != time.Second || !ok {
		t.Fatalf("backoff: %v %v", d, ok)
	}
	h.Set("Retry-After", "3")
	if d, ok := retryAfter(h, 0); d != 3*time.Second || !ok {
		t.Fatalf("seconds: %v %v", d, ok)
	}
	h.Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	if _, ok := retryAfter(h, 0); ok {
		t.Fatal("an hour is too long to wait")
	}
	for _, tc := range []struct {
		pe   ProviderError
		want bool
	}{
		{ProviderError{Status: 429}, true}, {ProviderError{Status: 408}, true}, {ProviderError{Status: 529, Code: "overloaded_error"}, true},
		{ProviderError{Status: 400}, false}, {ProviderError{Status: 401}, false},
		{ProviderError{Status: 429, Code: "subscription_sharing_usage_limit_exceeded"}, false},
		{ProviderError{Status: 403, Code: "chatpass_v2_scope_not_authorized"}, false},
		{ProviderError{Status: 503, Code: "subscription_sharing_usage_unavailable"}, true},
	} {
		if retryable(&tc.pe) != tc.want {
			t.Errorf("%+v: retryable %v", tc.pe, !tc.want)
		}
	}
}
