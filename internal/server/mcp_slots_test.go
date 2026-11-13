package server

import (
	"context"
	"testing"
	"time"
)

func TestMCPSlotsPerClientAndShared(t *testing.T) {
	m := newMCPSlots(3, 2)
	ctx := context.Background()
	a1, ok1 := m.acquire(ctx, "a", 0)
	_, ok2 := m.acquire(ctx, "a", 0)
	if !ok1 || !ok2 {
		t.Fatal("first two slots for a should be free")
	}
	if _, ok := m.acquire(ctx, "a", 20*time.Millisecond); ok {
		t.Fatal("a is over its own share")
	}
	if _, ok := m.acquire(ctx, "b", 0); !ok {
		t.Fatal("b still has room in the shared pool")
	}
	if _, ok := m.acquire(ctx, "c", 20*time.Millisecond); ok {
		t.Fatal("shared pool is full")
	}
	done := make(chan bool)
	go func() { _, ok := m.acquire(ctx, "c", time.Second); done <- ok }()
	time.Sleep(20 * time.Millisecond)
	a1()
	a1() // double release is harmless
	if !<-done {
		t.Fatal("c should get the slot a released while waiting")
	}
	if m.used != 3 || m.byUser["a"] != 1 {
		t.Fatalf("used=%d a=%d", m.used, m.byUser["a"])
	}
}

func TestMCPSlotsGiveUpWhenCancelled(t *testing.T) {
	m := newMCPSlots(1, 1)
	m.acquire(context.Background(), "a", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := m.acquire(ctx, "b", time.Second); ok {
		t.Fatal("cancelled request must not get a slot")
	}
}
