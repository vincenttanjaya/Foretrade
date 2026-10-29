package aiconfig

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

const (
	keyA = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	keyB = "ff0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
)

// Sealed secrets open only under their own SECRET_KEY; no key refuses both ways.
func TestBox(t *testing.T) {
	a, b := newBox(keyA), newBox(keyB)
	sealed, err := a.seal([]byte("sk-secret-value"))
	if err != nil || bytes.Contains(sealed, []byte("sk-secret-value")) {
		t.Fatalf("seal: %v", err)
	}
	if again, _ := a.seal([]byte("sk-secret-value")); bytes.Equal(again, sealed) {
		t.Fatal("a fresh nonce per seal")
	}
	if plain, err := a.open(sealed); err != nil || string(plain) != "sk-secret-value" {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	if _, err := b.open(sealed); err == nil {
		t.Fatal("wrong key opened it")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := a.open(sealed); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
	if _, err := a.open([]byte("short")); err == nil {
		t.Fatal("short input")
	}
	for _, bad := range []string{"", "abc", keyA[:62], "zz" + keyA[2:], keyA + "00"} {
		if newBox(bad) != nil {
			t.Errorf("SECRET_KEY %q accepted", bad)
		}
	}
	var none *box
	if _, err := none.seal([]byte("x")); !errors.Is(err, errNoKey) || !strings.Contains(err.Error(), "SECRET_KEY is not set") {
		t.Fatalf("no key: %v", err)
	}
	if _, err := none.open(sealed); !errors.Is(err, errNoKey) {
		t.Fatalf("no key open: %v", err)
	}
	for in, want := range map[string]string{"": "", "short-key-15chr": "", "sixteen-chars-ab": "…s-ab", "sk-or-v1-0123456789abcdef": "…cdef"} {
		if got := hint(in); got != want {
			t.Errorf("hint(%q) = %q, want %q", in, got, want)
		}
	}
}

func ptr(s string) *string { return &s }

// merge keeps the stored key only while the provider and base URL stay,
// names exactly the changed fields and validates each one.
func TestMerge(t *testing.T) {
	cur := llm.Settings{Provider: "openai", Model: "m1", APIKey: "sk-stored", KnowledgeCutoff: time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)}
	for _, c := range []struct {
		name    string
		d       draft
		key     string
		changed []string
	}{
		{"nothing", draft{}, "sk-stored", nil},
		{"same values", draft{Provider: ptr(" OpenAI "), Model: ptr("m1"), KnowledgeCutoff: ptr("2026-06-30")}, "sk-stored", nil},
		{"model only keeps the key", draft{Model: ptr("m2")}, "sk-stored", []string{"model"}},
		{"provider switch drops the key", draft{Provider: ptr("groq")}, "", []string{"provider", "apiKey"}},
		{"base URL change drops the key", draft{BaseURL: ptr("https://proxy.example/v1/")}, "", []string{"baseUrl", "apiKey"}},
		{"switch with a key", draft{Provider: ptr("groq"), APIKey: " gsk-new "}, "gsk-new", []string{"provider", "apiKey"}},
		{"same key again", draft{APIKey: "sk-stored"}, "sk-stored", nil},
		{"clear", draft{ClearKey: true}, "", []string{"apiKey"}},
		{"effort, region, cutoff", draft{Effort: ptr("off"), AWSRegion: ptr("eu-west-1"), KnowledgeCutoff: ptr("")}, "sk-stored",
			[]string{"effort", "awsRegion", "knowledgeCutoff"}},
	} {
		next, changed, err := merge(cur, true, &c.d)
		if err != nil || next.APIKey != c.key || !slices.Equal(changed, c.changed) {
			t.Errorf("%s: key %q changed %v err %v", c.name, next.APIKey, changed, err)
		}
	}
	if next, _, _ := merge(cur, true, &draft{BaseURL: ptr("http://host.docker.internal:11434/v1/")}); next.BaseURL != "http://host.docker.internal:11434/v1" {
		t.Errorf("trailing slash kept: %q", next.BaseURL)
	}
	if next, _, _ := merge(cur, true, &draft{KnowledgeCutoff: ptr("")}); !next.KnowledgeCutoff.IsZero() {
		t.Error("an empty cutoff is the default")
	}
	// A broken (undecryptable, so empty) stored key is still cleared on a switch.
	if _, changed, _ := merge(llm.Settings{Provider: "openai"}, true, &draft{Provider: ptr("groq")}); !slices.Equal(changed, []string{"provider", "apiKey"}) {
		t.Errorf("broken key not cleared: %v", changed)
	}
	if _, changed, _ := merge(llm.Settings{Provider: "openai"}, false, &draft{Provider: ptr("groq")}); !slices.Equal(changed, []string{"provider"}) {
		t.Errorf("no stored key, nothing to clear: %v", changed)
	}
	for _, bad := range []draft{{Effort: ptr("high")}, {KnowledgeCutoff: ptr("June")}, {BaseURL: ptr("ftp://x")}, {BaseURL: ptr("localhost:11434")},
		{Model: ptr(strings.Repeat("m", 201))}, {APIKey: strings.Repeat("k", 4097)}} {
		var bad400 invalid
		if _, _, err := merge(cur, true, &bad); !errors.As(err, &bad400) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

// A new crawl base drops the stored crawl token unless one comes with it.
func TestMergeSearch(t *testing.T) {
	cur := search{brave: "brave-key", crawlBase: "https://crawl.example", crawlToken: "tok"}
	stored := map[string]bool{"braveApiKey": true, "crawlToken": true}
	next, changed, err := mergeSearch(cur, stored, &searchDraft{CrawlBase: ptr("https://other.example/")})
	if err != nil || next.crawlBase != "https://other.example" || next.crawlToken != "" || !slices.Equal(changed, []string{"crawlBase", "crawlToken"}) {
		t.Fatalf("new base: %+v %v %v", next, changed, err)
	}
	next, changed, _ = mergeSearch(cur, stored, &searchDraft{CrawlBase: ptr("https://other.example"), CrawlToken: "tok2", ClearBraveAPIKey: true})
	if next.crawlToken != "tok2" || next.brave != "" || !slices.Equal(changed, []string{"braveApiKey", "crawlBase", "crawlToken"}) {
		t.Fatalf("new base and token: %+v %v", next, changed)
	}
	if _, changed, _ := mergeSearch(cur, stored, &searchDraft{}); changed != nil {
		t.Fatalf("empty draft: %v", changed)
	}
	if _, _, err := mergeSearch(cur, stored, &searchDraft{CrawlBase: ptr("not a url")}); err == nil {
		t.Fatal("bad crawl base")
	}
}
