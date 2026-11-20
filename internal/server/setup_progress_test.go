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
	defer func(l func() float64, m func() (uint64, uint64), c func() int) {
		readLoad1, readMemory, numCPU = l, m, c
	}(readLoad1, readMemory, numCPU)
	numCPU = func() int { return 4 }
	load, avail := 1.0, uint64(2<<30)
	readLoad1 = func() float64 { return load }
	readMemory = func() (uint64, uint64) { return avail, 4 << 30 }
	if why := machineBusy(); why != "" {
		t.Fatalf("calm machine busy: %s", why)
	}
	load = 6.3
	if why := machineBusy(); why == "" {
		t.Fatal("load 6.3 on 4 cores not busy")
	}
	load, avail = 1, 200<<20
	if why := machineBusy(); why == "" {
		t.Fatal("200 MB free not busy")
	}
	if !smallMachine() {
		t.Fatal("4 cores, 4 GB is a small machine")
	}
}
