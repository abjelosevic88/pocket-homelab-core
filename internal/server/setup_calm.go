package server

// A small machine (a Raspberry Pi) struggles when the next app's download, unpacking and first
// start pile onto the last one's. Between the setup's installs Core waits until the machine has
// caught up: the processor's load and the free memory, as the host's /proc shows them (load and
// meminfo are not per container). Slower, but nothing gets starved. It never waits forever.

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The machine's numbers; variables so tests can stand in for /proc.
var (
	readLoad1  = procLoad1
	readMemory = procMemory
	numCPU     = runtime.NumCPU
)

const (
	calmCheck   = 15 * time.Second // how often a waiting queue looks again
	calmMaxWait = 10 * time.Minute // then the next app goes anyway
)

func procLoad1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// procMemory is MemAvailable and MemTotal in bytes (0, 0 when unknown).
func procMemory() (avail, total uint64) {
	fh, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemAvailable:":
			avail = kb << 10
		case "MemTotal:":
			total = kb << 10
		}
	}
	return avail, total
}

// smallMachine: four cores or fewer, or 4 GB of memory or less (a Raspberry Pi, a mini PC).
func smallMachine() bool {
	_, total := readMemory()
	return numCPU() <= 4 || total > 0 && total <= 4<<30
}

// machineBusy says why the next install should wait, or "" when the machine has room.
func machineBusy() string {
	cpus := numCPU()
	if l := readLoad1(); l > float64(cpus)*1.2 {
		return fmt.Sprintf("the processor is busy (load %.1f on %d cores)", l, cpus)
	}
	if avail, total := readMemory(); total > 0 && (avail < 400<<20 || avail*100/total < 10) {
		return fmt.Sprintf("memory is short (%d MB free of %.1f GB)", avail>>20, float64(total)/(1<<30))
	}
	return ""
}

// waitCalm holds the queue before its next app: a breather on a small machine, then until the
// machine isn't busy (at most calmMaxWait). The reason shows in the progress chip. False = Core stops.
func (s *Server) waitCalm(first bool) bool {
	q := &s.setup
	defer q.setPause("")
	start := time.Now()
	if !first && smallMachine() {
		q.setPause("giving the machine a moment after the last app")
		if !s.sleep(30 * time.Second) {
			return false
		}
	}
	for time.Since(start) < calmMaxWait {
		why := machineBusy()
		if why == "" {
			return true
		}
		q.setPause(why)
		if !s.sleep(calmCheck) {
			return false
		}
	}
	return true
}

func (s *Server) sleep(d time.Duration) bool {
	select {
	case <-s.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (q *setupQueue) setPause(why string) {
	q.mu.Lock()
	q.pause = why
	q.mu.Unlock()
}
