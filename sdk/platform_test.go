package sdk

import (
	"context"
	"database/sql"
	"encoding/json"
	_ "modernc.org/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func platformDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{`PRAGMA foreign_keys=ON`, `CREATE TABLE users(id INTEGER PRIMARY KEY)`, `CREATE TABLE sessions(id TEXT PRIMARY KEY,user_id INTEGER)`, `INSERT INTO users VALUES(1)`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestIdentityBindingDoesNotFollowEmailReuse(t *testing.T) {
	c := New("", Options{DB: platformDB(t)})
	ctx := t.Context()
	id, err := c.ResolveIdentity(ctx, "issuer", "subject-1", func() (int64, error) { return 1, nil })
	if err != nil || id != 1 {
		t.Fatal(id, err)
	}
	id, err = c.ResolveIdentity(ctx, "issuer", "subject-1", func() (int64, error) { t.Fatal("existing subject must not be rematched by email"); return 2, nil })
	if err != nil || id != 1 {
		t.Fatal(id, err)
	}
	if _, err = c.ResolveIdentity(ctx, "issuer", "subject-2", func() (int64, error) { return 1, nil }); err == nil {
		t.Fatal("recycled email claimed an already linked account")
	}
}

func TestIntrospectionRevocationAndExpiry(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"active": active.Load(), "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	}))
	defer srv.Close()
	c := New(srv.URL, Options{Token: "app"})
	if v, err := c.Introspect(t.Context(), "user"); err != nil || !v.Active {
		t.Fatal(v, err)
	}
	active.Store(false)
	if v, err := c.Introspect(t.Context(), "user"); err != nil || v.Active || calls.Load() != 2 {
		t.Fatal("cached revoked token", v, err)
	}
	expired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"active": true, "expires_at": time.Now().Add(-time.Second).Format(time.RFC3339Nano)})
	}))
	defer expired.Close()
	c = New(expired.URL, Options{Token: "app", IntrospectCache: time.Minute})
	if v, err := c.Introspect(t.Context(), "user"); err != nil || v.Active {
		t.Fatal("accepted expired token", v, err)
	}
}

func TestRevokedAppDoesNotReenroll(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer revoked" {
			t.Error("used enrollment key after revocation")
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := New(srv.URL, Options{Token: "revoked"})
	if _, err := c.Register(t.Context(), "master-key", Manifest{}); err == nil || calls.Load() != 1 {
		t.Fatal("reenrollment attempted", err, calls.Load())
	}
}

func TestOutboxSurvivesRestartAndTransactionRollback(t *testing.T) {
	db := platformDB(t)
	ctx := t.Context()
	var fail atomic.Bool
	fail.Store(true)
	var calls atomic.Int64
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if key == "" {
			key = r.Header.Get("Idempotency-Key")
		} else if key != r.Header.Get("Idempotency-Key") {
			t.Error("retry key changed")
		}
		if fail.Load() {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(201)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, Options{DB: db, Token: "app"})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = QueueEvent(ctx, tx, "test.changed", "1", nil); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if st, err := c.OutboxStatus(ctx); err != nil || st.Pending != 0 {
		t.Fatal("rolled back domain left an event", st, err)
	}
	if err = c.Publish(ctx, "test.changed", "1", map[string]int{"value": 1}); err != nil {
		t.Fatal(err)
	}
	c.flushOutbox(ctx)
	if st, err := c.OutboxStatus(ctx); err != nil || st.Pending != 1 || st.Retrying != 1 {
		t.Fatal("failed publication was lost", st, err)
	}
	c = New(srv.URL, Options{DB: db, Token: "app"}) // process restart uses the same database
	fail.Store(false)
	db.Exec(`UPDATE pocket_outbox SET next_at=0`)
	c.flushOutbox(ctx)
	if st, err := c.OutboxStatus(ctx); err != nil || st.Pending != 0 || calls.Load() != 2 || key == "" {
		t.Fatal(st, err, calls.Load())
	}
}

func TestCoreSessionRevocationFailsClosed(t *testing.T) {
	db := platformDB(t)
	ctx := context.Background()
	var active atomic.Bool
	active.Store(true)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"active": active.Load(), "iss": srv.URL})
	}))
	defer srv.Close()
	c := New(srv.URL, Options{DB: db, Token: "app"})
	db.Exec(`INSERT INTO sessions VALUES(?,1)`, sessionHash("session"))
	if err := c.TrackSession(ctx, "session", srv.URL, "subject", 1); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateSession(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	active.Store(false)
	if err := c.ValidateSession(ctx, "session"); err == nil {
		t.Fatal("revoked session accepted")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Fatal("revoked local session retained")
	}
}
