package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsureTx initializes once in an existing owner transaction. The no-op
// conflict preserves stored preferences, memories, revision and timestamps.
func EnsureTx(ctx context.Context, tx pgx.Tx, user string, enabled bool) (Profile, error) {
	if err := ensureRow(ctx, tx, user, enabled); err != nil {
		return Profile{}, err
	}
	return load(ctx, tx, user, false)
}

func ensureRow(ctx context.Context, tx pgx.Tx, user string, enabled bool) error {
	prefs, err := json.Marshal(Default(enabled))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "insert into user_profiles(user_id,preferences) values($1,$2) on conflict(user_id) do nothing", user, prefs)
	return err
}

func Ensure(ctx context.Context, db *pgxpool.Pool, user string, enabled bool) (Profile, error) {
	var p Profile
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { var err error; p, err = EnsureTx(ctx, tx, user, enabled); return err })
	return p, err
}

func Load(ctx context.Context, db *pgxpool.Pool, user string) (Profile, error) {
	return Ensure(ctx, db, user, true)
}

func load(ctx context.Context, tx pgx.Tx, user string, lock bool) (Profile, error) {
	query := "select preferences,memories,revision,updated_at from user_profiles where user_id=$1"
	if lock {
		query += " for update"
	}
	var p Profile
	var prefs, memories []byte
	err := tx.QueryRow(ctx, query, user).Scan(&prefs, &memories, &p.Revision, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(prefs, &p.Preferences); err != nil {
		return p, err
	}
	if err = json.Unmarshal(memories, &p.Memories); err != nil {
		return p, err
	}
	if p.FocusSymbols == nil {
		p.FocusSymbols = []string{}
	}
	if p.Memories == nil {
		p.Memories = []Memory{}
	}
	return p, nil
}

func ownedTx(ctx context.Context, db *pgxpool.Pool, user string, change func(pgx.Tx, *Profile) error) (Profile, error) {
	var p Profile
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := ensureRow(ctx, tx, user, true); err != nil {
			return err
		}
		var err error
		p, err = load(ctx, tx, user, true)
		if err != nil {
			return err
		}
		if err = change(tx, &p); err != nil {
			return err
		}
		prefs, err := json.Marshal(p.Preferences)
		if err != nil {
			return err
		}
		memories, err := json.Marshal(p.Memories)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, "update user_profiles set preferences=$2,memories=$3,revision=revision+1,updated_at=clock_timestamp() where user_id=$1 returning revision,updated_at", user, prefs, memories).Scan(&p.Revision, &p.UpdatedAt)
	})
	return p, err
}

func Update(ctx context.Context, db *pgxpool.Pool, user string, prefs Preferences, revision int64) (Profile, error) {
	valid, err := Validate(prefs)
	if err != nil {
		return Profile{}, err
	}
	return ownedTx(ctx, db, user, func(_ pgx.Tx, p *Profile) error {
		if p.Revision != revision {
			return ErrConflict
		}
		p.Preferences = valid
		return nil
	})
}

func Confirm(ctx context.Context, db *pgxpool.Pool, user, id, text string) (Memory, Profile, error) {
	var memory Memory
	p, err := ownedTx(ctx, db, user, func(tx pgx.Tx, p *Profile) error {
		var exists bool
		if err := tx.QueryRow(ctx, "select exists(select 1 from profile_memory_receipts where user_id=$1 and proposal_id=$2)", user, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrConflict
		}
		binding, _ := json.Marshal([]any{map[string]any{"type": "memory", "data": map[string]string{"proposalId": id}}})
		var cards []byte
		err := tx.QueryRow(ctx, `select m.cards from chat_messages m join chats c on c.id=m.chat_id
			where c.user_id=$1 and m.role='assistant' and m.cards @> $2::jsonb
			limit 1 for share of c,m`, user, binding).Scan(&cards)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var parsed []struct {
			Type string         `json:"type"`
			Data MemoryProposal `json:"data"`
		}
		if err = json.Unmarshal(cards, &parsed); err != nil {
			return err
		}
		matched := false
		for _, card := range parsed {
			if card.Type == "memory" && card.Data.ProposalID == id && card.Data.Status == "open" {
				if card.Data.Text != text {
					return &BadRequest{"Memory text must exactly match its saved proposal."}
				}
				matched = true
				break
			}
		}
		if !matched {
			return ErrNotFound
		}
		if err = ValidateMemory(text); err != nil {
			return err
		}
		if len(p.Memories) >= 20 {
			return ErrConflict
		}
		memory.Text = text
		if err = tx.QueryRow(ctx, `insert into profile_memory_receipts(user_id,proposal_id,memory_id)
			values($1,$2,gen_random_uuid()) returning memory_id,confirmed_at`, user, id).Scan(&memory.ID, &memory.At); err != nil {
			return err
		}
		p.Memories = append(p.Memories, memory)
		return nil
	})
	return memory, p, err
}

func Delete(ctx context.Context, db *pgxpool.Pool, user, id string) (Profile, error) {
	return ownedTx(ctx, db, user, func(tx pgx.Tx, p *Profile) error {
		found := -1
		for i, m := range p.Memories {
			if m.ID == id {
				found = i
				break
			}
		}
		if found < 0 {
			return ErrNotFound
		}
		p.Memories = append(p.Memories[:found:found], p.Memories[found+1:]...)
		tag, err := tx.Exec(ctx, "update profile_memory_receipts set deleted_at=clock_timestamp() where user_id=$1 and memory_id=$2 and deleted_at is null", user, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("profile memory receipt missing")
		}
		return nil
	})
}

type Receipt struct {
	MemoryID string
	Deleted  bool
}

func Receipts(ctx context.Context, db *pgxpool.Pool, user string, ids []string) (map[string]Receipt, error) {
	result := map[string]Receipt{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := db.Query(ctx, "select proposal_id,memory_id,deleted_at is not null from profile_memory_receipts where user_id=$1 and proposal_id=any($2::uuid[])", user, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r Receipt
		if err = rows.Scan(&id, &r.MemoryID, &r.Deleted); err != nil {
			return nil, err
		}
		result[id] = r
	}
	return result, rows.Err()
}
