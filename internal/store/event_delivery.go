package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// InsertEventDeliveries commits the event and all its delivery work together.
// A publisher retry with the same source/key returns the original event.
func (s *Store) InsertEventDeliveries(ctx context.Context, e *Event, hooks []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	}
	canonical, err := json.Marshal([]any{e.Topic, e.Subject, json.RawMessage(e.Payload)})
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(canonical))
	if e.PublishKey != "" {
		var saved string
		err := tx.QueryRowContext(ctx, `SELECT request_hash,event_id,created_at FROM event_receipts WHERE source=? AND publish_key=?`, e.Source, e.PublishKey).Scan(&saved, &e.ID, &e.CreatedAt)
		if err == nil {
			if saved != hash {
				return ErrConflict
			}
			return tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	e.CreatedAt = Now()
	if err := tx.QueryRowContext(ctx, `UPDATE event_sequence SET value = MAX(value, (SELECT COALESCE(MAX(id),0) FROM events)) + 1 WHERE singleton = 1 RETURNING value`).Scan(&e.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events (id, topic, source, subject, payload, created_at, publish_key) VALUES (?, ?, ?, ?, ?, ?, ?)`, e.ID, e.Topic, e.Source, e.Subject, string(e.Payload), e.CreatedAt, e.PublishKey); err != nil {
		return err
	}
	if e.PublishKey != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_receipts(source,publish_key,request_hash,event_id,created_at) VALUES(?,?,?,?,?)`, e.Source, e.PublishKey, hash, e.ID, e.CreatedAt); err != nil {
			return err
		}
	}
	for _, id := range hooks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries (webhook_id, event_id, next_at) VALUES (?, ?, ?)`, id, e.ID, e.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LatestEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM event_sequence WHERE singleton = 1`).Scan(&id)
	return id, err
}
