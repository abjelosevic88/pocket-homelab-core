package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Event is one published message.
type Event struct {
	ID        int64           `json:"id"`
	Topic     string          `json:"topic"`
	Source    string          `json:"source"`
	Subject   string          `json:"subject"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// EventFilter narrows ListEvents.
type EventFilter struct {
	Topic   string // exact, or prefix with trailing "*"
	Source  string
	Subject string
	AfterID int64
	Since   string
	Limit   int
}

// InsertEvent persists an event and returns it with id and timestamp.
func (s *Store) InsertEvent(ctx context.Context, e *Event) error {
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	e.CreatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO events (topic, source, subject, payload, created_at) VALUES (?, ?, ?, ?, ?)`, e.Topic, e.Source, e.Subject, string(e.Payload), e.CreatedAt)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

// ListEvents returns events newest first unless AfterID is set (then oldest first, for replay).
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	var where []string
	var args []any
	if f.Topic != "" {
		if strings.HasSuffix(f.Topic, "*") {
			where = append(where, `topic LIKE ?`)
			args = append(args, strings.TrimSuffix(f.Topic, "*")+"%")
		} else {
			where = append(where, `topic = ?`)
			args = append(args, f.Topic)
		}
	}
	if f.Source != "" {
		where = append(where, `source = ?`)
		args = append(args, f.Source)
	}
	if f.Subject != "" {
		where = append(where, `subject = ?`)
		args = append(args, f.Subject)
	}
	if f.AfterID > 0 {
		where = append(where, `id > ?`)
		args = append(args, f.AfterID)
	}
	if f.Since != "" {
		where = append(where, `created_at >= ?`)
		args = append(args, f.Since)
	}
	q := `SELECT id, topic, source, subject, payload, created_at FROM events`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	if f.AfterID > 0 {
		q += ` ORDER BY id ASC`
	} else {
		q += ` ORDER BY id DESC`
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	q += ` LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var p string
		if err := rows.Scan(&e.ID, &e.Topic, &e.Source, &e.Subject, &p, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(p)
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEvent loads one event.
func (s *Store) GetEvent(ctx context.Context, id int64) (*Event, error) {
	var e Event
	var p string
	if err := s.DB.QueryRowContext(ctx, `SELECT id, topic, source, subject, payload, created_at FROM events WHERE id = ?`, id).Scan(&e.ID, &e.Topic, &e.Source, &e.Subject, &p, &e.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	e.Payload = json.RawMessage(p)
	return &e, nil
}

// Topics lists distinct topics with counts.
func (s *Store) Topics(ctx context.Context) ([]struct {
	Topic string `json:"topic"`
	Count int    `json:"count"`
	Last  string `json:"last"`
}, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT topic, COUNT(1), MAX(created_at) FROM events GROUP BY topic ORDER BY topic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []struct {
		Topic string `json:"topic"`
		Count int    `json:"count"`
		Last  string `json:"last"`
	}{}
	for rows.Next() {
		var t struct {
			Topic string `json:"topic"`
			Count int    `json:"count"`
			Last  string `json:"last"`
		}
		if err := rows.Scan(&t.Topic, &t.Count, &t.Last); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SourceCount is how many stored events one source published, and when last.
type SourceCount struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
	Last   string `json:"last"`
}

// EventSources counts the stored events per source.
func (s *Store) EventSources(ctx context.Context) ([]SourceCount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT source, COUNT(1), MAX(created_at) FROM events GROUP BY source ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceCount{}
	for rows.Next() {
		var c SourceCount
		if err := rows.Scan(&c.Source, &c.Count, &c.Last); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PurgeEvents drops events older than the retention window (and their deliveries).
func (s *Store) PurgeEvents(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, Stamp(time.Now().Add(-olderThan)))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---- webhooks ----

// Webhook is a subscription delivered by HTTP POST.
type Webhook struct {
	ID        int64  `json:"id"`
	AppID     int64  `json:"app_id,omitempty"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Topics    string `json:"topics"`
	Secret    string `json:"-"`
	HasSecret bool   `json:"has_secret"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

// Matches reports whether a topic matches the subscription list ("*", "a.b", "a.*").
func (w *Webhook) Matches(topic string) bool { return TopicMatches(w.Topics, topic) }

// TopicMatches evaluates a comma list of patterns against a topic.
func TopicMatches(patterns, topic string) bool {
	for _, p := range strings.Split(patterns, ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
			continue
		case p == "*":
			return true
		case strings.HasSuffix(p, "*"):
			if strings.HasPrefix(topic, strings.TrimSuffix(p, "*")) {
				return true
			}
		case p == topic:
			return true
		}
	}
	return false
}

const webhookCols = `id, app_id, name, url, topics, secret, enabled, created_at`

func (s *Store) scanWebhook(sc interface{ Scan(...any) error }) (*Webhook, error) {
	var w Webhook
	var appID sql.NullInt64
	var en int
	var sec string
	if err := sc.Scan(&w.ID, &appID, &w.Name, &w.URL, &w.Topics, &sec, &en, &w.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	if appID.Valid {
		w.AppID = appID.Int64
	}
	w.Enabled = en == 1
	w.Secret, _ = s.Box.Open(sec)
	w.HasSecret = w.Secret != ""
	return &w, nil
}

// ListWebhooks lists all subscriptions, or one app's.
func (s *Store) ListWebhooks(ctx context.Context, appID int64) ([]Webhook, error) {
	q := `SELECT ` + webhookCols + ` FROM webhooks ORDER BY id`
	args := []any{}
	if appID > 0 {
		q = `SELECT ` + webhookCols + ` FROM webhooks WHERE app_id = ? ORDER BY id`
		args = append(args, appID)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		w, err := s.scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// GetWebhook loads one.
func (s *Store) GetWebhook(ctx context.Context, id int64) (*Webhook, error) {
	return s.scanWebhook(s.DB.QueryRowContext(ctx, `SELECT `+webhookCols+` FROM webhooks WHERE id = ?`, id))
}

// SaveWebhook inserts or updates (upsert on app_id+url so manifests are idempotent).
func (s *Store) SaveWebhook(ctx context.Context, w *Webhook) error {
	var appID any
	if w.AppID > 0 {
		appID = w.AppID
	}
	if w.Topics == "" {
		w.Topics = "*"
	}
	if w.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO webhooks (app_id, name, url, topics, secret, enabled) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(app_id, url) DO UPDATE SET name = excluded.name, topics = excluded.topics, secret = CASE WHEN excluded.secret = '' THEN webhooks.secret ELSE excluded.secret END, enabled = excluded.enabled`,
			appID, w.Name, w.URL, w.Topics, s.Box.Seal(w.Secret), boolInt(w.Enabled))
		if err != nil {
			return conflict(err)
		}
		if id, _ := res.LastInsertId(); id > 0 {
			w.ID = id
		}
		if w.ID == 0 {
			_ = s.DB.QueryRowContext(ctx, `SELECT id FROM webhooks WHERE app_id IS ? AND url = ?`, appID, w.URL).Scan(&w.ID)
		}
	} else {
		_, err := s.DB.ExecContext(ctx, `UPDATE webhooks SET name = ?, url = ?, topics = ?, secret = CASE WHEN ? = '' THEN secret ELSE ? END, enabled = ? WHERE id = ?`,
			w.Name, w.URL, w.Topics, w.Secret, s.Box.Seal(w.Secret), boolInt(w.Enabled), w.ID)
		if err != nil {
			return conflict(err)
		}
	}
	if n, err := s.GetWebhook(ctx, w.ID); err == nil {
		*w = *n
	}
	return nil
}

// DeleteWebhook removes a subscription.
func (s *Store) DeleteWebhook(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

// ---- deliveries ----

// Delivery is one webhook attempt record.
type Delivery struct {
	ID          int64  `json:"id"`
	WebhookID   int64  `json:"webhook_id"`
	EventID     int64  `json:"event_id"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	NextAt      string `json:"next_at"`
	LastCode    int    `json:"last_code"`
	LastError   string `json:"last_error"`
	DeliveredAt string `json:"delivered_at,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// EnqueueDelivery creates a pending delivery.
func (s *Store) EnqueueDelivery(ctx context.Context, webhookID, eventID int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO deliveries (webhook_id, event_id, next_at) VALUES (?, ?, ?)`, webhookID, eventID, Now())
	return err
}

// DueDeliveries returns pending deliveries whose next_at has passed.
func (s *Store) DueDeliveries(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, webhook_id, event_id, status, attempts, next_at, last_code, last_error, delivered_at, created_at FROM deliveries WHERE status = 'pending' AND next_at <= ? ORDER BY id LIMIT ?`, Now(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var del sql.NullString
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.Status, &d.Attempts, &d.NextAt, &d.LastCode, &d.LastError, &del, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.DeliveredAt = nullStr(del)
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDelivery records an attempt.
func (s *Store) MarkDelivery(ctx context.Context, id int64, status string, code int, errMsg string, next time.Time) error {
	var delivered any
	if status == "ok" {
		delivered = Now()
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE deliveries SET status = ?, attempts = attempts + 1, last_code = ?, last_error = ?, next_at = ?, delivered_at = COALESCE(?, delivered_at) WHERE id = ?`,
		status, code, errMsg, Stamp(next), delivered, id)
	return err
}

// ListDeliveries returns recent deliveries for a webhook.
func (s *Store) ListDeliveries(ctx context.Context, webhookID int64, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, webhook_id, event_id, status, attempts, next_at, last_code, last_error, delivered_at, created_at FROM deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT ?`, webhookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var del sql.NullString
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.Status, &d.Attempts, &d.NextAt, &d.LastCode, &d.LastError, &del, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.DeliveredAt = nullStr(del)
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliveryStats counts deliveries by status.
func (s *Store) DeliveryStats(ctx context.Context) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT status, COUNT(1) FROM deliveries GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"pending": 0, "ok": 0, "failed": 0}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
