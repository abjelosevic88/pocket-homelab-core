package sdk

import (
	"database/sql"
	"errors"
	"testing"
)

func TestActionsSurviveRollbackAndRespectPersonOrder(t *testing.T) {
	db := platformDB(t)
	New("", Options{DB: db})
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = QueueAction(ctx, tx, 1, "test", map[string]int{"delta": 1}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	actions, err := PendingActions(ctx, db, 0)
	if err != nil || len(actions) != 0 {
		t.Fatal(actions, err)
	}
	ids := []string{}
	for _, user := range []int64{1, 1, 2} {
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		id, e := QueueAction(ctx, tx, user, "test", nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
	}
	actions, err = PendingActions(ctx, db, 0)
	if err != nil || len(actions) != 2 || actions[0].ID != ids[0] || actions[1].ID != ids[2] {
		t.Fatal(actions, err)
	}
	first := actions[0]
	if err = RetryAction(ctx, db, first, errors.New("acknowledgement lost")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pocket_actions SET next_at=9999999999 WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	actions, err = PendingActions(ctx, db, 0)
	if err != nil || len(actions) != 1 || actions[0].UserID != 2 {
		t.Fatal(actions, err)
	}
	failure := errors.New("projection unavailable")
	if err = FinishAction(ctx, db, first, func(tx *sql.Tx) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var done int
	db.QueryRow(`SELECT done_at FROM pocket_actions WHERE id=?`, first.ID).Scan(&done)
	if done != 0 {
		t.Fatal("completion survived projection failure")
	}
	if err = FinishAction(ctx, db, first, func(tx *sql.Tx) error { return QueueEvent(ctx, tx, "test.done", first.ID, nil) }); err != nil {
		t.Fatal(err)
	}
	if err = FinishAction(ctx, db, first, func(tx *sql.Tx) error { t.Fatal("duplicate completion"); return nil }); err != nil {
		t.Fatal(err)
	}
	actions, err = PendingActions(ctx, db, 1)
	if err != nil || len(actions) != 1 || actions[0].ID != ids[1] {
		t.Fatal(actions, err)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM pocket_outbox`).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
}
