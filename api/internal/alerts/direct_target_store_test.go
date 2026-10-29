package alerts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
)

var directStoreColumns = []string{
	"user_id", "symbol", "trigger", "op", "price", "every", "at_minute", "weekday", "next_run_at", "action",
	"notional", "stop_loss", "take_profit", "forecast_id", "market_slug", "market_id", "linked_market", "market_odds",
}

// Literal fixtures keep the DB boundary independent of HTTP validation. A
// writer bypassing Create must not be able to store an incomplete direct
// target that later crashes the checker or silently changes its contract.
func directStoreFixture(user string) map[string]any {
	return map[string]any{
		"user_id": user, "symbol": "", "trigger": "odds", "op": "above", "price": 0.5, "action": "notify",
		"market_slug": directSlug, "market_id": directID,
		"linked_market": map[string]any{
			"marketId": directID, "conditionId": directCond, "marketSlug": directSlug, "eventSlug": directEvent,
			"question": directQ, "description": directRules, "resolutionSource": directSource,
			"endDate": directEnd, "asOf": "2026-10-05T00:00:00Z",
		},
		"market_odds": map[string]any{
			"question": directQ, "url": "https://polymarket.com/event/" + directEvent,
			"outcome": nil, "yesPrice": 0.3, "asOf": "2026-10-05T00:00:00Z",
		},
	}
}

func directStoreWrite(t *testing.T, e *apptest.Env, mode, id string, row map[string]any) (string, error) {
	t.Helper()
	var storedID *string
	if id != "" {
		storedID = &id
	}
	args := []any{storedID}
	values, updates := []string{}, []string{}
	for i, column := range directStoreColumns {
		value := row[column]
		if value != nil && (column == "linked_market" || column == "market_odds") {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			value = string(encoded)
		}
		args = append(args, value)
		parameter := fmt.Sprintf("$%d", i+2)
		if column == "linked_market" || column == "market_odds" {
			parameter += "::jsonb"
		}
		values = append(values, parameter)
		updates = append(updates, column+" = "+parameter)
	}
	query := "insert into alerts (id, " + strings.Join(directStoreColumns, ", ") + ") values (coalesce($1::uuid, gen_random_uuid()), " + strings.Join(values, ", ") + ")"
	switch mode {
	case "insert":
	case "update":
		query = "update alerts set " + strings.Join(updates, ", ") + " where id = $1::uuid"
	case "upsert":
		query += " on conflict (id) do update set " + strings.Join(updates, ", ")
	default:
		t.Fatalf("unknown store write %q", mode)
	}
	var got string
	err := e.DB.QueryRow(ctx, query+" returning id::text", args...).Scan(&got)
	return got, err
}

func directStoreState(t *testing.T, e *apptest.Env, id string) []byte {
	t.Helper()
	var state []byte
	if err := e.DB.QueryRow(ctx, "select to_jsonb(a) from alerts a where id = $1", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// Every mutation must fail at insert, update and conflict-upsert, and rejected
// writes must leave the previously valid target unchanged. Timestamp parsers
// may reject malformed values as a SQLSTATE22 data exception rather than23514.
func directStoreRejects(t *testing.T, e *apptest.Env, invalid map[string]any) {
	t.Helper()
	for _, mode := range []string{"insert", "update", "upsert"} {
		t.Run(mode, func(t *testing.T) {
			id := ""
			var before []byte
			if mode != "insert" {
				var err error
				id, err = directStoreWrite(t, e, "insert", "", directStoreFixture(e.User))
				if err != nil {
					t.Fatalf("seed valid direct target: %v", err)
				}
				before = directStoreState(t, e, id)
				defer e.DB.Exec(ctx, "delete from alerts where id = $1", id)
			}
			stored, err := directStoreWrite(t, e, mode, id, invalid)
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" && !strings.HasPrefix(pg.Code, "22") {
				t.Errorf("invalid %s must be rejected by Postgres, got id=%q err=%v", mode, stored, err)
			}
			if mode == "insert" && stored != "" {
				e.DB.Exec(ctx, "delete from alerts where id = $1", stored)
			}
			if mode != "insert" && !bytes.Equal(directStoreState(t, e, id), before) {
				t.Errorf("rejected %s changed the stored canonical direct target", mode)
			}
		})
	}
}

func TestDirectAlertStoreAcceptsCanonicalAndExistingTargets(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	forecastID := oddsForecast(t, e, e.User, "fed-cut", 0.3)
	for _, c := range []struct {
		name string
		row  map[string]any
	}{
		{"direct leaf", directStoreFixture(e.User)},
		{"ordinary forecast", map[string]any{"user_id": e.User, "symbol": "", "trigger": "odds", "op": "above", "price": 0.5, "action": "notify", "forecast_id": forecastID}},
		{"ordinary price", map[string]any{"user_id": e.User, "symbol": "BTC", "trigger": "price", "op": "below", "price": 90000, "action": "notify"}},
		{"ordinary schedule", map[string]any{"user_id": e.User, "symbol": "BTC", "trigger": "schedule", "every": "day", "at_minute": 480, "next_run_at": time.Date(2099, 1, 1, 8, 0, 0, 0, time.UTC), "action": "notify"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, err := directStoreWrite(t, e, "insert", "", c.row)
			if err != nil {
				t.Fatalf("valid %s rejected: %v", c.name, err)
			}
			before := directStoreState(t, e, id)
			for _, mode := range []string{"update", "upsert"} {
				if got, err := directStoreWrite(t, e, mode, id, c.row); err != nil || got != id || !bytes.Equal(directStoreState(t, e, id), before) {
					t.Errorf("valid %s %s must preserve the target: id=%q err=%v", c.name, mode, got, err)
				}
			}
		})
	}
	// A source unavailable in Gamma remains explicitly null; neither a made-up
	// source nor a fresh timestamp is required for already stored alerts.
	nullSource := directStoreFixture(e.User)
	nullSource["linked_market"].(map[string]any)["resolutionSource"] = nil
	if _, err := directStoreWrite(t, e, "insert", "", nullSource); err != nil {
		t.Fatalf("explicitly unknown canonical source is valid: %v", err)
	}
}

func TestDirectAlertStoreRejectsIncompleteAndAmbiguousTargets(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	forecastID := oddsForecast(t, e, e.User, "fed-cut", 0.3)
	for _, c := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing both targets", func(r map[string]any) {
			delete(r, "market_slug")
			delete(r, "market_id")
			delete(r, "linked_market")
			delete(r, "market_odds")
		}},
		{"dual targets", func(r map[string]any) { r["forecast_id"] = forecastID }},
		{"missing slug", func(r map[string]any) { delete(r, "market_slug") }},
		{"missing ID", func(r map[string]any) { delete(r, "market_id") }},
		{"missing link", func(r map[string]any) { delete(r, "linked_market") }},
		{"missing odds", func(r map[string]any) { delete(r, "market_odds") }},
		{"slug only", func(r map[string]any) { delete(r, "market_id"); delete(r, "linked_market"); delete(r, "market_odds") }},
		{"ID only", func(r map[string]any) { delete(r, "market_slug"); delete(r, "linked_market"); delete(r, "market_odds") }},
		{"link only", func(r map[string]any) { delete(r, "market_slug"); delete(r, "market_id"); delete(r, "market_odds") }},
		{"odds only", func(r map[string]any) { delete(r, "market_slug"); delete(r, "market_id"); delete(r, "linked_market") }},
		{"invalid column slug", func(r map[string]any) { r["market_slug"] = "../leaf" }},
		{"invalid column ID", func(r map[string]any) { r["market_id"] = "not-a-market-ID" }},
		{"link JSON null", func(r map[string]any) { r["linked_market"] = json.RawMessage(`null`) }},
		{"link JSON array", func(r map[string]any) { r["linked_market"] = []map[string]any{} }},
		{"odds JSON null", func(r map[string]any) { r["market_odds"] = json.RawMessage(`null`) }},
		{"odds JSON array", func(r map[string]any) { r["market_odds"] = []map[string]any{} }},
		{"link ID mismatch", func(r map[string]any) { r["linked_market"].(map[string]any)["marketId"] = "8102" }},
		{"link slug mismatch", func(r map[string]any) { r["linked_market"].(map[string]any)["marketSlug"] = "other-leaf" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := directStoreFixture(e.User)
			c.edit(row)
			directStoreRejects(t, e, row)
		})
	}
}

func TestDirectAlertStoreRejectsIncompleteCanonicalSnapshots(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	for _, c := range []struct {
		name string
		key  string
		bad  any
	}{
		{"condition missing", "conditionId", nil},
		{"condition invalid", "conditionId", "0x1234"},
		{"condition not hex", "conditionId", "0x" + strings.Repeat("g", 64)},
		{"event missing", "eventSlug", nil},
		{"event invalid", "eventSlug", "../other-event"},
		{"question missing", "question", nil},
		{"question blank", "question", ""},
		{"question too short", "question", "Why?"},
		{"question too long", "question", strings.Repeat("q", 281)},
		{"question wrong JSON type", "question", 12345678901},
		{"rules missing", "description", nil},
		{"rules blank", "description", ""},
		{"rules too long", "description", strings.Repeat("r", 8001)},
		{"rules wrong JSON type", "description", true},
		{"end missing", "endDate", nil},
		{"end invalid", "endDate", "not-a-date"},
		{"end not an instant", "endDate", "2099-12-31"},
		{"asOf missing", "asOf", nil},
		{"asOf invalid", "asOf", "yesterday"},
		{"asOf zero", "asOf", "0001-01-01T00:00:00Z"},
		{"source wrong JSON type", "resolutionSource", []string{"made-up"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := directStoreFixture(e.User)
			link := maps.Clone(row["linked_market"].(map[string]any))
			if c.bad == nil {
				delete(link, c.key)
			} else {
				link[c.key] = c.bad
			}
			row["linked_market"] = link
			directStoreRejects(t, e, row)
		})
	}
}

func TestDirectAlertStoreRejectsOrdersProtectionAndOtherTriggerMetadata(t *testing.T) {
	e := apptest.New(t, Register, apptest.Options{})
	for _, c := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"buy", func(r map[string]any) { r["action"], r["notional"] = "buy", 100 }},
		{"sell", func(r map[string]any) { r["action"], r["notional"] = "sell", 100 }},
		{"stop protection", func(r map[string]any) { r["stop_loss"] = 0.2 }},
		{"target protection", func(r map[string]any) { r["take_profit"] = 0.8 }},
		{"price with direct metadata", func(r map[string]any) { r["trigger"], r["symbol"], r["price"] = "price", "BTC", 90000 }},
		{"schedule with direct metadata", func(r map[string]any) {
			r["trigger"], r["symbol"], r["op"], r["price"] = "schedule", "BTC", nil, nil
			r["every"], r["at_minute"], r["next_run_at"] = "day", 480, time.Date(2099, 1, 1, 8, 0, 0, 0, time.UTC)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := directStoreFixture(e.User)
			c.edit(row)
			directStoreRejects(t, e, row)
		})
	}
}
