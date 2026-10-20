package sdk

import (
	"database/sql"
	"errors"
	"testing"
)

func TestMutationReceiptAndOutboxShareCommit(t *testing.T) {
	db := platformDB(t)
	New("", Options{DB: db})
	ctx := t.Context()
	db.Exec(`CREATE TABLE records(id INTEGER PRIMARY KEY,value TEXT)`)
	op, err := NewOperation("person:1:app:inventory", "request-1", map[string]any{"delta": -1})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TRIGGER reject_outbox BEFORE INSERT ON pocket_outbox BEGIN SELECT RAISE(ABORT,'outbox unavailable'); END`)
	tx, _ := db.BeginTx(ctx, nil)
	tx.Exec(`INSERT INTO records VALUES(1,'one')`)
	if err = op.Save(ctx, tx, map[string]int{"id": 1}); err != nil {
		t.Fatal(err)
	}
	err = Commit(tx, func(tx *sql.Tx) error { return QueueEvent(ctx, tx, "inventory.adjusted", "item:1", nil) })
	if err == nil {
		t.Fatal("commit ignored enqueue failure")
	}
	tx.Rollback()
	for _, table := range []string{"records", "pocket_operations", "pocket_outbox"} {
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count)
		if count != 0 {
			t.Fatal(table, count)
		}
	}
	db.Exec(`DROP TRIGGER reject_outbox`)
	tx, _ = db.BeginTx(ctx, nil)
	tx.Exec(`INSERT INTO records VALUES(1,'one')`)
	op.Save(ctx, tx, map[string]int{"id": 1})
	if err = Commit(tx, func(tx *sql.Tx) error { return QueueEvent(ctx, tx, "inventory.adjusted", "item:1", nil) }); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.BeginTx(ctx, nil)
	var result map[string]int
	if ok, err := op.Replay(ctx, tx, &result); err != nil || !ok || result["id"] != 1 {
		t.Fatal(ok, err, result)
	}
	conflict, _ := NewOperation("person:1:app:inventory", "request-1", map[string]any{"delta": -2})
	if _, err := conflict.Replay(ctx, tx, &result); !errors.Is(err, ErrOperationConflict) {
		t.Fatal(err)
	}
	other, _ := NewOperation("person:2:app:inventory", "request-1", map[string]any{"delta": -1})
	if ok, err := other.Replay(ctx, tx, &result); ok || err != nil {
		t.Fatal("receipt crossed identity boundary", ok, err)
	}
	tx.Rollback()
}
