package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"tradingagents/internal/state"
	"tradingagents/pkg/model"
)

type Checkpoint struct {
	State    state.State
	NextNode string
	Step     int
}
type CheckpointStore interface {
	Load(context.Context, string) (*Checkpoint, error)
	Save(context.Context, string, Checkpoint) error
	Delete(context.Context, string) error
}
type SQLiteStore struct{ db *sql.DB }

func OpenSQLite(ctx context.Context, path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS runtime_checkpoints (thread_id TEXT PRIMARY KEY, version INTEGER NOT NULL, state TEXT NOT NULL, next_node TEXT NOT NULL, step INTEGER NOT NULL)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db}, nil
}
func (s *SQLiteStore) Close() error { return s.db.Close() }
func (s *SQLiteStore) Load(ctx context.Context, id string) (*Checkpoint, error) {
	var c Checkpoint
	var version int
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT version,state,next_node,step FROM runtime_checkpoints WHERE thread_id=?", id).Scan(&version, &data, &c.NextNode, &c.Step)
	if err == sql.ErrNoRows {
		var n int
		err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='checkpoints'").Scan(&n)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM checkpoints WHERE thread_id=?", id).Scan(&n)
			if err != nil {
				return nil, err
			}
			if n > 0 {
				return nil, fmt.Errorf("this run has a legacy framework checkpoint; resume with the previous version or clear checkpoints")
			}
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, fmt.Errorf("unsupported checkpoint version: %d", version)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var wrappers []json.RawMessage
	if err = json.Unmarshal(raw["messages"], &wrappers); err != nil {
		return nil, err
	}
	msgs := make([]model.Message, 0, len(wrappers))
	for _, w := range wrappers {
		var wrapped struct {
			Kind string          `json:"__ta_message__"`
			Data json.RawMessage `json:"data"`
		}
		if err = json.Unmarshal(w, &wrapped); err != nil {
			return nil, err
		}
		if wrapped.Kind != "" {
			w = wrapped.Data
		}
		var m model.Message
		if err = json.Unmarshal(w, &m); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	raw["messages"], err = json.Marshal(msgs)
	if err != nil {
		return nil, err
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &c.State)
	return &c, err
}
func (s *SQLiteStore) Save(ctx context.Context, id string, c Checkpoint) error {
	data, err := json.Marshal(c.State)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type wrapper struct {
		Kind string        `json:"__ta_message__"`
		Data model.Message `json:"data"`
	}
	msgs := make([]wrapper, 0, len(c.State.Messages))
	for _, m := range c.State.Messages {
		msgs = append(msgs, wrapper{m.Type, m})
	}
	raw["messages"], err = json.Marshal(msgs)
	if err != nil {
		return err
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT OR REPLACE INTO runtime_checkpoints VALUES (?,1,?,?,?)", id, string(data), c.NextNode, c.Step)
	return err
}
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM runtime_checkpoints WHERE thread_id=?", id)
	return err
}
