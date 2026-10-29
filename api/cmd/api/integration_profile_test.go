package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func profileObject(t *testing.T, value any, key string) map[string]any {
	t.Helper()
	obj, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("object: %T %v", value, value)
	}
	if key != "" {
		obj, ok = obj[key].(map[string]any)
		if !ok {
			t.Fatalf("missing object %s: %v", key, value)
		}
	}
	return obj
}

func profilePreferences(revision any) map[string]any {
	return map[string]any{"enabled": true, "riskProfile": nil, "style": nil, "focusSymbols": []string{}, "customPrompt": "", "revision": revision}
}

func persistedMemoryCard(t *testing.T, s *server, uid, text, role string) (string, string) {
	t.Helper()
	var chatID, proposalID string
	if err := s.db.QueryRow(context.Background(), "insert into chats(user_id,title) values($1,'Memory fixture') returning id,gen_random_uuid()", uid).Scan(&chatID, &proposalID); err != nil {
		t.Fatal(err)
	}
	cards, err := json.Marshal([]any{map[string]any{"type": "memory", "data": map[string]any{"proposalId": proposalID, "text": text, "status": "open"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(context.Background(), "insert into chat_messages(chat_id,role,content,cards) values($1,$2,'Suggesting a memory',$3)", chatID, role, cards); err != nil {
		t.Fatal(err)
	}
	return chatID, proposalID
}

func TestIntegrationProfile(t *testing.T) {
	t.Run("owned preferences and auth", func(t *testing.T) {
		s := boot(t, options{})
		c := s.signIn(t)
		me := profileObject(t, c.call("GET", "/api/me", nil, 200), "user")
		p := profileObject(t, me, "profile")
		if p["enabled"] != true || p["riskProfile"] != nil || p["style"] != nil || p["revision"] != float64(0) || len(p["memories"].([]any)) != 0 {
			t.Fatalf("default profile: %v", p)
		}
		got := profileObject(t, c.call("GET", "/api/me/profile", nil, 200), "profile")
		if fmt.Sprint(got) != fmt.Sprint(p) {
			t.Fatalf("me/profile mismatch: %v vs %v", p, got)
		}
		prefs := profilePreferences(p["revision"])
		prefs["riskProfile"], prefs["style"], prefs["customPrompt"], prefs["focusSymbols"] = "conservative", "swing", strings.Repeat("界", 600), []string{"btc", "NVDA", "BTC"}
		p = profileObject(t, c.call("PUT", "/api/me/profile", prefs, 200), "profile")
		if p["revision"] != float64(1) || len(p["focusSymbols"].([]any)) != 2 || len(p["memories"].([]any)) != 0 {
			t.Fatalf("saved: %v", p)
		}
		c.call("PUT", "/api/me/profile", prefs, 409)
		foreign := s.signIn(t)
		f := profileObject(t, foreign.call("GET", "/api/me/profile", nil, 200), "profile")
		if f["customPrompt"] != "" || f["riskProfile"] != nil {
			t.Fatalf("foreign profile: %v", f)
		}
		p2 := profileObject(t, profileObject(t, c.call("POST", "/api/auth/login", map[string]string{"email": c.email, "password": testPassword}, 200), "user"), "profile")
		if fmt.Sprint(p2) != fmt.Sprint(p) {
			t.Fatalf("login profile mismatch: %v", p2)
		}
	})
	t.Run("strict preference input", func(t *testing.T) {
		s := boot(t, options{})
		c := s.signIn(t)
		cases := []any{`null`, `[]`, `{} {}`, `{"enabled":true}`, `{"enabled":null,"riskProfile":null,"style":null,"focusSymbols":[],"customPrompt":"","revision":0}`}
		for _, field := range []string{"memories", "userId", "hold", "horizonDays"} {
			p := profilePreferences(0)
			p[field] = []any{}
			cases = append(cases, p)
		}
		for _, field := range []string{"enabled", "focusSymbols", "customPrompt", "revision"} {
			p := profilePreferences(0)
			p[field] = nil
			cases = append(cases, p)
		}
		for _, pair := range []struct {
			key   string
			value any
		}{{"riskProfile", "reckless"}, {"style", "hold"}, {"customPrompt", strings.Repeat("界", 601)}, {"focusSymbols", []string{"SPY"}}, {"focusSymbols", strings.Split(strings.Repeat("BTC,", 11), ",")}, {"revision", -1}, {"revision", 0.5}, {"enabled", "yes"}} {
			p := profilePreferences(0)
			p[pair.key] = pair.value
			cases = append(cases, p)
		}
		for i, body := range cases {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				child := *c
				child.t = t
				child.call("PUT", "/api/me/profile", body, 400)
			})
		}
		p := profileObject(t, c.call("GET", "/api/me/profile", nil, 200), "profile")
		if p["revision"] != float64(0) {
			t.Fatalf("invalid input wrote profile: %v", p)
		}
	})
	t.Run("durable owned receipt survives deletions", func(t *testing.T) {
		s := boot(t, options{})
		c := s.signIn(t)
		foreign := s.signIn(t)
		chat, id := persistedMemoryCard(t, s, c.id, "I prefer 4h charts", "assistant")
		body := map[string]string{"proposalId": id, "text": "I prefer 4h charts"}
		foreign.call("POST", "/api/me/profile/memories", body, 404)
		c.call("POST", "/api/me/profile/memories", map[string]string{"proposalId": id, "text": "Changed text"}, 400)
		_, userCard := persistedMemoryCard(t, s, c.id, "Untrusted user card", "user")
		c.call("POST", "/api/me/profile/memories", map[string]string{"proposalId": userCard, "text": "Untrusted user card"}, 404)
		nonMemoryChat, nonMemoryID := persistedMemoryCard(t, s, c.id, "Not a memory", "assistant")
		if _, err := s.db.Exec(t.Context(), `update chat_messages set cards=jsonb_set(cards,'{0,type}','"custom"') where chat_id=$1`, nonMemoryChat); err != nil {
			t.Fatal(err)
		}
		c.call("POST", "/api/me/profile/memories", map[string]string{"proposalId": nonMemoryID, "text": "Not a memory"}, 404)
		for _, invalid := range []any{`null`, `[]`, `{"proposalId":null,"text":"x"}`, map[string]any{"proposalId": id, "text": body["text"], "userId": c.id}, map[string]string{"proposalId": id, "text": strings.Repeat("界", 201)}} {
			c.call("POST", "/api/me/profile/memories", invalid, 400)
		}
		c.call("POST", "/api/me/profile/memories", map[string]string{"proposalId": someID, "text": "Fabricated"}, 404)
		confirmed := profileObject(t, c.call("POST", "/api/me/profile/memories", body, 201), "")
		memory := profileObject(t, confirmed, "memory")
		mid := memory["id"].(string)
		p := profileObject(t, confirmed, "profile")
		if p["revision"] != float64(1) || len(p["memories"].([]any)) != 1 {
			t.Fatalf("confirm: %v", confirmed)
		}
		c.call("POST", "/api/me/profile/memories", body, 409)
		foreign.call("DELETE", "/api/me/profile/memories/"+mid, map[string]any{}, 404)
		beforeDelete := c.call("GET", "/api/chats/"+chat, nil, 200)
		if !strings.Contains(fmt.Sprint(beforeDelete), "confirmed") || !strings.Contains(fmt.Sprint(beforeDelete), mid) {
			t.Fatalf("confirmed hydration: %v", beforeDelete)
		}
		prefs := profilePreferences(0)
		c.call("PUT", "/api/me/profile", prefs, 409)
		prefs["revision"] = p["revision"]
		prefs["customPrompt"] = "Kept instructions"
		p = profileObject(t, c.call("PUT", "/api/me/profile", prefs, 200), "profile")
		if len(p["memories"].([]any)) != 1 {
			t.Fatalf("PUT erased memory: %v", p)
		}
		p = profileObject(t, c.call("DELETE", "/api/me/profile/memories/"+mid, map[string]any{}, 200), "profile")
		if len(p["memories"].([]any)) != 0 || p["customPrompt"] != "Kept instructions" || p["revision"] != float64(3) {
			t.Fatalf("delete: %v", p)
		}
		c.call("DELETE", "/api/me/profile/memories/"+mid, map[string]any{}, 404)
		history := c.call("GET", "/api/chats/"+chat, nil, 200)
		if !strings.Contains(fmt.Sprint(history), "deleted") {
			t.Fatalf("deleted hydration: %v", history)
		}
		c.call("POST", "/api/me/profile/memories", body, 409)
		c.call("DELETE", "/api/chats/"+chat, map[string]any{}, 204)
		c.call("POST", "/api/me/profile/memories", body, 409)
		c.call("PUT", "/api/me/profile", prefs, 409)
		var receipts int
		if err := s.db.QueryRow(context.Background(), "select count(*) from profile_memory_receipts where user_id=$1 and proposal_id=$2 and deleted_at is not null", c.id, id).Scan(&receipts); err != nil || receipts != 1 {
			t.Fatalf("durable receipt: %d %v", receipts, err)
		}
	})
	t.Run("concurrent cap and duplicates", func(t *testing.T) {
		s := boot(t, options{})
		c := s.signIn(t)
		confirm := func(text string) map[string]string {
			_, id := persistedMemoryCard(t, s, c.id, text, "assistant")
			return map[string]string{"proposalId": id, "text": text}
		}
		for i := range 19 {
			c.call("POST", "/api/me/profile/memories", confirm(fmt.Sprint("memory", i)), 201)
		}
		bodies := []map[string]string{confirm("twentieth one"), confirm("twentieth two")}
		statuses := make(chan int, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, body := range bodies {
			req := c.request("POST", "/api/me/profile/memories", body)
			wg.Go(func() {
				<-start
				resp, err := c.http.Do(req)
				if err != nil {
					statuses <- 0
					return
				}
				resp.Body.Close()
				statuses <- resp.StatusCode
			})
		}
		close(start)
		wg.Wait()
		close(statuses)
		sum := 0
		for status := range statuses {
			sum += status
		}
		if sum != 201+409 {
			t.Fatalf("cap statuses sum %d", sum)
		}
		p := profileObject(t, c.call("GET", "/api/me/profile", nil, 200), "profile")
		if len(p["memories"].([]any)) != 20 || p["revision"] != float64(20) {
			t.Fatalf("concurrent cap: %v", p)
		}
		var receipts int
		if err := s.db.QueryRow(context.Background(), "select count(*) from profile_memory_receipts where user_id=$1", c.id).Scan(&receipts); err != nil || receipts != 20 {
			t.Fatalf("cap receipts %d %v", receipts, err)
		}
	})
}
