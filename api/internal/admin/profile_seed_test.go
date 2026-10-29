package admin

import (
	"encoding/json"
	"testing"
	"uuid"

	"github.com/vincenttanjaya/Foretrade/api/internal/app/apptest"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
)

func TestShowcaseExcludesProfileAndReceipts(t *testing.T) {
	e := setup(t, apptest.Options{})
	src, email, _ := account(t, e, "user")
	showcase(t, e, src)
	p, err := profile.Load(t.Context(), e.DB, src)
	if err != nil {
		t.Fatal(err)
	}
	prefs := p.Preferences
	prefs.Enabled = false
	prefs.RiskProfile = new("aggressive")
	prefs.CustomPrompt = "PRIVATE SHOWCASE PROFILE"
	if p, err = profile.Update(t.Context(), e.DB, src, prefs, p.Revision); err != nil {
		t.Fatal(err)
	}
	chat := scalar[string](t, e, "insert into chats(user_id,title) values($1,'Private memory') returning id", src)
	id := uuid.NewV4().String()
	text := "PRIVATE SHOWCASE MEMORY"
	cards, _ := json.Marshal([]any{map[string]any{"type": "memory", "data": profile.MemoryProposal{ProposalID: id, Text: text, Status: "open"}}})
	exec(t, e, "insert into chat_messages(chat_id,role,content,cards) values($1,'assistant','Suggestion',$2)", chat, cards)
	if _, _, err = profile.Confirm(t.Context(), e.DB, src, id, text); err != nil {
		t.Fatal(err)
	}
	w := e.Do("POST", "/api/admin/users", map[string]any{"email": "profile-seeded@example.test", "name": "Seeded", "seedFrom": email})
	res := apptest.JSON[seedResp](t, w)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	target, err := profile.Load(t.Context(), e.DB, res.User.ID)
	if err != nil || !target.Enabled || target.CustomPrompt != "" || target.RiskProfile != nil || len(target.Memories) != 0 || target.Revision != 0 {
		t.Fatalf("showcase copied private profile: %+v %v", target, err)
	}
	if n := scalar[int](t, e, "select count(*) from profile_memory_receipts where user_id=$1", res.User.ID); n != 0 {
		t.Fatalf("copied %d receipts", n)
	}
}
