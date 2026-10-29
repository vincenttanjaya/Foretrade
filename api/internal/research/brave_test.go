package research

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestBraveSearchesInEnglish(t *testing.T) {
	f := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"results":[]}`) })
	s := New(Config{BraveAPIKey: "k", BraveBase: f.srv.URL}, nil, nil)
	s.brave.gap = 0
	s.SearchNews(context.Background(), "chainlink", "pd", 5)
	s.SearchWeb(context.Background(), "chainlink", 5)
	for range 2 {
		if r := <-f.reqs; r.URL.Query().Get("search_lang") != "en" {
			t.Errorf("%s without search_lang=en: %v", r.URL.Path, r.URL.Query())
		}
	}
}

func TestSourceName(t *testing.T) {
	for _, tc := range []struct{ name, host, want string }{
		{"Yahoo Finance", "finance.yahoo.com", "Yahoo Finance"},
		{"24/7 Wall St.", "247wallst.com", "24/7 Wall St."},
		{"CoinDesk", "coindesk.com", "CoinDesk"},
		{"The Block", "theblock.co", "The Block"},
		{"Decrypt Media", "decrypt.co", "Decrypt Media"},
		{"United States", "newsbeep.com", "newsbeep.com"}, // Brave named the place, not the outlet
		{"", "crypto.news", "crypto.news"},
		{"!!", "crypto.news", "crypto.news"},
		{"BBC News", "bbc.co.uk", "bbc.co.uk"}, // ponytail: two-part suffixes show the hostname
		{"Fox Business", "x.com", "x.com"},     // a one-letter label matches only an equal name
		{"The Times", "t.co", "t.co"},
		{"X", "x.com", "X"},
	} {
		if got := sourceName(tc.name, tc.host); got != tc.want {
			t.Errorf("sourceName(%q, %q) = %q, want %q", tc.name, tc.host, got, tc.want)
		}
	}
}

// Ingest's macro searches ask Brave for US results; the same query without a
// country is a separate cache entry and sends none.
func TestBraveCountry(t *testing.T) {
	f := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"results":[]}`) })
	s := New(Config{BraveAPIKey: "k", BraveBase: f.srv.URL}, nil, nil)
	s.brave.gap = 0
	ctx := context.Background()
	s.search(ctx, "news", "US inflation CPI", "pd", "us", "", 10)
	s.SearchNews(ctx, "US inflation CPI", "pd", 10)
	s.search(ctx, "news", "US inflation CPI", "pd", "us", "", 10) // cached
	if r := <-f.reqs; r.URL.Query().Get("country") != "us" {
		t.Errorf("macro search: %v", r.URL.Query())
	}
	if r := <-f.reqs; r.URL.Query().Has("country") {
		t.Errorf("asset search: %v", r.URL.Query())
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("%d Brave calls, want 2", n)
	}
}

// SearchIn (deep research) sends its country, search_lang and freshness on
// either endpoint; lang is part of the cache key, a requested language keeps
// its Chinese results while every other junk rule still applies, and an
// unknown endpoint or freshness is refused before any call.
func TestSearchIn(t *testing.T) {
	f := newFakeBrave(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/web/search" {
			fmt.Fprint(w, `{"web":{"results":[{"title":"新交所 上市 规则","url":"https://rulebook.sgx.com/rule-8","description":"开盘 竞价"}]}}`)
			return
		}
		fmt.Fprint(w, `{"results":[{"title":"兰花材料 新交所 上市 招股","url":"https://www.zaobao.com.sg/finance/singapore/story1","description":"首日 开盘"},
			{"title":"Best Crypto to Buy Now","url":"https://promo.example/best","description":"presale"},
			{"title":"Orchid Materials SGX listing terms","url":"https://www.example.com/ipo","description":"Offer price"}]}`)
	})
	s := New(Config{BraveAPIKey: "k", BraveBase: f.srv.URL}, nil, nil)
	s.brave.gap = 0
	ctx := context.Background()

	zh, err := s.SearchIn(ctx, "news", "Orchid Materials 新交所 上市", "pm", "", "zh-hans", 8)
	if err != nil || len(zh) != 2 || zh[0].Title != "兰花材料 新交所 上市 招股" {
		t.Fatalf("zh-hans search keeps Chinese, drops promo: %v %+v", err, zh)
	}
	if r := <-f.reqs; r.URL.Path != "/news/search" || r.URL.Query().Get("search_lang") != "zh-hans" || r.URL.Query().Has("country") ||
		r.URL.Query().Get("freshness") != "pm" || r.URL.Query().Get("count") != "8" {
		t.Errorf("zh call %s %v", r.URL.Path, r.URL.Query())
	}
	en, _ := s.SearchIn(ctx, "news", "Orchid Materials 新交所 上市", "pm", "", "", 8) // same query in English: a separate entry
	if len(en) != 1 || en[0].Title != "Orchid Materials SGX listing terms" {
		t.Fatalf("English search drops Chinese: %+v", en)
	}
	if r := <-f.reqs; r.URL.Query().Get("search_lang") != "en" {
		t.Errorf("default lang %v", r.URL.Query())
	}
	web, _ := s.SearchIn(ctx, "web", "SGX opening auction", "py", "hk", "zh-hant", 8)
	if len(web) != 1 {
		t.Fatalf("web results %+v", web)
	}
	if r := <-f.reqs; r.URL.Path != "/web/search" || r.URL.Query().Get("freshness") != "py" || r.URL.Query().Get("country") != "hk" ||
		r.URL.Query().Get("search_lang") != "zh-hant" {
		t.Errorf("web call %s %v", r.URL.Path, r.URL.Query())
	}
	s.SearchIn(ctx, "news", "Orchid Materials 新交所 上市", "pm", "", "zh-hans", 8) // cached
	if n := f.calls.Load(); n != 3 {
		t.Fatalf("%d Brave calls, want 3", n)
	}
	if _, err := s.SearchIn(ctx, "images", "x", "", "", "", 8); err == nil {
		t.Error("an unknown endpoint is an error")
	}
	if _, err := s.SearchIn(ctx, "web", "x", "p2y", "", "", 8); err == nil {
		t.Error("an unknown freshness is an error")
	}
	if n := f.calls.Load(); n != 3 {
		t.Fatalf("refused calls reached Brave: %d", n)
	}
	if left, limit := s.BraveLeft(); left != 4997 || limit != 5000 {
		t.Errorf("BraveLeft %d/%d", left, limit)
	}
}

func TestValidFreshness(t *testing.T) {
	for f, want := range map[string]bool{"": true, "pd": true, "pw": true, "pm": true, "py": true, "2026-01-01to2026-02-01": true,
		"pyy": false, "2026-02-01to2026-01-01": false, "year": false} {
		if validFreshness(f) != want {
			t.Errorf("validFreshness(%q) = %v", f, !want)
		}
	}
}
