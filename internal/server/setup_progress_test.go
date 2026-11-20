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

// Swap used up and little memory to be had = out of memory (a Pi with 80 apps showed 850 MB
// "available" with its swap full and a load of 90); plenty of memory with a full swap is not.
func TestOutOfMemory(t *testing.T) {
	defer func(m func() (uint64, uint64), sw func() (uint64, uint64), c func() (float64, float64)) {
		readMemory, readSwap, readCPU = m, sw, c
	}(readMemory, readSwap, readCPU)
	readCPU = func() (float64, float64) { return 15, 5 }
	avail, swapFree := uint64(852<<20), uint64(0)
	readMemory = func() (uint64, uint64) { return avail, 3789 << 20 }
	readSwap = func() (uint64, uint64) { return swapFree, 2047 << 20 }
	var p pauseInfo
	if machineNow(&p) != "memory" || !p.Full || p.SwapUsed != 100 {
		t.Fatalf("the Pi's numbers: kind %q full %v swap %d", p.Kind, p.Full, p.SwapUsed)
	}
	avail = 2500 << 20 // swap full of idle pages, memory to spare: fine
	if machineNow(&p) != "" || p.Full {
		t.Fatalf("plenty of memory: kind %q full %v", p.Kind, p.Full)
	}
	avail, swapFree = 300<<20, 1500<<20 // short for now, not out: a pause, not a stop
	if machineNow(&p) != "memory" || p.Full {
		t.Fatalf("short of memory: kind %q full %v", p.Kind, p.Full)
	}
	readSwap = func() (uint64, uint64) { return 0, 0 } // no swap at all
	if machineNow(&p); p.SwapUsed != -1 || p.Full {
		t.Fatalf("no swap: used %d full %v", p.SwapUsed, p.Full)
	}
}

// The apps still waiting are marked not installed when the queue stops for memory.
func TestFailWaiting(t *testing.T) {
	a := &setupItem{Slug: "a", State: "done"}
	b := &setupItem{Slug: "b", State: "waiting"}
	c := &setupItem{Slug: "c", State: "waiting"}
	q := &setupQueue{items: []*setupItem{a, b, c}}
	if n := q.failWaiting("out of memory"); n != 2 || b.State != "failed" || c.Error != "out of memory" || a.State != "done" || b.Ended == nil {
		t.Fatalf("n %d, b %+v, c %+v", n, b, c)
	}
	if it, _ := q.next(); it != nil {
		t.Fatalf("nothing should be left to install, got %v", it)
	}
}
