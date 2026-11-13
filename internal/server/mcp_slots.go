package server

import (
	"context"
	"sync"
	"time"
)

// MCP concurrency: one shared pool for the whole server plus a per-credential
// share, so one busy person (or one runaway agent) cannot starve the others.
const (
	mcpRequestsPerSecond = 10
	mcpRequestBurst      = 60
	mcpTotalSlots        = 16
	mcpClientSlots       = 6
	mcpSlotWait          = 10 * time.Second
)

type mcpSlots struct {
	mu      sync.Mutex
	total   int
	perUser int
	used    int
	byUser  map[string]int
	freed   chan struct{} // closed and replaced whenever a slot is released
}

func newMCPSlots(total, perUser int) *mcpSlots {
	return &mcpSlots{total: total, perUser: perUser, byUser: map[string]int{}, freed: make(chan struct{})}
}

// acquire takes a slot for client, waiting up to wait for one to free up.
func (m *mcpSlots) acquire(ctx context.Context, client string, wait time.Duration) (func(), bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		m.mu.Lock()
		if m.used < m.total && m.byUser[client] < m.perUser {
			m.used++
			m.byUser[client]++
			m.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { m.release(client) }) }, true
		}
		freed := m.freed
		m.mu.Unlock()
		select {
		case <-freed:
		case <-timer.C:
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	}
}

func (m *mcpSlots) release(client string) {
	m.mu.Lock()
	m.used--
	if m.byUser[client]--; m.byUser[client] <= 0 {
		delete(m.byUser, client)
	}
	close(m.freed)
	m.freed = make(chan struct{})
	m.mu.Unlock()
}
