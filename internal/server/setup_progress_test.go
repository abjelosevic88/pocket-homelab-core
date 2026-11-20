package server

import (
	"testing"
	"time"
)

// The progress chip guesses the time left from when each app began and ended.
func TestSetupQueueTimes(t *testing.T) {
	it := &setupItem{Slug: "notes", State: "waiting"}
	q := &setupQueue{items: []*setupItem{it}, started: time.Now().UTC(), running: true}
	q.set(it, "installing", "", "")
	if it.Began == nil || it.Ended != nil {
		t.Fatalf("installing: began %v ended %v", it.Began, it.Ended)
	}
	began := *it.Began
	q.set(it, "installing", "", "job1") // the job id arrives: the start stays
	if !it.Began.Equal(began) || it.Job != "job1" {
		t.Fatalf("second installing moved the start or lost the job: %v %q", it.Began, it.Job)
	}
	q.set(it, "done", "", "")
	if it.Ended == nil || it.Ended.Before(began) {
		t.Fatalf("done: ended %v", it.Ended)
	}
	snap := q.snapshot()
	if _, ok := snap["started_at"]; !ok {
		t.Fatal("snapshot has no started_at")
	}
	if items := snap["items"].([]setupItem); items[0].Ended == nil {
		t.Fatal("snapshot lost ended_at")
	}
}
