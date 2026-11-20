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

// A busy or short-of-memory machine holds the next install; a calm one doesn't.
func TestMachineBusy(t *testing.T) {
	defer func(l func() (float64, float64), m func() (uint64, uint64), c func() int) {
		readCPU, readMemory, numCPU = l, m, c
	}(readCPU, readMemory, numCPU)
	numCPU = func() int { return 4 }
	load, wait, avail := 38.0, 2.0, uint64(2<<30)
	readCPU = func() (float64, float64) { return load, wait }
	readMemory = func() (uint64, uint64) { return avail, 4 << 30 }
	if why := machineBusy(); why != "" {
		t.Fatalf("calm machine busy: %s", why)
	}
	load = 95
	if why := machineBusy(); why == "" {
		t.Fatal("95% in use not busy")
	}
	load, wait = 30, 70
	if why := machineBusy(); why == "" {
		t.Fatal("70% waiting on the disk not busy")
	}
	load, wait, avail = 30, 2, 200<<20
	if why := machineBusy(); why == "" {
		t.Fatal("200 MB free not busy")
	}
	if _, w := procCPU(); w < -1 {
		t.Fatal("procCPU")
	}
	if b, _ := procCPU(); b < -1 || b > 100 {
		t.Fatalf("procCPU busy %v", b)
	}
	if !smallMachine() {
		t.Fatal("4 cores, 4 GB is a small machine")
	}
}

// The queue picks an interrupted install first, then the first one waiting (apps tried again too).
func TestSetupQueueNext(t *testing.T) {
	a := &setupItem{Slug: "a", State: "done"}
	b := &setupItem{Slug: "b", State: "failed"}
	c := &setupItem{Slug: "c", State: "waiting"}
	q := &setupQueue{items: []*setupItem{a, b, c}}
	if it, _ := q.next(); it != c {
		t.Fatalf("next = %v, want c", it)
	}
	b.State = "waiting" // tried again
	if it, _ := q.next(); it != b {
		t.Fatalf("next = %v, want b", it)
	}
	c.State = "installing"
	if it, st := q.next(); it != c || st != "installing" {
		t.Fatalf("next = %v %s, want the interrupted c", it, st)
	}
	b.State, c.State = "done", "done"
	if it, _ := q.next(); it != nil {
		t.Fatalf("next = %v, want nil", it)
	}
}
