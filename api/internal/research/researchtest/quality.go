// Package researchtest supplies explicit accepted provenance for consumer fixtures.
package researchtest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vincenttanjaya/Foretrade/api/internal/research"
)

// AcceptBriefs upgrades intentionally clean consumer fixtures using actual
// accepted stored inputs. Unverifiable-legacy tests do not call this helper.
func AcceptBriefs(t testing.TB, db *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	items, err := research.New(research.Config{}, db, nil).News(ctx, research.NewsFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	var n research.NewsItem
	if len(items) > 0 {
		n = items[0]
	} else {
		err = db.QueryRow(ctx, `insert into news_items (title,url,source,description,symbols,topic) values ('Crypto ETF demand grows','https://reuters.com/quality-fixture','Reuters','Crypto ETF flows','{}','macro') returning id,title,url,source,description,published_at,symbols,topic`).Scan(&n.ID, &n.Title, &n.URL, &n.Source, &n.Description, &n.PublishedAt, &n.Symbols, &n.Topic)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query(ctx, `select id,brief from market_briefs`)
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		id  string
		raw []byte
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.raw); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	for _, e := range entries {
		var b map[string]any
		if err := json.Unmarshal(e.raw, &b); err != nil {
			t.Fatal(err)
		}
		if b["headline"] == nil {
			b["headline"] = "ETF demand"
		}
		if b["summary"] == nil {
			b["summary"] = "Crypto ETF flows"
		}
		b["articleCount"] = 1
		b["bullets"] = []research.BriefBullet{{Text: "BTC flows", Sources: []string{n.ID}}, {Text: "ETH flows", Sources: []string{n.ID}}, {Text: "SOL flows", Sources: []string{n.ID}}}
		b["_quality"] = research.InputQuality{Version: 1, Inputs: []research.NewsItem{n}}
		raw, _ := json.Marshal(b)
		if _, err := db.Exec(ctx, `update market_briefs set brief=$2 where id=$1`, e.id, raw); err != nil {
			t.Fatal(err)
		}
	}
}

// Board supplies provenance for explicit deterministic board fixtures. It
// seeds every displayed model input and keeps the public board shape intact.
func Board(t testing.TB, db *pgxpool.Pool, raw []byte) []byte {
	t.Helper()
	ctx := context.Background()
	var b map[string]any
	json.Unmarshal(raw, &b)
	var inputs []research.NewsItem
	for _, side := range []string{"bullish", "bearish"} {
		picks, _ := b[side].([]any)
		for _, value := range picks {
			p := value.(map[string]any)
			hs, _ := p["headlines"].([]any)
			for _, value := range hs {
				h := value.(map[string]any)
				var n research.NewsItem
				err := db.QueryRow(ctx, `insert into news_items (title,url,source,symbols) values ($1,$2,$3,array[$4]::text[]) on conflict (url) do update set title=excluded.title returning id,title,url,source,description,published_at,symbols,topic`, h["title"], h["url"], h["source"], p["symbol"]).Scan(&n.ID, &n.Title, &n.URL, &n.Source, &n.Description, &n.PublishedAt, &n.Symbols, &n.Topic)
				if err != nil {
					t.Fatal(err)
				}
				duplicate := false
				for _, old := range inputs {
					duplicate = duplicate || old.ID == n.ID
				}
				if !duplicate {
					inputs = append(inputs, n)
				}
			}
		}
	}
	b["_quality"] = map[string]any{"version": 1, "inputCount": len(inputs), "inputs": inputs, "briefs": []any{}}
	result, _ := json.Marshal(b)
	return result
}
