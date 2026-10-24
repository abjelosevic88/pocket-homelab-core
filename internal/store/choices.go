package store

import "context"

// ListChoices returns a person's app choices, slot → provider.
func (s *Store) ListChoices(ctx context.Context, userID int64) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT slot, provider FROM app_choices WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var slot, provider string
		if err := rows.Scan(&slot, &provider); err != nil {
			return nil, err
		}
		out[slot] = provider
	}
	return out, rows.Err()
}

// SetChoice stores a person's choice for a slot; an empty provider goes back to automatic.
func (s *Store) SetChoice(ctx context.Context, userID int64, slot, provider string) error {
	if provider == "" {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM app_choices WHERE user_id = ? AND slot = ?`, userID, slot)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO app_choices (user_id, slot, provider, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, slot) DO UPDATE SET provider = excluded.provider, updated_at = excluded.updated_at`, userID, slot, provider, Now())
	return err
}
