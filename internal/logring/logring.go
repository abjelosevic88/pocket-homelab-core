// Package logring keeps the last lines Core wrote to its log, so the admin can read them from the
// app page (GET /api/v1/apps/core/logs) without a shell on the machine.
package logring

import (
	"bytes"
	"sync"
)

// Ring is an io.Writer that keeps the last Size complete lines written to it.
type Ring struct {
	mu      sync.Mutex
	lines   []string
	next    int
	full    bool
	partial []byte
}

// Size is how many lines Default keeps.
const Size = 2000

// Default is Core's own log (cmd/pocket-core tees its log output into it).
var Default = New(Size)

func New(size int) *Ring { return &Ring{lines: make([]string, size)} }

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := p
	if len(r.partial) > 0 {
		data = append(r.partial, p...)
		r.partial = nil
	}
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := string(data[:i])
		if len(line) > 8192 {
			line = line[:8192] + "…"
		}
		r.lines[r.next] = line
		r.next = (r.next + 1) % len(r.lines)
		r.full = r.full || r.next == 0
		data = data[i+1:]
	}
	if len(data) > 0 && len(data) < 8192 {
		r.partial = append([]byte(nil), data...)
	}
	return len(p), nil
}

// Last returns up to n lines, oldest first.
func (r *Ring) Last(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []string
	if r.full {
		all = append(append(all, r.lines[r.next:]...), r.lines[:r.next]...)
	} else {
		all = append(all, r.lines[:r.next]...)
	}
	if n < len(all) {
		all = all[len(all)-n:]
	}
	return all
}
