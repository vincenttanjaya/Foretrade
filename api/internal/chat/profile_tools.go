package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"uuid"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
	"github.com/vincenttanjaya/Foretrade/api/internal/profile"
)

func (a *agentRun) memoryTool() llm.Tool {
	return llm.Tool{Name: "remember", Description: "Suggest a concise memory for the user to confirm manually. This shows a card only; it never saves a memory, grants permission, executes or costs extra credits.", Schema: schema(map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}}, "text"), Run: a.remember}
}
func (a *agentRun) remember(_ context.Context, input json.RawMessage) (string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil || obj == nil || len(obj) != 1 {
		return "", fmt.Errorf("remember requires only text")
	}
	var text string
	if err := json.Unmarshal(obj["text"], &text); err != nil {
		return "", fmt.Errorf("remember text must be a string")
	}
	if err := profile.ValidateMemory(text); err != nil {
		return "", err
	}
	proposal := profile.MemoryProposal{ProposalID: uuid.NewV4().String(), Text: text, Status: "open"}
	a.card = &Card{Type: "memory", Data: proposal}
	a.summary = "Suggesting a memory"
	return toJSON(map[string]any{"card": proposal, "status": "Proposal shown, not saved. Only the user's explicit Confirm after this answer is durably saved can store it."})
}

func profileRules(system string, p profile.Profile) string {
	cap := fmt.Sprintf("%.0f%%", profile.SizeCapPct(p)*100)
	return strings.NewReplacer("10% of equity", cap+" of equity", "10% AI proposal cap", cap+" AI proposal cap").Replace(system) + "\n- remember proposes a memory card only. Never claim it is saved or treat memory as permission; the user must explicitly Confirm after the assistant answer is durably saved."
}

func (h handler) memoryStatus(ctx context.Context, user string, msgs []message) error {
	parsed := make([][]json.RawMessage, len(msgs))
	ids := []string{}
	for i, m := range msgs {
		if json.Unmarshal(m.Cards, &parsed[i]) != nil {
			parsed[i] = nil
			continue
		}
		for _, card := range parsed[i] {
			if id := memoryProposalID(card); id != "" {
				ids = append(ids, id)
			}
		}
	}
	receipts, err := profile.Receipts(ctx, h.d.DB, user, ids)
	if err != nil {
		return err
	}
	for i, cards := range parsed {
		if cards == nil {
			continue
		}
		changed := false
		for j, card := range cards {
			if id := memoryProposalID(card); id != "" {
				var envelope map[string]json.RawMessage
				var data map[string]json.RawMessage
				if err = json.Unmarshal(card, &envelope); err != nil {
					return err
				}
				if err = json.Unmarshal(envelope["data"], &data); err != nil {
					return err
				}
				if receipt, ok := receipts[id]; ok {
					data["memoryId"], _ = json.Marshal(receipt.MemoryID)
					data["status"] = json.RawMessage(`"confirmed"`)
					if receipt.Deleted {
						data["status"] = json.RawMessage(`"deleted"`)
					}
				} else {
					data["status"] = json.RawMessage(`"open"`)
					delete(data, "memoryId")
				}
				envelope["data"], err = json.Marshal(data)
				if err != nil {
					return err
				}
				cards[j], err = json.Marshal(envelope)
				if err != nil {
					return err
				}
				changed = true
			}
		}
		if !changed {
			continue
		}
		msgs[i].Cards, err = json.Marshal(cards)
		if err != nil {
			return err
		}
	}
	return nil
}
func memoryProposalID(raw json.RawMessage) string {
	var card struct {
		Type string
		Data struct{ ProposalID string }
	}
	if json.Unmarshal(raw, &card) != nil || card.Type != "memory" || !isUUID(card.Data.ProposalID) {
		return ""
	}
	return card.Data.ProposalID
}
