package screener

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/vincenttanjaya/Foretrade/api/internal/yahoo"
)

// constituents.json lists each index's constituents as researched on
// 2026-10-06, one row per index membership, each with its list's source, its
// kind (official: the index provider or exchange; secondary: a reproduction
// of it) and as-of date. Rows Yahoo did not serve then (every PSEi ticker)
// carry yahooResolves false and stay listed.
//
//go:embed constituents.json
var constituentsJSON []byte

type member struct {
	Index         string `json:"index"`
	Market        string `json:"market"`
	Yahoo         string `json:"yahoo"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	Source        string `json:"source"`
	SourceKind    string `json:"sourceKind"`
	AsOf          string `json:"asOf"`
	YahooResolves bool   `json:"yahooResolves"`
}

// Membership is a stock's place in one index and where that list came from.
type Membership struct {
	Index      string `json:"index"`
	Source     string `json:"source"`
	SourceKind string `json:"sourceKind"` // "official" | "secondary"
	AsOf       string `json:"asOf"`       // YYYY-MM-DD
}

// Stock is one screened listing: a Yahoo ticker with every index holding it.
type Stock struct {
	Ticker   string // Yahoo's, e.g. 7203.T, 005930.KS, 0700.HK
	Code     string // the exchange's own code
	Name     string // as the constituent list names it
	Market   string // one of Markets
	Currency string // the list's; Yahoo's meta wins once a run has read it
	Resolves bool   // Yahoo served the ticker when the list was built
	Indices  []Membership
}

// List is one index's embedded constituent list.
type List struct {
	Index      string   `json:"index"`
	Market     string   `json:"market"`
	Count      int      `json:"count"`
	AsOf       string   `json:"asOf"`
	SourceKind string   `json:"sourceKind"` // "official", "secondary", or "mixed" when its rows differ
	Sources    []string `json:"sources"`
}

// Market is a market code the screener filters by.
type Market struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Markets are the screened markets, North Asia first.
var Markets = []Market{
	{"JP", "Japan"}, {"KR", "South Korea"}, {"TW", "Taiwan"}, {"HK", "Hong Kong"}, {"SG", "Singapore"},
	{"ID", "Indonesia"}, {"TH", "Thailand"}, {"MY", "Malaysia"}, {"PH", "Philippines"}, {"VN", "Vietnam"},
}

var stocks, lists = mustLoad(constituentsJSON)

// Stocks is every screened listing in list order, one per Yahoo ticker.
func Stocks() []Stock { return slices.Clone(stocks) }

// Lists is every embedded index list, in list order.
func Lists() []List { return slices.Clone(lists) }

func mustLoad(raw []byte) ([]Stock, []List) {
	s, l, err := load(raw)
	if err != nil {
		panic(err)
	}
	return s, l
}

// load parses the rows and merges them by Yahoo ticker, keeping every index
// membership; a row that is not a valid constituent fails it.
func load(raw []byte) ([]Stock, []List, error) {
	var rows []member
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, nil, fmt.Errorf("screener: constituents: %w", err)
	}
	var out []Stock
	at := map[string]int{}
	var ls []List
	li := map[string]int{}
	for i, r := range rows {
		sym, ok := yahoo.Normalize(r.Yahoo)
		switch {
		case !ok:
			return nil, nil, fmt.Errorf("screener: constituent %d: invalid Yahoo ticker %q", i, r.Yahoo)
		case !slices.ContainsFunc(Markets, func(m Market) bool { return m.Code == r.Market }):
			return nil, nil, fmt.Errorf("screener: constituent %s: unknown market %q", sym, r.Market)
		case r.Index == "" || r.Name == "" || r.Code == "" || r.Source == "" || r.AsOf == "" ||
			r.SourceKind != "official" && r.SourceKind != "secondary":
			return nil, nil, fmt.Errorf("screener: constituent %s: missing index, name, code, source, kind or as-of", sym)
		}
		m := Membership{Index: r.Index, Source: r.Source, SourceKind: r.SourceKind, AsOf: r.AsOf}
		if j, seen := at[sym]; seen {
			if slices.ContainsFunc(out[j].Indices, func(x Membership) bool { return x.Index == r.Index }) {
				continue // the same membership twice
			}
			out[j].Indices = append(out[j].Indices, m)
			out[j].Resolves = out[j].Resolves || r.YahooResolves
		} else {
			at[sym] = len(out)
			out = append(out, Stock{Ticker: sym, Code: r.Code, Name: r.Name, Market: r.Market, Currency: r.Currency,
				Resolves: r.YahooResolves, Indices: []Membership{m}})
		}
		j, seen := li[r.Index]
		if !seen {
			j = len(ls)
			li[r.Index] = j
			ls = append(ls, List{Index: r.Index, Market: r.Market, AsOf: r.AsOf, SourceKind: r.SourceKind})
		}
		l := &ls[j]
		l.Count++
		l.AsOf = max(l.AsOf, r.AsOf)
		if l.SourceKind != r.SourceKind {
			l.SourceKind = "mixed"
		}
		if !slices.Contains(l.Sources, r.Source) {
			l.Sources = append(l.Sources, r.Source)
		}
	}
	return out, ls, nil
}
